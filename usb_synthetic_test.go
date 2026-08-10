package webrtp

import (
	"reflect"
	"strings"
	"testing"
)

func TestSyntheticDevicesValidate(t *testing.T) {
	cases := []struct {
		name    string
		devices []*SyntheticDevice
		wantErr string
	}{
		{
			name:    "nilDeclaration",
			devices: []*SyntheticDevice{nil},
			wantErr: "declaration is required",
		},
		{
			name:    "missingId",
			devices: []*SyntheticDevice{{Type: SyntheticDeviceTypePattern}},
			wantErr: "id is required",
		},
		{
			name:    "reservedId",
			devices: []*SyntheticDevice{{Id: "Default", Type: SyntheticDeviceTypePattern}},
			wantErr: "id is reserved",
		},
		{
			name: "duplicateId",
			devices: []*SyntheticDevice{
				{Id: "test", Type: SyntheticDeviceTypePattern},
				{Id: "TEST", Type: SyntheticDeviceTypePattern},
			},
			wantErr: "declared more than once",
		},
		{
			name:    "unknownType",
			devices: []*SyntheticDevice{{Id: "test", Type: "webcam"}},
			wantErr: "type must be",
		},
		{
			name:    "fileWithoutPath",
			devices: []*SyntheticDevice{{Id: "clip", Type: SyntheticDeviceTypeFile}},
			wantErr: "requires path",
		},
		{
			name:    "negativeDimensions",
			devices: []*SyntheticDevice{{Id: "test", Type: SyntheticDeviceTypePattern, Width: -1}},
			wantErr: "must not be negative",
		},
		{
			name:    "validPattern",
			devices: []*SyntheticDevice{{Id: "test", Type: "Pattern"}},
		},
		{
			name:    "validFile",
			devices: []*SyntheticDevice{{Id: "clip", Type: SyntheticDeviceTypeFile, Path: "/tmp/clip.mp4"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := SyntheticDevicesValidate(tc.devices)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(normalized) != len(tc.devices) {
				t.Fatalf("expected %d devices, got %d", len(tc.devices), len(normalized))
			}
		})
	}
}

func TestSyntheticDevicesValidateAppliesDefaults(t *testing.T) {
	normalized, err := SyntheticDevicesValidate([]*SyntheticDevice{
		{Id: " test ", Type: " PATTERN "},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &SyntheticDevice{
		Id:        "test",
		Name:      "test",
		Type:      SyntheticDeviceTypePattern,
		Width:     1280,
		Height:    720,
		FrameRate: 30,
	}
	if !reflect.DeepEqual(normalized[0], want) {
		t.Fatalf("got %+v want %+v", normalized[0], want)
	}
}

func TestSyntheticDevicesConfigureTranslatesToDevicesAndCapabilities(t *testing.T) {
	defer func() {
		if err := SyntheticDevicesConfigure(nil); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}()

	err := SyntheticDevicesConfigure([]*SyntheticDevice{
		{Id: "testPattern", Name: "Test Pattern", Type: SyntheticDeviceTypePattern, Width: 640, Height: 480, FrameRate: 15},
		{Id: "clipLoop", Type: SyntheticDeviceTypeFile, Path: "/tmp/clip.mp4"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	provider := &syntheticDeviceProvider{}
	devices, err := provider.DeviceList()
	if err != nil {
		t.Fatalf("device list: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("expected two synthetic devices, got %+v", devices)
	}
	for _, device := range devices {
		if device.Kind != UsbDeviceKindSynthetic {
			t.Fatalf("expected synthetic kind, got %+v", device)
		}
	}

	if _, ok := SyntheticDeviceResolve("testPattern"); !ok {
		t.Fatal("expected resolve by id")
	}
	if _, ok := SyntheticDeviceResolve("Test Pattern"); !ok {
		t.Fatal("expected resolve by name")
	}
	if _, ok := SyntheticDeviceResolve("unknown"); ok {
		t.Fatal("expected unknown device to not resolve")
	}

	caps, err := provider.DeviceCapabilitiesGet("testPattern")
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.Device == nil || caps.Device.Kind != UsbDeviceKindSynthetic {
		t.Fatalf("unexpected capability device: %+v", caps.Device)
	}
	if !reflect.DeepEqual(caps.Codecs, []string{"h264"}) {
		t.Fatalf("unexpected codecs: %v", caps.Codecs)
	}
	if len(caps.Modes) != 1 || caps.Modes[0].Width != 640 || caps.Modes[0].Height != 480 || !reflect.DeepEqual(caps.Modes[0].Fps, []float64{15}) {
		t.Fatalf("unexpected modes: %+v", caps.Modes[0])
	}

	if _, err := provider.DeviceCapabilitiesGet("unknown"); err == nil {
		t.Fatal("expected capability error for unknown synthetic device")
	}
}

func TestSyntheticDevicesConfigureRejectsMalformedInput(t *testing.T) {
	if err := SyntheticDevicesConfigure([]*SyntheticDevice{{Id: "", Type: SyntheticDeviceTypePattern}}); err == nil {
		t.Fatal("expected configure to reject malformed declarations")
	}
	if _, ok := SyntheticDeviceResolve(""); ok {
		t.Fatal("expected empty device to not resolve")
	}
}

func TestSyntheticFFmpegArgs(t *testing.T) {
	pattern := &SyntheticDevice{
		Id: "testPattern", Name: "Test Pattern", Type: SyntheticDeviceTypePattern,
		Width: 640, Height: 480, FrameRate: 15,
	}
	cfg := syntheticCaptureConfig(&Config{}, pattern)
	got := syntheticFFmpegArgs(cfg, pattern)
	want := []string{
		"-hide_banner", "-loglevel", "warning", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x480:rate=15",
		"-map", "0:v:0", "-an",
		"-vf", "scale=640:480",
		"-r", "15",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
		"-g", "30", "-keyint_min", "30", "-bf", "0",
		"-x264-params", "repeat-headers=1:aud=1:scenecut=0",
		"-f", "h264", "-",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pattern args mismatch\ngot:  %v\nwant: %v", got, want)
	}

	file := &SyntheticDevice{
		Id: "clipLoop", Name: "clipLoop", Type: SyntheticDeviceTypeFile, Path: "/tmp/clip.mp4",
		Width: 1280, Height: 720, FrameRate: 30,
	}
	cfg = syntheticCaptureConfig(&Config{Width: 640, Height: 360, FrameRate: 10, BitrateKbps: 800}, file)
	got = syntheticFFmpegArgs(cfg, file)
	want = []string{
		"-hide_banner", "-loglevel", "warning", "-re",
		"-stream_loop", "-1", "-i", "/tmp/clip.mp4",
		"-map", "0:v:0", "-an",
		"-vf", "scale=640:360",
		"-r", "10",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
		"-b:v", "800k", "-maxrate", "800k", "-bufsize", "1600k",
		"-g", "20", "-keyint_min", "20", "-bf", "0",
		"-x264-params", "repeat-headers=1:aud=1:scenecut=0",
		"-f", "h264", "-",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("file args mismatch\ngot:  %v\nwant: %v", got, want)
	}
}
