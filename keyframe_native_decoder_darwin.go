//go:build darwin && cgo

package webrtp

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework Foundation -framework VideoToolbox
#include <stdint.h>
#include <stdlib.h>

typedef void *WebrtpVTDecoderRef;

WebrtpVTDecoderRef WebrtpVTDecoderCreate(char **errOut);
void WebrtpVTDecoderClose(WebrtpVTDecoderRef ref);
int WebrtpVTDecoderDecodeH264(WebrtpVTDecoderRef ref, const void *sample, int sampleLen, const void *sps, int spsLen, const void *pps, int ppsLen, void **outData, int *outWidth, int *outHeight, int *outStride, char **errOut);
void WebrtpVTDecoderFreeFrame(void *ptr);
*/
import "C"

import (
	"bytes"
	"fmt"
	"image"
	"unsafe"

	openh264 "github.com/Azunyan1111/openh264-go"
)

type nativeH264Decoder interface {
	Decode([]byte) (image.Image, error)
	Close() error
}

type openH264Decoder struct {
	dec *openh264.Decoder
}

type videoToolboxH264Decoder struct {
	ref C.WebrtpVTDecoderRef
	sps []byte
	pps []byte
}

func newNativeH264Decoder() (nativeH264Decoder, error) {
	vt, err := newVideoToolboxH264Decoder()
	if err == nil {
		return vt, nil
	}
	dec, openErr := newOpenH264Decoder()
	if openErr != nil {
		return nil, fmt.Errorf("create videotoolbox decoder: %v; create openh264 decoder: %w", err, openErr)
	}
	return dec, nil
}

func newOpenH264Decoder() (nativeH264Decoder, error) {
	dec, err := openh264.NewDecoder(bytes.NewReader(nil))
	if err != nil {
		return nil, fmt.Errorf("create openh264 decoder: %w", err)
	}
	return &openH264Decoder{dec: dec}, nil
}

func newVideoToolboxH264Decoder() (nativeH264Decoder, error) {
	var cErr *C.char
	ref := C.WebrtpVTDecoderCreate(&cErr)
	if ref == nil {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("create videotoolbox decoder: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("create videotoolbox decoder: unknown error")
	}
	return &videoToolboxH264Decoder{ref: ref}, nil
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

func (d *videoToolboxH264Decoder) Decode(annexb []byte) (image.Image, error) {
	if len(annexb) == 0 {
		return nil, fmt.Errorf("empty h264 payload")
	}
	nalus := AnnexbToNalus(annexb)
	if len(nalus) == 0 {
		return nil, fmt.Errorf("invalid annexb payload")
	}
	filtered := make([][]byte, 0, len(nalus))
	for _, nalu := range nalus {
		if len(nalu) == 0 {
			continue
		}
		switch nalu[0] & 0x1F {
		case 7:
			d.sps = append(d.sps[:0], nalu...)
		case 8:
			d.pps = append(d.pps[:0], nalu...)
		default:
			filtered = append(filtered, nalu)
		}
	}
	if len(d.sps) == 0 || len(d.pps) == 0 {
		return nil, fmt.Errorf("missing h264 parameter sets for videotoolbox decode")
	}
	sample := AnnexbToAvcc(filtered)
	if len(sample) == 0 {
		return nil, fmt.Errorf("missing decodable h264 sample")
	}

	var outData unsafe.Pointer
	var outWidth C.int
	var outHeight C.int
	var outStride C.int
	var cErr *C.char
	status := C.WebrtpVTDecoderDecodeH264(
		d.ref,
		unsafe.Pointer(&sample[0]), C.int(len(sample)),
		unsafe.Pointer(&d.sps[0]), C.int(len(d.sps)),
		unsafe.Pointer(&d.pps[0]), C.int(len(d.pps)),
		&outData, &outWidth, &outHeight, &outStride, &cErr,
	)
	if status == 0 {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("videotoolbox decode: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("videotoolbox decode failed")
	}
	defer C.WebrtpVTDecoderFreeFrame(outData)

	width := int(outWidth)
	height := int(outHeight)
	stride := int(outStride)
	src := unsafe.Slice((*byte)(outData), stride*height)
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcRow := y * stride
		dstRow := y * dst.Stride
		for x := 0; x < width; x++ {
			srcPix := srcRow + x*4
			dstPix := dstRow + x*4
			dst.Pix[dstPix+0] = src[srcPix+2]
			dst.Pix[dstPix+1] = src[srcPix+1]
			dst.Pix[dstPix+2] = src[srcPix+0]
			dst.Pix[dstPix+3] = src[srcPix+3]
		}
	}
	return dst, nil
}

func (d *videoToolboxH264Decoder) Close() error {
	if d == nil || d.ref == nil {
		return nil
	}
	C.WebrtpVTDecoderClose(d.ref)
	d.ref = nil
	return nil
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
