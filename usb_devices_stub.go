//go:build !linux && !darwin && !windows

package webrtp

import "fmt"

type unsupportedDeviceProvider struct{}

func init() {
	DeviceProviderRegister(&unsupportedDeviceProvider{})
}

func (r *unsupportedDeviceProvider) DeviceProviderName() string {
	return "native"
}

func (r *unsupportedDeviceProvider) DeviceProviderPrecedence() int {
	return DeviceProviderPrecedenceNative
}

func (r *unsupportedDeviceProvider) DeviceList() ([]*UsbDevice, error) {
	return nil, fmt.Errorf("usb device listing is not supported on this platform")
}

func (r *unsupportedDeviceProvider) DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	return nil, fmt.Errorf("usb capability discovery is not supported on this platform")
}
