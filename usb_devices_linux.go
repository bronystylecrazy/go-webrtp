//go:build linux

package webrtp

import (
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

const vidiocQuerycap = 0x80685600

type v4l2Capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

type linuxDeviceProvider struct{}

func init() {
	DeviceProviderRegister(&linuxDeviceProvider{})
}

func (r *linuxDeviceProvider) DeviceProviderName() string {
	return "v4l2"
}

func (r *linuxDeviceProvider) DeviceProviderPrecedence() int {
	return DeviceProviderPrecedenceNative
}

func (r *linuxDeviceProvider) DeviceList() ([]*UsbDevice, error) {
	paths, err := filepath.Glob("/dev/video*")
	if err != nil {
		return nil, err
	}
	devices := make([]*UsbDevice, 0, len(paths))
	for _, path := range paths {
		node, err := v4l2NodeQuery(path)
		if err != nil {
			if err == unix.EINVAL {
				continue
			}
			// * keep an unqueryable node so restricted environments still match the previous listing
			devices = append(devices, &UsbDevice{Id: path, Name: path, Kind: UsbDeviceKindHardware})
			continue
		}
		if !V4l2NodeInclude(node) {
			continue
		}
		devices = append(devices, &UsbDevice{
			Id:   path,
			Name: V4l2NodeDisplayName(node),
			Kind: V4l2NodeKind(node),
		})
	}
	return devices, nil
}

func (r *linuxDeviceProvider) DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	return nil, fmt.Errorf("usb capability discovery is not supported on this platform")
}

func v4l2NodeQuery(path string) (*V4l2Node, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)

	caps := &v4l2Capability{}
	if err := usbIoctl(uintptr(fd), vidiocQuerycap, unsafe.Pointer(caps)); err != nil {
		return nil, err
	}
	return &V4l2Node{
		Path:         path,
		Driver:       v4l2String(caps.Driver[:]),
		Card:         v4l2String(caps.Card[:]),
		BusInfo:      v4l2String(caps.BusInfo[:]),
		Capabilities: caps.Capabilities,
		DeviceCaps:   caps.DeviceCaps,
	}, nil
}

func v4l2String(raw []byte) string {
	for idx, ch := range raw {
		if ch == 0 {
			return string(raw[:idx])
		}
	}
	return string(raw)
}
