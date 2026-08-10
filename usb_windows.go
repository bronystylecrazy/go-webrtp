//go:build windows

package webrtp

/*
#cgo LDFLAGS: -lole32 -loleaut32 -lmfplat -lmf -lmfuuid -lmfreadwrite -lpropsys -lstrmiids -luuid
#include <stdint.h>
#include <stdlib.h>

typedef void *WebrtpUsbWinCaptureRef;

extern void WebrtpUsbWinPacket(uintptr_t handle, void *data, int length, uint32_t pts90k);
extern void WebrtpUsbWinError(uintptr_t handle, char *msg);

WebrtpUsbWinCaptureRef WebrtpUsbWinCaptureStart(const char *device, const char *codec, const char *h264Profile, int width, int height, double fps, int bitrateKbps, int useDshow, uintptr_t handle, char **errOut);
void WebrtpUsbWinCaptureStop(WebrtpUsbWinCaptureRef ref);
char *WebrtpUsbWinDeviceList(char **errOut);
char *WebrtpUsbWinDeviceCapabilities(const char *device, char **errOut);
char *WebrtpUsbWinDshowDeviceList(char **errOut);
char *WebrtpUsbWinDshowDeviceCapabilities(const char *device, char **errOut);
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
	ref    C.WebrtpUsbWinCaptureRef
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
			C.WebrtpUsbWinCaptureStop(r.ref)
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

	useDshow := 0
	if entries, dshowErr := (&dshowDeviceProvider{}).MonikerList(); dshowErr == nil {
		if entry, ok := UsbDshowDeviceMatch(entries, device); ok {
			if codec != "h264" {
				return nil, fmt.Errorf("usb source %s is a directshow virtual device and supports codec h264 only", device)
			}
			useDshow = 1
			device = entry.Id
		}
	}

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
	ref := C.WebrtpUsbWinCaptureStart(cDevice, cCodec, cH264Profile, C.int(r.cfg.Width), C.int(r.cfg.Height), C.double(r.cfg.FrameRate), C.int(r.cfg.BitrateKbps), C.int(useDshow), C.uintptr_t(handle), &cErr)
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
	if codec == "h264" && strings.TrimSpace(r.cfg.H264Profile) != "" {
		r.logger.Printf("USB stream active (%s, codec=%s, h264Profile=%s)", device, strings.ToUpper(codec), r.cfg.H264Profile)
	} else {
		r.logger.Printf("USB stream active (%s, codec=%s)", device, strings.ToUpper(codec))
	}

	go func() {
		<-usbCtx.Done()
		conn.Close()
		usbRegistry.Delete(handle)
	}()

	return conn, nil
}

//export WebrtpUsbWinPacket
func WebrtpUsbWinPacket(handle C.uintptr_t, data unsafe.Pointer, length C.int, pts90k C.uint32_t) {
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

//export WebrtpUsbWinError
func WebrtpUsbWinError(handle C.uintptr_t, msg *C.char) {
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

type mfDeviceProvider struct{}

type dshowDeviceProvider struct{}

func init() {
	DeviceProviderRegister(&mfDeviceProvider{})
	DeviceProviderRegister(&dshowDeviceProvider{})
}

func (r *mfDeviceProvider) DeviceProviderName() string {
	return "mediafoundation"
}

func (r *mfDeviceProvider) DeviceProviderPrecedence() int {
	return DeviceProviderPrecedenceNative
}

func (r *mfDeviceProvider) DeviceList() ([]*UsbDevice, error) {
	var cErr *C.char
	result := C.WebrtpUsbWinDeviceList(&cErr)
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
		device := &UsbDevice{Id: row[0]}
		if len(row) > 1 && row[1] != "" {
			device.Name = row[1]
		} else {
			device.Name = row[0]
		}
		hwSource := ""
		if len(row) > 2 {
			hwSource = row[2]
		}
		device.Kind = UsbMfKindClassify(hwSource)
		devices = append(devices, device)
	}
	return devices, nil
}

func (r *mfDeviceProvider) DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))

	var cErr *C.char
	result := C.WebrtpUsbWinDeviceCapabilities(cDevice, &cErr)
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

func (r *dshowDeviceProvider) DeviceProviderName() string {
	return "directshow"
}

func (r *dshowDeviceProvider) DeviceProviderPrecedence() int {
	return DeviceProviderPrecedenceVirtual
}

func (r *dshowDeviceProvider) MonikerList() ([]*UsbDshowMoniker, error) {
	var cErr *C.char
	result := C.WebrtpUsbWinDshowDeviceList(&cErr)
	if result == nil {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("list directshow devices: %s", C.GoString(cErr))
		}
		return make([]*UsbDshowMoniker, 0), nil
	}
	defer C.free(unsafe.Pointer(result))

	rows := usbDeviceLinesParse(C.GoString(result))
	entries := make([]*UsbDshowMoniker, 0, len(rows))
	for _, row := range rows {
		entry := &UsbDshowMoniker{Id: row[0]}
		if len(row) > 1 {
			entry.Name = row[1]
		}
		if len(row) > 2 {
			entry.DevicePathReadable = row[2] == "1"
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (r *dshowDeviceProvider) DeviceList() ([]*UsbDevice, error) {
	entries, err := r.MonikerList()
	if err != nil {
		return nil, err
	}
	return UsbDshowSoftwareDevicesSelect(entries), nil
}

func (r *dshowDeviceProvider) DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))

	var cErr *C.char
	result := C.WebrtpUsbWinDshowDeviceCapabilities(cDevice, &cErr)
	if result == nil {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return nil, fmt.Errorf("directshow capabilities: %s", C.GoString(cErr))
		}
		return nil, fmt.Errorf("directshow capabilities: unknown error")
	}
	defer C.free(unsafe.Pointer(result))

	caps := &UsbDeviceCapabilities{}
	if err := json.Unmarshal([]byte(C.GoString(result)), caps); err != nil {
		return nil, fmt.Errorf("parse directshow capabilities: %w", err)
	}
	return caps, nil
}
