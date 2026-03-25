package streamcore

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	gwebrtp "github.com/bronystylecrazy/go-webrtp"
)

type usbFFmpegOutput struct {
	stream    *Stream
	upstream  *Upstream
	rendition *Rendition
}

type usbFFmpegConn struct {
	cancel    context.CancelFunc
	cmd       *exec.Cmd
	done      chan struct{}
	listeners []net.Listener
	closeOnce sync.Once
}

func (c *usbFFmpegConn) Close() {
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		for _, listener := range c.listeners {
			if listener != nil {
				_ = listener.Close()
			}
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
	})
}

func (c *usbFFmpegConn) Done() <-chan struct{} {
	return c.done
}

func (m *Manager) groupCreateUSBFFmpeg(index int, upstream *Upstream) (*Group, error) {
	groupName := UpstreamName(index, upstream)
	group := &Group{
		Name:     groupName,
		Upstream: upstream,
		Streams:  make([]*Stream, 0),
	}

	outputs := make([]*usbFFmpegOutput, 0, max(1, len(upstream.Renditions)))
	if len(upstream.Renditions) == 0 {
		stream, err := m.streamCreateManual(groupName, "", upstream)
		if err != nil {
			return nil, err
		}
		group.Streams = append(group.Streams, stream)
		group.Default = stream
		outputs = append(outputs, &usbFFmpegOutput{stream: stream, upstream: upstream})
	} else {
		defaultIdx := RenditionDefaultIndex(upstream.Renditions)
		for idx, rendition := range upstream.Renditions {
			streamUpstream := UpstreamWithRendition(upstream, rendition)
			stream, err := m.streamCreateManual(groupName, rendition.Name, streamUpstream)
			if err != nil {
				for _, item := range group.Streams {
					_ = item.Stop()
				}
				return nil, err
			}
			group.Streams = append(group.Streams, stream)
			outputs = append(outputs, &usbFFmpegOutput{stream: stream, upstream: streamUpstream, rendition: rendition})
			if idx == defaultIdx {
				group.Default = stream
			}
		}
	}
	if group.Default == nil && len(group.Streams) > 0 {
		group.Default = group.Streams[0]
	}

	logger := log.New(globalLogWriter{}, fmt.Sprintf("[%s] ", groupName), log.LstdFlags)
	controller := &sharedSourceController{
		logger: logger,
	}
	controller.streams = group.Streams
	controller.startFn = func(ctx context.Context) (sharedSourceConn, error) {
		return startUSBFFmpegSource(ctx, upstream, outputs, logger)
	}
	for _, stream := range group.Streams {
		stream.shared = controller
	}
	if !upstream.OnDemand {
		controller.EnsureStarted()
	}
	return group, nil
}

func (m *Manager) streamCreateManual(groupName, renditionName string, upstream *Upstream) (*Stream, error) {
	loggerName := groupName
	streamName := groupName
	if renditionName != "" {
		streamName = VariantName(groupName, renditionName)
		loggerName = fmt.Sprintf("%s/%s", groupName, renditionName)
	}
	logger := log.New(globalLogWriter{}, fmt.Sprintf("[%s] ", loggerName), log.LstdFlags)
	frameRate := 0.0
	if upstream.FrameRate != nil {
		frameRate = *upstream.FrameRate
	}
	bitrateKbps := 0
	if upstream.BitrateKbps != nil {
		bitrateKbps = *upstream.BitrateKbps
	}
	inst := gwebrtp.Init(&gwebrtp.Config{
		SourceType:        UpstreamSourceType(upstream),
		StreamName:        streamName,
		Device:            upstream.Device,
		Codec:             upstream.Codec,
		H264Profile:       valueOrEmpty(upstream.H264Profile),
		Width:             valueOrZero(upstream.Width),
		Height:            valueOrZero(upstream.Height),
		FrameRate:         frameRate,
		BitrateKbps:       bitrateKbps,
		KeyframeSink:      upstream.KeyframeSink,
		KeyframeOutput:    upstream.KeyframeOutput,
		KeyframeFormat:    upstream.KeyframeFormat,
		KeyframeMqttURL:   upstream.KeyframeMqttURL,
		KeyframeMqttTopic: upstream.KeyframeMqttTopic,
		Keyframer:         upstream.Keyframer,
		Logger:            logger,
	})
	return &Stream{
		Name:          streamName,
		GroupName:     groupName,
		RenditionName: renditionName,
		URL:           upstream.Device,
		Inst:          inst,
		Hub:           inst.GetHub(),
		Stop:          inst.Stop,
		OnDemand:      upstream.OnDemand,
	}, nil
}

func startUSBFFmpegSource(ctx context.Context, upstream *Upstream, outputs []*usbFFmpegOutput, logger globalLogger) (*usbFFmpegConn, error) {
	ffmpegCtx, cancel := context.WithCancel(ctx)
	listeners := make([]net.Listener, 0, len(outputs))
	targets := make([]string, 0, len(outputs))
	for range outputs {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			cancel()
			for _, item := range listeners {
				_ = item.Close()
			}
			return nil, fmt.Errorf("listen ffmpeg output: %w", err)
		}
		listeners = append(listeners, listener)
		targets = append(targets, "tcp://"+listener.Addr().String())
	}

	args := usbFFmpegArgs(upstream, outputs, targets)
	cmd := exec.CommandContext(ffmpegCtx, "ffmpeg", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil, fmt.Errorf("ffmpeg stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	conn := &usbFFmpegConn{
		cancel:    cancel,
		cmd:       cmd,
		done:      make(chan struct{}),
		listeners: listeners,
	}

	if logger != nil {
		logger.Printf("USB FFmpeg stream active (%s, outputs=%d, encoder=%s)", upstream.Device, len(outputs), defaultUSBFFmpegEncoder(upstream))
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && logger != nil {
				logger.Printf("ffmpeg: %s", line)
			}
		}
	}()

	go func() {
		defer close(conn.done)
		defer conn.Close()

		var wg sync.WaitGroup
		for idx, listener := range listeners {
			output := outputs[idx]
			target := targets[idx]
			wg.Add(1)
			go func(listener net.Listener, output *usbFFmpegOutput, target string) {
				defer wg.Done()
				handleUSBFFmpegOutput(ffmpegCtx, listener, output, target, logger)
			}(listener, output, target)
		}

		if err := cmd.Wait(); err != nil && ffmpegCtx.Err() == nil && logger != nil {
			logger.Printf("ffmpeg exited: %v", err)
		}
		cancel()
		for _, listener := range listeners {
			_ = listener.Close()
		}
		wg.Wait()
	}()

	return conn, nil
}

func handleUSBFFmpegOutput(ctx context.Context, listener net.Listener, output *usbFFmpegOutput, target string, logger globalLogger) {
	if listener == nil || output == nil || output.stream == nil || output.stream.Inst == nil {
		return
	}
	conn, err := listener.Accept()
	if err != nil {
		if ctx.Err() == nil && logger != nil {
			logger.Printf("ffmpeg output accept failed (%s): %v", target, err)
		}
		return
	}
	defer conn.Close()

	frameDur := usbFFmpegFrameDuration(output.upstream)
	ts := uint32(0)
	err = gwebrtp.EachH264AccessUnit(conn, func(au [][]byte) error {
		output.stream.Inst.PublishH264AccessUnit(au, ts)
		ts += frameDur
		return nil
	})
	if err != nil && ctx.Err() == nil && logger != nil {
		logger.Printf("ffmpeg output read failed (%s): %v", target, err)
	}
}

func usbFFmpegArgs(upstream *Upstream, outputs []*usbFFmpegOutput, targets []string) []string {
	args := []string{"-hide_banner", "-loglevel", "warning"}
	if format := strings.TrimSpace(upstream.FFmpegInputFormat); format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, append([]string(nil), upstream.FFmpegInputArgs...)...)
	args = append(args, "-i", usbFFmpegInputDevice(upstream))

	if len(outputs) == 1 {
		if filters := usbFFmpegSingleOutputFilter(outputs[0], upstream.FFmpegFilter); filters != "" {
			args = append(args, "-vf", filters)
		}
	} else if filters := usbFFmpegFilterComplex(outputs, upstream.FFmpegFilter); filters != "" {
		args = append(args, "-filter_complex", filters)
	}

	encoder := defaultUSBFFmpegEncoder(upstream)
	for idx, output := range outputs {
		if len(outputs) == 1 {
			args = append(args, "-map", "0:v:0")
		} else {
			args = append(args, "-map", fmt.Sprintf("[v%d]", idx))
		}
		args = append(args, "-an", "-c:v", encoder)
		args = append(args, append([]string(nil), upstream.FFmpegEncoderArgs...)...)
		if profile := valueOrEmpty(upstream.H264Profile); profile != "" {
			args = append(args, "-profile:v", profile)
		}
		gop := usbFFmpegGOP(output.upstream)
		args = append(args, "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0", "-bf", "0")
		if bitrate := valueOrZero(output.upstream.BitrateKbps); bitrate > 0 {
			rate := fmt.Sprintf("%dk", bitrate)
			args = append(args, "-b:v", rate, "-maxrate", rate, "-bufsize", fmt.Sprintf("%dk", bitrate*2))
		}
		args = append(args, "-f", "h264", targets[idx])
	}
	return args
}

func usbFFmpegInputDevice(upstream *Upstream) string {
	device := strings.TrimSpace(upstream.Device)
	if strings.EqualFold(strings.TrimSpace(upstream.FFmpegInputFormat), "dshow") && device != "" && !strings.Contains(device, "=") {
		return "video=" + device
	}
	return device
}

func usbFFmpegSingleOutputFilter(output *usbFFmpegOutput, base string) string {
	filters := make([]string, 0, 3)
	if base = strings.TrimSpace(base); base != "" {
		filters = append(filters, base)
	}
	if output != nil && output.rendition != nil {
		if filter := strings.TrimSpace(output.rendition.FFmpegFilter); filter != "" {
			filters = append(filters, filter)
		}
	}
	if scale := usbFFmpegScaleFilter(output.upstream); scale != "" {
		filters = append(filters, scale)
	}
	return strings.Join(filters, ",")
}

func usbFFmpegFilterComplex(outputs []*usbFFmpegOutput, base string) string {
	parts := make([]string, 0, len(outputs)+1)
	input := "[0:v]"
	if base = strings.TrimSpace(base); base != "" {
		input += base + ","
	}
	splitLabels := make([]string, 0, len(outputs))
	for idx := range outputs {
		splitLabels = append(splitLabels, fmt.Sprintf("[s%d]", idx))
	}
	parts = append(parts, input+fmt.Sprintf("split=%d", len(outputs))+strings.Join(splitLabels, ""))
	for idx, output := range outputs {
		filters := make([]string, 0, 2)
		if output != nil && output.rendition != nil {
			if filter := strings.TrimSpace(output.rendition.FFmpegFilter); filter != "" {
				filters = append(filters, filter)
			}
		}
		if scale := usbFFmpegScaleFilter(output.upstream); scale != "" {
			filters = append(filters, scale)
		}
		if len(filters) == 0 {
			parts = append(parts, fmt.Sprintf("[s%d]null[v%d]", idx, idx))
			continue
		}
		parts = append(parts, fmt.Sprintf("[s%d]%s[v%d]", idx, strings.Join(filters, ","), idx))
	}
	return strings.Join(parts, ";")
}

func usbFFmpegScaleFilter(upstream *Upstream) string {
	if upstream == nil || (upstream.Width == nil && upstream.Height == nil) {
		return ""
	}
	width := -2
	height := -2
	if upstream.Width != nil && *upstream.Width > 0 {
		width = *upstream.Width
	}
	if upstream.Height != nil && *upstream.Height > 0 {
		height = *upstream.Height
	}
	return fmt.Sprintf("scale=%d:%d", width, height)
}

func defaultUSBFFmpegEncoder(upstream *Upstream) string {
	if upstream == nil || strings.TrimSpace(upstream.FFmpegEncoder) == "" {
		return "libx264"
	}
	return strings.TrimSpace(upstream.FFmpegEncoder)
}

func usbFFmpegFrameDuration(upstream *Upstream) uint32 {
	fps := 30.0
	if upstream != nil && upstream.FrameRate != nil && *upstream.FrameRate > 0 {
		fps = *upstream.FrameRate
	}
	frameDur := uint32(math.Round(90000 / fps))
	if frameDur == 0 {
		return 3000
	}
	return frameDur
}

func usbFFmpegGOP(upstream *Upstream) int {
	fps := 30.0
	if upstream != nil && upstream.FrameRate != nil && *upstream.FrameRate > 0 {
		fps = *upstream.FrameRate
	}
	gop := int(math.Round(fps))
	if gop < 1 {
		return 30
	}
	return gop
}
