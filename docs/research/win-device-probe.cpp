// Probe: answers the four UNVERIFIED Windows items behind go-webrtp issue #1.
//
// Build (MSYS2 / MinGW g++):
//   g++ -std=c++17 -o win-device-probe.exe win-device-probe.cpp \
//       -lole32 -loleaut32 -lstrmiids -lmfplat -lmf -lmfuuid -luuid
//
// Build (MSVC "x64 Native Tools Command Prompt"):
//   cl /EHsc /std:c++17 win-device-probe.cpp ole32.lib oleaut32.lib strmiids.lib ^
//      mfplat.lib mf.lib mfuuid.lib
//
// Run with a physical webcam attached AND OBS Virtual Camera started.

#include <windows.h>
#include <dshow.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mfreadwrite.h>
#include <cstdio>
#include <string>
#include <vector>

#pragma comment(lib, "strmiids.lib")

struct DshowEntry {
    std::wstring friendlyName;
    std::wstring displayName;
    bool         devicePathOk = false;
    std::wstring devicePath;
    bool         hasStreamConfig = false;
    int          streamConfigCount = 0;
    int          streamConfigSize = 0;
    int          enumMediaCount = 0;
};

struct MfEntry {
    std::wstring friendlyName;
    std::wstring symbolicLink;
    bool         hwSourcePresent = false;
    UINT32       hwSourceValue = 0;
};

static void Line() { printf("--------------------------------------------------------------\n"); }

static std::wstring VariantToString(VARIANT &var) {
    if (var.vt == VT_BSTR && var.bstrVal != nullptr) return std::wstring(var.bstrVal);
    return std::wstring();
}

// * enumerate CLSID_VideoInputDeviceCategory and record whether DevicePath is readable
static std::vector<DshowEntry> EnumerateDshow() {
    std::vector<DshowEntry> out;

    ICreateDevEnum *devEnum = nullptr;
    if (FAILED(CoCreateInstance(CLSID_SystemDeviceEnum, nullptr, CLSCTX_INPROC_SERVER,
                                IID_PPV_ARGS(&devEnum)))) {
        printf("  !! CoCreateInstance(CLSID_SystemDeviceEnum) failed\n");
        return out;
    }

    IEnumMoniker *enumMoniker = nullptr;
    HRESULT hr = devEnum->CreateClassEnumerator(CLSID_VideoInputDeviceCategory, &enumMoniker, 0);
    if (hr != S_OK || enumMoniker == nullptr) {
        printf("  !! CreateClassEnumerator returned 0x%08lX (S_FALSE = no devices)\n", hr);
        devEnum->Release();
        return out;
    }

    IMoniker *moniker = nullptr;
    while (enumMoniker->Next(1, &moniker, nullptr) == S_OK) {
        DshowEntry entry;

        LPOLESTR display = nullptr;
        if (SUCCEEDED(moniker->GetDisplayName(nullptr, nullptr, &display)) && display) {
            entry.displayName = display;
            CoTaskMemFree(display);
        }

        IPropertyBag *bag = nullptr;
        if (SUCCEEDED(moniker->BindToStorage(nullptr, nullptr, IID_PPV_ARGS(&bag)))) {
            VARIANT var;
            VariantInit(&var);
            if (SUCCEEDED(bag->Read(L"FriendlyName", &var, nullptr))) {
                entry.friendlyName = VariantToString(var);
            }
            VariantClear(&var);

            // * the load-bearing read: does a software filter expose DevicePath at all?
            VariantInit(&var);
            HRESULT pathHr = bag->Read(L"DevicePath", &var, nullptr);
            if (SUCCEEDED(pathHr)) {
                entry.devicePathOk = true;
                entry.devicePath = VariantToString(var);
            }
            VariantClear(&var);
            bag->Release();
        }

        // * does this filter implement IAMStreamConfig, or only fixed media types?
        IBaseFilter *filter = nullptr;
        if (SUCCEEDED(moniker->BindToObject(nullptr, nullptr, IID_PPV_ARGS(&filter))) && filter) {
            IEnumPins *pins = nullptr;
            if (SUCCEEDED(filter->EnumPins(&pins))) {
                IPin *pin = nullptr;
                while (pins->Next(1, &pin, nullptr) == S_OK) {
                    PIN_DIRECTION dir;
                    if (SUCCEEDED(pin->QueryDirection(&dir)) && dir == PINDIR_OUTPUT) {
                        IAMStreamConfig *cfg = nullptr;
                        if (SUCCEEDED(pin->QueryInterface(IID_PPV_ARGS(&cfg))) && cfg) {
                            entry.hasStreamConfig = true;
                            int count = 0, size = 0;
                            if (SUCCEEDED(cfg->GetNumberOfCapabilities(&count, &size))) {
                                entry.streamConfigCount = count;
                                entry.streamConfigSize = size;
                            }
                            cfg->Release();
                        }
                        // * always run the fallback too -- this is the path the implementation uses
                        IEnumMediaTypes *types = nullptr;
                        if (SUCCEEDED(pin->EnumMediaTypes(&types)) && types) {
                            AM_MEDIA_TYPE *mt = nullptr;
                            while (types->Next(1, &mt, nullptr) == S_OK) {
                                entry.enumMediaCount++;
                                if (mt->pbFormat) CoTaskMemFree(mt->pbFormat);
                                if (mt->pUnk) mt->pUnk->Release();
                                CoTaskMemFree(mt);
                            }
                            types->Release();
                        }
                        pin->Release();
                        break;
                    }
                    pin->Release();
                }
                pins->Release();
            }
            filter->Release();
        }

        out.push_back(entry);
        moniker->Release();
    }

    enumMoniker->Release();
    devEnum->Release();
    return out;
}

// * enumerate MF VIDCAP sources and read the hardware-source attribute
static std::vector<MfEntry> EnumerateMf() {
    std::vector<MfEntry> out;

    IMFAttributes *attrs = nullptr;
    if (FAILED(MFCreateAttributes(&attrs, 1))) return out;
    attrs->SetGUID(MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE,
                   MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_GUID);

    IMFActivate **devices = nullptr;
    UINT32 count = 0;
    if (FAILED(MFEnumDeviceSources(attrs, &devices, &count))) {
        attrs->Release();
        return out;
    }

    for (UINT32 i = 0; i < count; i++) {
        MfEntry entry;
        WCHAR *buf = nullptr;
        UINT32 len = 0;

        if (SUCCEEDED(devices[i]->GetAllocatedString(
                MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME, &buf, &len))) {
            entry.friendlyName = buf;
            CoTaskMemFree(buf);
        }
        if (SUCCEEDED(devices[i]->GetAllocatedString(
                MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK, &buf, &len))) {
            entry.symbolicLink = buf;
            CoTaskMemFree(buf);
        }

        // * is the attribute present at all, and what does it say?
        UINT32 hw = 0;
        if (SUCCEEDED(devices[i]->GetUINT32(
                MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_HW_SOURCE, &hw))) {
            entry.hwSourcePresent = true;
            entry.hwSourceValue = hw;
        }

        out.push_back(entry);
        devices[i]->Release();
    }

    CoTaskMemFree(devices);
    attrs->Release();
    return out;
}

static bool EqualIgnoreCase(const std::wstring &a, const std::wstring &b) {
    if (a.size() != b.size()) return false;
    return _wcsicmp(a.c_str(), b.c_str()) == 0;
}

// * drop the trailing "#{interface-class-guid}\global" so the device instance can be compared
static std::wstring StripInterfaceGuid(const std::wstring &s) {
    size_t at = s.rfind(L"#{");
    return at == std::wstring::npos ? s : s.substr(0, at);
}

int main() {
    // * banner before any COM call, so a failure here can never be silent
    setvbuf(stdout, nullptr, _IONBF, 0);
    printf("win-device-probe: starting\n");
    fflush(stdout);

    HRESULT coHr = CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
    printf("  CoInitializeEx      : 0x%08lX %s\n", (unsigned long)coHr,
           SUCCEEDED(coHr) ? "OK" : "FAILED");
    fflush(stdout);
    if (FAILED(coHr)) {
        printf("  !! cannot continue without COM\n");
        return 1;
    }

    HRESULT mfHr = MFStartup(MF_VERSION);
    printf("  MFStartup           : 0x%08lX %s\n", (unsigned long)mfHr,
           SUCCEEDED(mfHr) ? "OK" : "FAILED (MF section will be empty)");
    fflush(stdout);

    printf("\n=== [1] DIRECTSHOW (CLSID_VideoInputDeviceCategory) ===\n");
    fflush(stdout);
    auto dshow = EnumerateDshow();
    printf("  (%zu DirectShow device(s) enumerated)\n", dshow.size());
    fflush(stdout);
    for (auto &d : dshow) {
        Line();
        printf("  FriendlyName : %ls\n", d.friendlyName.c_str());
        printf("  DisplayName  : %ls\n", d.displayName.c_str());
        printf("  DevicePath   : %s", d.devicePathOk ? "READ OK  <-- " : "READ FAILED  <-- ");
        printf("%s\n", d.devicePathOk ? "treated as HARDWARE by the spec's rule"
                                      : "treated as VIRTUAL by the spec's rule");
        if (d.devicePathOk) printf("                 %ls\n", d.devicePath.c_str());
        bool sizeOk = d.streamConfigSize == (int)sizeof(VIDEO_STREAM_CONFIG_CAPS);
        printf("  IAMStreamConfig: %s   GetStreamCaps=%d  capsSize=%d (expects %d) %s\n",
               d.hasStreamConfig ? "YES" : "NO", d.streamConfigCount, d.streamConfigSize,
               (int)sizeof(VIDEO_STREAM_CONFIG_CAPS),
               d.streamConfigCount > 0 ? (sizeOk ? "SIZE OK" : "SIZE MISMATCH -> impl falls back") : "");
        printf("                   EnumMediaTypes=%d  -> %s\n", d.enumMediaCount,
               ((d.streamConfigCount > 0 && sizeOk) || d.enumMediaCount > 0)
                   ? "USABLE (modes derivable by the implementation)"
                   : "NO MODES -- usbauto would refuse this device");
    }

    printf("\n=== [2] MEDIA FOUNDATION (VIDCAP) ===\n");
    fflush(stdout);
    auto mf = EnumerateMf();
    printf("  (%zu Media Foundation device(s) enumerated)\n", mf.size());
    fflush(stdout);
    for (auto &m : mf) {
        Line();
        printf("  FriendlyName : %ls\n", m.friendlyName.c_str());
        printf("  SymbolicLink : %ls\n", m.symbolicLink.c_str());
        if (m.hwSourcePresent) {
            printf("  HW_SOURCE    : PRESENT = %u  (%s)\n", m.hwSourceValue,
                   m.hwSourceValue ? "hardware" : "SOFTWARE -> classified virtual");
        } else {
            printf("  HW_SOURCE    : ABSENT  <-- spec says treat absent as HARDWARE\n");
        }
    }

    printf("\n=== [3] VERDICTS ===\n");
    Line();

    int softwareOnly = 0;
    for (auto &d : dshow) if (!d.devicePathOk) softwareOnly++;
    printf("  Q6  Does a software filter expose DevicePath?\n");
    printf("      %d of %zu DirectShow entries had NO readable DevicePath.\n",
           softwareOnly, dshow.size());
    printf("      -> The structural de-dup rule is %s.\n",
           (softwareOnly > 0 && softwareOnly < (int)dshow.size())
               ? "PLAUSIBLE (it separated the list)"
               : "SUSPECT (it did NOT separate the list -- redesign needed)");

    Line();
    printf("  Q5  Is the MF SymbolicLink equal to the DirectShow DevicePath?\n");
    int exact = 0, stripped = 0;
    for (auto &m : mf) {
        for (auto &d : dshow) {
            if (!d.devicePathOk) continue;
            if (EqualIgnoreCase(m.symbolicLink, d.devicePath)) {
                exact++;
                printf("      EXACT match: %ls\n", m.friendlyName.c_str());
            } else if (EqualIgnoreCase(StripInterfaceGuid(m.symbolicLink),
                                       StripInterfaceGuid(d.devicePath))) {
                stripped++;
                printf("      MATCH AFTER STRIPPING interface GUID: %ls\n", m.friendlyName.c_str());
                printf("        shared instance: %ls\n",
                       StripInterfaceGuid(m.symbolicLink).c_str());
            }
        }
    }
    if (exact == 0 && stripped == 0)
        printf("      No matches even after stripping -- no usable string key.\n");
    else if (exact == 0)
        printf("      -> Exact comparison FAILS; device-instance prefix IS a usable key.\n");

    Line();
    printf("  Q8  Do MF-registered virtual cameras report HW_SOURCE == FALSE?\n");
    bool any = false;
    for (auto &m : mf) {
        if (m.hwSourcePresent && m.hwSourceValue == 0) {
            printf("      SOFTWARE via MF: %ls\n", m.friendlyName.c_str());
            any = true;
        }
    }
    if (!any) printf("      None found (no MFCreateVirtualCamera camera installed, or all report TRUE).\n");

    Line();
    printf("  Q9  Does the OBS filter implement IAMStreamConfig?\n");
    for (auto &d : dshow) {
        if (d.devicePathOk) continue;
        printf("      %ls -> IAMStreamConfig=%s GetStreamCaps=%d EnumMediaTypes=%d\n",
               d.friendlyName.c_str(), d.hasStreamConfig ? "YES" : "NO",
               d.streamConfigCount, d.enumMediaCount);
        printf("      -> %s\n", (d.streamConfigCount > 0 || d.enumMediaCount > 0)
                                    ? "capabilities ARE derivable; usbauto can use it"
                                    : "NO modes from either path -- usbauto will refuse it");
    }
    printf("\n");

    printf("win-device-probe: done (exit 0)\n");
    fflush(stdout);

    MFShutdown();
    CoUninitialize();
    return 0;
}
