//go:build (darwin || windows) && !cgo

package webrtp

import (
	"context"
	"fmt"
	"runtime"
)

func (r *Instance) connectUsb(ctx context.Context) (sourceConn, error) {
	_ = ctx
	return nil, fmt.Errorf("usb source on %s requires cgo-enabled build", runtime.GOOS)
}

func UsbDeviceList() ([]*UsbDevice, error) {
	return nil, fmt.Errorf("usb device listing on %s requires cgo-enabled build", runtime.GOOS)
}
