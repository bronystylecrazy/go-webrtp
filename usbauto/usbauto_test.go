package usbauto

import (
	"reflect"
	"strings"
	"testing"

	gwebrtp "github.com/bronystylecrazy/go-webrtp"
)

func TestSelectBestModePrefersHighestResolutionAtTargetFPS(t *testing.T) {
	modes := []*gwebrtp.UsbCapabilityMode{
		{Width: 3840, Height: 2160, Fps: []float64{10}},
		{Width: 1920, Height: 1080, Fps: []float64{30}},
		{Width: 1280, Height: 720, Fps: []float64{60}},
	}

	got := selectBestMode(modes, 10)
	if got.Width != 3840 || got.Height != 2160 || got.FrameRate != 10 {
		t.Fatalf("unexpected mode: %+v", got)
	}
}

func TestSelectBestModeFallsBackWhenHighestModeMissesTargetFPS(t *testing.T) {
	modes := []*gwebrtp.UsbCapabilityMode{
		{Width: 3840, Height: 2160, Fps: []float64{5}},
		{Width: 2560, Height: 1440, Fps: []float64{10}},
		{Width: 1920, Height: 1080, Fps: []float64{30}},
	}

	got := selectBestMode(modes, 10)
	if got.Width != 2560 || got.Height != 1440 || got.FrameRate != 10 {
		t.Fatalf("unexpected mode: %+v", got)
	}
}

func TestSelectBestModeWithoutFPSPreferencePicksHighestResolution(t *testing.T) {
	modes := []*gwebrtp.UsbCapabilityMode{
		{Width: 3840, Height: 2160, Fps: []float64{5}},
		{Width: 2560, Height: 1440, Fps: []float64{10}},
		{Width: 1920, Height: 1080, Fps: []float64{30}},
	}

	got := selectBestMode(modes, 0)
	if got.Width != 3840 || got.Height != 2160 || got.FrameRate != 5 {
		t.Fatalf("unexpected mode: %+v", got)
	}
}

func TestDerivePreviewDimensionsPreservesAspectRatio(t *testing.T) {
	width, height := derivePreviewDimensions(4000, 3000, 720)
	if width != 960 || height != 720 {
		t.Fatalf("unexpected preview dimensions: %dx%d", width, height)
	}
}

func TestParseV4L2Formats(t *testing.T) {
	raw := `
[0]: 'MJPG' (Motion-JPEG, compressed)
	Size: Discrete 3840x2160
		Interval: Discrete 0.100s (10.000 fps)
		Interval: Discrete 0.200s (5.000 fps)
	Size: Discrete 1280x720
		Interval: Discrete 0.033s (30.000 fps)
[1]: 'YUYV' (YUYV 4:2:2)
	Size: Discrete 640x480
		Interval: Discrete 0.033s (30.000 fps)
`

	got := parseV4L2Formats(raw)
	want := []linuxModeCandidate{
		{mode: Mode{Width: 3840, Height: 2160, FrameRate: 10}, codec: "mjpeg"},
		{mode: Mode{Width: 1280, Height: 720, FrameRate: 30}, codec: "mjpeg"},
		{mode: Mode{Width: 640, Height: 480, FrameRate: 30}, codec: "yuyv422"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildInputArgsUsesPlatformSpecificCodecFlag(t *testing.T) {
	got := buildInputArgs("v4l2", Mode{Width: 3840, Height: 2160, FrameRate: 10}, "mjpeg", []string{"-thread_queue_size", "64"})
	want := []string{"-framerate", "10", "-video_size", "3840x2160", "-input_format", "mjpeg", "-thread_queue_size", "64"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildInputArgsUsesPixelFormatForAVFoundation(t *testing.T) {
	got := buildInputArgs("avfoundation", Mode{Width: 3840, Height: 2160, FrameRate: 10}, "uyvy422", nil)
	want := []string{"-framerate", "10", "-video_size", "3840x2160", "-pixel_format", "uyvy422"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestSelectDarwinPixelFormatPrefersModeSupportedFormat(t *testing.T) {
	caps := &gwebrtp.UsbDeviceCapabilities{
		Modes: []*gwebrtp.UsbCapabilityMode{
			{Width: 3840, Height: 2160, PixelFormats: []string{"nv12", "uyvy422"}},
		},
	}

	got := selectDarwinPixelFormat(caps, Mode{Width: 3840, Height: 2160, FrameRate: 10})
	if got != "uyvy422" {
		t.Fatalf("expected uyvy422 for 4k mode, got %q", got)
	}
}

func TestSelectDarwinPixelFormatNormalizesNV12Variants(t *testing.T) {
	caps := &gwebrtp.UsbDeviceCapabilities{
		Modes: []*gwebrtp.UsbCapabilityMode{
			{Width: 1280, Height: 720, PixelFormats: []string{"nv12-video", "bgr0"}},
		},
	}

	got := selectDarwinPixelFormat(caps, Mode{Width: 1280, Height: 720, FrameRate: 30})
	if got != "nv12" {
		t.Fatalf("expected nv12 normalization, got %q", got)
	}
}

func TestSelectDarwinPixelFormatFallsBackByResolution(t *testing.T) {
	if got := selectDarwinPixelFormat(nil, Mode{Width: 3840, Height: 2160}); got != "uyvy422" {
		t.Fatalf("expected 4k fallback to uyvy422, got %q", got)
	}
	if got := selectDarwinPixelFormat(nil, Mode{Width: 1280, Height: 720}); got != "nv12" {
		t.Fatalf("expected 720p fallback to nv12, got %q", got)
	}
}

func TestSelectWindowsInputCodecPrefersMJPEGWhenNoNativeH26x(t *testing.T) {
	caps := &gwebrtp.UsbDeviceCapabilities{
		Modes: []*gwebrtp.UsbCapabilityMode{
			{Width: 3840, Height: 2160, PixelFormats: []string{"mjpeg", "yuyv422"}},
		},
	}

	got := selectWindowsInputCodec(caps, Mode{Width: 3840, Height: 2160, FrameRate: 10})
	if got != "mjpeg" {
		t.Fatalf("expected mjpeg, got %q", got)
	}
}

func TestSelectWindowsInputCodecDoesNotOverrideNativeH264(t *testing.T) {
	caps := &gwebrtp.UsbDeviceCapabilities{
		Modes: []*gwebrtp.UsbCapabilityMode{
			{Width: 3840, Height: 2160, PixelFormats: []string{"h264", "mjpeg"}},
		},
	}

	got := selectWindowsInputCodec(caps, Mode{Width: 3840, Height: 2160, FrameRate: 10})
	if got != "" {
		t.Fatalf("expected no override when native h264 exists, got %q", got)
	}
}

func TestBuildFFmpegArgsPinsSharedFPS(t *testing.T) {
	args := buildFFmpegArgs(
		resolvedInput{
			format: "avfoundation",
			device: "0:none",
			args:   []string{"-framerate", "10", "-video_size", "3840x2160"},
			mode:   Mode{Width: 3840, Height: 2160, FrameRate: 10},
		},
		defaultOptions(),
		720,
		[]string{"tcp://127.0.0.1:1000", "tcp://127.0.0.1:1001"},
	)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "[0:v]fps=10,format=yuv420p,split=2") {
		t.Fatalf("expected filter graph to pin fps, got %q", joined)
	}
}

func TestResolveEncoderPrefersVideoToolboxOnDarwin(t *testing.T) {
	prevGOOS := currentGOOS
	prevProbe := encoderProbe
	t.Cleanup(func() {
		currentGOOS = prevGOOS
		encoderProbe = prevProbe
	})

	currentGOOS = "darwin"
	encoderProbe = func(name string) bool {
		return name == "h264_videotoolbox"
	}

	got := resolveEncoder(defaultOptions())
	if got.encoder != "h264_videotoolbox" {
		t.Fatalf("expected videotoolbox encoder, got %q", got.encoder)
	}
	if len(got.encoderArgs) != 0 {
		t.Fatalf("expected no default videotoolbox args, got %#v", got.encoderArgs)
	}
}

func TestResolveEncoderFallsBackToLibx264(t *testing.T) {
	prevGOOS := currentGOOS
	prevProbe := encoderProbe
	t.Cleanup(func() {
		currentGOOS = prevGOOS
		encoderProbe = prevProbe
	})

	currentGOOS = "darwin"
	encoderProbe = func(string) bool { return false }

	got := resolveEncoder(defaultOptions())
	if got.encoder != "libx264" {
		t.Fatalf("expected libx264 fallback, got %q", got.encoder)
	}
	wantArgs := []string{"-preset", "veryfast", "-tune", "zerolatency"}
	if !reflect.DeepEqual(got.encoderArgs, wantArgs) {
		t.Fatalf("unexpected libx264 args:\n got: %#v\nwant: %#v", got.encoderArgs, wantArgs)
	}
}

func TestResolveEncoderKeepsExplicitEncoder(t *testing.T) {
	prevGOOS := currentGOOS
	prevProbe := encoderProbe
	t.Cleanup(func() {
		currentGOOS = prevGOOS
		encoderProbe = prevProbe
	})

	currentGOOS = "darwin"
	encoderProbe = func(string) bool { return true }

	cfg := defaultOptions()
	WithEncoder("libx264")(&cfg)

	got := resolveEncoder(cfg)
	if got.encoder != "libx264" {
		t.Fatalf("expected explicit encoder to be preserved, got %q", got.encoder)
	}
	wantArgs := []string{"-preset", "veryfast", "-tune", "zerolatency"}
	if !reflect.DeepEqual(got.encoderArgs, wantArgs) {
		t.Fatalf("unexpected explicit libx264 args:\n got: %#v\nwant: %#v", got.encoderArgs, wantArgs)
	}
}

func TestResolveInputCandidatesIncludesDisplayNameFallback(t *testing.T) {
	got := resolveInputCandidates("device-id", "Camera Name")
	want := []string{"device-id", "Camera Name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestResolveInputCandidatesDeduplicatesAndSkipsEmpty(t *testing.T) {
	got := resolveInputCandidates("Camera Name", "Camera Name")
	want := []string{"Camera Name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates:\n got: %#v\nwant: %#v", got, want)
	}
}
