package webrtp

import (
	"context"
	"fmt"
)

type sourceConn interface {
	Close()
}

func (r *Instance) connectSource(ctx context.Context) (sourceConn, error) {
	switch r.cfg.SourceType {
	case "rtsp":
		return r.connectRtsp(ctx)
	case "usb":
		return r.connectUsb(ctx)
	case "file":
		return r.connectFile(ctx)
	default:
		return nil, fmt.Errorf("unsupported sourceType: %s", r.cfg.SourceType)
	}
}
