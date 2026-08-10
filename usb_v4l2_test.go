package webrtp

import "testing"

func TestV4l2NodeInclude(t *testing.T) {
	cases := []struct {
		name string
		node *V4l2Node
		want bool
	}{
		{
			name: "uvcCaptureNode",
			node: &V4l2Node{
				Driver:       "uvcvideo",
				Card:         "16MP USB Camera",
				BusInfo:      "usb-0000:00:14.0-2",
				Capabilities: V4l2CapDeviceCaps | V4l2CapVideoCapture | V4l2CapMetaCapture | V4l2CapStreaming,
				DeviceCaps:   V4l2CapVideoCapture | V4l2CapStreaming,
			},
			want: true,
		},
		{
			name: "uvcMetadataNodeAdvertisesCaptureOnlyInUnion",
			node: &V4l2Node{
				Driver:       "uvcvideo",
				Card:         "16MP USB Camera",
				BusInfo:      "usb-0000:00:14.0-2",
				Capabilities: V4l2CapDeviceCaps | V4l2CapVideoCapture | V4l2CapMetaCapture | V4l2CapStreaming,
				DeviceCaps:   V4l2CapMetaCapture | V4l2CapStreaming,
			},
			want: false,
		},
		{
			name: "loopbackIdleReportsCapture",
			node: &V4l2Node{
				Driver:       "v4l2 loopback",
				Card:         "OBS Virtual Camera",
				BusInfo:      "platform:v4l2loopback-000",
				Capabilities: V4l2CapDeviceCaps | V4l2CapVideoCapture | V4l2CapStreaming | V4l2CapReadwrite,
				DeviceCaps:   V4l2CapVideoCapture | V4l2CapStreaming | V4l2CapReadwrite,
			},
			want: true,
		},
		{
			name: "loopbackExclusiveWhileStreamingReportsOutputOnly",
			node: &V4l2Node{
				Driver:       "v4l2 loopback",
				Card:         "OBS Virtual Camera",
				BusInfo:      "platform:v4l2loopback-000",
				Capabilities: V4l2CapDeviceCaps | V4l2CapVideoOutput | V4l2CapStreaming | V4l2CapReadwrite,
				DeviceCaps:   V4l2CapVideoOutput | V4l2CapStreaming | V4l2CapReadwrite,
			},
			want: true,
		},
		{
			name: "nonLoopbackOutputNodeIsExcluded",
			node: &V4l2Node{
				Driver:       "somedriver",
				Card:         "Video Output",
				BusInfo:      "PCI:0000:01:00.0",
				Capabilities: V4l2CapDeviceCaps | V4l2CapVideoOutput | V4l2CapStreaming,
				DeviceCaps:   V4l2CapVideoOutput | V4l2CapStreaming,
			},
			want: false,
		},
		{
			name: "mplaneCaptureNode",
			node: &V4l2Node{
				Driver:       "soc-camera",
				Card:         "SoC Camera",
				BusInfo:      "platform:soc-camera",
				Capabilities: V4l2CapDeviceCaps | V4l2CapVideoCaptureMplane | V4l2CapStreaming,
				DeviceCaps:   V4l2CapVideoCaptureMplane | V4l2CapStreaming,
			},
			want: true,
		},
		{
			name: "legacyDriverWithoutDeviceCapsFallsBackToUnion",
			node: &V4l2Node{
				Driver:       "bttv",
				Card:         "BT878 Video",
				BusInfo:      "PCI:0000:05:00.0",
				Capabilities: V4l2CapVideoCapture | V4l2CapStreaming,
				DeviceCaps:   0,
			},
			want: true,
		},
		{
			name: "captureWithoutIoMethodIsExcluded",
			node: &V4l2Node{
				Driver:       "weird",
				Card:         "No IO",
				BusInfo:      "usb-0000:00:14.0-3",
				Capabilities: V4l2CapDeviceCaps | V4l2CapVideoCapture,
				DeviceCaps:   V4l2CapVideoCapture,
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := V4l2NodeInclude(tc.node); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestV4l2NodeKind(t *testing.T) {
	cases := []struct {
		name string
		node *V4l2Node
		want UsbDeviceKind
	}{
		{
			name: "loopbackDriverStringIsVirtual",
			node: &V4l2Node{Driver: "v4l2 loopback", BusInfo: "platform:v4l2loopback-000"},
			want: UsbDeviceKindVirtual,
		},
		{
			name: "loopbackBusPrefixIsVirtual",
			node: &V4l2Node{Driver: "other", BusInfo: "platform:v4l2loopback-003"},
			want: UsbDeviceKindVirtual,
		},
		{
			name: "socPlatformDeviceStaysHardware",
			node: &V4l2Node{Driver: "sun6i-csi", BusInfo: "platform:sun6i-csi"},
			want: UsbDeviceKindHardware,
		},
		{
			name: "cardNameIsNeverTheClassifier",
			node: &V4l2Node{Driver: "uvcvideo", Card: "OBS Virtual Camera", BusInfo: "usb-0000:00:14.0-2"},
			want: UsbDeviceKindHardware,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := V4l2NodeKind(tc.node); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestV4l2NodeDisplayName(t *testing.T) {
	node := &V4l2Node{Path: "/dev/video2", Card: "OBS Virtual Camera"}
	if got := V4l2NodeDisplayName(node); got != "OBS Virtual Camera" {
		t.Fatalf("expected card name, got %q", got)
	}
	node = &V4l2Node{Path: "/dev/video2", Card: "  "}
	if got := V4l2NodeDisplayName(node); got != "/dev/video2" {
		t.Fatalf("expected path fallback, got %q", got)
	}
}
