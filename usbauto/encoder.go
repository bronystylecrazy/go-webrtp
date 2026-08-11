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

	if selected := encoderSelect(hardwareEncoderCandidates(currentGOOS), encoderProbe); selected != "" {
		cfg.encoder = selected
		cfg.encoderArgs = nil
		return cfg
	}

	cfg.encoder = "libx264"
	cfg.encoderArgs = defaultLibx264Args()
	return cfg
}

// * probe every candidate concurrently but honour priority order, so a hung driver costs one timeout rather than one per candidate
func encoderSelect(candidates []string, probe func(string) bool) string {
	results := make([]chan bool, len(candidates))
	for idx, candidate := range candidates {
		result := make(chan bool, 1)
		results[idx] = result
		go func(name string, result chan<- bool) {
			result <- probe(name)
		}(candidate, result)
	}
	for idx, result := range results {
		if <-result {
			return candidates[idx]
		}
	}
	return ""
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

// * hardware encoders have no quality mode, so an unset bitrate needs a computed default while libx264 keeps crf
func encoderRequiresBitrate(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, goos := range []string{"darwin", "windows", "linux"} {
		for _, candidate := range hardwareEncoderCandidates(goos) {
			if strings.EqualFold(candidate, name) {
				return true
			}
		}
	}
	return false
}

// * same bits per pixel heuristic as the native media foundation encoder default
func defaultHardwareBitrateKbps(width, height int, fps float64) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	if fps <= 0 {
		fps = 30
	}
	kbps := int(float64(width) * float64(height) * fps * 0.12 / 1000)
	if kbps < 500 {
		return 500
	}
	// * the ceiling leaves 4k inspection streams enough headroom on a lan while bounding the formula
	if kbps > 25000 {
		return 25000
	}
	return kbps
}

func outputBitrateKbps(encoder string, configuredKbps, width, height int, fps float64) int {
	if configuredKbps > 0 {
		return configuredKbps
	}
	if !encoderRequiresBitrate(encoder) {
		return 0
	}
	return defaultHardwareBitrateKbps(width, height, fps)
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
