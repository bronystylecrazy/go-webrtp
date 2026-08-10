package webrtp

import (
	"reflect"
	"testing"
)

func TestRtspFFmpegArgs(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want []string
	}{
		{
			name: "baselineWithModeAndBitrate",
			cfg: &Config{
				Rtsp:        "rtsp://example/stream",
				H264Profile: "baseline",
				Width:       1280,
				Height:      720,
				FrameRate:   30,
				BitrateKbps: 2000,
			},
			want: []string{
				"-hide_banner", "-loglevel", "warning",
				"-rtsp_transport", "tcp",
				"-i", "rtsp://example/stream",
				"-map", "0:v:0", "-an",
				"-vf", "scale=1280:720",
				"-r", "30",
				"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
				"-profile:v", "baseline",
				"-b:v", "2000k", "-maxrate", "2000k", "-bufsize", "4000k",
				"-g", "60", "-keyint_min", "60", "-bf", "0",
				"-x264-params", "repeat-headers=1:aud=1:scenecut=0:cabac=0",
				"-f", "h264", "-",
			},
		},
		{
			name: "defaultsWithoutModeOrBitrate",
			cfg: &Config{
				Rtsp: "rtsp://example/other",
			},
			want: []string{
				"-hide_banner", "-loglevel", "warning",
				"-rtsp_transport", "tcp",
				"-i", "rtsp://example/other",
				"-map", "0:v:0", "-an",
				"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
				"-g", "60", "-keyint_min", "60", "-bf", "0",
				"-x264-params", "repeat-headers=1:aud=1:scenecut=0",
				"-f", "h264", "-",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rtspFFmpegArgs(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rtspFFmpegArgs mismatch\ngot:  %v\nwant: %v", got, tc.want)
			}
		})
	}
}
