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

WebrtpUsbMacCaptureRef WebrtpUsbMacCaptureStart(const char *device, const char *codec, const char *h264Profile, int width, int height, double fps, int bitrateKbps, uintptr_t handle, char **errOut);
void WebrtpUsbMacCaptureStop(WebrtpUsbMacCaptureRef ref);
void WebrtpUsbMacCaptureForceKeyFrame(WebrtpUsbMacCaptureRef ref);
char *WebrtpUsbMacDeviceList(char **errOut);
char *WebrtpUsbMacDeviceCapabilities(const char *device, char **errOut);
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
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

func (r *usbConn) ForceNextKeyFrame() error {
	if r.ref == nil {
		return fmt.Errorf("usb capture is not active")
	}
	C.WebrtpUsbMacCaptureForceKeyFrame(r.ref)
	return nil
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
	handler := &videoHandler{hub: r.hub, logger: r.logger, instance: r}
	handle := usbRegistrySeq.Add(1)
	usbRegistry.Store(handle, &usbRegistryEntry{
		handler: handler,
		logger:  r.logger,
		codec:   codec,
		cancel:  cancel,
	})

	cDevice := C.CString(device)
	cCodec := C.CString(codec)
	cH264Profile := C.CString(r.cfg.H264Profile)
	defer C.free(unsafe.Pointer(cDevice))
	defer C.free(unsafe.Pointer(cCodec))
	defer C.free(unsafe.Pointer(cH264Profile))

	var cErr *C.char
	ref := C.WebrtpUsbMacCaptureStart(cDevice, cCodec, cH264Profile, C.int(r.cfg.Width), C.int(r.cfg.Height), C.double(fps), C.int(r.cfg.BitrateKbps), C.uintptr_t(handle), &cErr)
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

type macDeviceProvider struct{}

func init() {
	DeviceProviderRegister(&macDeviceProvider{})
}

func (r *macDeviceProvider) DeviceProviderName() string {
	return "avfoundation"
}

func (r *macDeviceProvider) DeviceProviderPrecedence() int {
	return DeviceProviderPrecedenceNative
}

func (r *macDeviceProvider) DeviceList() ([]*UsbDevice, error) {
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

	rows := usbDeviceLinesParse(C.GoString(result))
	devices := make([]*UsbDevice, 0, len(rows))
	for _, row := range rows {
		device := &UsbDevice{Id: row[0], Kind: UsbDeviceKindHardware}
		if len(row) > 1 && row[1] != "" {
			device.Name = row[1]
		} else {
			device.Name = row[0]
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func (r *macDeviceProvider) DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))

	var cErr *C.char
	result := C.WebrtpUsbMacDeviceCapabilities(cDevice, &cErr)
	if result == nil {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("usb capabilities: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("usb capabilities: unknown error")
	}
	defer C.free(unsafe.Pointer(result))

	caps := &UsbDeviceCapabilities{}
	if err := json.Unmarshal([]byte(C.GoString(result)), caps); err != nil {
		return nil, fmt.Errorf("parse usb capabilities: %w", err)
	}
	return caps, nil
}
