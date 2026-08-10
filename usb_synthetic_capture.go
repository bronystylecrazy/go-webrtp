package webrtp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (r *Instance) connectSynthetic(ctx context.Context, device *SyntheticDevice) (*ffmpegSourceConn, error) {
	codec := strings.ToLower(strings.TrimSpace(r.cfg.Codec))
	if codec != "" && codec != "h264" {
		return nil, fmt.Errorf("synthetic device %s only supports codec h264", device.Id)
	}
	cfg := syntheticCaptureConfig(r.cfg, device)
	conn, err := r.connectFFmpegH264Source(ctx, syntheticFFmpegArgs(cfg, device), "synthetic ffmpeg", cfg.FrameRate)
	if err != nil {
		return nil, err
	}
	r.logger.Printf("Synthetic stream active (%s, type=%s)", device.Id, device.Type)
	return conn, nil
}

func syntheticCaptureConfig(cfg *Config, device *SyntheticDevice) *Config {
	resolved := *cfg
	if resolved.Width <= 0 || resolved.Height <= 0 {
		resolved.Width = device.Width
		resolved.Height = device.Height
	}
	if resolved.FrameRate <= 0 {
		resolved.FrameRate = device.FrameRate
	}
	return &resolved
}

func syntheticFFmpegArgs(cfg *Config, device *SyntheticDevice) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-re"}
	switch device.Type {
	case SyntheticDeviceTypeFile:
		args = append(args, "-stream_loop", "-1", "-i", device.Path)
	default:
		source := fmt.Sprintf("testsrc2=size=%dx%d:rate=%s", device.Width, device.Height, strconv.FormatFloat(device.FrameRate, 'f', -1, 64))
		args = append(args, "-f", "lavfi", "-i", source)
	}
	return append(args, ffmpegH264OutputArgs(cfg)...)
}
