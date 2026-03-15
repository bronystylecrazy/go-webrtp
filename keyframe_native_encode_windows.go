//go:build windows && cgo

package webrtp

/*
#cgo LDFLAGS: -lole32 -loleaut32 -lwindowscodecs
#include <stdlib.h>

int WebrtpWICEncodeJPEG(const void *rgba, int width, int height, int stride, int quality, void **outData, int *outLen, char **errOut);
void WebrtpWICFreeBuffer(void *ptr);
*/
import "C"

import (
	"fmt"
	"image"
	"unsafe"
)

func tryEncodeImageNative(img image.Image, format string, quality int) ([]byte, bool, error) {
	if format != "jpg" {
		return nil, false, nil
	}
	rgba := imageToRGBA(img)
	if rgba.Rect.Min.X != 0 || rgba.Rect.Min.Y != 0 {
		normalized := image.NewRGBA(image.Rect(0, 0, rgba.Rect.Dx(), rgba.Rect.Dy()))
		for y := 0; y < rgba.Rect.Dy(); y++ {
			srcStart := y*rgba.Stride + rgba.Rect.Min.X*4
			dstStart := y * normalized.Stride
			copy(normalized.Pix[dstStart:dstStart+rgba.Rect.Dx()*4], rgba.Pix[srcStart:srcStart+rgba.Rect.Dx()*4])
		}
		rgba = normalized
	}
	if len(rgba.Pix) == 0 {
		return []byte{}, true, nil
	}
	var outData unsafe.Pointer
	var outLen C.int
	var cErr *C.char
	status := C.WebrtpWICEncodeJPEG(
		unsafe.Pointer(&rgba.Pix[0]),
		C.int(rgba.Rect.Dx()),
		C.int(rgba.Rect.Dy()),
		C.int(rgba.Stride),
		C.int(quality),
		&outData,
		&outLen,
		&cErr,
	)
	if status == 0 {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, true, fmt.Errorf("wic jpeg encode: %s", C.GoString(cErr))
		}
		return nil, true, fmt.Errorf("wic jpeg encode failed")
	}
	defer C.WebrtpWICFreeBuffer(outData)
	return append([]byte(nil), unsafe.Slice((*byte)(outData), int(outLen))...), true, nil
}
