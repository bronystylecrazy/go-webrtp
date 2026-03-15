//go:build !windows || !cgo

package webrtp

import "image"

func tryEncodeImageNative(img image.Image, format string, quality int) ([]byte, bool, error) {
	_ = img
	_ = format
	_ = quality
	return nil, false, nil
}
