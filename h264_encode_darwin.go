//go:build darwin && cgo

package webrtp

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework Foundation -framework VideoToolbox
#include <stdint.h>
#include <stdlib.h>

typedef void *WebrtpVTH264EncoderRef;

WebrtpVTH264EncoderRef WebrtpVTH264EncoderCreate(char **errOut);
void WebrtpVTH264EncoderClose(WebrtpVTH264EncoderRef ref);
int WebrtpVTH264EncoderEncodeRGBA(WebrtpVTH264EncoderRef ref, const void *rgba, int width, int height, int stride, void **outData, int *outLen, char **errOut);
void WebrtpVTH264EncoderFreeBuffer(void *ptr);
*/
import "C"

import (
	"fmt"
	"image"
	"unsafe"
)

type videoToolboxH264FrameEncoder struct {
	ref C.WebrtpVTH264EncoderRef
}

func newNativeH264FrameEncoder() (nativeH264FrameEncoder, error) {
	var cErr *C.char
	ref := C.WebrtpVTH264EncoderCreate(&cErr)
	if ref == nil {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("create videotoolbox h264 encoder: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("create videotoolbox h264 encoder: unknown error")
	}
	return &videoToolboxH264FrameEncoder{ref: ref}, nil
}

func (e *videoToolboxH264FrameEncoder) Encode(img *image.RGBA) ([]byte, error) {
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
	status := C.WebrtpVTH264EncoderEncodeRGBA(
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
			return nil, fmt.Errorf("videotoolbox h264 encode: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("videotoolbox h264 encode failed")
	}
	defer C.WebrtpVTH264EncoderFreeBuffer(outData)
	return append([]byte(nil), unsafe.Slice((*byte)(outData), int(outLen))...), nil
}

func (e *videoToolboxH264FrameEncoder) Close() error {
	if e == nil || e.ref == nil {
		return nil
	}
	C.WebrtpVTH264EncoderClose(e.ref)
	e.ref = nil
	return nil
}
