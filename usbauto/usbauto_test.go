package usbauto

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	gwebrtp "github.com/bronystylecrazy/go-webrtp"
)

func TestSelectBestModePrefersHighestResolutionAtTargetFPS(t *testing.T) {
	modes := []*gwebrtp.UsbCapabilityMode{
		{Width: 3840, Height: 2160, Fps: []float64{10}},
		{Width: 1920, Height: 1080, Fps: []float64{30}},
		{Width: 1280, Height: 720, Fps: []float64{60}},
	}

	got := selectBestMode(modes, 10, 0, 0)
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

	got := selectBestMode(modes, 10, 0, 0)
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

	got := selectBestMode(modes, 0, 0, 0)
	if got.Width != 3840 || got.Height != 2160 || got.FrameRate != 5 {
		t.Fatalf("unexpected mode: %+v", got)
	}
}

func TestSelectBestModeHonorsMaxResolution(t *testing.T) {
	modes := []*gwebrtp.UsbCapabilityMode{
		{Width: 3840, Height: 2160, Fps: []float64{10}},
		{Width: 2560, Height: 1440, Fps: []float64{30}},
		{Width: 1920, Height: 1080, Fps: []float64{60}},
	}

	got := selectBestMode(modes, 0, 3839, 2159)
	if got.Width != 2560 || got.Height != 1440 || got.FrameRate != 30 {
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

func TestSelectLinuxModeHonorsMaxResolution(t *testing.T) {
	candidates := []linuxModeCandidate{
		{mode: Mode{Width: 3840, Height: 2160, FrameRate: 10}, codec: "mjpeg"},
		{mode: Mode{Width: 2560, Height: 1440, FrameRate: 30}, codec: "mjpeg"},
		{mode: Mode{Width: 1920, Height: 1080, FrameRate: 60}, codec: "yuyv422"},
	}

	got := selectLinuxMode(candidates, 0, 3839, 2159)
	if got.mode.Width != 2560 || got.mode.Height != 1440 || got.mode.FrameRate != 30 {
		t.Fatalf("unexpected mode: %+v", got)
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
			output: Mode{Width: 3840, Height: 2160, FrameRate: 10},
		},
		defaultOptions(),
		720,
		[]string{"tcp://127.0.0.1:1000", "tcp://127.0.0.1:1001"},
	)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "[0:v]fps=10,format=nv12|yuv420p,split=2") {
		t.Fatalf("expected filter graph to pin fps, got %q", joined)
	}
}

func TestFpsForTarget(t *testing.T) {
	cases := []struct {
		name   string
		items  []float64
		target float64
		want   float64
	}{
		{name: "noTargetPicksHighest", items: []float64{30, 60}, target: 0, want: 60},
		{name: "lowestRateMeetingTarget", items: []float64{30, 60}, target: 30, want: 30},
		{name: "unsortedInput", items: []float64{60, 30}, target: 30, want: 30},
		{name: "onlyHigherRateAdvertised", items: []float64{60}, target: 30, want: 60},
		{name: "nothingMeetsTargetFallsBackToHighest", items: []float64{5, 10}, target: 30, want: 10},
		{name: "exactMatch", items: []float64{15, 30, 60}, target: 30, want: 30},
		{name: "empty", items: nil, target: 30, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fpsForTarget(tc.items, tc.target); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestCappedFrameRate(t *testing.T) {
	cases := []struct {
		name   string
		fps    float64
		target float64
		want   float64
	}{
		{name: "capsAboveTarget", fps: 60, target: 30, want: 30},
		{name: "noTargetKeepsRate", fps: 60, target: 0, want: 60},
		{name: "belowTargetKeepsRate", fps: 25, target: 30, want: 25},
		{name: "equalKeepsRate", fps: 30, target: 30, want: 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cappedFrameRate(tc.fps, tc.target); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSelectBestModePicksLowestAdvertisedRateMeetingTarget(t *testing.T) {
	modes := []*gwebrtp.UsbCapabilityMode{
		{Width: 1920, Height: 1080, Fps: []float64{60, 30}},
	}

	got := selectBestMode(modes, 30, 0, 0)
	if got.Width != 1920 || got.Height != 1080 || got.FrameRate != 30 {
		t.Fatalf("unexpected mode: %+v", got)
	}
}

func TestBuildFFmpegArgsCapsPipelineRateAndPassesNv12Through(t *testing.T) {
	input := resolvedInput{
		format: "avfoundation",
		device: "OBS Virtual Camera:none",
		args:   []string{"-framerate", "60", "-video_size", "1920x1080", "-pixel_format", "nv12"},
		mode:   Mode{Width: 1920, Height: 1080, FrameRate: 60},
		output: Mode{Width: 1920, Height: 1080, FrameRate: 30},
	}
	cfg := defaultOptions()
	cfg.encoder = "h264_videotoolbox"
	cfg.previewHeight = 720

	got := buildFFmpegArgs(input, cfg, cfg.previewHeight, []string{"tcp://127.0.0.1:1", "tcp://127.0.0.1:2"})
	want := []string{
		"-hide_banner", "-loglevel", "warning", "-f", "avfoundation",
		"-framerate", "60", "-video_size", "1920x1080", "-pixel_format", "nv12",
		"-i", "OBS Virtual Camera:none",
		"-filter_complex", "[0:v]fps=30,format=nv12|yuv420p,split=2[s0][s1];[s0]null[v0];[s1]scale=-2:720[v1]",
		"-map", "[v0]", "-an", "-c:v", "h264_videotoolbox", "-profile:v", "high",
		"-g", "30", "-keyint_min", "30", "-sc_threshold", "0", "-bf", "0",
		"-b:v", "7464k", "-maxrate", "7464k", "-bufsize", "14928k",
		"-f", "h264", "tcp://127.0.0.1:1",
		"-map", "[v1]", "-an", "-c:v", "h264_videotoolbox", "-profile:v", "high",
		"-g", "30", "-keyint_min", "30", "-sc_threshold", "0", "-bf", "0",
		"-b:v", "3317k", "-maxrate", "3317k", "-bufsize", "6634k",
		"-f", "h264", "tcp://127.0.0.1:2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ffmpeg args mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestBuildFFmpegArgsKeepsBestFullSizeAndScalesPreview(t *testing.T) {
	// * the device advertises 1080p but delivers 4k, so the best stream stays 4k for the ai while the preview scales down
	input := resolvedInput{
		format: "avfoundation",
		device: "OBS Virtual Camera:none",
		args:   []string{"-framerate", "60", "-video_size", "1920x1080", "-pixel_format", "nv12"},
		mode:   Mode{Width: 1920, Height: 1080, FrameRate: 60},
		output: Mode{Width: 3840, Height: 2160, FrameRate: 60},
	}
	cfg := defaultOptions()
	cfg.encoder = "h264_videotoolbox"
	cfg.previewHeight = 1080

	got := buildFFmpegArgs(input, cfg, cfg.previewHeight, []string{"tcp://127.0.0.1:1", "tcp://127.0.0.1:2"})
	want := []string{
		"-hide_banner", "-loglevel", "warning", "-f", "avfoundation",
		"-framerate", "60", "-video_size", "1920x1080", "-pixel_format", "nv12",
		"-i", "OBS Virtual Camera:none",
		"-filter_complex", "[0:v]fps=60,format=nv12|yuv420p,split=2[s0][s1];[s0]null[v0];[s1]scale=-2:1080[v1]",
		"-map", "[v0]", "-an", "-c:v", "h264_videotoolbox", "-profile:v", "high",
		"-g", "60", "-keyint_min", "60", "-sc_threshold", "0", "-bf", "0",
		"-b:v", "25000k", "-maxrate", "25000k", "-bufsize", "50000k",
		"-f", "h264", "tcp://127.0.0.1:1",
		"-map", "[v1]", "-an", "-c:v", "h264_videotoolbox", "-profile:v", "high",
		"-g", "60", "-keyint_min", "60", "-sc_threshold", "0", "-bf", "0",
		"-b:v", "14929k", "-maxrate", "14929k", "-bufsize", "29858k",
		"-f", "h264", "tcp://127.0.0.1:2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ffmpeg args mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestBuildFFmpegArgsKeepsExplicitBitratesAndLibx264Untouched(t *testing.T) {
	input := resolvedInput{
		format: "avfoundation",
		device: "0:none",
		args:   nil,
		mode:   Mode{Width: 1920, Height: 1080, FrameRate: 30},
		output: Mode{Width: 1920, Height: 1080, FrameRate: 30},
	}
	cfg := defaultOptions()
	cfg.encoder = "libx264"
	joined := strings.Join(buildFFmpegArgs(input, cfg, 0, []string{"tcp://a", "tcp://b"}), " ")
	if strings.Contains(joined, "-b:v") {
		t.Fatalf("libx264 must keep crf, got %q", joined)
	}

	cfg.encoder = "h264_videotoolbox"
	cfg.bestBitrateKbps = 9000
	cfg.previewBitrateKbps = 1500
	joined = strings.Join(buildFFmpegArgs(input, cfg, 0, []string{"tcp://a", "tcp://b"}), " ")
	if !strings.Contains(joined, "-b:v 9000k") || !strings.Contains(joined, "-b:v 1500k") {
		t.Fatalf("explicit bitrates must win, got %q", joined)
	}
}

func TestDefaultHardwareBitrateKbps(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		height int
		fps    float64
		want   int
	}{
		{name: "1080p60", width: 1920, height: 1080, fps: 60, want: 14929},
		{name: "1080p30", width: 1920, height: 1080, fps: 30, want: 7464},
		{name: "720p30", width: 1280, height: 720, fps: 30, want: 3317},
		{name: "4k60ClampsToCeiling", width: 3840, height: 2160, fps: 60, want: 25000},
		{name: "tinyClampsToFloor", width: 160, height: 120, fps: 30, want: 500},
		{name: "zeroFpsAssumes30", width: 1920, height: 1080, fps: 0, want: 7464},
		{name: "zeroGeometry", width: 0, height: 0, fps: 30, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultHardwareBitrateKbps(tc.width, tc.height, tc.fps); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestEncoderRequiresBitrate(t *testing.T) {
	for _, name := range []string{"h264_videotoolbox", "h264_nvenc", "h264_qsv", "h264_amf"} {
		if !encoderRequiresBitrate(name) {
			t.Fatalf("expected %s to require a bitrate default", name)
		}
	}
	for _, name := range []string{"libx264", "", "libopenh264"} {
		if encoderRequiresBitrate(name) {
			t.Fatalf("expected %s to keep its own rate control", name)
		}
	}
}

func TestParseShowinfoSize(t *testing.T) {
	realLine := "[Parsed_showinfo_0 @ 0x79cec09bc0] n:   0 pts:      0 pts_time:0       duration:      0 duration_time:0       fmt:nv12 cl:unspecified sar:0/1 s:3840x2160 i:P iskey:1 type:I checksum:041C8DBF"
	width, height, ok := parseShowinfoSize(realLine)
	if !ok || width != 3840 || height != 2160 {
		t.Fatalf("got %dx%d ok=%v", width, height, ok)
	}
	if _, _, ok := parseShowinfoSize("no size here, sar:0/1"); ok {
		t.Fatal("expected no match")
	}
	if _, _, ok := parseShowinfoSize(""); ok {
		t.Fatal("expected no match on empty output")
	}
}

func TestDeliveredGeometry(t *testing.T) {
	mode := Mode{Width: 1920, Height: 1080}
	if w, h := deliveredGeometry(mode, 3840, 2160, true); w != 3840 || h != 2160 {
		t.Fatalf("expected probed geometry to win, got %dx%d", w, h)
	}
	if w, h := deliveredGeometry(mode, 0, 0, false); w != 1920 || h != 1080 {
		t.Fatalf("expected advertised fallback, got %dx%d", w, h)
	}
	if w, h := deliveredGeometry(mode, 1920, 1080, true); w != 1920 || h != 1080 {
		t.Fatalf("expected honest device to stay unchanged, got %dx%d", w, h)
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

func TestFfmpegEncoderListedParsesRealOutput(t *testing.T) {
	// * verbatim shape of `ffmpeg -encoders`, whose trailing column is the codec, not the encoder
	output := strings.Join([]string{
		"Encoders:",
		" V..... = Video",
		" ------",
		" V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)",
		" V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)",
		" V....D hevc_videotoolbox    VideoToolbox H.265 Encoder (codec hevc)",
		"",
	}, "\n")

	for _, name := range []string{"libx264", "h264_videotoolbox", "hevc_videotoolbox"} {
		if !ffmpegEncoderListed(output, name) {
			t.Fatalf("expected %s to be detected", name)
		}
	}
	for _, name := range []string{"h264", "h264)", "videotoolbox", "h264_nvenc"} {
		if ffmpegEncoderListed(output, name) {
			t.Fatalf("expected %s not to be detected", name)
		}
	}
}

func TestEncoderSelectHonoursPriorityOrder(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string
		working    map[string]bool
		want       string
	}{
		{
			name:       "firstWorkingWins",
			candidates: []string{"h264_nvenc", "h264_qsv", "h264_amf"},
			working:    map[string]bool{"h264_qsv": true, "h264_amf": true},
			want:       "h264_qsv",
		},
		{
			name:       "allWorkingPicksHighestPriority",
			candidates: []string{"h264_nvenc", "h264_qsv"},
			working:    map[string]bool{"h264_nvenc": true, "h264_qsv": true},
			want:       "h264_nvenc",
		},
		{
			name:       "noneWorking",
			candidates: []string{"h264_nvenc", "h264_qsv"},
			working:    map[string]bool{},
			want:       "",
		},
		{
			name:       "noCandidates",
			candidates: nil,
			working:    map[string]bool{},
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encoderSelect(tc.candidates, func(name string) bool {
				return tc.working[name]
			})
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestEncoderSelectSlowLowPriorityFailureDoesNotStealSelection(t *testing.T) {
	// * a slow failing low priority probe must not affect the winner
	got := encoderSelect([]string{"h264_nvenc", "h264_qsv"}, func(name string) bool {
		if name == "h264_qsv" {
			time.Sleep(50 * time.Millisecond)
			return false
		}
		return true
	})
	if got != "h264_nvenc" {
		t.Fatalf("got %q want h264_nvenc", got)
	}
}

func TestEncoderSelectProbesConcurrently(t *testing.T) {
	candidates := []string{"h264_nvenc", "h264_qsv", "h264_amf"}
	var startedGroup sync.WaitGroup
	startedGroup.Add(len(candidates))
	allStarted := make(chan struct{})
	go func() {
		startedGroup.Wait()
		close(allStarted)
	}()

	// * every probe blocks until all probes have started, which deadlocks if probing is sequential
	probe := func(name string) bool {
		startedGroup.Done()
		select {
		case <-allStarted:
		case <-time.After(5 * time.Second):
			t.Errorf("probe %s never saw the others start, probing is not concurrent", name)
			return false
		}
		return name == "h264_amf"
	}

	done := make(chan string, 1)
	go func() {
		done <- encoderSelect(candidates, probe)
	}()
	select {
	case got := <-done:
		if got != "h264_amf" {
			t.Fatalf("got %q want h264_amf", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("encoder selection did not finish")
	}
}

func TestHardwareEncoderCandidatesPerPlatform(t *testing.T) {
	cases := map[string][]string{
		"darwin":  {"h264_videotoolbox"},
		"windows": {"h264_nvenc", "h264_qsv", "h264_amf"},
		"linux":   {"h264_nvenc", "h264_qsv"},
		"freebsd": nil,
	}
	for goos, want := range cases {
		got := hardwareEncoderCandidates(goos)
		if len(got) != len(want) {
			t.Fatalf("%s: expected %v, got %v", goos, want, got)
		}
		for idx := range want {
			if got[idx] != want[idx] {
				t.Fatalf("%s: expected %v, got %v", goos, want, got)
			}
		}
	}
}

func TestResolveEncoderPrefersFirstUsableCandidate(t *testing.T) {
	prevProbe := encoderProbe
	prevGOOS := currentGOOS
	defer func() {
		encoderProbe = prevProbe
		currentGOOS = prevGOOS
	}()

	currentGOOS = "windows"
	// * nvenc is listed but unusable on this box, so selection must fall through to the next candidate
	encoderProbe = func(name string) bool { return name == "h264_amf" }

	cfg := resolveEncoder(options{})
	if cfg.encoder != "h264_amf" {
		t.Fatalf("expected h264_amf, got %q", cfg.encoder)
	}
	if len(cfg.encoderArgs) != 0 {
		t.Fatalf("expected no encoder args for a hardware encoder, got %v", cfg.encoderArgs)
	}
}

func TestResolveEncoderFallsBackWhenNoHardwareUsable(t *testing.T) {
	prevProbe := encoderProbe
	prevGOOS := currentGOOS
	defer func() {
		encoderProbe = prevProbe
		currentGOOS = prevGOOS
	}()

	currentGOOS = "windows"
	encoderProbe = func(string) bool { return false }

	cfg := resolveEncoder(options{})
	if cfg.encoder != "libx264" {
		t.Fatalf("expected libx264 fallback, got %q", cfg.encoder)
	}
	if len(cfg.encoderArgs) == 0 {
		t.Fatalf("expected libx264 args on fallback")
	}
}

func TestFfmpegEncoderRunArgsEncodesOneRealFrame(t *testing.T) {
	args := strings.Join(ffmpegEncoderRunArgs("h264_nvenc"), " ")
	for _, want := range []string{"-f lavfi", "-frames:v 1", "-c:v h264_nvenc", "-f null"} {
		if !strings.Contains(args, want) {
			t.Fatalf("expected probe args to contain %q, got %s", want, args)
		}
	}
}
