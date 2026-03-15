//go:build windows && cgo

package webrtp

/*
#cgo LDFLAGS: -lole32 -loleaut32 -lmfplat -lmf -lmfuuid -lpropsys -ld3d11 -ldxgi
#include <stdint.h>
#include <stdlib.h>

typedef void *WebrtpMFH264DecoderRef;

WebrtpMFH264DecoderRef WebrtpMFH264DecoderCreate(char **errOut);
void WebrtpMFH264DecoderClose(WebrtpMFH264DecoderRef ref);
int WebrtpMFH264DecoderDecodeH264(WebrtpMFH264DecoderRef ref, const void *sample, int sampleLen, const void *sps, int spsLen, const void *pps, int ppsLen, void **outData, int *outWidth, int *outHeight, int *outStride, char **errOut);
void WebrtpMFH264DecoderFreeFrame(void *ptr);
char *WebrtpMFH264DecoderDebugInfo(WebrtpMFH264DecoderRef ref);
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

type mediaFoundationH264Decoder struct {
	ref C.WebrtpMFH264DecoderRef
	sps []byte
	pps []byte
}

func newNativeH264Decoder() (nativeH264Decoder, error) {
	mf, err := newMediaFoundationH264Decoder()
	if err == nil {
		return mf, nil
	}
	dec, openErr := newOpenH264Decoder()
	if openErr != nil {
		return nil, fmt.Errorf("create media foundation decoder: %v; create openh264 decoder: %w", err, openErr)
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

func newMediaFoundationH264Decoder() (nativeH264Decoder, error) {
	var cErr *C.char
	ref := C.WebrtpMFH264DecoderCreate(&cErr)
	if ref == nil {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("create media foundation decoder: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("create media foundation decoder: unknown error")
	}
	return &mediaFoundationH264Decoder{ref: ref}, nil
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

func (d *mediaFoundationH264Decoder) Decode(annexb []byte) (image.Image, error) {
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
		return nil, fmt.Errorf("missing h264 parameter sets for media foundation decode")
	}
	sample := AnnexbToAvcc(filtered)
	if len(sample) == 0 {
		return nil, fmt.Errorf("missing decodable h264 sample")
	}

	var outData unsafe.Pointer
	var outWidth, outHeight, outStride C.int
	var cErr *C.char
	status := C.WebrtpMFH264DecoderDecodeH264(
		d.ref,
		unsafe.Pointer(&sample[0]), C.int(len(sample)),
		unsafe.Pointer(&d.sps[0]), C.int(len(d.sps)),
		unsafe.Pointer(&d.pps[0]), C.int(len(d.pps)),
		&outData, &outWidth, &outHeight, &outStride, &cErr,
	)
	if status == 0 {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("media foundation decode: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("media foundation decode failed")
	}
	defer C.WebrtpMFH264DecoderFreeFrame(outData)

	width := int(outWidth)
	height := int(outHeight)
	stride := int(outStride)
	src := unsafe.Slice((*byte)(outData), stride*height)
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	if stride == dst.Stride {
		copy(dst.Pix, src)
		return dst, nil
	}
	rowBytes := width * 4
	for y := 0; y < height; y++ {
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+rowBytes], src[y*stride:y*stride+rowBytes])
	}
	return dst, nil
}

func (d *mediaFoundationH264Decoder) Close() error {
	if d == nil || d.ref == nil {
		return nil
	}
	C.WebrtpMFH264DecoderClose(d.ref)
	d.ref = nil
	return nil
}

func (d *mediaFoundationH264Decoder) DebugInfo() string {
	if d == nil || d.ref == nil {
		return ""
	}
	info := C.WebrtpMFH264DecoderDebugInfo(d.ref)
	if info == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(info))
	return C.GoString(info)
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
	if w == nil {
		return
	}
	if w.h264 != nil {
		_ = w.h264.Close()
		w.h264 = nil
	}
	if w.h264Encoder != nil {
		_ = w.h264Encoder.Close()
		w.h264Encoder = nil
	}
}
