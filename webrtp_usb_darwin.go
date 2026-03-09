//go:build darwin

package webrtp

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework Foundation -framework VideoToolbox
#include <stdint.h>
#include <stdlib.h>

typedef void *WebrtpUsbMacCaptureRef;

extern void WebrtpUsbMacPacket(uintptr_t handle, void *data, int length, uint32_t pts90k);
extern void WebrtpUsbMacError(uintptr_t handle, char *msg);

WebrtpUsbMacCaptureRef WebrtpUsbMacCaptureStart(const char *device, const char *codec, double fps, uintptr_t handle, char **errOut);
void WebrtpUsbMacCaptureStop(WebrtpUsbMacCaptureRef ref);
char *WebrtpUsbMacDeviceList(char **errOut);
*/
import "C"

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

type usbConn struct {
	ref    C.WebrtpUsbMacCaptureRef
	cancel context.CancelFunc
	once   sync.Once
}

type usbRegistryEntry struct {
	handler *videoHandler
	logger  Logger
	codec   string
	cancel  context.CancelFunc
}

var usbRegistry sync.Map
var usbRegistrySeq atomic.Uintptr

func (r *usbConn) Close() {
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.ref != nil {
			C.WebrtpUsbMacCaptureStop(r.ref)
			r.ref = nil
		}
	})
}

func (r *Instance) connectUsb(ctx context.Context) (*usbConn, error) {
	device := strings.TrimSpace(r.cfg.Device)
	if device == "" {
		return nil, fmt.Errorf("usb source requires device")
	}
	codec := strings.ToLower(strings.TrimSpace(r.cfg.Codec))
	if codec != "h264" && codec != "h265" {
		return nil, fmt.Errorf("usb source requires codec to be h264 or h265")
	}

	fps := r.cfg.FrameRate

	usbCtx, cancel := context.WithCancel(ctx)
	conn := &usbConn{cancel: cancel}
	handler := &videoHandler{hub: r.hub, logger: r.logger}
	handle := usbRegistrySeq.Add(1)
	usbRegistry.Store(handle, &usbRegistryEntry{
		handler: handler,
		logger:  r.logger,
		codec:   codec,
		cancel:  cancel,
	})

	cDevice := C.CString(device)
	cCodec := C.CString(codec)
	defer C.free(unsafe.Pointer(cDevice))
	defer C.free(unsafe.Pointer(cCodec))

	var cErr *C.char
	ref := C.WebrtpUsbMacCaptureStart(cDevice, cCodec, C.double(fps), C.uintptr_t(handle), &cErr)
	if ref == nil {
		usbRegistry.Delete(handle)
		cancel()
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("start usb capture: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("start usb capture: unknown error")
	}

	conn.ref = ref
	r.logger.Printf("USB stream active (%s, codec=%s)", device, strings.ToUpper(codec))

	go func() {
		<-usbCtx.Done()
		conn.Close()
		usbRegistry.Delete(handle)
	}()

	return conn, nil
}

//export WebrtpUsbMacPacket
func WebrtpUsbMacPacket(handle C.uintptr_t, data unsafe.Pointer, length C.int, pts90k C.uint32_t) {
	entryValue, ok := usbRegistry.Load(uintptr(handle))
	if !ok {
		return
	}
	entry := entryValue.(*usbRegistryEntry)
	au := AnnexbToNalus(C.GoBytes(data, length))
	if len(au) == 0 {
		return
	}
	switch entry.codec {
	case "h264":
		entry.handler.processH264(au, uint32(pts90k), nil, nil)
	case "h265":
		entry.handler.processH265(au, uint32(pts90k), nil, nil, nil)
	}
}

//export WebrtpUsbMacError
func WebrtpUsbMacError(handle C.uintptr_t, msg *C.char) {
	entryValue, ok := usbRegistry.Load(uintptr(handle))
	if !ok {
		return
	}
	entry := entryValue.(*usbRegistryEntry)
	if msg != nil {
		entry.logger.Printf("usb capture failed: %s", C.GoString(msg))
	} else {
		entry.logger.Printf("usb capture failed")
	}
	entry.cancel()
}

func UsbDeviceList() ([]*UsbDevice, error) {
	var cErr *C.char
	result := C.WebrtpUsbMacDeviceList(&cErr)
	if result == nil {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("list usb devices: %s", C.GoString(cErr))
		}
		return make([]*UsbDevice, 0), nil
	}
	defer C.free(unsafe.Pointer(result))

	lines := strings.Split(strings.TrimSpace(C.GoString(result)), "\n")
	devices := make([]*UsbDevice, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		device := &UsbDevice{}
		device.Id = parts[0]
		if len(parts) > 1 && parts[1] != "" {
			device.Name = parts[1]
		} else {
			device.Name = parts[0]
		}
		devices = append(devices, device)
	}
	slices.SortFunc(devices, func(a, b *UsbDevice) int {
		if a.Name == b.Name {
			if a.Id < b.Id {
				return -1
			}
			if a.Id > b.Id {
				return 1
			}
			return 0
		}
		if a.Name < b.Name {
			return -1
		}
		return 1
	})
	return devices, nil
}
