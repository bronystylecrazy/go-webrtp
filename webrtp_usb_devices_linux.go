//go:build linux

package webrtp

import (
	"path/filepath"
	"slices"
)

func UsbDeviceList() ([]*UsbDevice, error) {
	paths, err := filepath.Glob("/dev/video*")
	if err != nil {
		return nil, err
	}
	devices := make([]*UsbDevice, 0, len(paths))
	for _, path := range paths {
		devices = append(devices, &UsbDevice{
			Id:   path,
			Name: path,
		})
	}
	slices.SortFunc(devices, func(a, b *UsbDevice) int {
		if a.Id < b.Id {
			return -1
		}
		if a.Id > b.Id {
			return 1
		}
		return 0
	})
	return devices, nil
}
