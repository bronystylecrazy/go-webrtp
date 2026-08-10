package webrtp

type UsbDeviceKind string

const (
	UsbDeviceKindHardware  UsbDeviceKind = "hardware"
	UsbDeviceKindVirtual   UsbDeviceKind = "virtual"
	UsbDeviceKindSynthetic UsbDeviceKind = "synthetic"
)

type UsbDevice struct {
	Id       string        `json:"id"`
	Name     string        `json:"name"`
	Kind     UsbDeviceKind `json:"kind,omitempty"`
	Provider string        `json:"provider,omitempty"`
}
