package streamcore

import (
	"runtime"
	"testing"
)

func TestManagerCreateUpdateDeleteRoundTrip(t *testing.T) {
	manager, err := NewManager(
		WithConfigFile(""),
		WithConfig(&Config{}),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	name := "camA"
	sourceType := "file"
	width := 1280
	height := 720
	req := &StreamRequest{
		Name:       &name,
		SourceType: &sourceType,
		Path:       "/tmp/camA.h264",
		Codec:      "h264",
		Width:      &width,
		Height:     &height,
		OnDemand:   true,
	}

	created, err := manager.Create(req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != name {
		t.Fatalf("unexpected created name: %q", created.Name)
	}
	if got := len(manager.ListResponses()); got != 1 {
		t.Fatalf("expected 1 response after create, got %d", got)
	}

	updatedName := "camB"
	updatedWidth := 1920
	updateReq := &StreamRequest{
		Name:       &updatedName,
		SourceType: &sourceType,
		Path:       "/tmp/camB.h264",
		Codec:      "h264",
		Width:      &updatedWidth,
		Height:     &height,
		OnDemand:   true,
	}
	updated, err := manager.Update(name, updateReq)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != updatedName {
		t.Fatalf("unexpected updated name: %q", updated.Name)
	}
	if _, ok := manager.Get(name); ok {
		t.Fatal("expected old stream name to be removed after update")
	}
	if got, ok := manager.Get(updatedName); !ok || got.Width == nil || *got.Width != updatedWidth {
		t.Fatalf("expected updated stream width %d, got %#v", updatedWidth, got)
	}

	if err := manager.Delete(updatedName); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := len(manager.ListResponses()); got != 0 {
		t.Fatalf("expected manager to be empty after delete, got %d items", got)
	}
}

func TestManagerLookupsRespectServeStream(t *testing.T) {
	name := "hidden"
	sourceType := "file"
	serve := false
	cfg := &Config{
		Upstreams: []*Upstream{{
			Name:        &name,
			SourceType:  &sourceType,
			Path:        "/tmp/hidden.h264",
			Codec:       "h264",
			ServeStream: &serve,
			OnDemand:    true,
		}},
	}
	manager, err := NewManager(
		WithConfigFile(""),
		WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	if got := len(manager.List()); got != 0 {
		t.Fatalf("expected hidden stream to be omitted from List, got %d", got)
	}
	if _, ok := manager.StreamByName(name); ok {
		t.Fatal("expected StreamByName to reject non-served stream")
	}
	if stream, ok := manager.StreamByNameAny(name); !ok || stream == nil {
		t.Fatal("expected StreamByNameAny to return non-served stream")
	}
	if _, ok := manager.StreamByIndex(0); ok {
		t.Fatal("expected StreamByIndex to reject non-served stream")
	}
}

func TestManagerCalibrationTargetsIncludesDerivedStreams(t *testing.T) {
	baseName := "base"
	derivedName := "derived"
	sourceType := "file"
	cfg := &Config{
		Upstreams: []*Upstream{
			{
				Name:       &baseName,
				SourceType: &sourceType,
				Path:       "/tmp/base.h264",
				Codec:      "h264",
				OnDemand:   true,
			},
			{
				Name:            &derivedName,
				SourceType:      &sourceType,
				Path:            "/tmp/derived.h264",
				Codec:           "h264",
				CalibrationFrom: baseName,
				OnDemand:        true,
			},
		},
	}
	manager, err := NewManager(
		WithConfigFile(""),
		WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	targets := manager.CalibrationTargets(baseName, "")
	if len(targets) != 2 {
		t.Fatalf("expected base and derived calibration targets, got %d", len(targets))
	}
	if targets[0].Name != baseName {
		t.Fatalf("expected base stream first, got %q", targets[0].Name)
	}
	if targets[1].Name != derivedName {
		t.Fatalf("expected derived stream second, got %q", targets[1].Name)
	}
}

func TestManagerStreamByNameQualityFallsBackToDefaultRendition(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("usb renditions are only supported on darwin/windows")
	}

	name := "cam"
	sourceType := "usb"
	width := 1280
	height := 720
	lowWidth := 640
	midWidth := 960
	cfg := &Config{
		Upstreams: []*Upstream{{
			Name:       &name,
			SourceType: &sourceType,
			Device:     "camera0",
			Codec:      "h264",
			Width:      &width,
			Height:     &height,
			OnDemand:   true,
			Renditions: []*Rendition{
				{Name: "low", Width: &lowWidth},
				{Name: "mid", Width: &midWidth},
			},
		}},
	}
	manager, err := NewManager(
		WithConfigFile(""),
		WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	stream, ok := manager.StreamByNameQuality(name, "missing")
	if !ok || stream == nil {
		t.Fatal("expected fallback default rendition for unknown quality")
	}
	if stream.RenditionName != "mid" {
		t.Fatalf("expected default rendition 'mid', got %q", stream.RenditionName)
	}
}

func TestUSBFFmpegRenditionsExposeMapPaths(t *testing.T) {
	name := "cam"
	sourceType := "usb"
	device := "16MP USB Camera"
	frameRate := 10.0
	ffmpegInputArgs := []string{"-framerate", "10", "-video_size", "4000x3000", "-vcodec", "mjpeg"}
	ffmpegEncoderArgs := []string{"-rc", "cbr"}
	fullWidth := 4000
	fullHeight := 3000
	fullBitrate := 20000
	sdWidth := 1600
	sdHeight := 1200
	sdBitrate := 4000
	cfg := &Config{
		Upstreams: []*Upstream{{
			Name:              &name,
			SourceType:        &sourceType,
			Device:            device,
			Codec:             "h264",
			FFmpegInputFormat: "dshow",
			FFmpegInputArgs:   ffmpegInputArgs,
			FFmpegFilter:      "format=yuv420p",
			FFmpegEncoder:     "h264_amf",
			FFmpegEncoderArgs: ffmpegEncoderArgs,
			FrameRate:         &frameRate,
			OnDemand:          true,
			Renditions: []*Rendition{
				{Name: "cam2", Width: &sdWidth, Height: &sdHeight, BitrateKbps: &sdBitrate, FFmpegFilter: "eq=contrast=1.1"},
				{Name: "cam1", Width: &fullWidth, Height: &fullHeight, BitrateKbps: &fullBitrate},
			},
		}},
	}
	manager, err := NewManager(WithConfigFile(""), WithConfig(cfg))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	resp, ok := manager.Get(name)
	if !ok {
		t.Fatal("expected stream response")
	}
	if resp.WsPath != "/streams/cam" {
		t.Fatalf("expected group ws path, got %q", resp.WsPath)
	}
	if len(resp.Renditions) != 2 {
		t.Fatalf("expected 2 renditions, got %d", len(resp.Renditions))
	}
	if resp.Renditions[0].WsPath != "/streams/cam?map=cam2" {
		t.Fatalf("unexpected low rendition ws path: %q", resp.Renditions[0].WsPath)
	}
	if resp.Renditions[0].FFmpegFilter != "eq=contrast=1.1" {
		t.Fatalf("unexpected low rendition filter: %q", resp.Renditions[0].FFmpegFilter)
	}
	if resp.Renditions[1].WsPath != "/streams/cam?map=cam1" {
		t.Fatalf("unexpected high rendition ws path: %q", resp.Renditions[1].WsPath)
	}
}

func TestUSBFFmpegRenditionFilterValidation(t *testing.T) {
	name := "cam"
	sourceType := "usb"
	device := "camera0"
	cfg := &Config{
		Upstreams: []*Upstream{{
			Name:       &name,
			SourceType: &sourceType,
			Device:     device,
			Codec:      "h264",
			OnDemand:   true,
			Renditions: []*Rendition{
				{Name: "cam1", FFmpegFilter: "crop=100:100"},
			},
		}},
	}
	_, err := NewManager(WithConfigFile(""), WithConfig(cfg))
	if err == nil || err.Error() != "rendition cam1 ffmpegFilter requires usb ffmpeg mode" {
		t.Fatalf("expected usb ffmpeg mode validation error, got %v", err)
	}
}
