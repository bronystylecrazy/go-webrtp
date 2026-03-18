//go:build cgo

package webrtp

import "image"

type nativeH264FrameEncoder interface {
	Encode(*image.RGBA) ([]byte, error)
	Close() error
}
