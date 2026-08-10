package webrtp

import "strings"

type UsbDshowMoniker struct {
	Id                 string
	Name               string
	DevicePathReadable bool
}

func UsbDshowDeviceMatch(entries []*UsbDshowMoniker, device string) (*UsbDshowMoniker, bool) {
	device = strings.TrimSpace(device)
	if device == "" || strings.EqualFold(device, "default") {
		return nil, false
	}
	// * only software filters qualify, and the unambiguous moniker id wins over the friendly name
	for _, entry := range entries {
		if entry == nil || entry.DevicePathReadable || entry.Id == "" {
			continue
		}
		if strings.EqualFold(entry.Id, device) {
			return entry, true
		}
	}
	for _, entry := range entries {
		if entry == nil || entry.DevicePathReadable || entry.Id == "" {
			continue
		}
		if entry.Name != "" && strings.EqualFold(entry.Name, device) {
			return entry, true
		}
	}
	return nil, false
}

func UsbDshowSoftwareDevicesSelect(entries []*UsbDshowMoniker) []*UsbDevice {
	devices := make([]*UsbDevice, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Id == "" {
			continue
		}
		// * a readable device path marks a pnp driver-backed moniker already covered by media foundation
		if entry.DevicePathReadable {
			continue
		}
		name := entry.Name
		if name == "" {
			name = entry.Id
		}
		devices = append(devices, &UsbDevice{
			Id:   entry.Id,
			Name: name,
			Kind: UsbDeviceKindVirtual,
		})
	}
	return devices
}

func UsbMfKindClassify(hwSource string) UsbDeviceKind {
	// * the documented default of the hardware source attribute is false, so a missing or failed read must stay hardware
	if hwSource == "0" {
		return UsbDeviceKindVirtual
	}
	return UsbDeviceKindHardware
}
