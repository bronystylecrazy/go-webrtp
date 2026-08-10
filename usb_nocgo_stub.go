//go:build (darwin || windows) && !cgo

package webrtp

import (
	"context"
	"fmt"
	"runtime"
)

type nocgoDeviceProvider struct{}

func init() {
	DeviceProviderRegister(&nocgoDeviceProvider{})
}

func (r *nocgoDeviceProvider) DeviceProviderName() string {
	return "native"
}

func (r *nocgoDeviceProvider) DeviceProviderPrecedence() int {
	return DeviceProviderPrecedenceNative
}

func (r *nocgoDeviceProvider) DeviceList() ([]*UsbDevice, error) {
	return nil, fmt.Errorf("usb device listing on %s requires cgo-enabled build", runtime.GOOS)
}

func (r *nocgoDeviceProvider) DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	return nil, fmt.Errorf("usb capability discovery on %s requires cgo-enabled build", runtime.GOOS)
}

func (r *Instance) connectUsb(ctx context.Context) (sourceConn, error) {
	_ = ctx
	return nil, fmt.Errorf("usb source on %s requires cgo-enabled build", runtime.GOOS)
}
