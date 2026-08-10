package webrtp

import (
	"fmt"
	"strings"
	"sync"
)

const (
	SyntheticDeviceTypePattern = "pattern"
	SyntheticDeviceTypeFile    = "file"
)

const (
	syntheticDefaultWidth     = 1280
	syntheticDefaultHeight    = 720
	syntheticDefaultFrameRate = 30.0
)

type SyntheticDevice struct {
	Id        string  `yaml:"id" json:"id"`
	Name      string  `yaml:"name,omitempty" json:"name,omitempty"`
	Type      string  `yaml:"type" json:"type"`
	Path      string  `yaml:"path,omitempty" json:"path,omitempty"`
	Width     int     `yaml:"width,omitempty" json:"width,omitempty"`
	Height    int     `yaml:"height,omitempty" json:"height,omitempty"`
	FrameRate float64 `yaml:"frameRate,omitempty" json:"frameRate,omitempty"`
}

var syntheticDeviceMu sync.RWMutex
var syntheticDevices = make([]*SyntheticDevice, 0)

type syntheticDeviceProvider struct{}

func (r *syntheticDeviceProvider) DeviceProviderName() string {
	return "synthetic"
}

func (r *syntheticDeviceProvider) DeviceProviderPrecedence() int {
	return DeviceProviderPrecedenceSynthetic
}

func (r *syntheticDeviceProvider) DeviceList() ([]*UsbDevice, error) {
	syntheticDeviceMu.RLock()
	defer syntheticDeviceMu.RUnlock()
	devices := make([]*UsbDevice, 0, len(syntheticDevices))
	for _, device := range syntheticDevices {
		devices = append(devices, &UsbDevice{
			Id:   device.Id,
			Name: device.Name,
			Kind: UsbDeviceKindSynthetic,
		})
	}
	return devices, nil
}

func (r *syntheticDeviceProvider) DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	resolved, ok := SyntheticDeviceResolve(device)
	if !ok {
		return nil, fmt.Errorf("synthetic device not found: %s", device)
	}
	return SyntheticDeviceCapabilities(resolved), nil
}

func SyntheticDevicesConfigure(devices []*SyntheticDevice) error {
	normalized, err := SyntheticDevicesValidate(devices)
	if err != nil {
		return err
	}
	syntheticDeviceMu.Lock()
	syntheticDevices = normalized
	syntheticDeviceMu.Unlock()
	if len(normalized) == 0 {
		DeviceProviderUnregister("synthetic")
		return nil
	}
	DeviceProviderRegister(&syntheticDeviceProvider{})
	return nil
}

func SyntheticDevicesValidate(devices []*SyntheticDevice) ([]*SyntheticDevice, error) {
	normalized := make([]*SyntheticDevice, 0, len(devices))
	seen := make(map[string]struct{}, len(devices))
	for idx, device := range devices {
		if device == nil {
			return nil, fmt.Errorf("synthetic device %d: declaration is required", idx)
		}
		id := strings.TrimSpace(device.Id)
		if id == "" {
			return nil, fmt.Errorf("synthetic device %d: id is required", idx)
		}
		if strings.EqualFold(id, "default") {
			return nil, fmt.Errorf("synthetic device %s: id is reserved", id)
		}
		if _, ok := seen[strings.ToLower(id)]; ok {
			return nil, fmt.Errorf("synthetic device %s: id is declared more than once", id)
		}
		seen[strings.ToLower(id)] = struct{}{}
		deviceType := strings.ToLower(strings.TrimSpace(device.Type))
		switch deviceType {
		case SyntheticDeviceTypePattern:
		case SyntheticDeviceTypeFile:
			if strings.TrimSpace(device.Path) == "" {
				return nil, fmt.Errorf("synthetic device %s: file type requires path", id)
			}
		default:
			return nil, fmt.Errorf("synthetic device %s: type must be %s or %s", id, SyntheticDeviceTypePattern, SyntheticDeviceTypeFile)
		}
		if device.Width < 0 || device.Height < 0 || device.FrameRate < 0 {
			return nil, fmt.Errorf("synthetic device %s: width, height and frameRate must not be negative", id)
		}
		name := strings.TrimSpace(device.Name)
		if name == "" {
			name = id
		}
		width := device.Width
		if width == 0 {
			width = syntheticDefaultWidth
		}
		height := device.Height
		if height == 0 {
			height = syntheticDefaultHeight
		}
		frameRate := device.FrameRate
		if frameRate == 0 {
			frameRate = syntheticDefaultFrameRate
		}
		normalized = append(normalized, &SyntheticDevice{
			Id:        id,
			Name:      name,
			Type:      deviceType,
			Path:      strings.TrimSpace(device.Path),
			Width:     width,
			Height:    height,
			FrameRate: frameRate,
		})
	}
	return normalized, nil
}

func SyntheticDeviceResolve(device string) (*SyntheticDevice, bool) {
	device = strings.TrimSpace(device)
	if device == "" {
		return nil, false
	}
	syntheticDeviceMu.RLock()
	defer syntheticDeviceMu.RUnlock()
	for _, item := range syntheticDevices {
		if item.Id == device || item.Name == device {
			return item, true
		}
	}
	return nil, false
}

func SyntheticDeviceCapabilities(device *SyntheticDevice) *UsbDeviceCapabilities {
	if device == nil {
		return nil
	}
	return &UsbDeviceCapabilities{
		Device: &UsbDevice{
			Id:       device.Id,
			Name:     device.Name,
			Kind:     UsbDeviceKindSynthetic,
			Provider: "synthetic",
		},
		Codecs:         []string{"h264"},
		BitrateControl: "target",
		Modes: []*UsbCapabilityMode{
			{
				Width:        device.Width,
				Height:       device.Height,
				Fps:          []float64{device.FrameRate},
				PixelFormats: []string{"yuv420p"},
			},
		},
	}
}
