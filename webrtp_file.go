package webrtp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type fileConn struct {
	cancel       context.CancelFunc
	cmd          *exec.Cmd
	done         chan struct{}
	playlistPath string
	statePath    string
	closeOnce    sync.Once
}

func (r *fileConn) Done() <-chan struct{} {
	return r.done
}

func (r *fileConn) Close() {
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.cmd != nil && r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
		if r.playlistPath != "" {
			_ = os.Remove(r.playlistPath)
		}
	})
}

func (r *Instance) connectFile(ctx context.Context) (*fileConn, error) {
	sourcePath := strings.TrimSpace(r.cfg.Path)
	if sourcePath == "" {
		return nil, fmt.Errorf("file source requires path")
	}

	files, playlistPath, err := fileSourceInputs(sourcePath)
	if err != nil {
		return nil, err
	}
	sourceKind := fileSourceKind(files)
	if sourceKind == fileSourceKindMixed {
		if playlistPath != "" {
			_ = os.Remove(playlistPath)
		}
		return nil, fmt.Errorf("file source directory cannot mix raw .h264 files with container files")
	}

	fps := r.cfg.FrameRate
	if fps <= 0 && sourceKind == fileSourceKindContainer {
		fps = probeVideoFrameRate(files[0])
	}
	if fps <= 0 {
		fps = 30
	}
	frameDur := uint32(math.Round(90000 / fps))
	if frameDur == 0 {
		frameDur = 3000
	}

	fileCtx, cancel := context.WithCancel(ctx)
	if sourceKind == fileSourceKindRawH264 {
		if r.cfg.H264Profile != "" {
			if len(files) != 1 {
				cancel()
				if playlistPath != "" {
					_ = os.Remove(playlistPath)
				}
				return nil, fmt.Errorf("h264Profile transcoding for raw .h264 inputs currently supports a single file path")
			}
			if playlistPath != "" {
				_ = os.Remove(playlistPath)
			}
			return r.connectFileFFmpeg(fileCtx, cancel, sourcePath, rawH264FFmpegArgs(files[0], r.cfg, fps), fps, frameDur, 1, ""), nil
		}
		if playlistPath != "" {
			_ = os.Remove(playlistPath)
		}
		return r.connectRawH264Files(fileCtx, cancel, sourcePath, files, fps, frameDur), nil
	}

	args := fileFFmpegArgs(files, playlistPath, r.cfg, fps)
	return r.connectFileFFmpeg(fileCtx, cancel, sourcePath, args, fps, frameDur, len(files), playlistPath), nil
}

func (r *Instance) connectFileFFmpeg(ctx context.Context, cancel context.CancelFunc, sourcePath string, args []string, fps float64, frameDur uint32, fileCount int, playlistPath string) *fileConn {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		if playlistPath != "" {
			_ = os.Remove(playlistPath)
		}
		r.logger.Printf("ffmpeg stdout pipe: %v", err)
		return &fileConn{cancel: cancel, done: closedDone(), playlistPath: playlistPath}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		if playlistPath != "" {
			_ = os.Remove(playlistPath)
		}
		r.logger.Printf("ffmpeg stderr pipe: %v", err)
		return &fileConn{cancel: cancel, done: closedDone(), playlistPath: playlistPath}
	}
	if err := cmd.Start(); err != nil {
		cancel()
		if playlistPath != "" {
			_ = os.Remove(playlistPath)
		}
		r.logger.Printf("ffmpeg start: %v", err)
		return &fileConn{cancel: cancel, done: closedDone(), playlistPath: playlistPath}
	}

	conn := &fileConn{
		cancel:       cancel,
		cmd:          cmd,
		done:         make(chan struct{}),
		playlistPath: playlistPath,
	}
	handler := &videoHandler{hub: r.hub, logger: r.logger, instance: r}

	r.logger.Printf("file stream active (%s, fps=%.2f, files=%d)", sourcePath, fps, fileCount)

	go func() {
		scanner := bufio.NewScanner(stderr)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				r.logger.Printf("ffmpeg: %s", line)
			}
		}
	}()

	go func() {
		defer close(conn.done)
		defer conn.Close()

		reader := newH264AccessUnitReader(stdout)
		ts := uint32(0)
		for {
			au, err := reader.Next()
			if err != nil {
				if err != io.EOF && ctx.Err() == nil {
					r.logger.Printf("file source read failed: %v", err)
				}
				break
			}
			if len(au) == 0 {
				continue
			}
			handler.processH264(au, ts, nil, nil)
			ts += frameDur
		}

		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			r.logger.Printf("ffmpeg exited: %v", err)
		}
	}()

	return conn
}

func (r *Instance) connectRawH264Files(ctx context.Context, cancel context.CancelFunc, sourcePath string, files []string, fps float64, frameDur uint32) *fileConn {
	statePath := rawH264StatePath(sourcePath)
	conn := &fileConn{
		cancel:    cancel,
		done:      make(chan struct{}),
		statePath: statePath,
	}
	handler := &videoHandler{hub: r.hub, logger: r.logger, instance: r}
	frameDelay := time.Duration(float64(time.Second) / fps)
	if frameDelay <= 0 {
		frameDelay = time.Second / 30
	}
	resume := loadRawH264ResumeState(statePath, files)
		if resume != nil {
			r.logger.Printf("raw h264 resume state loaded (%s, accessUnit=%d, lastIDR=%d)", resume.File, resume.AccessUnit, resume.LastIDR)
		}

	r.logger.Printf("raw h264 file stream active (%s, fps=%.2f, files=%d)", sourcePath, fps, len(files))

	go func() {
		defer close(conn.done)
		defer conn.Close()

		ts := uint32(0)
		startIndex := 0
		if resume != nil {
			startIndex = resume.FileIndex
		}
		for {
			for offset := 0; offset < len(files); offset++ {
				fileIndex := (startIndex + offset) % len(files)
				path := files[fileIndex]
				startAU := uint64(0)
				targetAU := uint64(0)
				if resume != nil && fileIndex == resume.FileIndex {
					startAU = resume.LastIDR
					targetAU = resume.AccessUnit
				}
				if err := r.streamRawH264File(ctx, handler, path, fileIndex, startAU, targetAU, frameDur, frameDelay, &ts, statePath); err != nil {
					if err == context.Canceled || ctx.Err() != nil {
						return
					}
					r.logger.Printf("raw h264 file source failed: %v", err)
					return
				}
			}
			startIndex = 0
			resume = nil
		}
	}()

	return conn
}

func (r *Instance) streamRawH264File(ctx context.Context, handler *videoHandler, path string, fileIndex int, startAU uint64, targetAU uint64, frameDur uint32, frameDelay time.Duration, ts *uint32, statePath string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open raw h264 file: %w", err)
	}
	defer file.Close()

	reader := newH264AccessUnitReader(file)
	currentAU := uint64(0)
	lastIDR := uint64(0)
	for {
		select {
		case <-ctx.Done():
			return context.Canceled
		default:
		}

		au, err := reader.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read raw h264 access unit: %w", err)
		}
		if len(au) == 0 {
			continue
		}
		isIDR := rawH264AccessUnitIsIDR(au)
		if isIDR {
			lastIDR = currentAU
		}
		if currentAU < startAU {
			currentAU++
			continue
		}
		if targetAU > startAU && currentAU < targetAU {
			currentAU++
			handler.processH264Warmup(au, *ts, nil, nil)
			*ts += frameDur
			continue
		}

		currentAU++
		handler.processH264(au, *ts, nil, nil)
		*ts += frameDur
		state := rawH264ResumeState{
			File:       path,
			FileIndex:  fileIndex,
			AccessUnit: currentAU,
			LastIDR:    lastIDR,
		}
		if isIDR {
			saveRawH264ResumeState(statePath, state)
		}

		timer := time.NewTimer(frameDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			saveRawH264ResumeState(statePath, state)
			return context.Canceled
		case <-timer.C:
		}
	}
}

func rawH264AccessUnitIsIDR(au [][]byte) bool {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		if (nalu[0] & 0x1F) == 5 {
			return true
		}
	}
	return false
}

func fileSourceInputs(sourcePath string) ([]string, string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, "", fmt.Errorf("stat file source path: %w", err)
	}
	if !info.IsDir() {
		return []string{sourcePath}, "", nil
	}

	entries, err := os.ReadDir(sourcePath)
	if err != nil {
		return nil, "", fmt.Errorf("read file source directory: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		switch ext {
		case ".h264", ".264", ".mkv", ".mp4", ".mov", ".avi", ".webm", ".m4v", ".ts", ".mts":
			files = append(files, filepath.Join(sourcePath, entry.Name()))
		}
	}
	if len(files) == 0 {
		return nil, "", fmt.Errorf("file source directory has no supported video files: %s", sourcePath)
	}
	sort.Strings(files)

	playlist, err := os.CreateTemp("", "webrtp-file-source-*.txt")
	if err != nil {
		return nil, "", fmt.Errorf("create concat playlist: %w", err)
	}
	defer playlist.Close()

	for _, file := range files {
		absPath, err := filepath.Abs(file)
		if err != nil {
			_ = os.Remove(playlist.Name())
			return nil, "", fmt.Errorf("resolve file source path: %w", err)
		}
		if _, err := fmt.Fprintf(playlist, "file '%s'\n", strings.ReplaceAll(absPath, "'", "'\\''")); err != nil {
			_ = os.Remove(playlist.Name())
			return nil, "", fmt.Errorf("write concat playlist: %w", err)
		}
	}

	return files, playlist.Name(), nil
}

type fileSourceType int

const (
	fileSourceKindUnknown fileSourceType = iota
	fileSourceKindRawH264
	fileSourceKindContainer
	fileSourceKindMixed
)

func fileSourceKind(files []string) fileSourceType {
	if len(files) == 0 {
		return fileSourceKindUnknown
	}
	rawCount := 0
	for _, file := range files {
		switch strings.ToLower(filepath.Ext(file)) {
		case ".h264", ".264":
			rawCount++
		}
	}
	switch {
	case rawCount == len(files):
		return fileSourceKindRawH264
	case rawCount == 0:
		return fileSourceKindContainer
	default:
		return fileSourceKindMixed
	}
}

func probeVideoFrameRate(path string) float64 {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=avg_frame_rate,r_frame_rate",
		"-of", "default=noprint_wrappers=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (key != "avg_frame_rate" && key != "r_frame_rate") {
			continue
		}
		if fps := parseFFmpegRate(value); fps > 0 {
			return fps
		}
	}
	return 0
}

func parseFFmpegRate(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" || value == "0/0" {
		return 0
	}
	if strings.Contains(value, "/") {
		num, den, ok := strings.Cut(value, "/")
		if !ok {
			return 0
		}
		n, err1 := strconv.ParseFloat(num, 64)
		d, err2 := strconv.ParseFloat(den, 64)
		if err1 != nil || err2 != nil || d == 0 {
			return 0
		}
		return n / d
	}
	fps, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return fps
}

type rawH264ResumeState struct {
	File       string `json:"file"`
	FileIndex  int    `json:"fileIndex"`
	AccessUnit uint64 `json:"accessUnit"`
	LastIDR    uint64 `json:"lastIdr"`
}

func rawH264StatePath(sourcePath string) string {
	info, err := os.Stat(sourcePath)
	if err == nil && info.IsDir() {
		return filepath.Join(sourcePath, ".webrtp-raw-h264-state.json")
	}
	return sourcePath + ".webrtp-state.json"
}

func loadRawH264ResumeState(path string, files []string) *rawH264ResumeState {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var state rawH264ResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	if state.File != "" {
		for index, file := range files {
			if file == state.File {
				state.FileIndex = index
				if state.LastIDR > state.AccessUnit {
					state.LastIDR = 0
				}
				return &state
			}
		}
	}
	if state.FileIndex >= 0 && state.FileIndex < len(files) {
		state.File = files[state.FileIndex]
		if state.LastIDR > state.AccessUnit {
			state.LastIDR = 0
		}
		return &state
	}
	return nil
}

func saveRawH264ResumeState(path string, state rawH264ResumeState) {
	if path == "" {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func fileFFmpegArgs(files []string, playlistPath string, cfg *Config, fps float64) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-re", "-stream_loop", "-1"}
	if playlistPath != "" {
		args = append(args, "-f", "concat", "-safe", "0", "-i", playlistPath)
	} else {
		args = append(args, "-i", files[0])
	}
	args = append(args, "-map", "0:v:0", "-an")
	if cfg.Width > 0 && cfg.Height > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", cfg.Width, cfg.Height))
	}
	if cfg.FrameRate > 0 {
		args = append(args, "-r", strconv.FormatFloat(cfg.FrameRate, 'f', -1, 64))
	}
	args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p")
	if cfg.H264Profile != "" {
		args = append(args, "-profile:v", cfg.H264Profile)
	}
	if cfg.BitrateKbps > 0 {
		bitrate := fmt.Sprintf("%dk", cfg.BitrateKbps)
		args = append(args, "-b:v", bitrate, "-maxrate", bitrate, "-bufsize", fmt.Sprintf("%dk", cfg.BitrateKbps*2))
	}
	gop := int(math.Round(fps * 2))
	if gop < 1 {
		gop = 60
	}
	x264Params := []string{"repeat-headers=1", "aud=1", "scenecut=0"}
	if cfg.H264Profile == "baseline" {
		x264Params = append(x264Params, "cabac=0")
	}
	args = append(args, "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-bf", "0", "-x264-params", strings.Join(x264Params, ":"), "-f", "h264", "-")
	return args
}

func rawH264FFmpegArgs(file string, cfg *Config, fps float64) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-re"}
	if fps > 0 {
		args = append(args, "-framerate", strconv.FormatFloat(fps, 'f', -1, 64))
	}
	args = append(args, "-f", "h264", "-i", file, "-map", "0:v:0", "-an")
	if cfg.Width > 0 && cfg.Height > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", cfg.Width, cfg.Height))
	}
	if cfg.FrameRate > 0 {
		args = append(args, "-r", strconv.FormatFloat(cfg.FrameRate, 'f', -1, 64))
	}
	args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p")
	if cfg.H264Profile != "" {
		args = append(args, "-profile:v", cfg.H264Profile)
	}
	if cfg.BitrateKbps > 0 {
		bitrate := fmt.Sprintf("%dk", cfg.BitrateKbps)
		args = append(args, "-b:v", bitrate, "-maxrate", bitrate, "-bufsize", fmt.Sprintf("%dk", cfg.BitrateKbps*2))
	}
	gop := int(math.Round(fps * 2))
	if gop < 1 {
		gop = 60
	}
	x264Params := []string{"repeat-headers=1", "aud=1", "scenecut=0"}
	if cfg.H264Profile == "baseline" {
		x264Params = append(x264Params, "cabac=0")
	}
	args = append(args, "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-bf", "0", "-x264-params", strings.Join(x264Params, ":"), "-f", "h264", "-")
	return args
}

func closedDone() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

type h264AccessUnitReader struct {
	r       *annexBNALUReader
	pending [][]byte
}

func newH264AccessUnitReader(rd io.Reader) *h264AccessUnitReader {
	return &h264AccessUnitReader{r: newAnnexBNALUReader(rd)}
}

func (r *h264AccessUnitReader) Next() ([][]byte, error) {
	for {
		nalu, err := r.r.Next()
		if err != nil {
			if err == io.EOF && len(r.pending) > 0 {
				au := r.pending
				r.pending = nil
				return au, nil
			}
			return nil, err
		}
		if len(nalu) == 0 {
			continue
		}

		if h264BeginsNewAccessUnit(r.pending, nalu) {
			au := r.pending
			r.pending = [][]byte{nalu}
			return au, nil
		}
		r.pending = append(r.pending, nalu)
	}
}

func h264BeginsNewAccessUnit(pending [][]byte, nalu []byte) bool {
	if len(pending) == 0 || len(nalu) == 0 {
		return false
	}

	naluType := nalu[0] & 0x1F
	switch naluType {
	case 9:
		return len(pending) > 0
	case 7, 8, 6:
		return h264HasVCL(pending)
	}

	if !h264IsVCL(naluType) {
		return false
	}
	if !h264HasVCL(pending) {
		return false
	}

	firstMB, ok := h264FirstMBInSlice(nalu)
	return ok && firstMB == 0
}

func h264HasVCL(nalus [][]byte) bool {
	for _, nalu := range nalus {
		if len(nalu) == 0 {
			continue
		}
		if h264IsVCL(nalu[0] & 0x1F) {
			return true
		}
	}
	return false
}

func h264IsVCL(naluType byte) bool {
	return naluType >= 1 && naluType <= 5
}

func h264FirstMBInSlice(nalu []byte) (uint, bool) {
	if len(nalu) < 2 {
		return 0, false
	}
	rbsp := make([]byte, 0, len(nalu)-1)
	zeros := 0
	for _, b := range nalu[1:] {
		if zeros == 2 && b == 0x03 {
			zeros = 0
			continue
		}
		rbsp = append(rbsp, b)
		if b == 0x00 {
			zeros++
		} else {
			zeros = 0
		}
	}
	br := newBitReader(rbsp)
	v, ok := br.readUE()
	return v, ok
}

type bitReader struct {
	data []byte
	pos  int
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

func (r *bitReader) readBit() (uint, bool) {
	if r.pos >= len(r.data)*8 {
		return 0, false
	}
	b := r.data[r.pos/8]
	shift := 7 - (r.pos % 8)
	r.pos++
	return uint((b >> shift) & 0x01), true
}

func (r *bitReader) readUE() (uint, bool) {
	leadingZeros := 0
	for {
		bit, ok := r.readBit()
		if !ok {
			return 0, false
		}
		if bit == 1 {
			break
		}
		leadingZeros++
	}
	suffix := uint(0)
	for i := 0; i < leadingZeros; i++ {
		bit, ok := r.readBit()
		if !ok {
			return 0, false
		}
		suffix = (suffix << 1) | bit
	}
	return (uint(1) << leadingZeros) - 1 + suffix, true
}

type annexBNALUReader struct {
	reader io.Reader
	buf    []byte
	eof    bool
}

func newAnnexBNALUReader(r io.Reader) *annexBNALUReader {
	return &annexBNALUReader{reader: r, buf: make([]byte, 0, 256*1024)}
}

func (r *annexBNALUReader) Next() ([]byte, error) {
	for {
		startPos, startLen := annexBStartCode(r.buf, 0)
		if startPos < 0 {
			if r.eof {
				return nil, io.EOF
			}
			if err := r.fill(); err != nil {
				return nil, err
			}
			continue
		}
		if startPos > 0 {
			copy(r.buf, r.buf[startPos:])
			r.buf = r.buf[:len(r.buf)-startPos]
		}

		nextPos, _ := annexBStartCode(r.buf, startLen)
		if nextPos >= 0 {
			nalu := append([]byte(nil), r.buf[startLen:nextPos]...)
			copy(r.buf, r.buf[nextPos:])
			r.buf = r.buf[:len(r.buf)-nextPos]
			if len(nalu) == 0 {
				continue
			}
			return nalu, nil
		}

		if r.eof {
			nalu := append([]byte(nil), r.buf[startLen:]...)
			r.buf = r.buf[:0]
			if len(nalu) == 0 {
				return nil, io.EOF
			}
			return nalu, nil
		}

		if err := r.fill(); err != nil {
			return nil, err
		}
	}
}

func (r *annexBNALUReader) fill() error {
	tmp := make([]byte, 64*1024)
	n, err := r.reader.Read(tmp)
	if n > 0 {
		r.buf = append(r.buf, tmp[:n]...)
	}
	if err != nil {
		if err == io.EOF {
			r.eof = true
			return nil
		}
		return err
	}
	return nil
}

func annexBStartCode(buf []byte, offset int) (int, int) {
	for i := offset; i+3 <= len(buf); i++ {
		if buf[i] != 0x00 || buf[i+1] != 0x00 {
			continue
		}
		if buf[i+2] == 0x01 {
			return i, 3
		}
		if i+3 < len(buf) && buf[i+2] == 0x00 && buf[i+3] == 0x01 {
			return i, 4
		}
	}
	return -1, 0
}
