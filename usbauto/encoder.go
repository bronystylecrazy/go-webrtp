package usbauto

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

var encoderSupportCache sync.Map
var currentGOOS = runtime.GOOS
var encoderProbe = ffmpegSupportsEncoder

// * a working probe returns in well under a second, so this only bounds a driver that hangs on init
const encoderProbeTimeout = 8 * time.Second

func resolveEncoder(cfg options) options {
	if strings.TrimSpace(cfg.encoder) != "" {
		if len(cfg.encoderArgs) == 0 && strings.EqualFold(cfg.encoder, "libx264") {
			cfg.encoderArgs = defaultLibx264Args()
		}
		return cfg
	}

	for _, candidate := range hardwareEncoderCandidates(currentGOOS) {
		if encoderProbe(candidate) {
			cfg.encoder = candidate
			cfg.encoderArgs = nil
			return cfg
		}
	}

	cfg.encoder = "libx264"
	cfg.encoderArgs = defaultLibx264Args()
	return cfg
}

// * hardware encoders that accept software frames, so they drop into the existing filter chain unchanged
func hardwareEncoderCandidates(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"h264_videotoolbox"}
	case "windows":
		// * nvidia, then intel onboard, then amd
		return []string{"h264_nvenc", "h264_qsv", "h264_amf"}
	case "linux":
		// * nvidia, then intel onboard; vaapi is excluded because it needs an explicit device
		// * plus a hwupload filter, so amd on linux falls back to software
		return []string{"h264_nvenc", "h264_qsv"}
	default:
		return nil
	}
}

func defaultLibx264Args() []string {
	return []string{"-preset", "veryfast", "-tune", "zerolatency"}
}

func ffmpegSupportsEncoder(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if cached, ok := encoderSupportCache.Load(name); ok {
		return cached.(bool)
	}

	supported := ffmpegEncoderListed(ffmpegEncoderList(), name) && ffmpegEncoderRuns(name)
	encoderSupportCache.Store(name, supported)
	return supported
}

var ffmpegEncoderListOnce sync.Once
var ffmpegEncoderListOutput string

func ffmpegEncoderList() string {
	ffmpegEncoderListOnce.Do(func() {
		cmd := exec.Command("ffmpeg", "-hide_banner", "-encoders")
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout
		if err := cmd.Run(); err == nil {
			ffmpegEncoderListOutput = stdout.String()
		}
	})
	return ffmpegEncoderListOutput
}

// * the encoder name is the second column of `ffmpeg -encoders`, after the capability flags
func ffmpegEncoderListed(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == name {
			return true
		}
	}
	return false
}

// * being listed only means ffmpeg was built with the encoder, not that the hardware is present,
// * so a real frame is encoded before the encoder is trusted
func ffmpegEncoderRuns(name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), encoderProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", ffmpegEncoderRunArgs(name)...)
	return cmd.Run() == nil
}

func ffmpegEncoderRunArgs(name string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=128x128:r=1:d=1",
		"-frames:v", "1", "-c:v", name,
		"-f", "null", "-",
	}
}
