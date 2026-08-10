package webrtp

import (
	"context"
)

func (r *Instance) connectRtspFFmpeg(ctx context.Context) (*ffmpegSourceConn, error) {
	conn, err := r.connectFFmpegH264Source(ctx, rtspFFmpegArgs(r.cfg), "rtsp ffmpeg", r.cfg.FrameRate)
	if err != nil {
		return nil, err
	}
	r.logger.Printf("RTSP FFmpeg transcode active (%s, profile=%s)", r.cfg.Rtsp, r.cfg.H264Profile)
	return conn, nil
}

func rtspFFmpegArgs(cfg *Config) []string {
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", cfg.Rtsp,
	}
	return append(args, ffmpegH264OutputArgs(cfg)...)
}
