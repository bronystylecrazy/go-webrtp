package webrtp

import (
	"reflect"
	"testing"
)

func TestUsbDshowSoftwareDevicesSelect(t *testing.T) {
	cases := []struct {
		name    string
		entries []*UsbDshowMoniker
		want    []*UsbDevice
	}{
		{
			name:    "empty",
			entries: nil,
			want:    []*UsbDevice{},
		},
		{
			name: "readableDevicePathIsDropped",
			entries: []*UsbDshowMoniker{
				{Id: "@device:pnp:usb#vid_1234", Name: "16MP USB Camera", DevicePathReadable: true},
			},
			want: []*UsbDevice{},
		},
		{
			name: "unreadableDevicePathIsEmittedAsVirtual",
			entries: []*UsbDshowMoniker{
				{Id: "@device:sw:{860BB310}\\{A3FCE0F5}", Name: "OBS Virtual Camera", DevicePathReadable: false},
			},
			want: []*UsbDevice{
				{Id: "@device:sw:{860BB310}\\{A3FCE0F5}", Name: "OBS Virtual Camera", Kind: UsbDeviceKindVirtual},
			},
		},
		{
			name: "mixedListKeepsOnlySoftwareFilters",
			entries: []*UsbDshowMoniker{
				{Id: "@device:pnp:one", Name: "Webcam", DevicePathReadable: true},
				{Id: "@device:sw:two", Name: "NDI Video", DevicePathReadable: false},
				{Id: "", Name: "Broken", DevicePathReadable: false},
				nil,
				{Id: "@device:sw:three", Name: "", DevicePathReadable: false},
			},
			want: []*UsbDevice{
				{Id: "@device:sw:two", Name: "NDI Video", Kind: UsbDeviceKindVirtual},
				{Id: "@device:sw:three", Name: "@device:sw:three", Kind: UsbDeviceKindVirtual},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UsbDshowSoftwareDevicesSelect(tc.entries)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestUsbDshowDeviceMatch(t *testing.T) {
	entries := []*UsbDshowMoniker{
		nil,
		{Id: "@device:pnp:usb#vid_1234", Name: "16MP USB Camera", DevicePathReadable: true},
		{Id: "@device:sw:{860BB310}\\{A3FCE0F5}", Name: "OBS Virtual Camera", DevicePathReadable: false},
		{Id: "@device:sw:{860BB310}\\{OTHER}", Name: "NDI Video", DevicePathReadable: false},
	}
	cases := []struct {
		name   string
		device string
		wantId string
		wantOk bool
	}{
		{name: "matchById", device: "@device:sw:{860BB310}\\{A3FCE0F5}", wantId: "@device:sw:{860BB310}\\{A3FCE0F5}", wantOk: true},
		{name: "matchByIdCaseInsensitive", device: "@DEVICE:SW:{860bb310}\\{a3fce0f5}", wantId: "@device:sw:{860BB310}\\{A3FCE0F5}", wantOk: true},
		{name: "matchByName", device: "OBS Virtual Camera", wantId: "@device:sw:{860BB310}\\{A3FCE0F5}", wantOk: true},
		{name: "readablePathNeverMatchesEvenOnExactName", device: "16MP USB Camera", wantOk: false},
		{name: "readablePathNeverMatchesById", device: "@device:pnp:usb#vid_1234", wantOk: false},
		{name: "noMatch", device: "Missing Camera", wantOk: false},
		{name: "emptyDevice", device: "", wantOk: false},
		{name: "defaultIsReserved", device: "default", wantOk: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry, ok := UsbDshowDeviceMatch(entries, tc.device)
			if ok != tc.wantOk {
				t.Fatalf("got ok=%v want %v", ok, tc.wantOk)
			}
			if ok && entry.Id != tc.wantId {
				t.Fatalf("got id %q want %q", entry.Id, tc.wantId)
			}
		})
	}
}

func TestUsbMfKindClassify(t *testing.T) {
	cases := []struct {
		hwSource string
		want     UsbDeviceKind
	}{
		{"1", UsbDeviceKindHardware},
		{"0", UsbDeviceKindVirtual},
		{"", UsbDeviceKindHardware},
		{"garbage", UsbDeviceKindHardware},
	}
	for _, tc := range cases {
		if got := UsbMfKindClassify(tc.hwSource); got != tc.want {
			t.Fatalf("hwSource %q: got %s want %s", tc.hwSource, got, tc.want)
		}
	}
}
