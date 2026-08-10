package webrtp

import "strings"

const (
	V4l2CapVideoCapture       = 0x00000001
	V4l2CapVideoOutput        = 0x00000002
	V4l2CapVideoCaptureMplane = 0x00001000
	V4l2CapVideoOutputMplane  = 0x00002000
	V4l2CapMetaCapture        = 0x00800000
	V4l2CapReadwrite          = 0x01000000
	V4l2CapStreaming          = 0x04000000
	V4l2CapDeviceCaps         = 0x80000000
)

const v4l2LoopbackDriver = "v4l2 loopback"
const v4l2LoopbackBusPrefix = "platform:v4l2loopback-"

type V4l2Node struct {
	Path         string
	Driver       string
	Card         string
	BusInfo      string
	Capabilities uint32
	DeviceCaps   uint32
}

func V4l2NodeCaps(node *V4l2Node) uint32 {
	// * only trust the per node field when the driver declares it populated
	if node.Capabilities&V4l2CapDeviceCaps != 0 {
		return node.DeviceCaps
	}
	return node.Capabilities
}

func V4l2NodeLoopback(node *V4l2Node) bool {
	return node.Driver == v4l2LoopbackDriver || strings.HasPrefix(node.BusInfo, v4l2LoopbackBusPrefix)
}

func V4l2NodeInclude(node *V4l2Node) bool {
	if node == nil {
		return false
	}
	caps := V4l2NodeCaps(node)
	if caps&(V4l2CapStreaming|V4l2CapReadwrite) == 0 {
		return false
	}
	capture := caps&(V4l2CapVideoCapture|V4l2CapVideoCaptureMplane) != 0
	output := caps&(V4l2CapVideoOutput|V4l2CapVideoOutputMplane) != 0
	// * an exclusive mode loopback node reports output while its producer streams, so accept either direction
	if V4l2NodeLoopback(node) {
		return capture || output
	}
	return capture
}

func V4l2NodeKind(node *V4l2Node) UsbDeviceKind {
	if V4l2NodeLoopback(node) {
		return UsbDeviceKindVirtual
	}
	return UsbDeviceKindHardware
}

func V4l2NodeDisplayName(node *V4l2Node) string {
	if card := strings.TrimSpace(node.Card); card != "" {
		return card
	}
	return node.Path
}
