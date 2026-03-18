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
