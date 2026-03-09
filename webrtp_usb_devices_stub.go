//go:build !linux && !darwin && !windows

package webrtp

import "fmt"

func UsbDeviceList() ([]*UsbDevice, error) {
	return nil, fmt.Errorf("usb device listing is not supported on this platform")
}
