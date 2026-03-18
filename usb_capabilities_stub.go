//go:build !darwin && !windows

package webrtp

import "fmt"

func UsbDeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	return nil, fmt.Errorf("usb capability discovery is not supported on this platform")
}
