package webrtp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

type rtspFFmpegConn struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	done   chan struct{}
}

func (r *rtspFFmpegConn) Done() <-chan struct{} {
	return r.done
}

func (r *rtspFFmpegConn) Close() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
}

func (r *Instance) connectRtspFFmpeg(ctx context.Context) (*rtspFFmpegConn, error) {
	ffmpegCtx, cancel := context.WithCancel(ctx)
	args := rtspFFmpegArgs(r.cfg)
	cmd := exec.CommandContext(ffmpegCtx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("ffmpeg stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	conn := &rtspFFmpegConn{
		cancel: cancel,
		cmd:    cmd,
		done:   make(chan struct{}),
	}
	handler := &videoHandler{hub: r.hub, logger: r.logger, instance: r}
	fps := r.cfg.FrameRate
	if fps <= 0 {
		fps = 30
	}
	frameDur := uint32(90000 / fps)
	if frameDur == 0 {
		frameDur = 3000
	}

	r.logger.Printf("RTSP FFmpeg transcode active (%s, profile=%s)", r.cfg.Rtsp, r.cfg.H264Profile)

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
				if err != io.EOF && ffmpegCtx.Err() == nil {
					r.logger.Printf("rtsp ffmpeg read failed: %v", err)
				}
				break
			}
			if len(au) == 0 {
				continue
			}
			handler.processH264(au, ts, nil, nil)
			ts += frameDur
		}

		if err := cmd.Wait(); err != nil && ffmpegCtx.Err() == nil {
			r.logger.Printf("ffmpeg exited: %v", err)
		}
	}()

	return conn, nil
}

func rtspFFmpegArgs(cfg *Config) []string {
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", cfg.Rtsp,
		"-map", "0:v:0", "-an",
	}
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
	gop := int(cfg.FrameRate * 2)
	if gop < 1 {
		gop = 60
	}
	args = append(args, "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-bf", "0")
	x264Params := []string{"repeat-headers=1", "aud=1", "scenecut=0"}
	if cfg.H264Profile == "baseline" {
		x264Params = append(x264Params, "cabac=0")
	}
	args = append(args, "-x264-params", strings.Join(x264Params, ":"), "-f", "h264", "-")
	return args
}
