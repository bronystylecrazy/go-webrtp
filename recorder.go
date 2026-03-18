package webrtp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type RecordingStatus struct {
	Active           bool      `json:"active"`
	Path             string    `json:"path,omitempty"`
	OfflineMode      string    `json:"offlineMode,omitempty"`
	StartedAt        time.Time `json:"startedAt,omitempty"`
	BytesWritten     int64     `json:"bytesWritten"`
	RequestedStartAt time.Time `json:"requestedStartAt,omitempty"`
	ActualStartAt    time.Time `json:"actualStartAt,omitempty"`
	RequestedStopAt  time.Time `json:"requestedStopAt,omitempty"`
	ActualStopAt     time.Time `json:"actualStopAt,omitempty"`
	StartDriftMs     int64     `json:"startDriftMs"`
	StopDriftMs      int64     `json:"stopDriftMs"`
	MediaDurationMs  int64     `json:"mediaDurationMs"`
	TrimStartMs      int64     `json:"trimStartMs"`
	TrimEndMs        int64     `json:"trimEndMs"`
	MissingStartMs   int64     `json:"missingStartMs"`
	MissingEndMs     int64     `json:"missingEndMs"`
}

type Recorder struct {
	mu           sync.Mutex
	logger       Logger
	file         *os.File
	path         string
	mode         string
	offlineMode  string
	startedAt    time.Time
	bytesWritten atomic.Int64
	initData     []byte
	initWritten  bool
	seqNr        uint32
	dts          uint64
	active       bool
	bufferDur    uint64
	prebuffer    []recordingSample
	waitForIDR   bool

	codec      string
	width      int
	height     int
	frameRate  float64
	lastDur    uint32
	offlineGen offlineFrameGenerator
	offlineRun bool
	offlineGap bool

	requestedStartAt time.Time
	actualStartAt    time.Time
	requestedStopAt  time.Time
	actualStopAt     time.Time
}

type recordingSample struct {
	avcc  []byte
	dur   uint32
	isIDR bool
}

type offlineFrameGenerator interface {
	NextFrame() ([]byte, bool, error)
	Close() error
}

func NewRecorder(logger Logger) *Recorder {
	return &Recorder{logger: logger}
}

func normalizeRecordingMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "instant"
	}
	return mode
}

func normalizeOfflineMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "pause"
	}
	return mode
}

func (r *Recorder) Start(path, mode, offlineMode string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	mode = normalizeRecordingMode(mode)
	offlineMode = normalizeOfflineMode(offlineMode)

	if mode != "fmp4" && mode != "instant" && mode != "exact" {
		return fmt.Errorf("unsupported recording mode: %s", mode)
	}
	switch offlineMode {
	case "pause", "stop":
	case "black":
		if err := validateOfflineBlackSupport(r.codec, r.width, r.height); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported offline mode: %s", offlineMode)
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("recording path is required")
	}
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create recording directory: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create recording file: %w", err)
	}

	r.file = file
	r.path = path
	r.mode = mode
	r.offlineMode = offlineMode
	r.startedAt = time.Now()
	r.requestedStartAt = r.startedAt
	r.actualStartAt = time.Time{}
	r.requestedStopAt = time.Time{}
	r.actualStopAt = time.Time{}
	r.bytesWritten.Store(0)
	r.initWritten = false
	r.seqNr = 0
	r.dts = 0
	r.active = true
	r.waitForIDR = mode == "exact"
	r.offlineGap = false
	r.stopOfflineGeneratorLocked()

	if len(r.initData) > 0 {
		if _, err := r.file.Write(r.initData); err != nil {
			_ = r.file.Close()
			r.file = nil
			r.active = false
			return fmt.Errorf("write init segment: %w", err)
		}
		r.bytesWritten.Add(int64(len(r.initData)))
		r.initWritten = true
	}
	if r.mode == "instant" {
		if !r.writePrebufferLocked() {
			r.waitForIDR = true
		}
	}
	r.logger.Printf("recording started: %s (%s, offline=%s)", path, mode, offlineMode)
	return nil
}

func (r *Recorder) SetInit(initData []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.initData = append(r.initData[:0], initData...)
	if !r.active || r.file == nil || r.initWritten || len(r.initData) == 0 {
		return
	}
	if _, err := r.file.Write(r.initData); err != nil {
		r.logger.Printf("recording init write failed: %v", err)
		return
	}
	r.bytesWritten.Add(int64(len(r.initData)))
	r.initWritten = true
}

func (r *Recorder) RecordSample(avcc []byte, dur uint32, isIDR bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cacheSampleLocked(avcc, dur, isIDR)

	if !r.canRecordLiveLocked() {
		return
	}
	if !r.transitionToLiveLocked(isIDR) {
		return
	}
	if !r.ensureInitWrittenLocked() {
		return
	}
	if !r.acceptLiveSampleLocked(isIDR) {
		return
	}

	r.lastDur = dur
	r.writeFragmentLocked(avcc, dur, isIDR, "recording fragment")
}

func (r *Recorder) cacheSampleLocked(avcc []byte, dur uint32, isIDR bool) {
	const maxPrebufferDur = uint64(90000 * 3)

	copyData := make([]byte, len(avcc))
	copy(copyData, avcc)
	r.prebuffer = append(r.prebuffer, recordingSample{
		avcc:  copyData,
		dur:   dur,
		isIDR: isIDR,
	})
	r.bufferDur += uint64(dur)

	for len(r.prebuffer) > 0 && r.bufferDur > maxPrebufferDur {
		r.bufferDur -= uint64(r.prebuffer[0].dur)
		r.prebuffer = r.prebuffer[1:]
	}
}

func (r *Recorder) writePrebufferLocked() bool {
	if len(r.prebuffer) == 0 || r.file == nil || len(r.initData) == 0 {
		return false
	}
	start := -1
	for i := len(r.prebuffer) - 1; i >= 0; i-- {
		if r.prebuffer[i].isIDR {
			start = i
			break
		}
	}
	if start < 0 {
		return false
	}
	for _, sample := range r.prebuffer[start:] {
		r.seqNr++
		frag, err := BuildFragment(r.seqNr, r.dts, sample.dur, sample.isIDR, sample.avcc)
		if err != nil {
			r.logger.Printf("recording prebuffer fragment build failed: %v", err)
			continue
		}
		if _, err := r.file.Write(frag); err != nil {
			r.logger.Printf("recording prebuffer write failed: %v", err)
			return false
		}
		now := time.Now()
		if r.actualStartAt.IsZero() {
			r.actualStartAt = now
		}
		r.actualStopAt = now
		r.bytesWritten.Add(int64(len(frag)))
		r.dts += uint64(sample.dur)
	}
	return true
}

func (r *Recorder) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requestedStopAt = time.Now()

	return r.closeLocked()
}

func (r *Recorder) SetSourceInfo(codec string, width, height int, frameRate float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codec = strings.ToLower(strings.TrimSpace(codec))
	r.width = width
	r.height = height
	r.frameRate = frameRate
}

func (r *Recorder) ensureInitWrittenLocked() bool {
	if r.initWritten {
		return true
	}
	if len(r.initData) == 0 || r.file == nil {
		return false
	}
	if _, err := r.file.Write(r.initData); err != nil {
		r.logger.Printf("recording init write failed: %v", err)
		return false
	}
	r.bytesWritten.Add(int64(len(r.initData)))
	r.initWritten = true
	return true
}

func (r *Recorder) writeFragmentLocked(avcc []byte, dur uint32, isIDR bool, logPrefix string) bool {
	r.seqNr++
	frag, err := BuildFragment(r.seqNr, r.dts, dur, isIDR, avcc)
	if err != nil {
		r.logger.Printf("%s build failed: %v", logPrefix, err)
		return false
	}
	if _, err := r.file.Write(frag); err != nil {
		r.logger.Printf("%s write failed: %v", logPrefix, err)
		return false
	}
	now := time.Now()
	if r.actualStartAt.IsZero() {
		r.actualStartAt = now
	}
	r.actualStopAt = now
	r.bytesWritten.Add(int64(len(frag)))
	r.dts += uint64(dur)
	return true
}

func (r *Recorder) closeLocked() error {
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			return err
		}
		r.file = nil
	}
	r.active = false
	r.waitForIDR = false
	r.offlineGap = false
	r.stopOfflineGeneratorLocked()
	return nil
}

func (r *Recorder) Status() RecordingStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	var startDriftMs int64
	if !r.requestedStartAt.IsZero() && !r.actualStartAt.IsZero() {
		startDriftMs = r.actualStartAt.Sub(r.requestedStartAt).Milliseconds()
	}
	var stopDriftMs int64
	if !r.requestedStopAt.IsZero() && !r.actualStopAt.IsZero() {
		stopDriftMs = r.actualStopAt.Sub(r.requestedStopAt).Milliseconds()
	}
	var mediaDurationMs int64
	if !r.actualStartAt.IsZero() {
		end := r.actualStopAt
		if end.IsZero() && r.active {
			end = time.Now()
		}
		if !end.IsZero() {
			mediaDurationMs = end.Sub(r.actualStartAt).Milliseconds()
		}
	}
	trimStartMs := maxInt64(0, -startDriftMs)
	trimEndMs := maxInt64(0, stopDriftMs)
	missingStartMs := maxInt64(0, startDriftMs)
	missingEndMs := maxInt64(0, -stopDriftMs)

	return RecordingStatus{
		Active:           r.active,
		Path:             r.path,
		OfflineMode:      r.offlineMode,
		StartedAt:        r.startedAt,
		BytesWritten:     r.bytesWritten.Load(),
		RequestedStartAt: r.requestedStartAt,
		ActualStartAt:    r.actualStartAt,
		RequestedStopAt:  r.requestedStopAt,
		ActualStopAt:     r.actualStopAt,
		StartDriftMs:     startDriftMs,
		StopDriftMs:      stopDriftMs,
		MediaDurationMs:  mediaDurationMs,
		TrimStartMs:      trimStartMs,
		TrimEndMs:        trimEndMs,
		MissingStartMs:   missingStartMs,
		MissingEndMs:     missingEndMs,
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
