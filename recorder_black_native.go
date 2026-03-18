//go:build (windows || darwin || linux) && cgo

package webrtp

import (
	"fmt"
	"image"
)

type h264OfflineFrameGenerator struct {
	enc   nativeH264FrameEncoder
	frame *image.RGBA
	first bool
}

func validateOfflineBlackSupport(codec string, width, height int) error {
	if codec != "h264" {
		return fmt.Errorf("offlineMode=black is currently only supported for h264 recordings")
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("offlineMode=black requires initialized stream dimensions")
	}
	return nil
}

func newOfflineFrameGenerator(codec string, width, height int) (offlineFrameGenerator, error) {
	if err := validateOfflineBlackSupport(codec, width, height); err != nil {
		return nil, err
	}
	enc, err := newNativeH264FrameEncoder()
	if err != nil {
		return nil, fmt.Errorf("offlineMode=black h264 encoder: %w", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = 0
		img.Pix[i+1] = 0
		img.Pix[i+2] = 0
		img.Pix[i+3] = 0xff
	}
	return &h264OfflineFrameGenerator{enc: enc, frame: img, first: true}, nil
}

func (g *h264OfflineFrameGenerator) NextFrame() ([]byte, bool, error) {
	if g == nil || g.enc == nil || g.frame == nil {
		return nil, false, fmt.Errorf("offline black generator is not initialized")
	}
	annexb, err := g.enc.Encode(g.frame)
	if err != nil {
		return nil, false, err
	}
	avcc := AnnexbToAvcc(AnnexbToNalus(annexb))
	isIDR := g.first
	g.first = false
	return avcc, isIDR, nil
}

func (g *h264OfflineFrameGenerator) Close() error {
	if g == nil || g.enc == nil {
		return nil
	}
	err := g.enc.Close()
	g.enc = nil
	return err
}
