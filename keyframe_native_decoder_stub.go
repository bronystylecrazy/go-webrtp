//go:build !cgo

package webrtp

import (
	"fmt"
	"image"
)

type nativeH264Decoder interface {
	Decode([]byte) (image.Image, error)
	Close() error
}

func (w *decoderWorker) decodeH264(annexb []byte) (image.Image, error) {
	_ = annexb
	return nil, fmt.Errorf("native h264 decoder unavailable without cgo")
}

func (w *decoderWorker) close() {}
