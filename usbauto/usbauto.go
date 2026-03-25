package usbauto

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	gwebrtp "github.com/bronystylecrazy/go-webrtp"
)

const reconnectDelay = 10 * time.Second

// Mode is the selected source mode for the camera.
type Mode struct {
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FrameRate float64 `json:"frameRate"`
}

// Stream is one branch of the USB camera pipeline.
type Stream struct {
	Name        string
	Width       int
	Height      int
	FrameRate   float64
	AccessUnits <-chan gwebrtp.H264AccessUnit
	Keyframes   <-chan gwebrtp.H264AccessUnit
}

// Camera owns a single FFmpeg process that exposes the best stream and a preview stream.
type Camera struct {
	best     *Stream
	preview  *Stream
	source   Mode
	device   string
	deviceID string
	cfg      options

	bestAccess    chan gwebrtp.H264AccessUnit
	bestKeyframes chan gwebrtp.H264AccessUnit
	previewAccess chan gwebrtp.H264AccessUnit

	stateMu   sync.RWMutex
	cancel    context.CancelFunc
	cmd       *exec.Cmd
	done      chan struct{}
	listeners []net.Listener
	closeOnce sync.Once
	chOnce    sync.Once
	logger    logger
}

// Open probes the device, selects the best source mode automatically, and starts one split pipeline.
func Open(ctx context.Context, deviceID string, opts ...Option) (*Camera, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, fmt.Errorf("usbauto: missing device id")
	}

	cfg := defaultOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	cfg = resolveEncoder(cfg)
	if ctx == nil {
		ctx = context.Background()
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("usbauto: ffmpeg not found: %w", err)
	}

	input, _, err := resolveInputWithFallback(deviceID, "", cfg)
	if err != nil {
		return nil, err
	}

	ffmpegCtx, cancel := context.WithCancel(ctx)

	bestWidth := input.mode.Width
	bestHeight := input.mode.Height
	previewWidth, previewHeight := derivePreviewDimensions(bestWidth, bestHeight, cfg.previewHeight)

	camera := &Camera{
		bestAccess:    make(chan gwebrtp.H264AccessUnit, cfg.streamBuffer),
		bestKeyframes: make(chan gwebrtp.H264AccessUnit, max(1, min(cfg.streamBuffer, 4))),
		previewAccess: make(chan gwebrtp.H264AccessUnit, cfg.streamBuffer),
		deviceID:      deviceID,
		cfg:           cfg,
		cancel:        cancel,
		done:          make(chan struct{}),
		logger:        cfg.logger,
		source:        input.mode,
		device:        input.displayName,
	}
	camera.best = &Stream{
		Name:        "best",
		Width:       bestWidth,
		Height:      bestHeight,
		FrameRate:   input.mode.FrameRate,
		AccessUnits: camera.bestAccess,
		Keyframes:   camera.bestKeyframes,
	}
	camera.preview = &Stream{
		Name:        "preview",
		Width:       previewWidth,
		Height:      previewHeight,
		FrameRate:   input.mode.FrameRate,
		AccessUnits: camera.previewAccess,
	}
	if err := camera.startFFmpeg(ffmpegCtx, input); err != nil {
		cancel()
		camera.closeChannels()
		return nil, err
	}
	go camera.run(ffmpegCtx)

	return camera, nil
}

// Best returns the best-quality stream for AI or archival consumers.
func (c *Camera) Best() *Stream {
	if c == nil {
		return nil
	}
	return c.best
}

// Preview returns the scaled preview stream intended for browser playback.
func (c *Camera) Preview() *Stream {
	if c == nil {
		return nil
	}
	return c.preview
}

// SourceMode returns the selected camera input mode.
func (c *Camera) SourceMode() Mode {
	if c == nil {
		return Mode{}
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.source
}

// Device returns the resolved camera name when available.
func (c *Camera) Device() string {
	if c == nil {
		return ""
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.device
}

// Done closes when the FFmpeg process exits or the camera is closed.
func (c *Camera) Done() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.done
}

// Close stops the FFmpeg process and closes all stream channels.
func (c *Camera) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		c.closeProcess()
	})
	return nil
}

func (c *Camera) run(ctx context.Context) {
	defer close(c.done)
	defer c.closeChannels()
	defer c.Close()

	for {
		c.runCurrentProcess(ctx)
		if ctx.Err() != nil {
			return
		}
		if c.logger != nil {
			c.logger.Printf("usbauto: source unavailable, retrying in %s", reconnectDelay)
		}
		if !sleepWithContext(ctx, reconnectDelay) {
			return
		}
		for {
			if ctx.Err() != nil {
				return
			}
			input, candidate, err := resolveInputWithFallback(c.deviceID, c.Device(), c.cfg)
			if err == nil {
				if err := c.startFFmpeg(ctx, input); err == nil {
					if c.logger != nil {
						if candidate != "" && candidate != c.deviceID {
							c.logger.Printf("usbauto: reconnected using fallback device %q", candidate)
						} else {
							c.logger.Printf("usbauto: reconnected device %s", c.deviceID)
						}
					}
					break
				} else if c.logger != nil {
					c.logger.Printf("usbauto: restart failed: %v", err)
				}
			} else if c.logger != nil {
				c.logger.Printf("usbauto: reconnect probe failed: %v", err)
			}
			if !sleepWithContext(ctx, reconnectDelay) {
				return
			}
		}
	}
}

func (c *Camera) handleOutput(ctx context.Context, index int, listener net.Listener, sampler *gwebrtp.H264KeyframeSampler) {
	if listener == nil {
		return
	}
	conn, err := listener.Accept()
	if err != nil {
		if ctx.Err() == nil && c.logger != nil {
			c.logger.Printf("usbauto: accept output %d: %v", index, err)
		}
		return
	}
	defer conn.Close()

	pts := uint32(0)
	frameDur := frameDuration90k(c.source.FrameRate)
	err = gwebrtp.EachH264AccessUnit(conn, func(au [][]byte) error {
		packet := cloneAccessUnit(gwebrtp.H264AccessUnit{NALUs: au, PTS90k: pts})
		pts += frameDur
		switch index {
		case 0:
			sendLatest(c.bestAccess, packet)
			if sampler != nil && sampler.Accept(packet) {
				sendLatest(c.bestKeyframes, cloneAccessUnit(packet))
			}
		case 1:
			sendLatest(c.previewAccess, packet)
		}
		return nil
	})
	if err != nil && ctx.Err() == nil && c.logger != nil {
		c.logger.Printf("usbauto: read output %d: %v", index, err)
	}
}

func (c *Camera) closeChannels() {
	c.chOnce.Do(func() {
		close(c.bestAccess)
		close(c.bestKeyframes)
		close(c.previewAccess)
	})
}

func (c *Camera) runCurrentProcess(ctx context.Context) {
	cmd, listeners := c.snapshotProcess()
	if cmd == nil {
		return
	}

	var wg sync.WaitGroup
	sampler := gwebrtp.NewH264KeyframeSampler(1)
	for idx, listener := range listeners {
		wg.Add(1)
		go func(idx int, listener net.Listener) {
			defer wg.Done()
			c.handleOutput(ctx, idx, listener, sampler)
		}(idx, listener)
	}

	if err := cmd.Wait(); err != nil && ctx.Err() == nil && c.logger != nil {
		c.logger.Printf("usbauto: ffmpeg exited: %v", err)
	}
	for _, listener := range listeners {
		_ = listener.Close()
	}
	wg.Wait()
	c.clearProcess(cmd)
}

func (c *Camera) startFFmpeg(ctx context.Context, input resolvedInput) error {
	listeners := make([]net.Listener, 0, 2)
	targets := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, item := range listeners {
				_ = item.Close()
			}
			return fmt.Errorf("usbauto: listen ffmpeg output: %w", err)
		}
		listeners = append(listeners, listener)
		targets = append(targets, "tcp://"+listener.Addr().String())
	}

	bestWidth := input.mode.Width
	bestHeight := input.mode.Height
	previewWidth, previewHeight := derivePreviewDimensions(bestWidth, bestHeight, c.cfg.previewHeight)
	args := buildFFmpegArgs(input, c.cfg, previewHeight, targets)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return fmt.Errorf("usbauto: ffmpeg stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return fmt.Errorf("usbauto: ffmpeg start: %w", err)
	}

	c.stateMu.Lock()
	c.source = input.mode
	c.device = input.displayName
	c.best.Width = bestWidth
	c.best.Height = bestHeight
	c.best.FrameRate = input.mode.FrameRate
	c.preview.Width = previewWidth
	c.preview.Height = previewHeight
	c.preview.FrameRate = input.mode.FrameRate
	c.cmd = cmd
	c.listeners = listeners
	c.stateMu.Unlock()

	if c.logger != nil {
		c.logger.Printf(
			"usbauto: active device=%s input=%s mode=%dx%d@%.2ffps preview=%dx%d encoder=%s",
			c.deviceID,
			input.format,
			bestWidth,
			bestHeight,
			input.mode.FrameRate,
			previewWidth,
			previewHeight,
			c.cfg.encoder,
		)
	}

	go c.logFFmpeg(stderr)
	return nil
}

func (c *Camera) snapshotProcess() (*exec.Cmd, []net.Listener) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	listeners := append([]net.Listener(nil), c.listeners...)
	return c.cmd, listeners
}

func (c *Camera) clearProcess(cmd *exec.Cmd) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.cmd == cmd {
		c.cmd = nil
		c.listeners = nil
	}
}

func (c *Camera) closeProcess() {
	c.stateMu.Lock()
	cmd := c.cmd
	listeners := append([]net.Listener(nil), c.listeners...)
	c.cmd = nil
	c.listeners = nil
	c.stateMu.Unlock()
	for _, listener := range listeners {
		if listener != nil {
			_ = listener.Close()
		}
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func resolveInputWithFallback(deviceID, fallbackDisplayName string, cfg options) (resolvedInput, string, error) {
	var lastErr error
	for _, candidate := range resolveInputCandidates(deviceID, fallbackDisplayName) {
		input, err := resolveInput(candidate, cfg)
		if err == nil {
			return input, candidate, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("usbauto: unable to resolve input")
	}
	return resolvedInput{}, "", lastErr
}

func resolveInputCandidates(deviceID, fallbackDisplayName string) []string {
	candidates := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, item := range []string{strings.TrimSpace(deviceID), strings.TrimSpace(fallbackDisplayName)} {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		candidates = append(candidates, item)
	}
	return candidates
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func buildFFmpegArgs(input resolvedInput, cfg options, previewHeight int, targets []string) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-f", input.format}
	args = append(args, input.args...)
	args = append(args, "-i", formatInputDevice(input.format, input.device))

	sharedFilters := make([]string, 0, 2)
	if input.mode.FrameRate > 0 {
		sharedFilters = append(sharedFilters, "fps="+strconv.FormatFloat(input.mode.FrameRate, 'f', -1, 64))
	}
	sharedFilters = append(sharedFilters, "format=yuv420p")
	previewFilter := "null"
	if previewHeight > 0 && (input.mode.Height == 0 || previewHeight < input.mode.Height) {
		previewFilter = fmt.Sprintf("scale=-2:%d", previewHeight)
	}
	args = append(args, "-filter_complex", fmt.Sprintf("[0:v]%s,split=2[s0][s1];[s0]null[v0];[s1]%s[v1]", strings.Join(sharedFilters, ","), previewFilter))

	gop := gopForFPS(input.mode.FrameRate)
	for idx, target := range targets {
		args = append(args, "-map", fmt.Sprintf("[v%d]", idx), "-an", "-c:v", cfg.encoder)
		args = append(args, append([]string(nil), cfg.encoderArgs...)...)
		if cfg.h264Profile != "" {
			args = append(args, "-profile:v", cfg.h264Profile)
		}
		args = append(args, "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0", "-bf", "0")
		switch idx {
		case 0:
			appendBitrateArgs(&args, cfg.bestBitrateKbps)
		case 1:
			appendBitrateArgs(&args, cfg.previewBitrateKbps)
		}
		args = append(args, "-f", "h264", target)
	}
	return args
}

func appendBitrateArgs(args *[]string, bitrateKbps int) {
	if bitrateKbps <= 0 {
		return
	}
	rate := fmt.Sprintf("%dk", bitrateKbps)
	*args = append(*args, "-b:v", rate, "-maxrate", rate, "-bufsize", fmt.Sprintf("%dk", bitrateKbps*2))
}

func formatInputDevice(format, device string) string {
	if strings.EqualFold(strings.TrimSpace(format), "dshow") && device != "" && !strings.Contains(device, "=") {
		return "video=" + device
	}
	return device
}

func gopForFPS(fps float64) int {
	if fps <= 0 {
		return 10
	}
	gop := int(math.Round(fps))
	if gop < 1 {
		return 10
	}
	return gop
}

func frameDuration90k(fps float64) uint32 {
	if fps <= 0 {
		fps = 10
	}
	dur := uint32(math.Round(90000 / fps))
	if dur == 0 {
		return 9000
	}
	return dur
}

func derivePreviewDimensions(width, height, previewHeight int) (int, int) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	if previewHeight <= 0 || height <= previewHeight {
		return width, height
	}
	scaledWidth := int(math.Round(float64(width) * (float64(previewHeight) / float64(height))))
	if scaledWidth%2 != 0 {
		scaledWidth--
	}
	if scaledWidth < 2 {
		scaledWidth = 2
	}
	return scaledWidth, previewHeight
}

func sendLatest(ch chan gwebrtp.H264AccessUnit, item gwebrtp.H264AccessUnit) {
	select {
	case ch <- item:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- item:
	default:
	}
}

func cloneAccessUnit(src gwebrtp.H264AccessUnit) gwebrtp.H264AccessUnit {
	dst := gwebrtp.H264AccessUnit{PTS90k: src.PTS90k}
	if len(src.NALUs) == 0 {
		return dst
	}
	dst.NALUs = make([][]byte, 0, len(src.NALUs))
	for _, nalu := range src.NALUs {
		dst.NALUs = append(dst.NALUs, append([]byte(nil), nalu...))
	}
	return dst
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (c *Camera) logFFmpeg(stderrReader io.Reader) {
	if stderrReader == nil {
		return
	}
	scanner := bufio.NewScanner(stderrReader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && c.logger != nil {
			c.logger.Printf("usbauto ffmpeg: %s", line)
		}
	}
}
