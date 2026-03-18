//go:build linux && cgo

package webrtp

import (
	"fmt"
	"image"

	openh264 "github.com/Azunyan1111/openh264-go"
)

type openH264FrameEncoder struct {
	enc *openh264.Encoder
}

func newNativeH264FrameEncoder() (nativeH264FrameEncoder, error) {
	params := openh264.NewEncoderParams()
	params.UsageType = openh264.CameraVideoRealTime
	params.RCMode = openh264.RCBitrateMode
	params.EnableFrameSkip = false
	params.IntraPeriod = 30
	params.SliceNum = 1
	params.SliceMode = openh264.SMSingleSlice
	params.MaxFrameRate = 30
	params.BitRate = 500_000

	enc, err := openh264.NewEncoder(params)
	if err != nil {
		return nil, fmt.Errorf("create openh264 encoder: %w", err)
	}
	if err := enc.ForceKeyFrame(); err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("prepare openh264 keyframe: %w", err)
	}
	return &openH264FrameEncoder{enc: enc}, nil
}

func (e *openH264FrameEncoder) Encode(img *image.RGBA) ([]byte, error) {
	if e == nil || e.enc == nil || img == nil {
		return nil, fmt.Errorf("invalid h264 frame encoder input")
	}
	if img.Rect.Min.X != 0 || img.Rect.Min.Y != 0 {
		img = imageToRGBA(img)
	}
	if len(img.Pix) == 0 {
		return nil, fmt.Errorf("empty rgba frame")
	}
	yuv := rgbaToYCbCr420(img)
	data, err := e.enc.Encode(yuv)
	if err != nil {
		return nil, fmt.Errorf("openh264 encode: %w", err)
	}
	return data, nil
}

func (e *openH264FrameEncoder) Close() error {
	if e == nil || e.enc == nil {
		return nil
	}
	err := e.enc.Close()
	e.enc = nil
	return err
}

func rgbaToYCbCr420(img *image.RGBA) *image.YCbCr {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	dst := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for y := 0; y < h; y += 2 {
		srcRow0 := y * img.Stride
		srcRow1 := srcRow0
		if y+1 < h {
			srcRow1 = (y + 1) * img.Stride
		}
		yRow0 := y * dst.YStride
		yRow1 := yRow0
		if y+1 < h {
			yRow1 = (y + 1) * dst.YStride
		}
		cRow := (y / 2) * dst.CStride
		for x := 0; x < w; x += 2 {
			var cbSum, crSum uint32
			samples := 0
			for dy := 0; dy < 2; dy++ {
				yy := y + dy
				if yy >= h {
					continue
				}
				srcRow := srcRow0
				dstRow := yRow0
				if dy == 1 {
					srcRow = srcRow1
					dstRow = yRow1
				}
				for dx := 0; dx < 2; dx++ {
					xx := x + dx
					if xx >= w {
						continue
					}
					i := srcRow + xx*4
					r := img.Pix[i+0]
					g := img.Pix[i+1]
					bl := img.Pix[i+2]
					yyv, cb, cr := colorToYCbCr(r, g, bl)
					dst.Y[dstRow+xx] = yyv
					cbSum += uint32(cb)
					crSum += uint32(cr)
					samples++
				}
			}
			if samples == 0 {
				continue
			}
			dst.Cb[cRow+x/2] = uint8(cbSum / uint32(samples))
			dst.Cr[cRow+x/2] = uint8(crSum / uint32(samples))
		}
	}
	return dst
}

func colorToYCbCr(r, g, b uint8) (yy, cb, cr uint8) {
	yy = clampByte(((66*int(r) + 129*int(g) + 25*int(b) + 128) >> 8) + 16)
	cb = clampByte(((-38*int(r) - 74*int(g) + 112*int(b) + 128) >> 8) + 128)
	cr = clampByte(((112*int(r) - 94*int(g) - 18*int(b) + 128) >> 8) + 128)
	return yy, cb, cr
}

func clampByte(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
