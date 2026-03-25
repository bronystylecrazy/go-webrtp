package usbauto

import (
	"bytes"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

var encoderSupportCache sync.Map
var currentGOOS = runtime.GOOS
var encoderProbe = ffmpegSupportsEncoder

func resolveEncoder(cfg options) options {
	if strings.TrimSpace(cfg.encoder) != "" {
		if len(cfg.encoderArgs) == 0 && strings.EqualFold(cfg.encoder, "libx264") {
			cfg.encoderArgs = defaultLibx264Args()
		}
		return cfg
	}

	if currentGOOS == "darwin" && encoderProbe("h264_videotoolbox") {
		cfg.encoder = "h264_videotoolbox"
		cfg.encoderArgs = nil
		return cfg
	}

	cfg.encoder = "libx264"
	cfg.encoderArgs = defaultLibx264Args()
	return cfg
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

	cmd := exec.Command("ffmpeg", "-hide_banner", "-encoders")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	supported := false
	if err := cmd.Run(); err == nil {
		for _, line := range strings.Split(stdout.String(), "\n") {
			if strings.Contains(line, name) {
				fields := strings.Fields(line)
				if len(fields) > 1 && fields[len(fields)-1] == name {
					supported = true
					break
				}
			}
		}
	}
	encoderSupportCache.Store(name, supported)
	return supported
}
