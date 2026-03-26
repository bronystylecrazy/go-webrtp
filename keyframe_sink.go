package webrtp

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type keyframeSink struct {
	cfg             *Config
	logger          Logger
	format          string
	renderer        keyframeRenderer
	queue           chan keyframeJob
	closeCh         chan struct{}
	closeWg         sync.WaitGroup
	dropOnce        sync.Once
	workers         int
	stateMu         sync.RWMutex
	distort         bool
	deskEnabled     bool
	fx              float64
	fy              float64
	scale           float64
	desk            []point
	rateMu          sync.Mutex
	lastSave        time.Time
	writeFS         bool
	publishMQTT     bool
	customKeyframer Keyframer
	mqttPublisher   *mqttPublisher
	snapshotTableID int
	deskMetaMu      sync.Mutex
	deskMeta        *deskViewMetadataJob
	deskMetaWake    chan struct{}
	deskMetaLast    time.Time
}

var imageEncodeBufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

type decoderWorker struct {
	h264                nativeH264Decoder
	h264Encoder         nativeH264FrameEncoder
	h264DiagLog         bool
	h264FallbackLog     bool
	h264LastDecodePath  string
	h264LastDecodeError string
}

type keyframeJob struct {
	codec    string
	width    int
	height   int
	annexb   []byte
	frameNo  uint32
	queuedAt time.Time
}

type point struct {
	x float64
	y float64
}

type deskViewMetadataJob struct {
	topic   string
	payload []byte
}

func newKeyframeSink(cfg *Config, logger Logger) *keyframeSink {
	if cfg == nil {
		return nil
	}
	targets, err := parseKeyframeSinkTargets(cfg.KeyframeSink)
	if err != nil {
		logger.Printf("keyframe sink disabled: %v", err)
		return nil
	}
	if len(targets) == 0 && cfg.Keyframer == nil {
		return nil
	}
	if targets["fs"] && strings.TrimSpace(cfg.KeyframeOutput) == "" {
		logger.Printf("keyframe sink disabled: fs sink requires keyframeOutput")
		return nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if strings.ToLower(strings.TrimSpace(cfg.KeyframeFormat)) != "h264" {
			logger.Printf("keyframe sink disabled: ffmpeg not found: %v", err)
			return nil
		}
	}
	format := strings.ToLower(strings.TrimSpace(cfg.KeyframeFormat))
	if format == "" {
		format = "jpg"
	}
	if format == "jpeg" {
		format = "jpg"
	}
	if format != "jpg" && format != "png" && format != "h264" {
		logger.Printf("keyframe sink disabled: unsupported format %q", format)
		return nil
	}
	if targets["fs"] {
		if err := os.MkdirAll(cfg.KeyframeOutput, 0o755); err != nil {
			logger.Printf("keyframe sink disabled: create output dir: %v", err)
			return nil
		}
	}
	sink := &keyframeSink{
		cfg:             cfg,
		logger:          logger,
		format:          format,
		renderer:        newKeyframeRenderer(logger),
		distort:         true,
		deskEnabled:     true,
		fx:              0.12,
		fy:              0.15,
		scale:           0.95,
		queue:           make(chan keyframeJob, 1),
		closeCh:         make(chan struct{}),
		workers:         1,
		writeFS:         targets["fs"],
		publishMQTT:     targets["mqtt"],
		customKeyframer: cfg.Keyframer,
		snapshotTableID: 1,
		deskMetaWake:    make(chan struct{}, 1),
	}
	if sink.publishMQTT {
		publisher, err := newMQTTPublisher(cfg, logger)
		if err != nil {
			logger.Printf("keyframe sink disabled: mqtt publisher: %v", err)
			return nil
		}
		sink.mqttPublisher = publisher
	}
	for i := 0; i < sink.workers; i++ {
		sink.closeWg.Add(1)
		go sink.run(i)
	}
	if sink.publishMQTT {
		sink.closeWg.Add(1)
		go sink.runDeskMetadataPublisher()
	}
	return sink
}

func (s *keyframeSink) UpdateCalibration(distort, deskEnabled bool, fx, fy, scale float64, deskRaw string) error {
	if s == nil {
		return nil
	}
	desk, err := parseDeskPoints(deskRaw)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	s.distort = distort
	s.deskEnabled = deskEnabled
	s.fx = fx
	s.fy = fy
	s.scale = normalizedScale(scale)
	s.desk = desk
	s.stateMu.Unlock()
	return nil
}

func (s *keyframeSink) Close() {
	if s == nil {
		return
	}
	s.dropOnce.Do(func() {
		close(s.closeCh)
		s.closeWg.Wait()
		if s.mqttPublisher != nil {
			s.mqttPublisher.Close()
		}
		if closer, ok := s.customKeyframer.(interface{ Close() error }); ok {
			_ = closer.Close()
		} else if closer, ok := s.customKeyframer.(interface{ Close() }); ok {
			closer.Close()
		}
		if s.renderer != nil {
			s.renderer.Close()
		}
	})
}

func (s *keyframeSink) Enqueue(codec string, width, height int, au [][]byte, frameNo uint32) {
	if s == nil || len(au) == 0 || width <= 0 || height <= 0 {
		return
	}
	now := time.Now()
	s.rateMu.Lock()
	if !s.lastSave.IsZero() && now.Sub(s.lastSave) < time.Second {
		s.rateMu.Unlock()
		return
	}
	s.lastSave = now
	s.rateMu.Unlock()
	job := keyframeJob{
		codec:    strings.ToLower(strings.TrimSpace(codec)),
		width:    width,
		height:   height,
		annexb:   annexbFromAU(au),
		frameNo:  frameNo,
		queuedAt: now,
	}
	if job.codec == "" || len(job.annexb) == 0 {
		return
	}
	select {
	case s.queue <- job:
	default:
		select {
		case dropped := <-s.queue:
			s.logger.Printf("keyframe sink dropping stale queued frame %d for newer frame %d", dropped.frameNo, frameNo)
		default:
		}
		select {
		case s.queue <- job:
		default:
			s.rateMu.Lock()
			s.lastSave = time.Time{}
			s.rateMu.Unlock()
			s.logger.Printf("keyframe sink queue full, dropping frame %d", frameNo)
		}
	}
}

func normalizedScale(scale float64) float64 {
	if scale <= 0 {
		return 1
	}
	return scale
}

func parseKeyframeSinkTargets(raw string) (map[string]bool, error) {
	targets := make(map[string]bool)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return targets, nil
	}
	for _, part := range strings.Split(raw, ",") {
		target := strings.ToLower(strings.TrimSpace(part))
		if target == "" {
			continue
		}
		switch target {
		case "fs", "mqtt":
			targets[target] = true
		default:
			return nil, fmt.Errorf("unsupported keyframe sink %q", target)
		}
	}
	return targets, nil
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

func annexbFromAU(au [][]byte) []byte {
	var out []byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, nalu...)
	}
	return out
}

func parseDeskPoints(raw string) ([]point, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ";")
	if len(parts) != 4 {
		return nil, fmt.Errorf("expected 4 points, got %d", len(parts))
	}
	points := make([]point, 0, 4)
	for _, part := range parts {
		var p point
		if _, err := fmt.Sscanf(strings.TrimSpace(part), "%f,%f", &p.x, &p.y); err != nil {
			return nil, fmt.Errorf("parse point %q: %w", part, err)
		}
		if p.x < 0 || p.x > 1 || p.y < 0 || p.y > 1 {
			return nil, fmt.Errorf("point %q out of range [0,1]", part)
		}
		points = append(points, p)
	}
	return points, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
