# Video device enumeration APIs — primary-source research

Research notes de-risking [issue #1 — *Support virtual and synthetic video devices across enumeration and capture*](https://github.com/bronystylecrazy/go-webrtp/issues/1).

**Ground rules used here:** every claim cites the primary source that owns it — Apple/Microsoft/kernel documentation, SDK headers on disk, or first-party source code. Secondary sources were only used to locate a primary source, which was then read directly. Anything that could not be established from a primary source is labelled **UNVERIFIED**.

Machine used for local verification: macOS 27.0 (build 26A5388g), SDK `/Library/Developer/CommandLineTools/SDKs/MacOSX.sdk` version 27.0.

---

## Answers at a glance

**macOS**

1. Virtual cameras on current macOS register as **Core Media I/O camera extensions** (`CMIOExtension`, macOS 12.3+), not DAL plug-ins. `CMIOHardwarePlugIn.h` carries `API_DEPRECATED("Use CMIOExtension instead", macos(10.7, 12.3))`.
2. **DAL plug-ins are disabled by default from macOS Sonoma 14.1.** Apple support article HT/108387 documents the recovery-mode `system-override` opt-out to re-enable them.
3. A camera extension is reachable through AVFoundation as an ordinary `AVCaptureDevice`; Apple's own sample retrieves it via `AVCaptureDevice.DiscoverySession(deviceTypes: [.externalUnknown], …)`. On macOS 14+, `.externalUnknown` is a deprecated synonym for `.external`.
4. **`kCMIOHardwarePropertyAllowScreenCaptureDevices` is not the virtual-camera opt-in.** The SDK header says it governs *screen capture devices*; it is not deprecated. (The header claims it defaults to 1, but a fresh process on macOS 27.0 read **0** — see §1.4. Either way it is irrelevant to camera extensions.)
5. OBS Studio ships a macOS camera extension from **OBS 30.0** (PR #7777, merged 2023-05-18); the legacy DAL plug-in target is still in the tree and still built.
6. **Correct implementation path:** `AVCaptureDevice.DiscoverySession` including `AVCaptureDeviceTypeExternal` (plus `AVCaptureDeviceTypeExternalUnknown` when building against/for pre-14 SDKs). No CoreMediaIO calls, no entitlement, no code-signing requirement *for the consuming app* — the requirements fall on the extension's publisher.

**Windows**

7. OBS Virtual Camera on Windows is a **pure DirectShow software filter**: OBS registers it with `IFilterMapper2::RegisterFilter(CLSID_OBS_VirtualVideo, L"OBS Virtual Camera", &moniker, &CLSID_VideoInputDeviceCategory, …)`. There is no PnP device and no KS driver behind it. This is the direct confirmation that the spec's premise is right.
8. Microsoft documents the two halves of the split (MF enumerates *device drivers* with symbolic links; DirectShow's category enumerator returns registry-registered COM filters as well as KsProxy wrappers), but **the explicit negative statement "`MFEnumDeviceSources` does not return DirectShow-only software filters" does not appear in Microsoft's documentation** — it is inferred structurally. Labelled UNVERIFIED-as-documented.
9. `MFCreateVirtualCamera` (Windows build 22000 / Windows 11) registers the virtual camera under **KS device interface categories** (`KSCATEGORY_VIDEO_CAMERA`, `KSCATEGORY_VIDEO`, `KSCATEGORY_CAPTURE`), so cameras created that way **do** appear in `MFEnumDeviceSources` — the existing code path already sees them.
10. **`MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_HW_SOURCE`** is a documented per-device hardware/software boolean on the MF activation object. The spec's rule "MF ⇒ hardware" is therefore wrong for MF virtual cameras; use this attribute instead.
11. **De-duplication:** the honest answer is that MF's `MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK` and DirectShow's moniker `DevicePath` property are each documented as *uniquely identifying a video capture device*, and the MF symbolic link is documented to be a PnP **device interface path**. That they are the *same string* for the same device is **UNVERIFIED** — Microsoft never states it. A reliable, fully-documented alternative exists (see §2.4).
12. FFmpeg's `-list_devices` "Alternative name" is `IMoniker::GetDisplayName` with every `:` rewritten to `_` — which is exactly why the issue's output shows `@device_pnp_` / `@device_sw_` rather than `@device:pnp:` / `@device:sw:`. The display-name *grammar* itself is **UNVERIFIED** (not documented by Microsoft).

**Linux**

13. See §3 — `VIDIOC_QUERYCAP` is the right ioctl, `device_caps` (not `capabilities`) is the field to test on a specific node, and v4l2loopback is identifiable by its `driver`/`bus_info` strings.

---

## 1. macOS — CoreMediaIO virtual camera discovery

### 1.1 The two mechanisms, and which one is current

**DAL plug-ins (legacy).** Bundles in `/Library/CoreMediaIO/Plug-Ins/DAL/`, implementing the `CMIOHardwarePlugInInterface` from `CMIOHardwarePlugIn.h`. On this machine the directory exists but contains only Apple's own label file:

```
/Library/CoreMediaIO/Plug-Ins/DAL/plugins-info.txt  →  "Third party CoreMediaIO DAL Plug-Ins"
```

The interface is deprecated in the SDK. From `$SDK/System/Library/Frameworks/CoreMediaIO.framework/Headers/CMIOHardwarePlugIn.h`:

> ```
> } API_DEPRECATED("Use CMIOExtension instead", macos(10.7, 12.3));          // line 515, CMIOHardwarePlugInInterface
> …                                                                          // lines 543, 572, 594 — same annotation on the plug-in entry points
> ```

**Camera extensions (current).** `CMIOExtensionProvider` / `CMIOExtensionDevice` / `CMIOExtensionStream`, all annotated `API_AVAILABLE(macos(12.3), macCatalyst(15.4))` — see `CMIOExtensionDevice.h:21`, `CMIOExtensionStream.h:70`, `CMIOExtensionProperties.h:19` in the same SDK.

Apple's framework overview states the replacement directly ([Core Media I/O framework page](https://developer.apple.com/documentation/coremediaio)):

> "Starting in macOS 12.3, the framework builds on … to enable you to support custom devices while maintaining system privacy and security protections. The system prevents apps from loading extension code into their process to ensure that they can't bypass macOS privacy protections or mask their identity. **Apple recommends replacing legacy Device Abstraction Layer (DAL) plug-ins with Core Media I/O extensions.**"

### 1.2 Is DAL deprecated, or removed? — the authoritative version boundary

WWDC22 session 10022, *Create camera extensions with Core Media IO* ([developer.apple.com/videos/play/wwdc2022/10022/](https://developer.apple.com/videos/play/wwdc2022/10022/), transcript, Brad Ford, Camera Software Engineering):

> "As of macOS 12.3, DAL plug-ins are already deprecated, so you get a compilation warning when building. That's a good start, but it's not enough. As long as legacy DAL plug-ins are allowed to load, camera apps will still be at risk. To fully address security vulnerabilities and make the system more robust for all users, **we plan to disable DAL plug-ins entirely in the next major release after macOS Ventura.**"

That plan shipped. Apple support article [*If you can't use your camera or video output device after updating to macOS Sonoma 14.1*](https://support.apple.com/en-us/108387):

> "Starting in macOS Sonoma 14.1, cameras and video output devices that don't use modern system extensions won't be available to use unless you restore the legacy settings."
> "Starting in macOS Sonoma 14.1, only cameras and video output devices that use modern system extensions are supported on macOS."
> "If your camera or video output device still uses older software, it won't appear as an option to select and use in apps after you update to macOS Sonoma 14.1."

The documented escape hatch is a Recovery-mode boot argument:

> "`system-override legacy-camera-plugins-without-sw-camera-indication=on`"

**Version boundary to encode in the spec:**

| macOS | DAL plug-in status |
| --- | --- |
| ≤ 12.2 | Only mechanism available |
| 12.3 – 14.0 | Deprecated (compile warning), still loads |
| ≥ 14.1 | **Not loaded** unless the user set `legacy-camera-plugins-without-sw-camera-indication=on` from Recovery |

Since the repo's supported macOS versions are all ≥ 14.1 in practice, **DAL discovery is not worth implementing.** Camera extensions are the mechanism.

### 1.3 Does `AVCaptureDevice.DiscoverySession` list them, and under which device type?

Yes, and the required device type is the *external* one. Two primary sources.

WWDC22 10022 explains the layering — CMIO extensions are re-published to AVFoundation through the same path DAL plug-ins used:

> "Within a camera app process, there are several layers at play… One level up is another private layer that translates CoreMedia IO Extension calls to legacy DAL plug-in calls. Up again, we find the public CoreMedia IO APIs that publish DAL plug-ins. **To the client of this interface, there's no difference between CoreMedia IO Extensions and DAL plug-ins. Everything looks like a DAL plug-in.** And finally, at the top is AVFoundation, which is a client of CoreMedia IO. **It re-publishes DAL plug-ins as AVCaptureDevices.**"

And on identity:

> "Your device's `localizedName` becomes the AVCaptureDevice's `localizedName`. **Your specified `deviceID` becomes the AVCaptureDevice's `uniqueIdentifier`, unless you also provide a `legacyDeviceID`.** You only need to provide this if you're modernizing a DAL plug-in and need to maintain backward compatibility with the uniqueIdentifier you've previously shipped. If you provide a `legacyDeviceID`, AVCaptureDevice will use it as the `uniqueIdentifier`."

Apple's article [*Creating a camera extension with Core Media I/O*](https://developer.apple.com/documentation/coremediaio/creating-a-camera-extension-with-core-media-i-o) gives the exact discovery code:

> "After you've allowed the system to use your custom extension, it's automatically available as a selectable camera in system apps like FaceTime and PhotoBooth. **Camera extensions are also fully compatible with AVFoundation capture APIs, which means you can access your extension as an `AVCaptureDevice` object and use it like any other device.** For example, to retrieve your custom camera extension (as well as any others on the system), retrieve it as an `externalUnknown` device type as shown below."
>
> ```swift
> // Discover devices with a device type of `externalUnknown`.
> let discoverySession = AVCaptureDevice.DiscoverySession(deviceTypes: [.externalUnknown],
>                                                         mediaType: .video,
>                                                         position: .unspecified)
> // Access the external devices.
> let externalDevices = discoverySession.devices
> ```

**Device-type version boundaries**, from `$SDK/System/Library/Frameworks/AVFoundation.framework/Headers/AVCaptureDevice.h`:

| Constant | Availability | Header line |
| --- | --- | --- |
| `AVCaptureDeviceTypeExternalUnknown` | `API_DEPRECATED_WITH_REPLACEMENT("AVCaptureDeviceTypeExternal", macos(10.15, 14.0))` — "A deprecated synonym for AVCaptureDeviceTypeExternal." | 615–618 |
| `AVCaptureDeviceTypeExternal` | `API_AVAILABLE(macos(14.0), …)` | 491–502 |
| `AVCaptureDeviceTypeBuiltInWideAngleCamera` | `API_AVAILABLE(macos(10.15), …)` | 511–514 |
| `AVCaptureDeviceTypeDeskViewCamera` | `API_AVAILABLE(macos(13.0))` | 608–611 |
| `AVCaptureDeviceTypeContinuityCamera` | `API_AVAILABLE(macos(14.0), …)` | 595–605 |

Two behavioural notes from the same header that matter for a device picker:

> `AVCaptureDeviceTypeContinuityCamera` — "Starting in macOS 14.0 and Mac Catalyst 17.0, apps may opt in for using `AVCaptureDeviceTypeContinuityCamera` by adding the following key to their Info.plist: `<key>NSCameraUseContinuityCameraDeviceType</key><true/>` Otherwise, continuity cameras on macOS and Mac Catalyst report that their device type is `AVCaptureDeviceTypeBuiltInWideAngleCamera`." (lines 599–603)

> `AVCaptureDeviceDiscoverySession` — "The list of device types is mandatory. This is used to make sure that clients only get access to devices of types they expect. **This prevents new device types from automatically being included in the list of devices.**" (line ~2867)

That last sentence is the reason the current implementation misses virtual cameras if it only asks for built-in types: `DiscoverySession` is a strict allow-list, never an "everything" query. `+[AVCaptureDevice devices]`, the old catch-all, is `API_DEPRECATED("Use AVCaptureDeviceDiscoverySession instead.", …, macos(10.7, 10.15))`.

**Empirical check on this machine** (`clang -framework AVFoundation -framework CoreMediaIO`, macOS 27.0; source kept out of the repo, in the session scratchpad). A Continuity Camera and its Desk View were present; the installed OBS camera extension was in state `activated waiting for user` so it did not publish a device:

```
== wideAngle only ==
  FaceTime HD Camera        uid=3F45E80A-…  type=AVCaptureDeviceTypeBuiltInWideAngleCamera
== external only ==
  Sirawit's iPhone 15 Pro Camera  uid=A846446E-…-39CC00000001  type=AVCaptureDeviceTypeExternal
== all-ish (wide + external + deskView + continuity) ==
  FaceTime HD Camera  /  iPhone 15 Pro Camera (External)  /  iPhone 15 Pro Desk View Camera (DeskViewCamera)
== CMIO kCMIOHardwarePropertyDevices: 3 devices ==   (same three, same UIDs)
```

Observations worth carrying into the implementation:

- The AVFoundation device list and the raw CoreMediaIO `kCMIOHardwarePropertyDevices` list were **identical** (same three devices, same UIDs). There is no evidence of a CMIO-visible camera that AVFoundation hides — consistent with WWDC22's "AVFoundation … re-publishes DAL plug-ins as AVCaptureDevices". So dropping to the CoreMediaIO C API buys nothing for *enumeration*.
- `AVCaptureDevice.uniqueID` equals the CMIO `kCMIODevicePropertyDeviceUID`. That is the stable identity to key on.
- In a plain command-line tool (no `Info.plist`), the Continuity Camera reported `AVCaptureDeviceTypeExternal`, **not** `AVCaptureDeviceTypeBuiltInWideAngleCamera` as the header's Continuity note would suggest. Do not rely on `deviceType == .external` alone to mean "virtual" — see §1.6.
- **UNVERIFIED:** that an *enabled* OBS camera extension surfaces specifically as `.external`. Apple's sample code says `externalUnknown`/`external` is the type to ask for, and the extension on this machine could not be activated without user consent, so this was not observed first-hand.

### 1.4 Is `kCMIOHardwarePropertyAllowScreenCaptureDevices` required? — No.

This is the folklore item the spec flagged. The only prose Apple publishes is in the SDK header; the online documentation page for the constant exists but carries **no discussion text at all** (`https://developer.apple.com/tutorials/data/documentation/coremediaio/kcmiohardwarepropertyallowscreencapturedevices.json` returns platform metadata — macOS 10.10+, Mac Catalyst 13.0+, **not** marked deprecated — and an empty abstract).

Verbatim, from `$SDK/System/Library/Frameworks/CoreMediaIO.framework/Headers/CMIOHardwareSystem.h`, lines 103–106:

> ```
> @constant       kCMIOHardwarePropertyAllowScreenCaptureDevices
>                     A UInt32 where 1 means that screen capture devices will be presented to the process. A 0 means screen capture devices will be ignored. By default, this property is 1.
> @constant       kCMIOHardwarePropertyAllowWirelessScreenCaptureDevices
>                     A UInt32 where 1 means that wireless screen capture devices will be presented to the process. A 0 means wireless screen capture devices will be ignored. By default, this property is 0.
> ```

Selector values (same file, lines 122–123): `'yes '` and `'wscd'`.

Precise statement of what this does and does not do:

- **What it affects:** *screen capture devices* — nothing broader. The header says nothing about DAL plug-ins in general or virtual cameras. There is no primary source saying it gates DAL plug-in visibility.
- **Still honoured?** Yes on macOS 27.0: `CMIOObjectGetPropertyData`/`SetPropertyData` on `kCMIOObjectSystemObject` both returned `noErr` (0), and the value read back as 1 after setting.
- **Deprecated?** No. No `API_DEPRECATED` annotation on either constant (the only deprecation in `CMIOHardwareSystem.h` is `kCMIOHardwarePropertyProcessIsMaster`, line 111).
- **Discrepancy worth recording:** the header says "By default, this property is 1", but a fresh process on macOS 27.0 reads **0** from `kCMIOHardwarePropertyAllowScreenCaptureDevices` before any set. Whether the DAL treats "never set" as 1 internally while the getter reports the raw per-process value is **UNVERIFIED**. Either way it is irrelevant to camera extensions: setting it to 1 changed nothing about the device list on this machine.

**Conclusion:** do not put this property in the implementation. It is a screen-capture-device toggle, not a virtual-camera opt-in. The spec's caution ("the implementer should treat the mechanism as unconfirmed rather than assuming a particular property is sufficient") was well placed — the property is neither necessary nor sufficient.

### 1.5 What OBS actually ships on macOS

From this machine (`systemextensionsctl list`):

```
--- com.apple.system_extension.cmio
enabled  active  teamID     bundleID (version)                                              name                  [state]
         *       2MMRE5MTB8 com.obsproject.obs-studio.mac-camera-extension (32.1.2/24742…)  OBS Virtual Camera    [activated waiting for user]
```

From the OBS Studio source ([obsproject/obs-studio](https://github.com/obsproject/obs-studio)):

- `plugins/mac-virtualcam/src/` on `master` contains **both** `dal-plugin` and `camera-extension`, and `plugins/mac-virtualcam/CMakeLists.txt` unconditionally adds both:
  ```cmake
  add_subdirectory(src/obs-plugin)
  add_subdirectory(src/dal-plugin)
  add_subdirectory(src/camera-extension)
  ```
- The camera extension arrived in **OBS Studio 30.0**: PR [obsproject/obs-studio#7777](https://github.com/obsproject/obs-studio/pull/7777) *"mac-virtualcam: Add macOS camera extension for macOS 13+"*, merged `2023-05-18T18:41:09Z`, milestone `OBS Studio 30.0`. From the PR body:
  > "Adds native macOS camera extension as a virtual camera for macOS 13+."
  > "Building OBS with a camera extensions requires(!) codesigning and a valid provisioning profile, as well as the entitlement for system extensions"
  > "OBS needs to be run from the `/Applications` directory, macOS will not(!) load extensions from applications in different locations"
  > "The current DAL plugin functionality used for virtual cameras was deprecated in macOS 12.3 and will be removed in …"

So: **OBS ≥ 30.0 on macOS 13+ publishes a CMIOExtension**; earlier OBS (and OBS on macOS 12) used the DAL plug-in, which stops working at macOS 14.1 without the recovery override.

### 1.6 Concrete guidance for the cgo/Objective-C implementation

**Enumeration.** One `AVCaptureDeviceDiscoverySession`, with an explicit device-type allow-list. Nothing else.

```objc
NSMutableArray<AVCaptureDeviceType> *types = [NSMutableArray array];
[types addObject:AVCaptureDeviceTypeBuiltInWideAngleCamera];
[types addObject:AVCaptureDeviceTypeExternal];        // macOS 14+; virtual cameras land here
[types addObject:AVCaptureDeviceTypeDeskViewCamera];  // macOS 13+
[types addObject:AVCaptureDeviceTypeContinuityCamera];// macOS 14+
AVCaptureDeviceDiscoverySession *s =
    [AVCaptureDeviceDiscoverySession discoverySessionWithDeviceTypes:types
                                                           mediaType:AVMediaTypeVideo
                                                            position:AVCaptureDevicePositionUnspecified];
```

If the module must still compile against a pre-14 SDK, add `AVCaptureDeviceTypeExternalUnknown` under an availability guard — it is the same constant value, just renamed (`AVCaptureDevice.h:615`).

**Classifying `hardware` vs `virtual`.** There is **no first-party API that reports "this AVCaptureDevice is a virtual camera"** — searched `AVCaptureDevice.h` in the 27.0 SDK; the only `virtual*` API is `isVirtualDevice`/`constituentDevices`, which means "multi-lens composite camera" and is `API_UNAVAILABLE(macos)` (line 784). Marked **UNVERIFIED / does not exist**. Practical consequences:

- `deviceType == AVCaptureDeviceTypeExternal` is **not** a virtual-camera test — a UVC webcam and a Continuity Camera also report it (observed above).
- `-[AVCaptureDevice isContinuityCamera]` (`API_AVAILABLE(macos(13.0))`, line 2558) lets you subtract Continuity Cameras from the external set.
- Whatever is left in the external set is *either* a USB/UVC camera *or* a camera extension. Distinguishing them from a first-party signal is **UNVERIFIED**. Candidates worth probing at implementation time (none confirmed): `AVCaptureDevice.transportType` (CMIO exposes `kCMIODevicePropertyTransportType`; on this machine the built-in camera reported `'bltn'` and the Continuity devices reported an empty/zero transport), and `CMIOExtensionPropertyDeviceTransportType` which an extension sets on its own device (`CMIOExtensionProperties.h:66`). A pragmatic fallback consistent with the spec's precedence model is to classify by *provider*: on macOS there is only one provider, so classify `external && !continuityCamera` as `virtual` only if a transport-type probe supports it, otherwise report `hardware` and revisit.

**Entitlements / signing / sandbox.** For the *consuming* application — the one this repo builds — there is **no** extra entitlement, no code-signing requirement, and no sandbox exception for enumerating or opening a camera extension: Apple's article states extensions are "fully compatible with AVFoundation capture APIs … use it like any other device". The heavy requirements land on the extension's *publisher*, per Apple's article: the host app needs `com.apple.developer.system-extension.install` plus an App Group, must live in `/Applications`, and the extension must be sandboxed —

> "Only apps that reside in the `/Applications` directory can activate an extension."
> "Before the extension is available to the system, a person with Admin privileges for the Mac must explicitly allow access to it in the Systems & Privacy screen of System Settings."

and from WWDC22 10022: *"Your extension must be app sandboxed"*.

The one thing the consuming app **does** need is the ordinary camera TCC permission (`NSCameraUsageDescription` + user consent) to *open* a device. Enumeration worked in an unsigned command-line tool with no Info.plist in the test above.

---

## 2. Windows — confirming the enumeration split

### 2.1 What Media Foundation's VIDCAP enumeration returns

Documented behaviour of `MFEnumDeviceSources` ([mfidl.h reference](https://learn.microsoft.com/en-us/windows/win32/api/mfidl/nf-mfidl-mfenumdevicesources)):

> "Enumerates a list of audio or video capture devices."
> Attributes set on each returned `IMFActivate`: `MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME` ("The display name of the device"), `MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_HW_SOURCE` ("Whether a device is a hardware or software device. (Video devices only.)"), `MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK` ("The symbolic link for the device driver. (Video devices only.)").

The symbolic link is a **PnP device interface path**, per [MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK](https://learn.microsoft.com/en-us/windows/win32/medfound/mf-devsource-attribute-source-type-vidcap-symbolic-link):

> "Contains the symbolic link for a video capture **driver**."
> "The symbolic link should be considered an opaque string."
> "**The MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK attribute can be passed in as the value of the `DevicePath` argument of the `SetupDiOpenDeviceInterface` function.**"

And [Audio/Video Capture in Media Foundation](https://learn.microsoft.com/en-us/windows/win32/medfound/audio-video-capture-in-media-foundation):

> "**Video capture devices are supported through the UVC class driver and must be compatible with UVC 1.1.**"
> "the `MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK` attribute contains the symbolic link to the device. The symbolic link **uniquely identifies the device on the system**, but is not a readable string."
> "The `MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME` attribute contains the display name of the device. The display name is suitable for showing to the user, but **might not be unique**."

Every device MF returns therefore has a driver and a device-interface path. A DirectShow filter registered only as a COM server in the registry has neither.

**The explicit negative is not documented.** Microsoft nowhere writes "`MFEnumDeviceSources` will not return DirectShow software filters." Mark that sentence **UNVERIFIED as an official statement**; what *is* documented is the structural asymmetry above, plus the OBS registration code in §2.2 showing the OBS camera has no driver and no device interface. The inference is sound but it is an inference.

Note one genuine subtlety that reads as a contradiction but is not: `MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_CATEGORY` takes the value **`CLSID_VideoInputDeviceCategory`** ([reference](https://learn.microsoft.com/en-us/windows/win32/medfound/mf-devsource-attribute-source-type-vidcap-category)). MF reuses the DirectShow category GUID as a *label*; it does not mean MF walks the DirectShow filter registry.

### 2.2 OBS Virtual Camera really is a DirectShow-only filter — verified in source

`plugins/win-dshow/virtualcam-module/virtualcam-module.cpp` on `obsproject/obs-studio@master`:

```cpp
static bool RegFilters(bool reg)                                  // line 168
{
    ComPtr<IFilterMapper2> fm;
    hr = CoCreateInstance(CLSID_FilterMapper2, nullptr, CLSCTX_INPROC_SERVER,
                          IID_IFilterMapper2, (void **)&fm);       // line 173
    …
    hr = fm->RegisterFilter(CLSID_OBS_VirtualVideo, L"OBS Virtual Camera", &moniker,
                            &CLSID_VideoInputDeviceCategory, nullptr, &rf2);   // lines 186-187
    …
    hr = fm->UnregisterFilter(&CLSID_VideoInputDeviceCategory, 0, CLSID_OBS_VirtualVideo); // line 192
}
```

Registration is `DllRegisterServer` (line 203) writing an in-process COM server plus a filter-category entry — `RegServer(CLSID_OBS_VirtualVideo, L"OBS Virtual Camera", file)` at line 162. **No `MFCreateVirtualCamera`, no KS driver, no PnP device.** The friendly name registered is exactly `OBS Virtual Camera`, matching the ffmpeg output in the issue.

### 2.3 The correct DirectShow enumeration path

From [Selecting a Capture Device](https://learn.microsoft.com/en-us/windows/win32/directshow/selecting-a-capture-device) and [Using the System Device Enumerator](https://learn.microsoft.com/en-us/windows/win32/directshow/using-the-system-device-enumerator):

1. `CoCreateInstance(CLSID_SystemDeviceEnum, …, IID_ICreateDevEnum, …)`
2. `ICreateDevEnum::CreateClassEnumerator(CLSID_VideoInputDeviceCategory, &pEnum, 0)` — **must be tested for `S_OK`, not `SUCCEEDED`**:
   > "If the category is empty (or does not exist), the method returns S_FALSE rather than an error code. If so, the returned `IEnumMoniker` pointer is NULL and dereferencing it will cause an exception. Therefore, explicitly test for S_OK."
3. `IEnumMoniker::Next` — again "returns S_FALSE, so again check for S_OK."
4. `IMoniker::BindToStorage(0, 0, IID_IPropertyBag, …)` then `IPropertyBag::Read`.

Documented moniker properties (from *Selecting a Capture Device*):

| Property | Documented meaning | VARIANT type |
| --- | --- | --- |
| `FriendlyName` | "The name of the device." — "**available for every device**" | `VT_BSTR` |
| `Description` | "available only for DV and D-VHS/MPEG camcorder devices" | `VT_BSTR` |
| `DevicePath` | "A unique string that identifies the device. **(Video capture devices only.)**" | `VT_BSTR` |
| `WaveInID` | audio capture devices only | `VT_I4` |

> "The `DevicePath` property is not a human-readable string, but is **guaranteed to be unique for each video capture device on the system**. You can use this property to distinguish between two or more instances of the same model of device."

The category holds two structurally different kinds of entry — this is the crux of the issue:

- **KsProxy wrappers over WDM/KS drivers.** [How Hardware Devices Participate in the Filter Graph](https://learn.microsoft.com/en-us/windows/win32/directshow/how-hardware-devices-participate-in-the-filter-graph): "DirectShow also provides a filter called KsProxy, which can represent any type of Windows Driver Model (WDM) streaming device… An application uses the System Device Enumerator to find WDM device monikers on the system. KsProxy is instantiated by calling `BindToObject` on the moniker… KsProxy does not appear in the filter graph under the name 'KsProxy.' It always takes the friendly name of the device, which is found in the registry."
- **Plain user-mode COM filters** registered into the category via `IFilterMapper2::RegisterFilter` — what OBS does. *Using the System Device Enumerator* frames the enumerator generally as "a uniform way to enumerate, by category, **the filters registered on a user's system**", with PnP devices merely "automatically include[d]".

**Does a software filter have a `DevicePath`?** The documentation restricts `DevicePath` to "video capture devices" but never says whether a registry-registered software filter in the video-input category counts. **UNVERIFIED.** FFmpeg's implementation is evidence that the property is not universally readable — `libavdevice/dshow.c:509`:

```c
/* GetDisplayname works for both video and audio, DevicePath doesn't */
r = IMoniker_GetDisplayName(m, bind_ctx, NULL, &olestr);
```

FFmpeg does not read `DevicePath` at all; it uses `IMoniker::GetDisplayName` as the unique key. Per [Using the System Device Enumerator](https://learn.microsoft.com/en-us/windows/win32/directshow/using-the-system-device-enumerator):

> "The `IMoniker::GetDisplayName` method returns the display name of the moniker. Although the display name is readable, you would not typically display it to an end-user. Get the friendly name from the property bag instead."

The only display-name grammar Microsoft documents is the *default* moniker form: "Use a display name with the form `@device:*:{category-clsid}`". The `@device:pnp:` / `@device:sw:` forms visible in ffmpeg's output are **not documented by Microsoft** — **UNVERIFIED**.

### 2.4 De-duplication: what actually works

Setting the string formats side by side:

| | Media Foundation | DirectShow |
| --- | --- | --- |
| Identity attribute | `MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK` | moniker property `DevicePath`, or `IMoniker::GetDisplayName` |
| Documented as unique | yes ("uniquely identifies the device on the system") | yes ("guaranteed to be unique for each video capture device on the system") |
| Documented as a PnP device-interface path | **yes** — usable as `SetupDiOpenDeviceInterface`'s `DevicePath` argument | not stated |
| Present for a software-only filter | n/a (device does not appear) | **UNVERIFIED** |
| Display-name prefix | n/a | `@device:pnp:` vs `@device:sw:` — **UNVERIFIED grammar** |

**Plainly: Microsoft does not document that the MF symbolic link and the DirectShow `DevicePath` are the same string for the same physical camera.** Both are documented as unique identifiers of a video capture device, and the MF one is documented to be a device-interface path, which makes equality highly likely for KS-backed cameras — but "highly likely" is not a citation. Do not build the de-duplication on an assumed byte-equality without checking it on the target machine first (a five-line probe printing both strings settles it).

**What *is* reliable, using only documented facts:**

1. **Make MF the authority for hardware and DirectShow a strictly-additive source.** Rather than matching identities across the two enumerations, filter the DirectShow list down to the entries MF structurally cannot produce. Every MF-enumerated video device is driver-backed with a device-interface path (§2.1); the OBS-style entry is a registry-only COM filter (§2.2). Concretely: for each DirectShow moniker, attempt to read `DevicePath`; **if the read succeeds, the entry is PnP/driver-backed — drop it, MF already reported it. If it fails, the entry is a software filter — emit it as `virtual`.** This needs no cross-enumeration string comparison at all. Its two failure directions are asymmetric: a false "has DevicePath" hides a virtual camera (visible bug, easy to spot), while a *failed* `DevicePath` read on a genuinely PnP/KS-backed moniker would emit a physical camera as `virtual` next to MF's `hardware` entry — the duplicate the spec forbids. The property table implies every video-capture device moniker carries `DevicePath`, so the second direction should not occur, but it is not ruled out by any documented guarantee. Cheap mitigation: log a warning whenever a DirectShow-only device's `FriendlyName` exactly matches an MF device's `MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME`, which is the signature of that failure.
2. If a cross-check is still wanted, compare `MF symbolic link` to `DevicePath` case-insensitively after trimming, and treat a match as confirmation, not as the primary rule. Windows device-interface paths are conventionally compared case-insensitively; that convention is **UNVERIFIED** as a documented guarantee for these two specific strings.
3. Do **not** de-duplicate on `FriendlyName`. Microsoft says it "might not be unique" ([AV Capture in MF](https://learn.microsoft.com/en-us/windows/win32/medfound/audio-video-capture-in-media-foundation)), and two identical webcams will collide.
4. Do **not** parse the moniker display name for `@device:sw:` as the classification rule. It is undocumented and ffmpeg mangles it. It is fine as a *diagnostic* logged next to the device.

### 2.5 FFmpeg's implementation, as a reference

`libavdevice/dshow.c` on `FFmpeg/FFmpeg@master` (1942 lines as read):

- `dshow_cycle_devices()` (line 463) enumerates `{ &CLSID_VideoInputDeviceCategory, &CLSID_AudioInputDeviceCategory }` (line 477) via `ICreateDevEnum_CreateClassEnumerator` (line 482).
- Unique key = `IMoniker_GetDisplayName` (line 510), then **every `:` is rewritten to `_`** (lines 514–518) "since we use : to delineate between sources". *This is why the issue's ffmpeg output shows `@device_pnp_…` and `@device_sw_…` — those are not the real moniker strings.*
- Human-readable name = `IPropertyBag_Read(bag, L"FriendlyName", …)` (line 525).
- Listing output (line 592): `av_log(avctx, AV_LOG_INFO, "  Alternative name \"%s\"\n", unique_name);`
- Device matching accepts **either** name: `if (strcmp(device_name, friendly_name) && strcmp(device_name, unique_name)) goto fail;` (line 533) — so `-i video=<friendly name>` and `-i video=<alternative name>` both work.
- Media-type probing, `dshow_get_device_media_types()` (line 382): `IBaseFilter_EnumPins` → skip pins where `info.dir != PINDIR_OUTPUT` → require `IKsPropertySet_Get(p, &AMPROPSETID_Pin, AMPROPERTY_PIN_CATEGORY, …)` to equal `PIN_CATEGORY_CAPTURE` → `IPin_EnumMediaTypes` → classify by `type->majortype` against `MEDIATYPE_Video`/`MEDIATYPE_Audio`.

### 2.6 `MFCreateVirtualCamera` — some virtual cameras are already visible

[MFCreateVirtualCamera](https://learn.microsoft.com/en-us/windows/win32/api/mfvirtualcamera/nf-mfvirtualcamera-mfcreatevirtualcamera), "Minimum supported client: **Windows Build 22000**":

> "An optional list of device interface categories under which the virtual camera is registered… **If nullptr is specified, the virtual camera is registered under the `KSCATEGORY_VIDEO_CAMERA`, `KSCATEGORY_VIDEO` and `KSCATEGORY_CAPTURE` categories.**"
> "If `MFVirtualCameraLifetime_Session` is specified, when the returned `IMFVirtualCamera` object is disposed or `IMFVirtualCamera::Shutdown` is called, **the virtual camera will no longer be enumerable or activatable** on the device."
> "If `MFVirtualCameraAccess_CurrentUser` is specified, the virtual camera is only created for the user account that called the `MFCreateVirtualCamera`; if `MFVirtualCameraAccess_AllUsers` is specified, all users on the device will be able to enumerate or activate the virtual camera."
> "The pipeline will automatically append '**Windows Virtual Camera**' to the provided friendly name to ensure end users can distinguish virtual cameras from physical cameras based on the friendly name."

Because registration is into **KS device interface categories**, a camera created this way is a first-class MF capture device and **is** returned by `MFEnumDeviceSources` — i.e. the existing code path already lists such cameras today, and would mislabel them `hardware` under the spec's current rule. Two consequences:

- Classify MF devices with **`MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_HW_SOURCE`** ([reference](https://learn.microsoft.com/en-us/windows/win32/medfound/mf-devsource-attribute-source-type-vidcap-hw-source)): "If the value is TRUE, the capture source is a hardware device. If the value is FALSE, it is a software device. **The default value is FALSE.**" Note the default: a device that does not set the attribute reads as *software*, so read it with an explicit `GetUINT32` failure path and decide what a missing attribute means (recommend: treat failure as `hardware`, since the current MF-only behaviour has always been "everything here is a webcam").
- **UNVERIFIED:** that an `MFCreateVirtualCamera`-created camera actually reports `HW_SOURCE == FALSE`. Microsoft documents the attribute and documents the API, but never connects them. Verify on a Windows 11 machine before relying on it.
- The friendly-name suffix "Windows Virtual Camera" is documented and is a usable secondary signal.

### 2.7 DirectShow capture-graph API surface for a `@device:sw:` device

The interfaces needed, all from the documented DirectShow surface, with ffmpeg's `dshow.c` as the worked reference:

| Step | API | Reference |
| --- | --- | --- |
| Get the source filter from the moniker | `IMoniker::BindToObject(…, IID_IBaseFilter, …)`, or `IFilterGraph2::AddSourceFilterForMoniker` | [Using the System Device Enumerator](https://learn.microsoft.com/en-us/windows/win32/directshow/using-the-system-device-enumerator) — "For device monikers, you can pass the moniker to the `IFilterGraph2::AddSourceFilterForMoniker` method to create a capture filter for the device." `dshow.c:544`, `dshow.c:1389` |
| Build the graph | `CoCreateInstance(CLSID_FilterGraph, …, IID_IGraphBuilder)`; `IGraphBuilder::AddFilter` | `dshow.c:1716`, `dshow.c:1389` |
| Find the capture pin | `IBaseFilter::EnumPins` → `IPin::QueryPinInfo` (require `PINDIR_OUTPUT`) → `IKsPropertySet::Get(AMPROPSETID_Pin, AMPROPERTY_PIN_CATEGORY)` == `PIN_CATEGORY_CAPTURE` | `dshow.c:382-436` |
| Enumerate formats (the capability list) | `IPin::QueryInterface(IID_IAMStreamConfig)` → `IAMStreamConfig::GetNumberOfCapabilities` → `IAMStreamConfig::GetStreamCaps(i, &AM_MEDIA_TYPE, (BYTE*)&VIDEO_STREAM_CONFIG_CAPS)` | [IAMStreamConfig::GetStreamCaps](https://learn.microsoft.com/en-us/windows/win32/api/strmif/nf-strmif-iamstreamconfig-getstreamcaps), [VIDEO_STREAM_CONFIG_CAPS](https://learn.microsoft.com/en-us/windows/win32/api/strmif/ns-strmif-video_stream_config_caps); `dshow.c:823-888` |
| Fallback when `IAMStreamConfig` is absent | `IPin::EnumMediaTypes` → `IEnumMediaTypes::Next` | `dshow.c:771-782` |
| Read resolution / frame rate out of a format | `AM_MEDIA_TYPE.pbFormat` as `VIDEOINFOHEADER` or `VIDEOINFOHEADER2`; `AvgTimePerFrame` (100 ns units) → fps; `bmiHeader.biWidth/biHeight`; `subtype` GUID → pixel format | `dshow.c:693-760`, `dshow.c:900-920` |
| Select a format | `IAMStreamConfig::SetFormat` | `dshow.c:1020`, `1047`, `1055` |
| Deliver frames to your code | a custom `IBaseFilter` + `IPin` + `IMemInputPin` sink implemented by the caller (ffmpeg's `libAVFilter` in `libavdevice/dshow_filter.c` / `dshow_pin.c`, created at `dshow.c:1402`) | `dshow.c:1402`, `1451` |
| Connect and run | `CoCreateInstance(CLSID_CaptureGraphBuilder2, … IID_ICaptureGraphBuilder2)` → `SetFiltergraph` → `RenderStream(NULL, NULL, device_pin, NULL, capture_filter)`; then `IGraphBuilder::QueryInterface(IID_IMediaControl)` → `Run` | `dshow.c:1463-1474`, `dshow.c:1791` |

Two cautions:

- **Do not use the Sample Grabber filter.** Microsoft: "[**Deprecated.** This API may be removed from future releases of Windows.]" ([Sample Grabber Filter](https://learn.microsoft.com/en-us/windows/win32/directshow/sample-grabber-filter)). Implement a private sink filter as ffmpeg does.
- Note that ffmpeg's *format enumeration* per-device relies on `IAMStreamConfig` on the capture pin. A trivially simple software filter may expose a fixed media type on the pin and no `IAMStreamConfig` at all, in which case capability reporting must fall back to `IPin::EnumMediaTypes`. Whether OBS's virtual camera implements `IAMStreamConfig` is **UNVERIFIED** (`plugins/win-dshow/virtualcam-module/virtualcam-filter.cpp` was not read line-by-line for this).
- Every DirectShow doc page now carries the banner: "The feature associated with this page, DirectShow, is a legacy feature… Microsoft strongly recommends that new code use MediaPlayer, IMFMediaEngine and Audio/Video Capture in Media Foundation instead of DirectShow, when possible." This does not block the plan — there is no MF route to a DirectShow-only filter — but it should be recorded as accepted technical debt.

---

## 3. Linux — v4l2 enumeration correctness

### 3.1 `VIDIOC_QUERYCAP` is the right ioctl — with one trap

From [VIDIOC_QUERYCAP](https://docs.kernel.org/userspace-api/media/v4l/vidioc-querycap.html):

> "All V4L2 devices support the `VIDIOC_QUERYCAP` ioctl. It is used to identify kernel devices compatible with this specification and to obtain information about driver and hardware capabilities. The ioctl takes a pointer to a `struct v4l2_capability` which is filled by the driver. When the driver is not compatible with this specification the ioctl returns an `EINVAL` error code."

So QUERYCAP answers both halves of the question: it *is* the compatibility test, and it carries the name.

**`capabilities` vs `device_caps` — this is the trap.** Verbatim from the same page:

> `capabilities`: "Available capabilities of the physical device **as a whole**… The same physical device can export multiple devices in /dev (e.g. /dev/videoX, /dev/vbiY and /dev/radioZ). The `capabilities` field should contain **a union of all capabilities** available around the several V4L2 devices exported to userspace. **For all those devices the `capabilities` field returns the same set of capabilities.**"

> `device_caps`: "Device capabilities of **the opened device**… Should contain the available capabilities of that **specific device node**… This field is only set if the `capabilities` field contains the `V4L2_CAP_DEVICE_CAPS` capability. Only the `capabilities` field can have the `V4L2_CAP_DEVICE_CAPS` capability, `device_caps` will never set `V4L2_CAP_DEVICE_CAPS`."

> `V4L2_CAP_DEVICE_CAPS` — `0x80000000` — "The driver fills the `device_caps` field. This capability can only appear in the `capabilities` field and never in the `device_caps` field."

**Therefore: test `device_caps`, never `capabilities`.** Testing `capabilities & V4L2_CAP_VIDEO_CAPTURE` accepts every node of a camera *including its metadata node* — see §3.2 for the source proof that this is not theoretical.

**Which flags mean "can deliver video frames"** (Device Capabilities Flags table, same page):

| Flag | Value | Documented meaning |
| --- | --- | --- |
| `V4L2_CAP_VIDEO_CAPTURE` | `0x00000001` | "The device supports the single-planar API through the Video Capture interface." |
| `V4L2_CAP_VIDEO_CAPTURE_MPLANE` | `0x00001000` | "…the multi-planar API through the Video Capture interface." |
| `V4L2_CAP_VIDEO_OUTPUT` | `0x00000002` | Video **Output** interface — opposite direction |
| `V4L2_CAP_META_CAPTURE` | `0x00800000` | "The device supports the Metadata Interface capture interface." |
| `V4L2_CAP_STREAMING` | `0x04000000` | "The device supports the streaming I/O method." |
| `V4L2_CAP_READWRITE` | `0x01000000` | "The device supports the `read()` and/or `write()` I/O methods." |

The interface flags say *what* the node is; the I/O flags say *how* buffers move. [Metadata Interface](https://docs.kernel.org/userspace-api/media/v4l/dev-meta.html) §4.14.1 states the general rule: "At least one of the read/write or streaming I/O methods must be supported." So the filter is:

```
(device_caps & (V4L2_CAP_VIDEO_CAPTURE | V4L2_CAP_VIDEO_CAPTURE_MPLANE)) &&
(device_caps & (V4L2_CAP_STREAMING     | V4L2_CAP_READWRITE))
```

**The name fields**, verbatim:

- `driver[16]` — "Name of the driver, a **unique** NUL-terminated ASCII string. For example: 'bttv'."
- `card[32]` — "Name of the device, a NUL-terminated UTF-8 string. For example: 'Yoyodyne TV/FM'… This information is intended for **users**… **this name should be combined with the character device file name (e.g. `/dev/video2`) or the `bus_info` string to avoid ambiguities.**"
- `bus_info[32]` — "Location of the device in the system… This information is intended for users, **to distinguish multiple identical devices**. If no such information is available the field must simply count the devices controlled by the driver ('platform:vivid-000'). The `bus_info` must start with 'PCI:' for PCI boards, 'PCIe:' for PCI Express boards, 'usb-' for USB devices, 'I2C:' for i2c devices, 'ISA:' for ISA devices, 'parport' for parallel port devices and '**platform:**' for platform devices."

Note carefully: **`bus_info` is *not* documented as unique**, and in practice is not unique per node — the uvcvideo video node and metadata node both derive it from `usb_make_path()` on the same USB device (`drivers/media/usb/uvc/uvc_v4l2.c:621` in `uvc_ioctl_querycap`, and `drivers/media/usb/uvc/uvc_metadata.c:35` in `uvc_meta_v4l2_querycap`). `card` is what to display; the node path is what disambiguates.

**Privileges / open flags: UNVERIFIED.** The QUERYCAP page says nothing about `O_RDONLY`, `O_NONBLOCK`, or privilege. Source evidence that the core imposes no extra gate: the dispatch table entry is `IOCTL_INFO(VIDIOC_QUERYCAP, v4l_querycap, v4l_print_querycap, 0)` — flags `0`, no `INFO_FL_PRIO` — at `drivers/media/v4l2-core/v4l2-ioctl.c:2917`. Source-supported, not documented.

### 3.2 Why multiple `/dev/videoN` nodes per camera

[Opening and Closing Devices](https://docs.kernel.org/userspace-api/media/v4l/open.html) §1.1.3:

> "Devices can support several functions… The V4L2 API creates different V4L2 device nodes for each of these functions."
> "The V4L2 API was designed with the idea that one device node could support all functions. However, in practice this never worked… **Today each V4L2 device node supports just one function.**"
> "One problem with all these devices is that **the V4L2 API makes no provisions to find these related V4L2 device nodes.**"

[Metadata Interface](https://docs.kernel.org/userspace-api/media/v4l/dev-meta.html):

> "**The metadata interface is implemented on video device nodes.**"
> "Device nodes supporting the metadata capture interface set the `V4L2_CAP_META_CAPTURE` flag in the **`device_caps`** field."

[V4L2_META_FMT_UVC](https://docs.kernel.org/userspace-api/media/v4l/metafmt-uvc.html):

> "This format describes standard UVC metadata, extracted from UVC packet headers and **provided by the UVC driver through metadata video nodes.**"

**Source proof of the trap.** `drivers/media/usb/uvc/uvc_driver.c:2059-2070` (`uvc_register_video_device`) fixes each node's `device_caps`:

```c
case V4L2_BUF_TYPE_META_CAPTURE:
        vdev->device_caps = V4L2_CAP_META_CAPTURE | V4L2_CAP_STREAMING;
        break;
```

but `uvc_meta_v4l2_querycap()` sets `cap->capabilities = V4L2_CAP_DEVICE_CAPS | V4L2_CAP_STREAMING | chain->caps` (`uvc_metadata.c:36-37`), and `chain->caps` includes `V4L2_CAP_VIDEO_CAPTURE` (`uvc_driver.c:2106-2110`). **A UVC metadata node reports `V4L2_CAP_VIDEO_CAPTURE` in `capabilities` and not in `device_caps`.** That is precisely the node the current `filepath.Glob("/dev/video*")` offers to operators today, and precisely the node a `capabilities`-based filter would keep.

**`/dev/v4l/by-id/` and `by-path/`** are not kernel-documented; they are defined by systemd's udev rules, [`rules.d/60-persistent-v4l.rules`](https://github.com/systemd/systemd/blob/main/rules.d/60-persistent-v4l.rules):

- line 7: `IMPORT{program}="v4l_id $devnode"`
- line 10: `KERNEL=="video*", ENV{ID_SERIAL}=="?*", SYMLINK+="v4l/by-id/$env{ID_BUS}-$env{ID_SERIAL}-video-index$attr{index}"`
- line 17: `KERNEL=="video*|vbi*", ENV{ID_PATH}=="?*", SYMLINK+="v4l/by-path/$env{ID_PATH}-video-index$attr{index}"`

Two consequences readable straight off the rules: the sysfs `index` attribute is what separates nodes of one physical camera, and `by-id` requires `ENV{ID_SERIAL}` from `usb_id` — **a platform device such as a v4l2loopback node gets no `by-id` symlink at all**. That `index0` means "the capture node" is **UNVERIFIED**; the rules file does not say it.

### 3.3 v4l2loopback

Source read: `umlaeute/v4l2loopback` branch `main`, commit `9ef83fb9bc88e8f841786753c362ac52c580defc`, version **0.15.4** (`v4l2loopback.h:13-15`). All of the following is from `vidioc_querycap()`, `v4l2loopback.c:900-932`.

| Field | Exact value | Line |
| --- | --- | --- |
| `driver` | `"v4l2 loopback"` — **with a space**, not `v4l2loopback` | `strscpy(cap->driver, "v4l2 loopback", …)` at `:908` |
| `card` | `dev->card_label`; default `"Dummy video device (0x%04X)"` with the device index | `:909`; default set at `:2861-2867` |
| `bus_info` | `"platform:v4l2loopback-%03d"`, e.g. `platform:v4l2loopback-000` | `snprintf(cap->bus_info, …, "platform:v4l2loopback-%03d", device_nr)` at `:910-911` |

`card` is **fully user-controlled** — `static char *card_label[MAX_DEVICES]; module_param_array(card_label, charp, NULL, 0000);` at `:217-219`, also settable at runtime through the control device at `:3116-3117`. It is a display string, never an identity or trust signal.

**Capability flags** (`:906`, `:913-923`, `:925-929`):

- always `V4L2_CAP_STREAMING | V4L2_CAP_READWRITE`, plus `V4L2_CAP_DEVICE_CAPS` in `capabilities` only;
- never any `_MPLANE`, `M2M`, or `META_` flag;
- **by default one node reports BOTH `V4L2_CAP_VIDEO_CAPTURE` and `V4L2_CAP_VIDEO_OUTPUT`** — `announce_all_caps` is true when `exclusive_caps=0`, which is the module default (`V4L2LOOPBACK_DEFAULT_EXCLUSIVECAPS 0` at `:165-166`, wired at `:2796-2798`, `:2898`).

With `exclusive_caps=1` the node advertises only one direction, chosen **per opener at QUERYCAP time**:

```c
if (opener->io_method == V4L2L_IO_TIMEOUT ||
    (has_output_token(dev->stream_tokens) && !dev->keep_format)) {
        capabilities |= V4L2_CAP_VIDEO_OUTPUT;
} else
        capabilities |= V4L2_CAP_VIDEO_CAPTURE;
```
(`v4l2loopback.c:916-922`)

**OBS on Linux uses v4l2loopback, with `exclusive_caps=1`.** From `plugins/linux-v4l2/v4l2-output.c` on `obsproject/obs-studio@master`:

- `loopback_module_loaded()` (lines 67-86) greps `/proc/modules` for `"v4l2loopback"`;
- `loopback_module_load()` (lines 103-107):
  ```c
  return run_command(
      "pkexec modprobe v4l2loopback exclusive_caps=1 card_label='OBS Virtual Camera' && sleep 0.5");
  ```

So an OBS virtual camera on Linux is: `driver == "v4l2 loopback"`, `bus_info == "platform:v4l2loopback-NNN"`, `card == "OBS Virtual Camera"`. **And because OBS sets `exclusive_caps=1`, that node will report `V4L2_CAP_VIDEO_OUTPUT` instead of `V4L2_CAP_VIDEO_CAPTURE` to an enumerator whenever a writer holds the output token** — i.e. a strict capture-only filter can make the OBS camera disappear from the list exactly when OBS is producing frames. This is the single most dangerous Linux finding for this spec.

### 3.4 Is there a generic "this is a virtual device" signal? — No.

There is **no capability flag for virtual/emulated/loopback** anywhere in the Device Capabilities Flags table.

The only documented generic signal is the `bus_info` prefix convention quoted in §3.1, whose kernel-canonical implementation is `media_set_bus_info()` in [`include/media/media-device.h`](https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/plain/include/media/media-device.h) lines 504-525:

```c
if (!dev)
        strscpy(bus_info, "no bus info", bus_info_size);
else if (dev_is_platform(dev))
        snprintf(bus_info, bus_info_size, "platform:%s", dev_name(dev));
else if (dev_is_pci(dev))
        snprintf(bus_info, bus_info_size, "PCI:%s", dev_name(dev));
```

Note the QUERYCAP page's own example of the `platform:` fallback is `"platform:vivid-000"` — `vivid` being the kernel's *virtual* video test driver.

**UNVERIFIED, and explicitly so:** no primary source states that `platform:` implies "virtual". Many genuinely physical embedded camera pipelines (SoC ISPs, CSI-2 receivers) are platform devices. The inverse reads more safely — a `usb-` / `PCI:` / `PCIe:` / `I2C:` prefix is positive evidence of real hardware, since a loopback driver has no such parent to derive one from. Treat "`platform:` ⇒ virtual" as a heuristic, and identify v4l2loopback specifically by its exact `driver` string.

---

## Impact on the spec

Ordered by how much they change issue #1.

### Simplifies — macOS is far easier than the spec assumed

The spec says:

> "macOS enumeration is extended to discover CoreMediaIO DAL plug-in devices, which is how virtual cameras register on that platform. AVFoundation's default device discovery does not include them; discovery requires opting in through the CoreMediaIO layer. The precise opt-in mechanism and its behaviour across macOS versions is the one genuinely uncertain area of this spec…"

Three corrections, all from primary sources:

1. **DAL plug-ins are not how virtual cameras register on current macOS.** They are deprecated since 12.3 (`CMIOHardwarePlugIn.h:515`) and **not loaded at all from macOS 14.1** unless the user set a Recovery-mode override ([Apple HT 108387](https://support.apple.com/en-us/108387)). Targeting DAL would build support for a mechanism the OS refuses to run. The mechanism is **CMIOExtension** (macOS 12.3+).
2. **"discovery requires opting in through the CoreMediaIO layer" is false.** Apple's own documentation retrieves camera extensions through `AVCaptureDevice.DiscoverySession(deviceTypes: [.externalUnknown], …)` with no CoreMediaIO call whatsoever. Locally, the AVFoundation device list and the raw `kCMIOHardwarePropertyDevices` list were identical.
3. **`kCMIOHardwarePropertyAllowScreenCaptureDevices` is a red herring** — it governs screen-capture devices, defaults to 1 per the header, and is not deprecated. It is neither necessary nor sufficient for virtual cameras.

**Net effect:** the "one genuinely uncertain area of this spec" collapses to *adding `AVCaptureDeviceTypeExternal` to the discovery-session type list* — a few lines in `usb_darwin.m`. The contingency plan in Further Notes ("If it proves harder than expected, macOS virtual-camera support can be split out") is not needed; macOS is now the **cheapest** of the three platforms, and should be sequenced first, not last.

### Complicates — macOS cannot reliably report `kind`

There is no first-party API that says "this `AVCaptureDevice` is a virtual camera". `AVCaptureDeviceTypeExternal` covers UVC webcams, Continuity Cameras (observed) **and** camera extensions. `-[AVCaptureDevice isContinuityCamera]` subtracts one of those three; nothing documented separates the other two. This puts **user story 3** ("each device labelled with its kind") at risk on macOS specifically. Options, in preference order: (a) probe `transportType` / `CMIOExtensionPropertyDeviceTransportType` and validate against a real installed extension before committing; (b) report `hardware` for all macOS externals in v1 and treat macOS `kind` as a follow-up. Decide this explicitly rather than discovering it during implementation.

### Contradicts — "Media Foundation ⇒ hardware" is wrong on Windows 11

The spec states:

> "Devices found only through DirectShow are classified as `virtual`; devices found through Media Foundation are `hardware`."

`MFCreateVirtualCamera` (Windows build 22000+) registers virtual cameras under `KSCATEGORY_VIDEO_CAMERA` / `KSCATEGORY_VIDEO` / `KSCATEGORY_CAPTURE`, so such cameras **are** returned by `MFEnumDeviceSources` and would be labelled `hardware`. Fix: classify MF devices by **`MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_HW_SOURCE`** (`TRUE` = hardware, `FALSE` = software, default `FALSE`). Read it with an explicit failure branch — a missing attribute defaults to *software*, which would flip every legacy webcam to `virtual` if handled naively; recommend treating a failed read as `hardware`. The documented friendly-name suffix "Windows Virtual Camera" is a corroborating signal. Note this also means **some virtual cameras are already visible to today's code** — worth saying so in the issue, since it changes the "invisible to the platform" framing for Windows 11 users of MF-based virtual cameras.

### Complicates — the de-duplication key is not what the spec implies

The spec says:

> "Matching across providers is done on device identity attributes rather than on display name alone, since display names are not unique."

Correct instinct, but **there is no documented shared identity attribute.** MF's symbolic link and DirectShow's `DevicePath` are each documented as uniquely identifying a video capture device, and the MF one is documented as a PnP device-interface path — but Microsoft never states they are the same string, and equality is therefore **UNVERIFIED**. Recommendation (§2.4): make the rule *structural* rather than *comparative* — enumerate DirectShow, attempt to read the `DevicePath` property on each moniker, and emit only the entries where that read **fails** (registry-only software filters, which MF structurally cannot produce). No cross-enumeration string matching, and it fails safe: a mistake hides a virtual camera rather than duplicating a webcam. If a string comparison is kept, keep it as a confirmation path and verify byte-equality on a real machine first — a five-line probe settles it.

This does not weaken the spec's testing plan: "de-duplication and precedence" remains the highest-value unit test; only the matching key changes, and a structural key is *easier* to fake in tests than a pair of opaque platform strings.

### Confirms — the Windows premise is exactly right

OBS Virtual Camera registers via `IFilterMapper2::RegisterFilter(CLSID_OBS_VirtualVideo, L"OBS Virtual Camera", &moniker, &CLSID_VideoInputDeviceCategory, …)` (`plugins/win-dshow/virtualcam-module/virtualcam-module.cpp:186-187`). It is a registry-registered COM filter with no driver and no PnP device. The spec's diagnosis — "Media Foundation's enumeration covers kernel-streaming camera devices and structurally cannot see them" — is right, with the caveat in §2.1 that Microsoft never writes that negative down; it is an inference from the documented positives.

The spec's Further Notes also say "DirectShow's `@device_sw_` identifier prefix in the reference ffmpeg output is the direct marker of a software-registered device". True in practice, but that string is `IMoniker::GetDisplayName` with `:` rewritten to `_` by ffmpeg (`libavdevice/dshow.c:509-518`), and Microsoft documents no such display-name grammar. Fine as a log line; do not make it the classification rule.

### Complicates — Linux `V4L2_CAP_VIDEO_CAPTURE` on the wrong field breaks user story 16

The spec says the Linux provider will query "each candidate node… for its capabilities so that non-capture nodes (metadata and output nodes…) are excluded". The implementation detail that decides whether this works: **`device_caps`, not `capabilities`.** A UVC metadata node reports `V4L2_CAP_VIDEO_CAPTURE` in `capabilities` (the physical-device union) and only `V4L2_CAP_META_CAPTURE | V4L2_CAP_STREAMING` in `device_caps` (`uvc_driver.c:2059-2070`, `uvc_metadata.c:36-37`). Reading the wrong field silently reproduces today's bug. Also guard on `capabilities & V4L2_CAP_DEVICE_CAPS` before trusting `device_caps`, per the documented contract.

### Complicates — the Linux OBS camera can vanish while it is running

OBS loads v4l2loopback with `exclusive_caps=1` (`plugins/linux-v4l2/v4l2-output.c:103-107`). Under that mode the node reports `V4L2_CAP_VIDEO_OUTPUT` instead of `V4L2_CAP_VIDEO_CAPTURE` to an opener while a writer holds the output token (`v4l2loopback.c:916-922`). A strict "capture only" filter therefore risks dropping the OBS virtual camera from the list **precisely when OBS is streaming into it** — the inverse of user stories 4, 5 and 6. Mitigation: on Linux, when `driver == "v4l2 loopback"`, accept the node if `device_caps` shows *either* capture or output, and classify it `virtual`. Note this interacts with the polling device websocket: a device that flips capability every few seconds would produce spurious added/removed deltas.

### Complicates — Linux `kind` classification has no generic mechanism

The spec says "Loopback devices — the mechanism OBS uses to publish a virtual camera on Linux — are classified as `virtual`". There is no capability flag for virtual, and `platform:` in `bus_info` does not imply virtual (SoC camera pipelines are platform devices too). The reliable rule is driver-specific: `driver == "v4l2 loopback"` (exact string, with the space) and/or `bus_info` prefix `platform:v4l2loopback-`. Do not classify by `card`, which is a user-supplied module parameter (`card_label`, `v4l2loopback.c:217-219`).

### Complicates — device identity stability (user story 17)

- **Linux:** the spec keeps "Device paths remain the identifier for compatibility", but `/dev/videoN` numbering is not stable across unplug/replug — the kernel docs state outright that "the V4L2 API makes no provisions to find these related V4L2 device nodes". The stable identifier is the udev `by-id` symlink, which **does not exist for platform devices** (no `ID_SERIAL`), so loopback devices need a different key (e.g. `bus_info`). Worth an explicit decision in the issue: keeping the path as the identifier means user story 17 is *not* satisfied on Linux, and that should be stated rather than implied.
- **macOS:** `AVCaptureDevice.uniqueID` is the right key and matches CMIO's device UID. Per WWDC22 10022, an extension's `deviceID` becomes it, unless the extension supplies a `legacyDeviceID` — so a vendor migrating from a DAL plug-in can deliberately keep an old identifier. Identifiers can therefore *change* when a vendor ships a migration; not a blocker, but the "identity survives" story has a caveat.

### Notes on the DirectShow capture work

- **Do not use the Sample Grabber filter** — Microsoft marks it "[Deprecated. This API may be removed from future releases of Windows.]". Implement a private `IBaseFilter`/`IPin`/`IMemInputPin` sink, as ffmpeg does. This is real work and reinforces the spec's decision to sequence the capture graph last.
- Capability derivation should try `IAMStreamConfig::GetNumberOfCapabilities` / `GetStreamCaps` first and fall back to `IPin::EnumMediaTypes`; a minimal software filter may expose a fixed media type and no `IAMStreamConfig`. Whether OBS's filter implements `IAMStreamConfig` is **UNVERIFIED**.
- Every DirectShow documentation page now carries a "legacy feature… Microsoft strongly recommends that new code use… Media Foundation" banner. There is no MF path to a DirectShow-only filter, so the plan stands — but record it as accepted technical debt.

### Verified afterwards on a real Windows host

A purpose-built probe (`win-device-probe.cpp`, cross-compiled with mingw-w64) was run on Windows with a Logitech C270 attached and OBS Virtual Camera started. It settled four of the items below by direct observation:

- **Item 6 — RESOLVED, and it validates the de-duplication design.** OBS Virtual Camera's `DevicePath` moniker property read **fails**; the physical C270's succeeds. The structural rule ("emit only DirectShow monikers whose `DevicePath` read fails") separates the two enumerations exactly as intended.
- **Item 5 — RESOLVED, with a correction.** Exact string comparison fails, but *only* because of the trailing interface-class GUID — DirectShow reports the `KSCATEGORY_CAPTURE` interface and Media Foundation the `KSCATEGORY_VIDEO_CAMERA` one. The device-instance prefix (`\\?\usb#vid_046d&pid_0825&mi_00#7&15c19c49&0&0000`) is **identical** across both. So a usable cross-enumeration key does exist, contrary to this document's earlier "not a usable key" phrasing. The structural rule is still preferred — it needs no string comparison at all — but a comparative fallback is available.
- **Item 9 — RESOLVED.** OBS's filter implements `IAMStreamConfig` and, **while the virtual camera is running**, reports 3 capabilities with `capsSize` equal to `sizeof(VIDEO_STREAM_CONFIG_CAPS)` (128). `IPin::EnumMediaTypes` independently reports 3. Both paths work, so capability derivation is safe. Note the dependency on run state: with the virtual camera **stopped**, the same filter reports **0** capabilities from both paths, which would make `usbauto` refuse the device.
- **Item 8 — PARTIALLY RESOLVED.** `MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_HW_SOURCE` is present on the physical camera with value **4**, not 1 — so it must be read as "non-zero means hardware", never compared against 1. Whether an `MFCreateVirtualCamera` camera reports 0 remains untested; no such camera was installed.

End-to-end confirmation: `webrtp --list-usb-devices` on the same host listed the C270 as `hardware` (by MF symbolic link) and OBS Virtual Camera as `virtual` (by DirectShow moniker), each exactly once.

### Verified afterwards on macOS

macOS 27.0 with OBS 32.2.1. Once the camera extension is enabled — `systemextensionsctl list` reporting `[activated enabled]` rather than `[activated waiting for user]` — `OBS Virtual Camera` is returned by the AVFoundation discovery session, identified by its CoreMediaIO device UID, with no CoreMediaIO call anywhere in the path. This resolves item 1 below and confirms the correction above: CMIOExtension is the mechanism, and asking for the external device type is sufficient.

**Operational caveat, discovered by observation.** A macOS process does **not** pick up a camera extension that is enabled after the process starts. Two enumerations on the same machine seconds apart disagreed: a long-running process returned two devices, a freshly started one returned three including the virtual camera. AVFoundation appears to bind to the set of registered extensions at the time the process first connects, and no amount of re-enumeration within that process recovers.

This is materially different from Windows, where a virtual camera starting and stopping is picked up by ordinary polling. On macOS, *registration* of the extension is bound at process start; only the camera's running state is dynamic afterwards. Any application offering a device picker must therefore be restarted after the user enables a virtual camera for the first time, and should say so rather than appearing broken.

### Everything labelled UNVERIFIED in this document

1. ~~That an activated OBS/CMIO camera extension surfaces specifically as `AVCaptureDeviceTypeExternal` (Apple's sample says to ask for it; not observed first-hand — the extension on the test machine was awaiting user consent).~~ **RESOLVED — observed first-hand on macOS 27.0 with OBS 32.2.1. Once the camera extension is enabled (`systemextensionsctl list` reports `[activated enabled]`), `OBS Virtual Camera` is returned by the AVFoundation discovery session with the external device type and no CoreMediaIO call, carrying the CMIO device UID as its identifier. It reports `hardware`, per the accepted macOS gap in item 2.**
2. Any first-party API that identifies an `AVCaptureDevice` as a virtual camera — none found in the macOS 27.0 SDK.
3. Why `kCMIOHardwarePropertyAllowScreenCaptureDevices` reads 0 in a fresh process when the header says it defaults to 1.
4. An explicit Microsoft statement that `MFEnumDeviceSources` does not return DirectShow-only software filters (inferred structurally).
5. ~~Whether the MF `..._VIDCAP_SYMBOLIC_LINK` string and the DirectShow moniker `DevicePath` string are byte-equal (or case-insensitively equal) for the same physical camera.~~ **RESOLVED — not equal; they differ only in the interface-class GUID, and the device-instance prefix matches. See "Verified afterwards" above.**
6. ~~Whether a DirectShow software filter exposes a `DevicePath` moniker property at all.~~ **RESOLVED — it does not; the read fails for OBS Virtual Camera. See "Verified afterwards" above.**
7. The `@device:pnp:` / `@device:sw:` moniker display-name grammar — Microsoft documents only the `@device:*:{category-clsid}` default-moniker form.
8. Whether an `MFCreateVirtualCamera`-created camera reports `MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_HW_SOURCE == FALSE`. **PARTIAL — a physical camera reports `4`, so the attribute must be read as non-zero-means-hardware. The virtual-camera half is still untested.**
9. ~~Whether OBS's Windows DirectShow filter implements `IAMStreamConfig`.~~ **RESOLVED — it does, reporting 3 capabilities with the expected `capsSize`, but only while the virtual camera is running. See "Verified afterwards" above.**
10. Whether `VIDIOC_QUERYCAP` requires any privilege or a particular open mode — source-supported (`v4l2-ioctl.c:2917`, flags `0`) but not documented.
11. Whether `bus_info` is unique in any documented sense (it is not, in practice, per node).
12. That udev's `-video-index0` denotes the primary capture node.
13. That a `platform:` `bus_info` prefix implies a virtual device.
