//go:build !cgo || (!windows && !darwin && !linux)

package webrtp

import (
	"fmt"
	"image"
)

type nativeH264FrameEncoder interface {
	Encode(*image.RGBA) ([]byte, error)
	Close() error
}

func newNativeH264FrameEncoder() (nativeH264FrameEncoder, error) {
	return nil, fmt.Errorf("native h264 frame encoder is only available on windows/darwin/linux cgo builds")
}
