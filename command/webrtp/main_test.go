package main

import (
	"testing"

	"github.com/bronystylecrazy/go-webrtp/streamcore"
)

func TestPreferredStreamVariant(t *testing.T) {
	if got := preferredStreamVariant("cam1", "cam2"); got != "cam1" {
		t.Fatalf("expected quality to win, got %q", got)
	}
	if got := preferredStreamVariant("", "cam2"); got != "cam2" {
		t.Fatalf("expected map fallback, got %q", got)
	}
	if got := preferredStreamVariant("   ", "  cam2 "); got != "cam2" {
		t.Fatalf("expected trimmed map fallback, got %q", got)
	}
}

func TestResolveStreamRouteNameSupportsUsbDeviceAliases(t *testing.T) {
	name := "usbCamera"
	sourceType := "usb"
	deviceID := "camera-raw-id"
	cfg := &streamcore.Config{
		Upstreams: []*streamcore.Upstream{{
			Name:       &name,
			SourceType: &sourceType,
			Device:     deviceID,
			Codec:      "h264",
			OnDemand:   true,
		}},
	}

	manager, err := streamcore.NewManager(
		streamcore.WithConfigFile(""),
		streamcore.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	if got := resolveStreamRouteName(manager, name); got != name {
		t.Fatalf("expected exact stream name to resolve to itself, got %q", got)
	}
	if got := resolveStreamRouteName(manager, deviceID); got != name {
		t.Fatalf("expected raw device id to resolve to %q, got %q", name, got)
	}
	if got := resolveStreamRouteName(manager, hashDeviceID(deviceID)); got != name {
		t.Fatalf("expected hashed device id to resolve to %q, got %q", name, got)
	}
}
