//go:build windows && cgo

package webrtp

/*
#cgo LDFLAGS: -lole32 -loleaut32 -lmfplat -lmf -lmfuuid -lpropsys -ld3d11 -ldxgi
#include <stdint.h>
#include <stdlib.h>

typedef void *WebrtpMFH264EncoderRef;

WebrtpMFH264EncoderRef WebrtpMFH264EncoderCreate(char **errOut);
void WebrtpMFH264EncoderClose(WebrtpMFH264EncoderRef ref);
int WebrtpMFH264EncoderEncodeRGBA(WebrtpMFH264EncoderRef ref, const void *rgba, int width, int height, int stride, void **outData, int *outLen, char **errOut);
void WebrtpMFH264EncoderFreeBuffer(void *ptr);
*/
import "C"

import (
	"fmt"
	"image"
	"unsafe"
)

type mediaFoundationH264FrameEncoder struct {
	ref C.WebrtpMFH264EncoderRef
}

func newNativeH264FrameEncoder() (nativeH264FrameEncoder, error) {
	var cErr *C.char
	ref := C.WebrtpMFH264EncoderCreate(&cErr)
	if ref == nil {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("create media foundation h264 encoder: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("create media foundation h264 encoder: unknown error")
	}
	return &mediaFoundationH264FrameEncoder{ref: ref}, nil
}

func (e *mediaFoundationH264FrameEncoder) Encode(img *image.RGBA) ([]byte, error) {
	if e == nil || e.ref == nil || img == nil {
		return nil, fmt.Errorf("invalid h264 frame encoder input")
	}
	if img.Rect.Min.X != 0 || img.Rect.Min.Y != 0 {
		img = imageToRGBA(img)
	}
	if len(img.Pix) == 0 {
		return nil, fmt.Errorf("empty rgba frame")
	}
	var outData unsafe.Pointer
	var outLen C.int
	var cErr *C.char
	status := C.WebrtpMFH264EncoderEncodeRGBA(
		e.ref,
		unsafe.Pointer(&img.Pix[0]),
		C.int(img.Rect.Dx()),
		C.int(img.Rect.Dy()),
		C.int(img.Stride),
		&outData,
		&outLen,
		&cErr,
	)
	if status == 0 {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("media foundation h264 encode: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("media foundation h264 encode failed")
	}
	defer C.WebrtpMFH264EncoderFreeBuffer(outData)
	return append([]byte(nil), unsafe.Slice((*byte)(outData), int(outLen))...), nil
}

func (e *mediaFoundationH264FrameEncoder) Close() error {
	if e == nil || e.ref == nil {
		return nil
	}
	C.WebrtpMFH264EncoderClose(e.ref)
	e.ref = nil
	return nil
}
