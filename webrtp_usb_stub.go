//go:build !linux && !darwin && !windows

package webrtp

import (
	"context"
	"fmt"
)

func (r *Instance) connectUsb(ctx context.Context) (sourceConn, error) {
	_ = ctx
	return nil, fmt.Errorf("usb source is only supported on linux")
}
