package webrtp

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

const (
	DeviceProviderPrecedenceNative    = 100
	DeviceProviderPrecedenceVirtual   = 50
	DeviceProviderPrecedenceSynthetic = 10
)

type DeviceProvider interface {
	DeviceProviderName() string
	DeviceProviderPrecedence() int
	DeviceList() ([]*UsbDevice, error)
	DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error)
}

var deviceProviderMu sync.RWMutex
var deviceProviders = make(map[string]DeviceProvider)

func DeviceProviderRegister(provider DeviceProvider) {
	if provider == nil || strings.TrimSpace(provider.DeviceProviderName()) == "" {
		return
	}
	deviceProviderMu.Lock()
	defer deviceProviderMu.Unlock()
	deviceProviders[provider.DeviceProviderName()] = provider
}

func DeviceProviderUnregister(name string) {
	deviceProviderMu.Lock()
	defer deviceProviderMu.Unlock()
	delete(deviceProviders, name)
}

func deviceProviderSnapshot() []DeviceProvider {
	deviceProviderMu.RLock()
	providers := make([]DeviceProvider, 0, len(deviceProviders))
	for _, provider := range deviceProviders {
		providers = append(providers, provider)
	}
	deviceProviderMu.RUnlock()
	deviceProvidersSort(providers)
	return providers
}

func deviceProvidersSort(providers []DeviceProvider) {
	slices.SortFunc(providers, func(a, b DeviceProvider) int {
		if a.DeviceProviderPrecedence() != b.DeviceProviderPrecedence() {
			return b.DeviceProviderPrecedence() - a.DeviceProviderPrecedence()
		}
		return strings.Compare(a.DeviceProviderName(), b.DeviceProviderName())
	})
}

// * a partial provider failure must not blank the list for callers that check err first
func UsbDeviceList() ([]*UsbDevice, error) {
	devices, err := usbDeviceListAggregate(deviceProviderSnapshot())
	if len(devices) > 0 {
		return devices, nil
	}
	return devices, err
}

// * same aggregation, but always reports why a provider failed
func UsbDeviceListDetailed() ([]*UsbDevice, error) {
	return usbDeviceListAggregate(deviceProviderSnapshot())
}

func usbDeviceListAggregate(providers []DeviceProvider) ([]*UsbDevice, error) {
	devices := make([]*UsbDevice, 0)
	seen := make(map[string]struct{})
	errs := make([]error, 0)
	for _, provider := range providers {
		items, err := provider.DeviceList()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", provider.DeviceProviderName(), err))
			continue
		}
		for _, device := range items {
			if device == nil || device.Id == "" {
				continue
			}
			if _, ok := seen[device.Id]; ok {
				continue
			}
			seen[device.Id] = struct{}{}
			if device.Kind == "" {
				device.Kind = UsbDeviceKindHardware
			}
			device.Provider = provider.DeviceProviderName()
			devices = append(devices, device)
		}
	}
	usbDevicesSort(devices)
	return devices, errors.Join(errs...)
}

func UsbDeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	return usbDeviceCapabilitiesAggregate(deviceProviderSnapshot(), device)
}

func usbDeviceCapabilitiesAggregate(providers []DeviceProvider, device string) (*UsbDeviceCapabilities, error) {
	errs := make([]error, 0)
	for _, provider := range providers {
		caps, err := provider.DeviceCapabilitiesGet(device)
		if err == nil && caps != nil {
			populateSuggestedUsbRenditions(caps)
			return caps, nil
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", provider.DeviceProviderName(), err))
		}
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("usb capabilities: no device provider registered")
	}
	return nil, errors.Join(errs...)
}

func usbDevicesSort(devices []*UsbDevice) {
	slices.SortFunc(devices, func(a, b *UsbDevice) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Id, b.Id)
	})
}

func usbDeviceLinesParse(raw string) [][]string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows
}

type UsbCaptureBackend string

const (
	UsbCaptureBackendNative UsbCaptureBackend = "native"
	UsbCaptureBackendFfmpeg UsbCaptureBackend = "ffmpeg"
)

func UsbCaptureBackendSelect(kind UsbDeviceKind) UsbCaptureBackend {
	if kind == UsbDeviceKindSynthetic {
		return UsbCaptureBackendFfmpeg
	}
	return UsbCaptureBackendNative
}

func usbDeviceOfflineReason(device string, devices []*UsbDevice) string {
	device = strings.TrimSpace(device)
	if device == "" || strings.EqualFold(device, "default") {
		return ""
	}
	for _, item := range devices {
		if item == nil {
			continue
		}
		if item.Id == device || item.Name == device {
			return ""
		}
	}
	return fmt.Sprintf("usb device %q is not present (unplugged, or its providing application closed)", device)
}

func UsbDeviceOfflineReason(device string) string {
	devices, err := UsbDeviceList()
	if err != nil {
		return ""
	}
	return usbDeviceOfflineReason(device, devices)
}
