package webrtp

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type fakeDeviceProvider struct {
	name       string
	precedence int
	devices    []*UsbDevice
	err        error
	caps       map[string]*UsbDeviceCapabilities
}

func (r *fakeDeviceProvider) DeviceProviderName() string {
	return r.name
}

func (r *fakeDeviceProvider) DeviceProviderPrecedence() int {
	return r.precedence
}

func (r *fakeDeviceProvider) DeviceList() ([]*UsbDevice, error) {
	if r.err != nil {
		return nil, r.err
	}
	devices := make([]*UsbDevice, 0, len(r.devices))
	for _, device := range r.devices {
		clone := *device
		devices = append(devices, &clone)
	}
	return devices, nil
}

func (r *fakeDeviceProvider) DeviceCapabilitiesGet(device string) (*UsbDeviceCapabilities, error) {
	if r.err != nil {
		return nil, r.err
	}
	caps, ok := r.caps[device]
	if !ok {
		return nil, fmt.Errorf("device not found: %s", device)
	}
	return caps, nil
}

func TestUsbDeviceListAggregateMergesAndOrdersDeterministically(t *testing.T) {
	providers := []DeviceProvider{
		&fakeDeviceProvider{
			name:       "native",
			precedence: DeviceProviderPrecedenceNative,
			devices: []*UsbDevice{
				{Id: "cam-b", Name: "Camera B"},
				{Id: "cam-a", Name: "Camera A"},
			},
		},
		&fakeDeviceProvider{
			name:       "synthetic",
			precedence: DeviceProviderPrecedenceSynthetic,
			devices: []*UsbDevice{
				{Id: "loop", Name: "Loop", Kind: UsbDeviceKindSynthetic},
			},
		},
	}

	first, err := usbDeviceListAggregate(providers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := usbDeviceListAggregate(providers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("aggregation is not stable across calls:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	names := make([]string, 0, len(first))
	for _, device := range first {
		names = append(names, device.Name)
	}
	if !reflect.DeepEqual(names, []string{"Camera A", "Camera B", "Loop"}) {
		t.Fatalf("unexpected ordering: %v", names)
	}
	if first[0].Provider != "native" || first[2].Provider != "synthetic" {
		t.Fatalf("provider attribution missing: %+v", first)
	}
	if first[0].Kind != UsbDeviceKindHardware {
		t.Fatalf("expected default kind hardware, got %s", first[0].Kind)
	}
}

func TestUsbDeviceListAggregateDeduplicatesByPrecedence(t *testing.T) {
	providers := []DeviceProvider{
		&fakeDeviceProvider{
			name:       "extra",
			precedence: DeviceProviderPrecedenceVirtual,
			devices: []*UsbDevice{
				{Id: "shared", Name: "Shared Camera", Kind: UsbDeviceKindVirtual},
			},
		},
		&fakeDeviceProvider{
			name:       "native",
			precedence: DeviceProviderPrecedenceNative,
			devices: []*UsbDevice{
				{Id: "shared", Name: "Shared Camera", Kind: UsbDeviceKindHardware},
			},
		},
	}

	devices, err := usbDeviceListAggregate(deviceProvidersOrdered(providers))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected one device after de-duplication, got %d", len(devices))
	}
	if devices[0].Provider != "native" || devices[0].Kind != UsbDeviceKindHardware {
		t.Fatalf("expected the higher precedence provider to win, got %+v", devices[0])
	}
}

func TestUsbDeviceListAggregateWindowsDualEnumerationModel(t *testing.T) {
	// * model the windows split: media foundation reports the webcam, directshow reports every moniker and only unreadable device paths survive
	monikers := []*UsbDshowMoniker{
		{Id: "@device:pnp:usb#vid", Name: "16MP USB Camera", DevicePathReadable: true},
		{Id: "@device:sw:obs", Name: "OBS Virtual Camera", DevicePathReadable: false},
	}
	providers := []DeviceProvider{
		&fakeDeviceProvider{
			name:       "mediafoundation",
			precedence: DeviceProviderPrecedenceNative,
			devices: []*UsbDevice{
				{Id: "\\\\?\\usb#vid", Name: "16MP USB Camera", Kind: UsbDeviceKindHardware},
			},
		},
		&fakeDeviceProvider{
			name:       "directshow",
			precedence: DeviceProviderPrecedenceVirtual,
			devices:    UsbDshowSoftwareDevicesSelect(monikers),
		},
	}

	devices, err := usbDeviceListAggregate(deviceProvidersOrdered(providers))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("expected exactly two devices, got %+v", devices)
	}
	byName := make(map[string]*UsbDevice)
	for _, device := range devices {
		byName[device.Name] = device
	}
	webcam := byName["16MP USB Camera"]
	if webcam == nil || webcam.Provider != "mediafoundation" || webcam.Kind != UsbDeviceKindHardware {
		t.Fatalf("unexpected webcam entry: %+v", webcam)
	}
	obs := byName["OBS Virtual Camera"]
	if obs == nil || obs.Provider != "directshow" || obs.Kind != UsbDeviceKindVirtual {
		t.Fatalf("unexpected virtual camera entry: %+v", obs)
	}
}

func TestUsbDeviceListAggregateIsolatesProviderFailure(t *testing.T) {
	providers := []DeviceProvider{
		&fakeDeviceProvider{
			name:       "native",
			precedence: DeviceProviderPrecedenceNative,
			devices: []*UsbDevice{
				{Id: "cam", Name: "Camera"},
			},
		},
		&fakeDeviceProvider{
			name:       "broken",
			precedence: DeviceProviderPrecedenceVirtual,
			err:        errors.New("enumeration exploded"),
		},
	}

	devices, err := usbDeviceListAggregate(deviceProvidersOrdered(providers))
	if len(devices) != 1 || devices[0].Id != "cam" {
		t.Fatalf("expected surviving devices, got %+v", devices)
	}
	if err == nil || !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "enumeration exploded") {
		t.Fatalf("expected surfaced provider error, got %v", err)
	}
}

func TestUsbDeviceListAggregateReturnsEmptySliceNotNil(t *testing.T) {
	devices, err := usbDeviceListAggregate(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if devices == nil || len(devices) != 0 {
		t.Fatalf("expected non-nil empty slice, got %#v", devices)
	}
}

func TestUsbDeviceCapabilitiesAggregatePrefersHigherPrecedence(t *testing.T) {
	providers := []DeviceProvider{
		&fakeDeviceProvider{
			name:       "native",
			precedence: DeviceProviderPrecedenceNative,
			caps: map[string]*UsbDeviceCapabilities{
				"cam": {
					Device: &UsbDevice{Id: "cam", Name: "Camera"},
					Modes: []*UsbCapabilityMode{
						{Width: 1280, Height: 720, Fps: []float64{30}},
					},
				},
			},
		},
		&fakeDeviceProvider{
			name:       "synthetic",
			precedence: DeviceProviderPrecedenceSynthetic,
			caps: map[string]*UsbDeviceCapabilities{
				"cam": {Device: &UsbDevice{Id: "cam", Name: "Impostor"}},
			},
		},
	}

	caps, err := usbDeviceCapabilitiesAggregate(deviceProvidersOrdered(providers), "cam")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if caps.Device == nil || caps.Device.Name != "Camera" {
		t.Fatalf("expected native capabilities, got %+v", caps.Device)
	}
	if len(caps.SuggestedRenditions) == 0 {
		t.Fatal("expected suggested renditions to be populated by the aggregator")
	}
}

func TestUsbDeviceCapabilitiesAggregateJoinsErrors(t *testing.T) {
	providers := []DeviceProvider{
		&fakeDeviceProvider{name: "native", precedence: DeviceProviderPrecedenceNative},
		&fakeDeviceProvider{name: "synthetic", precedence: DeviceProviderPrecedenceSynthetic},
	}
	caps, err := usbDeviceCapabilitiesAggregate(deviceProvidersOrdered(providers), "missing")
	if caps != nil || err == nil {
		t.Fatalf("expected error for unknown device, got caps=%+v err=%v", caps, err)
	}
	if !strings.Contains(err.Error(), "native") || !strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("expected both providers in error, got %v", err)
	}
}

func TestUsbDeviceLinesParse(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want [][]string
	}{
		{name: "empty", raw: "", want: [][]string{}},
		{name: "idOnly", raw: "abc", want: [][]string{{"abc"}}},
		{
			name: "idNameKind",
			raw:  "id1\tCamera\t1\n\nid2\tOther\t",
			want: [][]string{{"id1", "Camera", "1"}, {"id2", "Other"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := usbDeviceLinesParse(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
		})
	}
}

func TestUsbCaptureBackendSelect(t *testing.T) {
	cases := []struct {
		kind UsbDeviceKind
		want UsbCaptureBackend
	}{
		{UsbDeviceKindHardware, UsbCaptureBackendNative},
		{UsbDeviceKindVirtual, UsbCaptureBackendNative},
		{UsbDeviceKindSynthetic, UsbCaptureBackendFfmpeg},
	}
	for _, tc := range cases {
		if got := UsbCaptureBackendSelect(tc.kind); got != tc.want {
			t.Fatalf("kind %s: got %s want %s", tc.kind, got, tc.want)
		}
	}
}

func TestUsbDeviceOfflineReason(t *testing.T) {
	devices := []*UsbDevice{
		{Id: "cam-1", Name: "Camera One"},
	}
	if reason := usbDeviceOfflineReason("cam-1", devices); reason != "" {
		t.Fatalf("expected present device by id, got %q", reason)
	}
	if reason := usbDeviceOfflineReason("Camera One", devices); reason != "" {
		t.Fatalf("expected present device by name, got %q", reason)
	}
	if reason := usbDeviceOfflineReason("default", devices); reason != "" {
		t.Fatalf("expected default device to be skipped, got %q", reason)
	}
	if reason := usbDeviceOfflineReason("gone", devices); !strings.Contains(reason, "gone") {
		t.Fatalf("expected named cause for missing device, got %q", reason)
	}
}

func TestUsbDeviceListKeepsDevicesWhenOneProviderFails(t *testing.T) {
	defer deviceProvidersReplace([]DeviceProvider{
		&fakeDeviceProvider{
			name:       "native",
			precedence: DeviceProviderPrecedenceNative,
			devices:    []*UsbDevice{{Id: "cam-1", Name: "Camera One"}},
		},
		&fakeDeviceProvider{
			name:       "broken",
			precedence: DeviceProviderPrecedenceVirtual,
			err:        errors.New("enumeration failed"),
		},
	})()

	devices, err := UsbDeviceList()
	if err != nil {
		t.Fatalf("expected partial failure to be suppressed, got %v", err)
	}
	if len(devices) != 1 || devices[0].Id != "cam-1" {
		t.Fatalf("expected surviving device, got %+v", devices)
	}

	detailed, err := UsbDeviceListDetailed()
	if err == nil || !strings.Contains(err.Error(), "enumeration failed") {
		t.Fatalf("expected detailed listing to report the failure, got %v", err)
	}
	if len(detailed) != 1 {
		t.Fatalf("expected detailed listing to keep devices, got %+v", detailed)
	}
}

func TestUsbDeviceListReportsErrorWhenNoDevicesSurvive(t *testing.T) {
	defer deviceProvidersReplace([]DeviceProvider{
		&fakeDeviceProvider{
			name:       "native",
			precedence: DeviceProviderPrecedenceNative,
			err:        errors.New("enumeration failed"),
		},
	})()

	devices, err := UsbDeviceList()
	if err == nil || !strings.Contains(err.Error(), "enumeration failed") {
		t.Fatalf("expected total failure to surface, got %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected no devices, got %+v", devices)
	}
}

// * swaps the global registry for the duration of a test, returning the restore func
func deviceProvidersReplace(providers []DeviceProvider) func() {
	previous := deviceProviderSnapshot()
	for _, provider := range previous {
		DeviceProviderUnregister(provider.DeviceProviderName())
	}
	for _, provider := range providers {
		DeviceProviderRegister(provider)
	}
	return func() {
		for _, provider := range providers {
			DeviceProviderUnregister(provider.DeviceProviderName())
		}
		for _, provider := range previous {
			DeviceProviderRegister(provider)
		}
	}
}

func deviceProvidersOrdered(providers []DeviceProvider) []DeviceProvider {
	ordered := make([]DeviceProvider, len(providers))
	copy(ordered, providers)
	deviceProvidersSort(ordered)
	return ordered
}
