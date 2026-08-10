package webrtp

type UsbDshowMoniker struct {
	Id                 string
	Name               string
	DevicePathReadable bool
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
