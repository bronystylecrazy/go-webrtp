package webrtp

type UsbCapabilityMode struct {
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Fps    []float64 `json:"fps,omitempty"`
}

type UsbDeviceCapabilities struct {
	Device         *UsbDevice            `json:"device,omitempty"`
	Codecs         []string              `json:"codecs,omitempty"`
	Modes          []*UsbCapabilityMode  `json:"modes,omitempty"`
	BitrateControl string                `json:"bitrateControl,omitempty"`
}

