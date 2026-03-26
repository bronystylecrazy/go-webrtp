package usbauto

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	gwebrtp "github.com/bronystylecrazy/go-webrtp"
)

type resolvedInput struct {
	format      string
	device      string
	displayName string
	args        []string
	mode        Mode
}

type linuxModeCandidate struct {
	mode  Mode
	codec string
}

func resolveInput(deviceID string, cfg options) (resolvedInput, error) {
	format := strings.TrimSpace(cfg.inputFormat)
	if format == "" {
		format = defaultInputFormat()
	}

	inputDevice := deviceID
	displayName := deviceID
	mode := Mode{FrameRate: cfg.targetFPS}
	inputCodec := strings.TrimSpace(cfg.inputCodec)

	switch runtime.GOOS {
	case "darwin", "windows":
		caps, err := gwebrtp.UsbDeviceCapabilitiesGet(deviceID)
		if err != nil {
			return resolvedInput{}, fmt.Errorf("usbauto: query capabilities: %w", err)
		}
		mode = selectBestMode(caps.Modes, cfg.targetFPS)
		if mode.Width <= 0 || mode.Height <= 0 {
			return resolvedInput{}, fmt.Errorf("usbauto: no usable video modes reported for %s", deviceID)
		}
		if caps != nil && caps.Device != nil && strings.TrimSpace(caps.Device.Name) != "" {
			displayName = strings.TrimSpace(caps.Device.Name)
			inputDevice = strings.TrimSpace(caps.Device.Name)
		}
		if inputCodec == "" {
			inputCodec = selectAutoInputCodec(format, mode, caps)
		}
		if runtime.GOOS == "darwin" && !strings.Contains(inputDevice, ":") {
			inputDevice += ":none"
		}
	case "linux":
		candidates, err := probeLinuxModes(deviceID)
		if err != nil {
			return resolvedInput{}, err
		}
		selected := selectLinuxMode(candidates, cfg.targetFPS)
		if selected.mode.Width <= 0 || selected.mode.Height <= 0 {
			return resolvedInput{}, fmt.Errorf("usbauto: no usable v4l2 modes reported for %s", deviceID)
		}
		mode = selected.mode
		if inputCodec == "" {
			inputCodec = selected.codec
		}
	default:
		return resolvedInput{}, fmt.Errorf("usbauto: unsupported platform %s", runtime.GOOS)
	}

	if mode.FrameRate <= 0 {
		mode.FrameRate = cfg.targetFPS
		if mode.FrameRate <= 0 {
			mode.FrameRate = 10
		}
	}

	return resolvedInput{
		format:      format,
		device:      inputDevice,
		displayName: displayName,
		args:        buildInputArgs(format, mode, inputCodec, cfg.inputArgs),
		mode:        mode,
	}, nil
}

func selectAutoInputCodec(format string, mode Mode, caps *gwebrtp.UsbDeviceCapabilities) string {
	switch {
	case strings.EqualFold(format, "avfoundation"):
		return selectDarwinPixelFormat(caps, mode)
	case strings.EqualFold(format, "dshow"):
		return selectWindowsInputCodec(caps, mode)
	default:
		return ""
	}
}

func defaultInputFormat() string {
	switch runtime.GOOS {
	case "windows":
		return "dshow"
	case "darwin":
		return "avfoundation"
	case "linux":
		return "v4l2"
	default:
		return ""
	}
}

func buildInputArgs(format string, mode Mode, inputCodec string, extra []string) []string {
	args := make([]string, 0, 8+len(extra))
	if mode.FrameRate > 0 {
		args = append(args, "-framerate", strconv.FormatFloat(mode.FrameRate, 'f', -1, 64))
	}
	if mode.Width > 0 && mode.Height > 0 {
		args = append(args, "-video_size", fmt.Sprintf("%dx%d", mode.Width, mode.Height))
	}
	if inputCodec = strings.TrimSpace(inputCodec); inputCodec != "" {
		switch {
		case strings.EqualFold(format, "v4l2"):
			args = append(args, "-input_format", inputCodec)
		case strings.EqualFold(format, "avfoundation"):
			args = append(args, "-pixel_format", inputCodec)
		default:
			args = append(args, "-vcodec", inputCodec)
		}
	}
	args = append(args, append([]string(nil), extra...)...)
	return args
}

func selectDarwinPixelFormat(caps *gwebrtp.UsbDeviceCapabilities, mode Mode) string {
	if caps != nil {
		for _, candidate := range caps.Modes {
			if candidate == nil || candidate.Width != mode.Width || candidate.Height != mode.Height {
				continue
			}
			if selected := choosePreferredPixelFormat(candidate.PixelFormats, mode.Width, mode.Height); selected != "" {
				return selected
			}
		}
	}
	return defaultDarwinPixelFormat(mode.Width, mode.Height)
}

func choosePreferredPixelFormat(items []string, width, height int) string {
	if len(items) == 0 {
		return ""
	}
	preferred := []string{"nv12", "uyvy422", "yuyv422", "0rgb", "bgr0"}
	if width > 1920 || height > 1080 {
		preferred = []string{"uyvy422", "nv12", "yuyv422", "0rgb", "bgr0"}
	}
	available := make(map[string]string, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if lower == "nv12-full" || lower == "nv12-video" {
			lower = "nv12"
		}
		available[lower] = trimmed
	}
	for _, item := range preferred {
		if _, ok := available[item]; ok {
			return item
		}
	}
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			lower := strings.ToLower(trimmed)
			if lower == "nv12-full" || lower == "nv12-video" {
				return "nv12"
			}
			return lower
		}
	}
	return ""
}

func defaultDarwinPixelFormat(width, height int) string {
	if width > 1920 || height > 1080 {
		return "uyvy422"
	}
	return "nv12"
}

func selectWindowsInputCodec(caps *gwebrtp.UsbDeviceCapabilities, mode Mode) string {
	if caps == nil {
		return ""
	}
	available := make(map[string]bool)
	for _, candidate := range caps.Modes {
		if candidate == nil || candidate.Width != mode.Width || candidate.Height != mode.Height {
			continue
		}
		for _, item := range candidate.PixelFormats {
			label := strings.ToLower(strings.TrimSpace(item))
			if label == "" {
				continue
			}
			available[label] = true
		}
	}
	if len(available) == 0 {
		return ""
	}
	if available["h264"] || available["h265"] {
		return ""
	}
	if available["mjpeg"] {
		return "mjpeg"
	}
	return ""
}

func selectBestMode(modes []*gwebrtp.UsbCapabilityMode, targetFPS float64) Mode {
	best := selectBestModeWithFilter(modes, targetFPS, true)
	if best.Width > 0 && best.Height > 0 {
		return best
	}
	return selectBestModeWithFilter(modes, targetFPS, false)
}

func selectBestModeWithFilter(modes []*gwebrtp.UsbCapabilityMode, targetFPS float64, requireFPS bool) Mode {
	var best Mode
	bestArea := -1
	bestFPS := -1.0

	for _, mode := range modes {
		if mode == nil || mode.Width <= 0 || mode.Height <= 0 {
			continue
		}
		fps := highestFPS(mode.Fps)
		if requireFPS && targetFPS > 0 && fps > 0 && fps < targetFPS {
			continue
		}
		area := mode.Width * mode.Height
		if area > bestArea || (area == bestArea && fps > bestFPS) {
			best = Mode{Width: mode.Width, Height: mode.Height, FrameRate: fps}
			bestArea = area
			bestFPS = fps
		}
	}
	return best
}

func highestFPS(items []float64) float64 {
	best := 0.0
	for _, fps := range items {
		if fps > best {
			best = fps
		}
	}
	return best
}

func probeLinuxModes(deviceID string) ([]linuxModeCandidate, error) {
	if _, err := exec.LookPath("v4l2-ctl"); err != nil {
		return nil, fmt.Errorf("usbauto: linux auto probe requires v4l2-ctl: %w", err)
	}
	cmd := exec.Command("v4l2-ctl", "-d", deviceID, "--list-formats-ext")
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("usbauto: v4l2 capability probe failed: %s", msg)
	}
	candidates := parseV4L2Formats(string(output))
	if len(candidates) == 0 {
		return nil, fmt.Errorf("usbauto: no v4l2 modes parsed for %s", deviceID)
	}
	return candidates, nil
}

func selectLinuxMode(candidates []linuxModeCandidate, targetFPS float64) linuxModeCandidate {
	best := selectLinuxModeWithFilter(candidates, targetFPS, true)
	if best.mode.Width > 0 && best.mode.Height > 0 {
		return best
	}
	return selectLinuxModeWithFilter(candidates, targetFPS, false)
}

func selectLinuxModeWithFilter(candidates []linuxModeCandidate, targetFPS float64, requireFPS bool) linuxModeCandidate {
	var best linuxModeCandidate
	bestArea := -1
	bestFPS := -1.0
	bestCompressed := false

	for _, candidate := range candidates {
		if candidate.mode.Width <= 0 || candidate.mode.Height <= 0 {
			continue
		}
		fps := candidate.mode.FrameRate
		if requireFPS && targetFPS > 0 && fps > 0 && fps < targetFPS {
			continue
		}
		area := candidate.mode.Width * candidate.mode.Height
		compressed := isCompressedCodec(candidate.codec)
		if area > bestArea ||
			(area == bestArea && compressed && !bestCompressed) ||
			(area == bestArea && compressed == bestCompressed && fps > bestFPS) {
			best = candidate
			bestArea = area
			bestFPS = fps
			bestCompressed = compressed
		}
	}
	return best
}

func isCompressedCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "mjpeg", "h264", "hevc", "h265":
		return true
	default:
		return false
	}
}

var (
	v4l2FormatRE   = regexp.MustCompile(`\[\d+\]: '([^']+)'`)
	v4l2SizeRE     = regexp.MustCompile(`Size:\s+Discrete\s+(\d+)x(\d+)`)
	v4l2IntervalRE = regexp.MustCompile(`Interval:\s+Discrete\s+[0-9.]+s\s+\(([0-9.]+)\s+fps\)`)
)

func parseV4L2Formats(raw string) []linuxModeCandidate {
	lines := strings.Split(raw, "\n")
	items := make([]linuxModeCandidate, 0, 8)
	currentFormat := ""
	currentIndex := -1

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if match := v4l2FormatRE.FindStringSubmatch(line); len(match) == 2 {
			currentFormat = normalizeV4L2Codec(match[1])
			currentIndex = -1
			continue
		}
		if match := v4l2SizeRE.FindStringSubmatch(line); len(match) == 3 {
			width, _ := strconv.Atoi(match[1])
			height, _ := strconv.Atoi(match[2])
			items = append(items, linuxModeCandidate{
				mode:  Mode{Width: width, Height: height},
				codec: currentFormat,
			})
			currentIndex = len(items) - 1
			continue
		}
		if match := v4l2IntervalRE.FindStringSubmatch(line); len(match) == 2 && currentIndex >= 0 {
			fps, _ := strconv.ParseFloat(match[1], 64)
			if fps > items[currentIndex].mode.FrameRate {
				items[currentIndex].mode.FrameRate = fps
			}
		}
	}

	return items
}

func normalizeV4L2Codec(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "MJPG", "JPEG":
		return "mjpeg"
	case "H264":
		return "h264"
	case "HEVC", "H265":
		return "hevc"
	case "YUYV", "YUY2":
		return "yuyv422"
	case "UYVY":
		return "uyvy422"
	case "NV12":
		return "nv12"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}
