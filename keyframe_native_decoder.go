//go:build cgo && !darwin && !windows

package webrtp

import (
	"bytes"
	"fmt"
	"image"

	openh264 "github.com/Azunyan1111/openh264-go"
)

type nativeH264Decoder interface {
	Decode([]byte) (image.Image, error)
	Close() error
}

type openH264Decoder struct {
	dec *openh264.Decoder
}

func newNativeH264Decoder() (nativeH264Decoder, error) {
	dec, err := openh264.NewDecoder(bytes.NewReader(nil))
	if err != nil {
		return nil, fmt.Errorf("create openh264 decoder: %w", err)
	}
	return &openH264Decoder{dec: dec}, nil
}

func (d *openH264Decoder) Decode(data []byte) (image.Image, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty h264 payload")
	}
	img, err := d.dec.Decode(data)
	if err != nil {
		return nil, err
	}
	if img != nil {
		return img, nil
	}
	return d.dec.Flush()
}

func (d *openH264Decoder) Close() error {
	if d == nil || d.dec == nil {
		return nil
	}
	return d.dec.Close()
}

func (w *decoderWorker) decodeH264(annexb []byte) (image.Image, error) {
	if w == nil {
		return nil, fmt.Errorf("missing decoder worker")
	}
	if w.h264 == nil {
		dec, err := newNativeH264Decoder()
		if err != nil {
			return nil, err
		}
		w.h264 = dec
	}
	return w.h264.Decode(annexb)
}

func (w *decoderWorker) close() {
	if w == nil || w.h264 == nil {
		return
	}
	_ = w.h264.Close()
	w.h264 = nil
}
