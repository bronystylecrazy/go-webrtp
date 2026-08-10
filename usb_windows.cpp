#include <mfapi.h>
#include <mfidl.h>
#include <mfreadwrite.h>
#include <mftransform.h>
#include <mferror.h>
#include <propvarutil.h>
#include <wmcodecdsp.h>
#include <codecapi.h>
#include <windows.h>
#include <dshow.h>
#include <dvdmedia.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <stdio.h>

#include <algorithm>
#include <string>
#include <vector>

extern "C" void WebrtpUsbWinPacket(uintptr_t handle, void *data, int length, uint32_t pts90k);
extern "C" void WebrtpUsbWinError(uintptr_t handle, char *msg);

namespace {

struct WinCapture {
    HANDLE thread;
    HANDLE stopEvent;
    HANDLE readyEvent;
    uintptr_t handle;
    std::wstring device;
    std::wstring codec;
    std::wstring h264Profile;
    int width;
    int height;
    double fps;
    int bitrateKbps;
    bool useDshow;
    bool started;
    std::string error;
};

bool DshowCaptureRun(WinCapture *capture);

struct MediaTypeSelection {
    IMFMediaType *type;
    UINT32 width;
    UINT32 height;
};

struct RawOutputSelection {
    GUID inputSubtype;
    GUID outputSubtype;
    UINT32 width;
    UINT32 height;
    UINT32 fpsNum;
    UINT32 fpsDen;
};

struct EncodedPacket {
    std::vector<uint8_t> annexb;
    LONGLONG sampleTime;
};

struct H264EncoderContext {
    IMFTransform *transform;
    IMFMediaType *inputType;
    IMFMediaType *outputType;
    ICodecAPI *codecApi;
    UINT32 width;
    UINT32 height;
    UINT32 fpsNum;
    UINT32 fpsDen;
    UINT32 bitrate;
    GUID inputSubtype;
    std::vector<std::vector<uint8_t>> codecConfig;
    uint32_t nalLengthSize;
    bool streamingBegun;
    LONGLONG nextForcedKeyframeTime;
    UINT32 profile;
};

UINT32 H264ProfileValue(const std::wstring &profile) {
    if (_wcsicmp(profile.c_str(), L"baseline") == 0) {
        return 66;
    }
    if (_wcsicmp(profile.c_str(), L"high") == 0) {
        return 100;
    }
    return 77;
}

std::wstring Utf8ToWide(const char *src) {
    if (src == nullptr || src[0] == '\0') {
        return std::wstring();
    }
    int len = MultiByteToWideChar(CP_UTF8, 0, src, -1, nullptr, 0);
    if (len <= 0) {
        return std::wstring();
    }
    std::vector<wchar_t> buf(static_cast<size_t>(len), L'\0');
    MultiByteToWideChar(CP_UTF8, 0, src, -1, buf.data(), len);
    return std::wstring(buf.data());
}

char *WideToUtf8Dup(const std::wstring &src) {
    if (src.empty()) {
        char *empty = static_cast<char *>(malloc(1));
        if (empty != nullptr) {
            empty[0] = '\0';
        }
        return empty;
    }
    int len = WideCharToMultiByte(CP_UTF8, 0, src.c_str(), -1, nullptr, 0, nullptr, nullptr);
    if (len <= 0) {
        return nullptr;
    }
    char *dst = static_cast<char *>(malloc(static_cast<size_t>(len)));
    if (dst == nullptr) {
        return nullptr;
    }
    WideCharToMultiByte(CP_UTF8, 0, src.c_str(), -1, dst, len, nullptr, nullptr);
    return dst;
}

std::string WideToUtf8String(const std::wstring &src) {
    char *tmp = WideToUtf8Dup(src);
    if (tmp == nullptr) {
        return std::string();
    }
    std::string out(tmp);
    free(tmp);
    return out;
}

char *StringDup(const std::string &src) {
    char *dst = static_cast<char *>(malloc(src.size() + 1));
    if (dst == nullptr) {
        return nullptr;
    }
    memcpy(dst, src.c_str(), src.size() + 1);
    return dst;
}

template <typename T>
void SafeRelease(T **ptr) {
    if (ptr != nullptr && *ptr != nullptr) {
        (*ptr)->Release();
        *ptr = nullptr;
    }
}

bool GuidEqual(const GUID &a, const GUID &b) {
    return memcmp(&a, &b, sizeof(GUID)) == 0;
}

bool IsCompressedSubtype(const GUID &subtype) {
    return GuidEqual(subtype, MFVideoFormat_H264) || GuidEqual(subtype, MFVideoFormat_HEVC);
}

bool IsRawOrConvertibleSubtype(const GUID &subtype) {
    return GuidEqual(subtype, MFVideoFormat_NV12) ||
           GuidEqual(subtype, MFVideoFormat_YUY2) ||
           GuidEqual(subtype, MFVideoFormat_MJPG) ||
           GuidEqual(subtype, MFVideoFormat_RGB32);
}

std::wstring CaptureErrorMessage(HRESULT hr, const char *msg) {
    std::wstring text = Utf8ToWide(msg);
    wchar_t *sys = nullptr;
    DWORD flags = FORMAT_MESSAGE_ALLOCATE_BUFFER | FORMAT_MESSAGE_FROM_SYSTEM | FORMAT_MESSAGE_IGNORE_INSERTS;
    if (FormatMessageW(flags, nullptr, static_cast<DWORD>(hr), 0, reinterpret_cast<LPWSTR>(&sys), 0, nullptr) > 0 && sys != nullptr) {
        text += L": ";
        text += sys;
        LocalFree(sys);
    }
    return text;
}

HRESULT MfStartupScoped() {
    HRESULT hr = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    if (FAILED(hr)) {
        return hr;
    }
    hr = MFStartup(MF_VERSION, MFSTARTUP_LITE);
    if (FAILED(hr)) {
        CoUninitialize();
        return hr;
    }
    return S_OK;
}

void MfShutdownScoped() {
    MFShutdown();
    CoUninitialize();
}

HRESULT EnumerateDevices(IMFActivate ***devicesOut, UINT32 *countOut) {
    IMFAttributes *attrs = nullptr;
    HRESULT hr = MFCreateAttributes(&attrs, 1);
    if (FAILED(hr)) {
        return hr;
    }
    hr = attrs->SetGUID(MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE, MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_GUID);
    if (FAILED(hr)) {
        attrs->Release();
        return hr;
    }
    hr = MFEnumDeviceSources(attrs, devicesOut, countOut);
    attrs->Release();
    return hr;
}

HRESULT DeviceString(IMFActivate *device, const GUID &key, std::wstring *valueOut) {
    WCHAR *buf = nullptr;
    UINT32 len = 0;
    HRESULT hr = device->GetAllocatedString(key, &buf, &len);
    if (FAILED(hr)) {
        return hr;
    }
    valueOut->assign(buf, len);
    CoTaskMemFree(buf);
    return S_OK;
}

HRESULT FindDevice(const std::wstring &needle, IMFActivate **deviceOut) {
    IMFActivate **devices = nullptr;
    UINT32 count = 0;
    HRESULT hr = EnumerateDevices(&devices, &count);
    if (FAILED(hr)) {
        return hr;
    }

    IMFActivate *found = nullptr;
    for (UINT32 idx = 0; idx < count; idx++) {
        IMFActivate *device = devices[idx];
        std::wstring name;
        std::wstring id;
        DeviceString(device, MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME, &name);
        DeviceString(device, MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK, &id);
        if (needle.empty() || _wcsicmp(needle.c_str(), L"default") == 0) {
            found = device;
            found->AddRef();
            break;
        }
        if (_wcsicmp(name.c_str(), needle.c_str()) == 0 || _wcsicmp(id.c_str(), needle.c_str()) == 0) {
            found = device;
            found->AddRef();
            break;
        }
    }

    for (UINT32 idx = 0; idx < count; idx++) {
        SafeRelease(&devices[idx]);
    }
    CoTaskMemFree(devices);

    if (found == nullptr) {
        return HRESULT_FROM_WIN32(ERROR_NOT_FOUND);
    }
    *deviceOut = found;
    return S_OK;
}

HRESULT DeviceListString(std::string *resultOut) {
    IMFActivate **devices = nullptr;
    UINT32 count = 0;
    HRESULT hr = EnumerateDevices(&devices, &count);
    if (FAILED(hr)) {
        return hr;
    }

    std::string result;
    for (UINT32 idx = 0; idx < count; idx++) {
        std::wstring name;
        std::wstring id;
        DeviceString(devices[idx], MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME, &name);
        DeviceString(devices[idx], MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK, &id);
        char *idUtf8 = WideToUtf8Dup(id);
        char *nameUtf8 = WideToUtf8Dup(name);
        if (idUtf8 != nullptr && nameUtf8 != nullptr) {
            if (!result.empty()) {
                result.push_back('\n');
            }
            result.append(idUtf8);
            result.push_back('\t');
            result.append(nameUtf8);
            result.push_back('\t');
            UINT32 hwSource = 0;
            HRESULT hwHr = devices[idx]->GetUINT32(MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_HW_SOURCE, &hwSource);
            if (SUCCEEDED(hwHr)) {
                result.append(hwSource != 0 ? "1" : "0");
            }
        }
        free(idUtf8);
        free(nameUtf8);
        SafeRelease(&devices[idx]);
    }
    CoTaskMemFree(devices);
    *resultOut = result;
    return S_OK;
}

std::string JsonEscape(const std::string &src) {
    std::string out;
    out.reserve(src.size() + 8);
    for (char ch : src) {
        switch (ch) {
        case '\\': out += "\\\\"; break;
        case '"': out += "\\\""; break;
        case '\n': out += "\\n"; break;
        case '\r': out += "\\r"; break;
        case '\t': out += "\\t"; break;
        default:
            out += ch;
            break;
        }
    }
    return out;
}

struct CapabilityModeEntry {
    UINT32 width;
    UINT32 height;
    std::vector<double> fps;
    std::vector<std::string> pixelFormats;
};

void AppendUniqueFps(std::vector<double> *values, double fps) {
    if (values == nullptr || fps <= 0) {
        return;
    }
    for (double existing : *values) {
        if (fabs(existing - fps) < 0.01) {
            return;
        }
    }
    values->push_back(fps);
}

void SortFps(std::vector<double> *values) {
    if (values == nullptr) {
        return;
    }
    std::sort(values->begin(), values->end());
}

void AppendUniqueFormat(std::vector<std::string> *values, const std::string &format) {
    if (values == nullptr || format.empty()) {
        return;
    }
    for (const auto &existing : *values) {
        if (_stricmp(existing.c_str(), format.c_str()) == 0) {
            return;
        }
    }
    values->push_back(format);
}

std::string CapabilityFormatLabel(const GUID &subtype) {
    if (GuidEqual(subtype, MFVideoFormat_H264)) {
        return "h264";
    }
    if (GuidEqual(subtype, MFVideoFormat_HEVC)) {
        return "h265";
    }
    if (GuidEqual(subtype, MFVideoFormat_MJPG)) {
        return "mjpeg";
    }
    if (GuidEqual(subtype, MFVideoFormat_NV12)) {
        return "nv12";
    }
    if (GuidEqual(subtype, MFVideoFormat_YUY2)) {
        return "yuyv422";
    }
    if (GuidEqual(subtype, MFVideoFormat_RGB32)) {
        return "rgb32";
    }
    return "";
}

void SortFormats(std::vector<std::string> *values) {
    if (values == nullptr) {
        return;
    }
    std::sort(values->begin(), values->end(), [](const std::string &a, const std::string &b) {
        return _stricmp(a.c_str(), b.c_str()) < 0;
    });
}

void MergeMode(std::vector<CapabilityModeEntry> *modes, UINT32 width, UINT32 height, double fps, const std::string &format) {
    if (modes == nullptr || width == 0 || height == 0) {
        return;
    }
    for (auto &mode : *modes) {
        if (mode.width == width && mode.height == height) {
            AppendUniqueFps(&mode.fps, fps);
            AppendUniqueFormat(&mode.pixelFormats, format);
            return;
        }
    }
    CapabilityModeEntry mode = {};
    mode.width = width;
    mode.height = height;
    AppendUniqueFps(&mode.fps, fps);
    AppendUniqueFormat(&mode.pixelFormats, format);
    modes->push_back(mode);
}

CapabilityModeEntry *FindMode(std::vector<CapabilityModeEntry> *modes, UINT32 width, UINT32 height) {
    if (modes == nullptr) {
        return nullptr;
    }
    for (auto &mode : *modes) {
        if (mode.width == width && mode.height == height) {
            return &mode;
        }
    }
    return nullptr;
}

void SortModeEntries(std::vector<CapabilityModeEntry> *modes) {
    if (modes == nullptr) return;
    for (auto &mode : *modes) {
        SortFps(&mode.fps);
        SortFormats(&mode.pixelFormats);
    }
    std::sort(modes->begin(), modes->end(), [](const CapabilityModeEntry &a, const CapabilityModeEntry &b) {
        UINT64 areaA = static_cast<UINT64>(a.width) * static_cast<UINT64>(a.height);
        UINT64 areaB = static_cast<UINT64>(b.width) * static_cast<UINT64>(b.height);
        if (areaA == areaB) {
            if (a.width == b.width) return a.height < b.height;
            return a.width < b.width;
        }
        return areaA < areaB;
    });
}

std::string CapabilitiesJsonBuild(const std::wstring &id, const std::wstring &name, const std::vector<std::string> &codecs, const std::string &bitrateControl, const std::vector<CapabilityModeEntry> &modes) {
    std::string json = "{";
    json += "\"device\":{\"id\":\"" + JsonEscape(WideToUtf8String(id)) + "\",\"name\":\"" + JsonEscape(WideToUtf8String(name)) + "\"},";
    json += "\"codecs\":[";
    for (size_t i = 0; i < codecs.size(); i++) {
        if (i > 0) json += ",";
        json += "\"" + codecs[i] + "\"";
    }
    json += "],";
    json += "\"bitrateControl\":\"" + bitrateControl + "\",";
    json += "\"modes\":[";
    for (size_t i = 0; i < modes.size(); i++) {
        if (i > 0) json += ",";
        const auto &mode = modes[i];
        json += "{\"width\":" + std::to_string(mode.width) + ",\"height\":" + std::to_string(mode.height);
        if (!mode.fps.empty()) {
            json += ",\"fps\":[";
            for (size_t f = 0; f < mode.fps.size(); f++) {
                if (f > 0) json += ",";
                char buf[32];
                snprintf(buf, sizeof(buf), "%.2f", mode.fps[f]);
                std::string fpsString(buf);
                while (!fpsString.empty() && fpsString.back() == '0') fpsString.pop_back();
                if (!fpsString.empty() && fpsString.back() == '.') fpsString.pop_back();
                json += fpsString;
            }
            json += "]";
        }
        if (!mode.pixelFormats.empty()) {
            json += ",\"pixelFormats\":[";
            for (size_t p = 0; p < mode.pixelFormats.size(); p++) {
                if (p > 0) json += ",";
                json += "\"" + JsonEscape(mode.pixelFormats[p]) + "\"";
            }
            json += "]";
        }
        json += "}";
    }
    json += "]}";
    return json;
}

HRESULT DeviceCapabilitiesJson(IMFActivate *device, std::string *resultOut) {
    if (device == nullptr || resultOut == nullptr) {
        return E_POINTER;
    }
    IMFMediaSource *source = nullptr;
    IMFSourceReader *reader = nullptr;
    std::vector<CapabilityModeEntry> h264Modes;
    std::vector<CapabilityModeEntry> h265Modes;
    std::vector<CapabilityModeEntry> rawModes;
    std::wstring name;
    std::wstring id;
    DeviceString(device, MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME, &name);
    DeviceString(device, MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK, &id);

    HRESULT hr = device->ActivateObject(IID_PPV_ARGS(&source));
    if (FAILED(hr)) {
        return hr;
    }
    hr = MFCreateSourceReaderFromMediaSource(source, nullptr, &reader);
    if (FAILED(hr)) {
        SafeRelease(&source);
        return hr;
    }

    for (DWORD idx = 0;; idx++) {
        IMFMediaType *mediaType = nullptr;
        hr = reader->GetNativeMediaType(MF_SOURCE_READER_FIRST_VIDEO_STREAM, idx, &mediaType);
        if (hr == MF_E_NO_MORE_TYPES) {
            hr = S_OK;
            break;
        }
        if (FAILED(hr)) {
            break;
        }
        GUID subtype = GUID_NULL;
        UINT32 width = 0;
        UINT32 height = 0;
        UINT32 frNum = 0;
        UINT32 frDen = 0;
        mediaType->GetGUID(MF_MT_SUBTYPE, &subtype);
        MFGetAttributeSize(mediaType, MF_MT_FRAME_SIZE, &width, &height);
        MFGetAttributeRatio(mediaType, MF_MT_FRAME_RATE, &frNum, &frDen);
        double fps = (frDen != 0) ? static_cast<double>(frNum) / static_cast<double>(frDen) : 0.0;
        std::string format = CapabilityFormatLabel(subtype);
        if (GuidEqual(subtype, MFVideoFormat_H264)) {
            MergeMode(&h264Modes, width, height, fps, format);
        } else if (GuidEqual(subtype, MFVideoFormat_HEVC)) {
            MergeMode(&h265Modes, width, height, fps, format);
        } else if (IsRawOrConvertibleSubtype(subtype)) {
            MergeMode(&rawModes, width, height, fps, format);
        }
        SafeRelease(&mediaType);
    }
    SafeRelease(&reader);
    SafeRelease(&source);
    if (FAILED(hr)) {
        return hr;
    }

    SortModeEntries(&h264Modes);
    SortModeEntries(&h265Modes);

    std::vector<std::string> codecs;
    if (!h264Modes.empty()) codecs.push_back("h264");
    if (!h265Modes.empty()) codecs.push_back("h265");

    std::vector<CapabilityModeEntry> mergedModes = h264Modes;
    for (const auto &mode : h265Modes) {
        CapabilityModeEntry *target = FindMode(&mergedModes, mode.width, mode.height);
        if (target == nullptr) {
            mergedModes.push_back(mode);
            continue;
        }
        for (double fps : mode.fps) {
            AppendUniqueFps(&target->fps, fps);
        }
        SortFps(&target->fps);
        for (const auto &format : mode.pixelFormats) {
            AppendUniqueFormat(&target->pixelFormats, format);
        }
        SortFormats(&target->pixelFormats);
    }
    for (const auto &mode : rawModes) {
        CapabilityModeEntry *target = FindMode(&mergedModes, mode.width, mode.height);
        if (target == nullptr) {
            mergedModes.push_back(mode);
            continue;
        }
        for (double fps : mode.fps) {
            AppendUniqueFps(&target->fps, fps);
        }
        SortFps(&target->fps);
        for (const auto &format : mode.pixelFormats) {
            AppendUniqueFormat(&target->pixelFormats, format);
        }
        SortFormats(&target->pixelFormats);
    }
    SortModeEntries(&mergedModes);

    *resultOut = CapabilitiesJsonBuild(id, name, codecs, "native", mergedModes);
    return S_OK;
}

HRESULT SelectCompressedMediaType(IMFSourceReader *reader, const GUID &subtype, int widthHint, int heightHint, double fpsHint, MediaTypeSelection *selectionOut) {
    IMFMediaType *best = nullptr;
    UINT32 bestWidth = 0;
    UINT32 bestHeight = 0;
    double bestScore = 0.0;
    bool bestScoreSet = false;

    for (DWORD idx = 0;; idx++) {
        IMFMediaType *mediaType = nullptr;
        HRESULT hr = reader->GetNativeMediaType(MF_SOURCE_READER_FIRST_VIDEO_STREAM, idx, &mediaType);
        if (hr == MF_E_NO_MORE_TYPES) {
            break;
        }
        if (FAILED(hr)) {
            return hr;
        }

        GUID currentSubtype = GUID_NULL;
        if (FAILED(mediaType->GetGUID(MF_MT_SUBTYPE, &currentSubtype)) || !GuidEqual(currentSubtype, subtype)) {
            SafeRelease(&mediaType);
            continue;
        }

        UINT32 width = 0;
        UINT32 height = 0;
        MFGetAttributeSize(mediaType, MF_MT_FRAME_SIZE, &width, &height);
        UINT32 frNum = 0;
        UINT32 frDen = 0;
        MFGetAttributeRatio(mediaType, MF_MT_FRAME_RATE, &frNum, &frDen);
        double fps = (frDen != 0) ? static_cast<double>(frNum) / static_cast<double>(frDen) : 0.0;
        double score = 0.0;
        if (widthHint > 0) {
            score += fabs(static_cast<double>(width) - widthHint) * 1000.0;
        }
        if (heightHint > 0) {
            score += fabs(static_cast<double>(height) - heightHint) * 1000.0;
        }
        if (fpsHint > 0) {
            if (fps > 0) {
                score += fabs(fps - fpsHint) * 100.0;
            } else {
                score += 1000000.0;
            }
        }
        if (widthHint <= 0 && heightHint <= 0) {
            score -= static_cast<double>(width) * static_cast<double>(height) / 1000000.0;
        }
        if (fpsHint <= 0 && fps > 0) {
            score -= fps / 1000.0;
        }

        bool better = !bestScoreSet || score < bestScore;

        if (better) {
            SafeRelease(&best);
            best = mediaType;
            bestWidth = width;
            bestHeight = height;
            bestScore = score;
            bestScoreSet = true;
        } else {
            SafeRelease(&mediaType);
        }
    }

    if (best == nullptr) {
        return MF_E_TOPO_CODEC_NOT_FOUND;
    }
    selectionOut->type = best;
    selectionOut->width = bestWidth;
    selectionOut->height = bestHeight;
    return S_OK;
}

HRESULT SelectRawMediaType(IMFSourceReader *reader, int widthHint, int heightHint, double fpsHint, RawOutputSelection *selectionOut) {
    if (reader == nullptr || selectionOut == nullptr) {
        return E_POINTER;
    }
    IMFMediaType *best = nullptr;
    GUID bestSubtype = GUID_NULL;
    UINT32 bestWidth = 0;
    UINT32 bestHeight = 0;
    UINT32 bestFrNum = 0;
    UINT32 bestFrDen = 1;
    double bestScore = 0.0;
    bool bestScoreSet = false;

    for (DWORD idx = 0;; idx++) {
        IMFMediaType *mediaType = nullptr;
        HRESULT hr = reader->GetNativeMediaType(MF_SOURCE_READER_FIRST_VIDEO_STREAM, idx, &mediaType);
        if (hr == MF_E_NO_MORE_TYPES) {
            break;
        }
        if (FAILED(hr)) {
            return hr;
        }

        GUID subtype = GUID_NULL;
        if (FAILED(mediaType->GetGUID(MF_MT_SUBTYPE, &subtype)) || !IsRawOrConvertibleSubtype(subtype)) {
            SafeRelease(&mediaType);
            continue;
        }

        UINT32 width = 0;
        UINT32 height = 0;
        UINT32 frNum = 0;
        UINT32 frDen = 0;
        MFGetAttributeSize(mediaType, MF_MT_FRAME_SIZE, &width, &height);
        MFGetAttributeRatio(mediaType, MF_MT_FRAME_RATE, &frNum, &frDen);
        double fps = (frDen != 0) ? static_cast<double>(frNum) / static_cast<double>(frDen) : 0.0;
        double score = 0.0;
        if (widthHint > 0) score += fabs(static_cast<double>(width) - widthHint) * 1000.0;
        if (heightHint > 0) score += fabs(static_cast<double>(height) - heightHint) * 1000.0;
        if (fpsHint > 0) {
            if (fps > 0) score += fabs(fps - fpsHint) * 100.0;
            else score += 1000000.0;
        }
        if (GuidEqual(subtype, MFVideoFormat_NV12)) score -= 20.0;
        else if (GuidEqual(subtype, MFVideoFormat_YUY2)) score -= 10.0;
        else if (GuidEqual(subtype, MFVideoFormat_MJPG)) score += 5.0;
        else if (GuidEqual(subtype, MFVideoFormat_RGB32)) score += 20.0;

        bool better = !bestScoreSet || score < bestScore;
        if (better) {
            SafeRelease(&best);
            best = mediaType;
            bestSubtype = subtype;
            bestWidth = width;
            bestHeight = height;
            bestFrNum = frNum;
            bestFrDen = frDen != 0 ? frDen : 1;
            bestScore = score;
            bestScoreSet = true;
        } else {
            SafeRelease(&mediaType);
        }
    }

    if (best == nullptr) {
        return MF_E_TOPO_CODEC_NOT_FOUND;
    }
    SafeRelease(&best);
    selectionOut->inputSubtype = bestSubtype;
    selectionOut->outputSubtype = GuidEqual(bestSubtype, MFVideoFormat_NV12) ? MFVideoFormat_NV12 : MFVideoFormat_YUY2;
    selectionOut->width = bestWidth;
    selectionOut->height = bestHeight;
    selectionOut->fpsNum = bestFrNum != 0 ? bestFrNum : 30;
    selectionOut->fpsDen = bestFrDen != 0 ? bestFrDen : 1;
    return S_OK;
}

HRESULT SetReaderRawOutputType(IMFSourceReader *reader, const RawOutputSelection &selection) {
    IMFMediaType *requested = nullptr;
    HRESULT hr = MFCreateMediaType(&requested);
    if (FAILED(hr)) {
        return hr;
    }
    hr = requested->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    if (SUCCEEDED(hr)) hr = requested->SetGUID(MF_MT_SUBTYPE, selection.outputSubtype);
    if (SUCCEEDED(hr)) hr = MFSetAttributeSize(requested, MF_MT_FRAME_SIZE, selection.width, selection.height);
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(requested, MF_MT_FRAME_RATE, selection.fpsNum, selection.fpsDen);
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(requested, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
    if (SUCCEEDED(hr)) hr = requested->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
    if (SUCCEEDED(hr)) hr = reader->SetCurrentMediaType(MF_SOURCE_READER_FIRST_VIDEO_STREAM, nullptr, requested);
    SafeRelease(&requested);
    return hr;
}

std::vector<std::vector<uint8_t>> ParseAvcc(const uint8_t *data, size_t size, uint32_t *nalLengthSizeOut) {
    std::vector<std::vector<uint8_t>> units;
    if (size < 7 || data[0] != 1) {
        return units;
    }
    *nalLengthSizeOut = (data[4] & 0x03) + 1;
    size_t offset = 5;
    uint8_t spsCount = data[offset] & 0x1f;
    offset++;
    for (uint8_t idx = 0; idx < spsCount && offset + 2 <= size; idx++) {
        uint16_t len = (static_cast<uint16_t>(data[offset]) << 8) | data[offset + 1];
        offset += 2;
        if (offset + len > size) {
            return units;
        }
        units.emplace_back(data + offset, data + offset + len);
        offset += len;
    }
    if (offset >= size) {
        return units;
    }
    uint8_t ppsCount = data[offset];
    offset++;
    for (uint8_t idx = 0; idx < ppsCount && offset + 2 <= size; idx++) {
        uint16_t len = (static_cast<uint16_t>(data[offset]) << 8) | data[offset + 1];
        offset += 2;
        if (offset + len > size) {
            return units;
        }
        units.emplace_back(data + offset, data + offset + len);
        offset += len;
    }
    return units;
}

std::vector<std::vector<uint8_t>> ParseHvcc(const uint8_t *data, size_t size, uint32_t *nalLengthSizeOut) {
    std::vector<std::vector<uint8_t>> units;
    if (size < 23 || data[0] != 1) {
        return units;
    }
    *nalLengthSizeOut = (data[21] & 0x03) + 1;
    size_t offset = 22;
    uint8_t numArrays = data[offset++];
    for (uint8_t arr = 0; arr < numArrays && offset + 3 <= size; arr++) {
        offset++;
        uint16_t numNalus = (static_cast<uint16_t>(data[offset]) << 8) | data[offset + 1];
        offset += 2;
        for (uint16_t n = 0; n < numNalus && offset + 2 <= size; n++) {
            uint16_t len = (static_cast<uint16_t>(data[offset]) << 8) | data[offset + 1];
            offset += 2;
            if (offset + len > size) {
                return units;
            }
            units.emplace_back(data + offset, data + offset + len);
            offset += len;
        }
    }
    return units;
}

HRESULT LoadCodecConfig(IMFMediaType *type, const GUID &subtype, std::vector<std::vector<uint8_t>> *unitsOut, uint32_t *nalLengthSizeOut) {
    UINT8 *blob = nullptr;
    UINT32 blobSize = 0;
    HRESULT hr = type->GetAllocatedBlob(MF_MT_MPEG_SEQUENCE_HEADER, &blob, &blobSize);
    if (FAILED(hr) || blob == nullptr || blobSize == 0) {
        return hr;
    }
    if (GuidEqual(subtype, MFVideoFormat_H264)) {
        *unitsOut = ParseAvcc(blob, blobSize, nalLengthSizeOut);
    } else {
        *unitsOut = ParseHvcc(blob, blobSize, nalLengthSizeOut);
    }
    CoTaskMemFree(blob);
    return S_OK;
}

std::vector<uint8_t> ToAnnexB(const uint8_t *data, size_t size, uint32_t nalLengthSize, const std::vector<std::vector<uint8_t>> &codecConfig, bool prependCodecConfig) {
    static const uint8_t startCode[] = {0x00, 0x00, 0x00, 0x01};
    std::vector<uint8_t> out;
    if (prependCodecConfig) {
        for (const auto &unit : codecConfig) {
            out.insert(out.end(), startCode, startCode + sizeof(startCode));
            out.insert(out.end(), unit.begin(), unit.end());
        }
    }

    if (size >= 4 && data[0] == 0x00 && data[1] == 0x00 && ((data[2] == 0x01) || (data[2] == 0x00 && data[3] == 0x01))) {
        out.insert(out.end(), data, data + size);
        return out;
    }

    if (nalLengthSize == 0 || nalLengthSize > 4) {
        return out;
    }

    size_t offset = 0;
    while (offset + nalLengthSize <= size) {
        uint32_t naluSize = 0;
        for (uint32_t idx = 0; idx < nalLengthSize; idx++) {
            naluSize = (naluSize << 8) | data[offset + idx];
        }
        offset += nalLengthSize;
        if (naluSize == 0 || offset + naluSize > size) {
            break;
        }
        out.insert(out.end(), startCode, startCode + sizeof(startCode));
        out.insert(out.end(), data + offset, data + offset + naluSize);
        offset += naluSize;
    }
    return out;
}

UINT32 DefaultH264Bitrate(UINT32 width, UINT32 height, UINT32 fpsNum, UINT32 fpsDen) {
    double fps = (fpsDen != 0) ? static_cast<double>(fpsNum) / static_cast<double>(fpsDen) : 30.0;
    double bits = static_cast<double>(width) * static_cast<double>(height) * std::max(1.0, fps) * 0.12;
    if (bits < 500000.0) bits = 500000.0;
    if (bits > 12000000.0) bits = 12000000.0;
    return static_cast<UINT32>(bits);
}

HRESULT CreateH264Encoder(UINT32 width, UINT32 height, UINT32 fpsNum, UINT32 fpsDen, UINT32 bitrate, UINT32 profile, GUID inputSubtype, H264EncoderContext *ctx) {
    if (ctx == nullptr) {
        return E_POINTER;
    }
    *ctx = H264EncoderContext{};
    ctx->width = width;
    ctx->height = height;
    ctx->fpsNum = fpsNum != 0 ? fpsNum : 30;
    ctx->fpsDen = fpsDen != 0 ? fpsDen : 1;
    ctx->bitrate = bitrate != 0 ? bitrate : DefaultH264Bitrate(width, height, ctx->fpsNum, ctx->fpsDen);
    ctx->inputSubtype = inputSubtype;
    ctx->nalLengthSize = 4;
    ctx->nextForcedKeyframeTime = 0;
    ctx->profile = profile != 0 ? profile : 77;

    HRESULT hr = CoCreateInstance(CLSID_CMSH264EncoderMFT, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(&ctx->transform));
    if (FAILED(hr) || ctx->transform == nullptr) {
        return FAILED(hr) ? hr : E_FAIL;
    }

    IMFMediaType *outputType = nullptr;
    hr = MFCreateMediaType(&outputType);
    if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_H264);
    if (SUCCEEDED(hr)) hr = MFSetAttributeSize(outputType, MF_MT_FRAME_SIZE, width, height);
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(outputType, MF_MT_FRAME_RATE, ctx->fpsNum, ctx->fpsDen);
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(outputType, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
    if (SUCCEEDED(hr)) hr = outputType->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
    if (SUCCEEDED(hr)) hr = outputType->SetUINT32(MF_MT_AVG_BITRATE, ctx->bitrate);
    if (SUCCEEDED(hr)) hr = outputType->SetUINT32(MF_MT_MPEG2_PROFILE, ctx->profile);
    if (SUCCEEDED(hr)) hr = ctx->transform->SetOutputType(0, outputType, 0);
    if (FAILED(hr)) {
        SafeRelease(&outputType);
        SafeRelease(&ctx->transform);
        return hr;
    }
    ctx->outputType = outputType;

    IMFMediaType *inputType = nullptr;
    hr = MFCreateMediaType(&inputType);
    if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_SUBTYPE, inputSubtype);
    if (SUCCEEDED(hr)) hr = MFSetAttributeSize(inputType, MF_MT_FRAME_SIZE, width, height);
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(inputType, MF_MT_FRAME_RATE, ctx->fpsNum, ctx->fpsDen);
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(inputType, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
    if (SUCCEEDED(hr)) hr = inputType->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
    if (SUCCEEDED(hr)) hr = ctx->transform->SetInputType(0, inputType, 0);
    if (FAILED(hr)) {
        SafeRelease(&inputType);
        SafeRelease(&ctx->outputType);
        SafeRelease(&ctx->transform);
        return hr;
    }
    ctx->inputType = inputType;

    if (SUCCEEDED(ctx->transform->QueryInterface(IID_ICodecAPI, reinterpret_cast<void **>(&ctx->codecApi))) && ctx->codecApi != nullptr) {
        const UINT32 fps = ctx->fpsDen != 0 ? std::max<UINT32>(1, (ctx->fpsNum + ctx->fpsDen - 1) / ctx->fpsDen) : 30U;
        VARIANT value;
        VariantInit(&value);

        value.vt = VT_UI4;
        value.ulVal = eAVEncCommonRateControlMode_CBR;
        ctx->codecApi->SetValue(&CODECAPI_AVEncCommonRateControlMode, &value);

        value.ulVal = ctx->bitrate;
        ctx->codecApi->SetValue(&CODECAPI_AVEncCommonMeanBitRate, &value);

        value.ulVal = fps;
        ctx->codecApi->SetValue(&CODECAPI_AVEncMPVGOPSize, &value);

        value.ulVal = 0;
        ctx->codecApi->SetValue(&CODECAPI_AVEncMPVDefaultBPictureCount, &value);

        value.ulVal = 1;
        ctx->codecApi->SetValue(&CODECAPI_AVLowLatencyMode, &value);

        value.ulVal = ctx->profile;
#ifdef CODECAPI_AVEncH264VProfile
        ctx->codecApi->SetValue(&CODECAPI_AVEncH264VProfile, &value);
#endif

        VariantClear(&value);
    }

    hr = LoadCodecConfig(ctx->outputType, MFVideoFormat_H264, &ctx->codecConfig, &ctx->nalLengthSize);
    if (FAILED(hr)) {
        ctx->codecConfig.clear();
        ctx->nalLengthSize = 4;
    }

    hr = ctx->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
    if (SUCCEEDED(hr)) hr = ctx->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
    if (SUCCEEDED(hr)) ctx->streamingBegun = true;
    if (FAILED(hr)) {
        SafeRelease(&ctx->inputType);
        SafeRelease(&ctx->outputType);
        SafeRelease(&ctx->transform);
    }
    return hr;
}

void CloseH264Encoder(H264EncoderContext *ctx) {
    if (ctx == nullptr) {
        return;
    }
    if (ctx->transform != nullptr && ctx->streamingBegun) {
        ctx->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
        ctx->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
        ctx->transform->ProcessMessage(MFT_MESSAGE_COMMAND_FLUSH, 0);
    }
    SafeRelease(&ctx->codecApi);
    SafeRelease(&ctx->inputType);
    SafeRelease(&ctx->outputType);
    SafeRelease(&ctx->transform);
    ctx->codecConfig.clear();
    ctx->streamingBegun = false;
}

HRESULT EncodeH264Sample(H264EncoderContext *ctx, IMFSample *inputSample, std::vector<EncodedPacket> *packetsOut) {
    if (ctx == nullptr || ctx->transform == nullptr || inputSample == nullptr || packetsOut == nullptr) {
        return E_POINTER;
    }
    if (ctx->codecApi != nullptr) {
        LONGLONG sampleTime = 0;
        if (SUCCEEDED(inputSample->GetSampleTime(&sampleTime))) {
            const LONGLONG interval100ns = 10 * 1000 * 1000;
            if (ctx->nextForcedKeyframeTime == 0 || sampleTime >= ctx->nextForcedKeyframeTime) {
                VARIANT value;
                VariantInit(&value);
                value.vt = VT_UI4;
                value.ulVal = 1;
                ctx->codecApi->SetValue(&CODECAPI_AVEncVideoForceKeyFrame, &value);
                VariantClear(&value);
                ctx->nextForcedKeyframeTime = sampleTime + interval100ns;
            }
        }
    }
    HRESULT hr = ctx->transform->ProcessInput(0, inputSample, 0);
    if (FAILED(hr)) {
        return hr;
    }

    for (;;) {
        MFT_OUTPUT_STREAM_INFO streamInfo = {};
        hr = ctx->transform->GetOutputStreamInfo(0, &streamInfo);
        if (FAILED(hr)) {
            return hr;
        }

        IMFSample *outputSample = nullptr;
        IMFMediaBuffer *outputBuffer = nullptr;
        hr = MFCreateSample(&outputSample);
        if (FAILED(hr)) {
            return hr;
        }
        hr = MFCreateMemoryBuffer(streamInfo.cbSize > 0 ? streamInfo.cbSize : 1024 * 1024, &outputBuffer);
        if (SUCCEEDED(hr)) {
            hr = outputSample->AddBuffer(outputBuffer);
        }
        SafeRelease(&outputBuffer);
        if (FAILED(hr)) {
            SafeRelease(&outputSample);
            return hr;
        }

        MFT_OUTPUT_DATA_BUFFER output = {};
        output.dwStreamID = 0;
        output.pSample = outputSample;
        DWORD status = 0;
        hr = ctx->transform->ProcessOutput(0, 1, &output, &status);
        if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) {
            SafeRelease(&outputSample);
            return S_OK;
        }
        if (FAILED(hr)) {
            SafeRelease(&outputSample);
            return hr;
        }

        IMFMediaBuffer *buffer = nullptr;
        hr = outputSample->ConvertToContiguousBuffer(&buffer);
        if (SUCCEEDED(hr) && buffer != nullptr) {
            BYTE *raw = nullptr;
            DWORD maxLen = 0;
            DWORD curLen = 0;
            hr = buffer->Lock(&raw, &maxLen, &curLen);
            if (SUCCEEDED(hr)) {
                UINT32 cleanPoint = 0;
                bool isKeyFrame = outputSample->GetUINT32(MFSampleExtension_CleanPoint, &cleanPoint) == S_OK && cleanPoint != 0;
                EncodedPacket packet = {};
                packet.annexb = ToAnnexB(raw, curLen, ctx->nalLengthSize, ctx->codecConfig, isKeyFrame);
                outputSample->GetSampleTime(&packet.sampleTime);
                if (!packet.annexb.empty()) {
                    packetsOut->push_back(std::move(packet));
                }
                buffer->Unlock();
            }
        }
        SafeRelease(&buffer);
        SafeRelease(&outputSample);
    }
}

DWORD WINAPI CaptureThreadMain(LPVOID param) {
    WinCapture *capture = static_cast<WinCapture *>(param);
    HRESULT hr = MfStartupScoped();
    if (FAILED(hr)) {
        capture->error = WideToUtf8String(CaptureErrorMessage(hr, "initialize media foundation"));
        SetEvent(capture->readyEvent);
        return 1;
    }

    if (capture->useDshow) {
        if (!DshowCaptureRun(capture) && capture->error.empty()) {
            capture->error = "directshow device not found";
        }
        if (!capture->started) {
            SetEvent(capture->readyEvent);
        }
        MfShutdownScoped();
        return capture->started ? 0 : 1;
    }

    IMFActivate *device = nullptr;
    IMFMediaSource *source = nullptr;
    IMFSourceReader *reader = nullptr;
    IMFMediaType *currentType = nullptr;
    std::vector<std::vector<uint8_t>> codecConfig;
    uint32_t nalLengthSize = 4;
    GUID subtype = MFVideoFormat_H264;
    bool useEncoder = false;
    RawOutputSelection rawSelection = {};
    H264EncoderContext encoder = {};

    do {
        hr = FindDevice(capture->device, &device);
        if (FAILED(hr)) {
            // * a device media foundation cannot see may still be a directshow software filter
            if (DshowCaptureRun(capture)) {
                break;
            }
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "find usb device"));
            break;
        }

        hr = device->ActivateObject(IID_PPV_ARGS(&source));
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "activate usb device"));
            break;
        }

        hr = MFCreateSourceReaderFromMediaSource(source, nullptr, &reader);
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "create source reader"));
            break;
        }

        hr = reader->SetStreamSelection(MF_SOURCE_READER_ALL_STREAMS, FALSE);
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "disable unused streams"));
            break;
        }
        hr = reader->SetStreamSelection(MF_SOURCE_READER_FIRST_VIDEO_STREAM, TRUE);
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "enable video stream"));
            break;
        }

        subtype = (_wcsicmp(capture->codec.c_str(), L"h265") == 0) ? MFVideoFormat_HEVC : MFVideoFormat_H264;
        const bool forceH264Encoder = GuidEqual(subtype, MFVideoFormat_H264) && !capture->h264Profile.empty();
        HRESULT compressedHr = E_FAIL;
        MediaTypeSelection selection = {};
        if (!forceH264Encoder) {
            compressedHr = SelectCompressedMediaType(reader, subtype, capture->width, capture->height, capture->fps, &selection);
        }
        if (forceH264Encoder || FAILED(compressedHr)) {
            if (!GuidEqual(subtype, MFVideoFormat_H264)) {
                capture->error = WideToUtf8String(L"device does not expose native " + capture->codec + L" output");
                break;
            }
            hr = SelectRawMediaType(reader, capture->width, capture->height, capture->fps, &rawSelection);
            if (FAILED(hr)) {
                capture->error = WideToUtf8String(L"device does not expose native h264 and no suitable raw output was found");
                break;
            }
            hr = SetReaderRawOutputType(reader, rawSelection);
            if (FAILED(hr)) {
                capture->error = WideToUtf8String(CaptureErrorMessage(hr, "set raw media type"));
                break;
            }
            UINT32 bitrate = capture->bitrateKbps > 0 ? static_cast<UINT32>(capture->bitrateKbps) * 1000U : 0U;
            const UINT32 profile = H264ProfileValue(capture->h264Profile);
            hr = CreateH264Encoder(rawSelection.width, rawSelection.height, rawSelection.fpsNum, rawSelection.fpsDen, bitrate, profile, rawSelection.outputSubtype, &encoder);
            if (FAILED(hr)) {
                capture->error = WideToUtf8String(CaptureErrorMessage(hr, "create h264 encoder"));
                break;
            }
            useEncoder = true;
        } else {
            hr = reader->SetCurrentMediaType(MF_SOURCE_READER_FIRST_VIDEO_STREAM, nullptr, selection.type);
            if (FAILED(hr)) {
                SafeRelease(&selection.type);
                capture->error = WideToUtf8String(CaptureErrorMessage(hr, "set current media type"));
                break;
            }
            currentType = selection.type;

            hr = LoadCodecConfig(currentType, subtype, &codecConfig, &nalLengthSize);
            if (FAILED(hr)) {
                codecConfig.clear();
                nalLengthSize = 4;
            }
        }

        capture->started = true;
        SetEvent(capture->readyEvent);

        while (WaitForSingleObject(capture->stopEvent, 0) != WAIT_OBJECT_0) {
            DWORD streamFlags = 0;
            LONGLONG sampleTime = 0;
            IMFSample *sample = nullptr;
            hr = reader->ReadSample(MF_SOURCE_READER_FIRST_VIDEO_STREAM, 0, nullptr, &streamFlags, &sampleTime, &sample);
            if (FAILED(hr)) {
                capture->error = WideToUtf8String(CaptureErrorMessage(hr, "read sample"));
                WebrtpUsbWinError(capture->handle, StringDup(capture->error));
                break;
            }
            if ((streamFlags & MF_SOURCE_READERF_ENDOFSTREAM) != 0) {
                WebrtpUsbWinError(capture->handle, StringDup("usb device reached end of stream"));
                SafeRelease(&sample);
                break;
            }
            if (sample == nullptr) {
                Sleep(1);
                continue;
            }

            IMFMediaBuffer *buffer = nullptr;
            hr = sample->ConvertToContiguousBuffer(&buffer);
            if (SUCCEEDED(hr) && buffer != nullptr) {
                BYTE *raw = nullptr;
                DWORD maxLen = 0;
                DWORD curLen = 0;
                hr = buffer->Lock(&raw, &maxLen, &curLen);
                if (SUCCEEDED(hr)) {
                    if (useEncoder) {
                        std::vector<EncodedPacket> packets;
                        hr = EncodeH264Sample(&encoder, sample, &packets);
                        if (FAILED(hr)) {
                            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "encode h264 sample"));
                            buffer->Unlock();
                            SafeRelease(&buffer);
                            SafeRelease(&sample);
                            WebrtpUsbWinError(capture->handle, StringDup(capture->error));
                            break;
                        }
                        for (const auto &packet : packets) {
                            if (!packet.annexb.empty()) {
                                uint32_t pts90k = static_cast<uint32_t>((packet.sampleTime * 9) / 1000);
                                WebrtpUsbWinPacket(capture->handle, const_cast<uint8_t *>(packet.annexb.data()), static_cast<int>(packet.annexb.size()), pts90k);
                            }
                        }
                    } else {
                        UINT32 cleanPoint = 0;
                        bool isKeyFrame = sample->GetUINT32(MFSampleExtension_CleanPoint, &cleanPoint) == S_OK && cleanPoint != 0;
                        std::vector<uint8_t> annexb = ToAnnexB(raw, curLen, nalLengthSize, codecConfig, isKeyFrame);
                        if (!annexb.empty()) {
                            uint32_t pts90k = static_cast<uint32_t>((sampleTime * 9) / 1000);
                            WebrtpUsbWinPacket(capture->handle, annexb.data(), static_cast<int>(annexb.size()), pts90k);
                        }
                    }
                    buffer->Unlock();
                }
            }
            SafeRelease(&buffer);
            SafeRelease(&sample);
        }
    } while (false);

    if (!capture->started) {
        SetEvent(capture->readyEvent);
    }

    SafeRelease(&currentType);
    SafeRelease(&reader);
    CloseH264Encoder(&encoder);
    if (source != nullptr) {
        source->Shutdown();
    }
    SafeRelease(&source);
    SafeRelease(&device);
    MfShutdownScoped();
    return 0;
}

}  // namespace

extern "C" void *WebrtpUsbWinCaptureStart(const char *device, const char *codec, const char *h264Profile, int width, int height, double fps, int bitrateKbps, int useDshow, uintptr_t handle, char **errOut) {
    WinCapture *capture = new WinCapture();
    capture->thread = nullptr;
    capture->stopEvent = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    capture->readyEvent = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    capture->handle = handle;
    capture->device = Utf8ToWide(device);
    capture->codec = Utf8ToWide(codec);
    capture->h264Profile = Utf8ToWide(h264Profile);
    capture->width = width;
    capture->height = height;
    capture->fps = fps;
    capture->bitrateKbps = bitrateKbps;
    capture->useDshow = useDshow != 0;
    capture->started = false;

    if (capture->stopEvent == nullptr || capture->readyEvent == nullptr) {
        if (errOut != nullptr) {
            *errOut = StringDup("create windows capture events failed");
        }
        if (capture->stopEvent != nullptr) {
            CloseHandle(capture->stopEvent);
        }
        if (capture->readyEvent != nullptr) {
            CloseHandle(capture->readyEvent);
        }
        delete capture;
        return nullptr;
    }

    capture->thread = CreateThread(nullptr, 0, CaptureThreadMain, capture, 0, nullptr);
    if (capture->thread == nullptr) {
        if (errOut != nullptr) {
            *errOut = StringDup("create windows capture thread failed");
        }
        CloseHandle(capture->stopEvent);
        CloseHandle(capture->readyEvent);
        delete capture;
        return nullptr;
    }

    WaitForSingleObject(capture->readyEvent, INFINITE);
    if (!capture->started) {
        if (errOut != nullptr) {
            *errOut = StringDup(capture->error.empty() ? std::string("windows usb capture start failed") : capture->error);
        }
        SetEvent(capture->stopEvent);
        WaitForSingleObject(capture->thread, INFINITE);
        CloseHandle(capture->thread);
        CloseHandle(capture->stopEvent);
        CloseHandle(capture->readyEvent);
        delete capture;
        return nullptr;
    }

    return capture;
}

extern "C" void WebrtpUsbWinCaptureStop(void *ref) {
    if (ref == nullptr) {
        return;
    }
    WinCapture *capture = static_cast<WinCapture *>(ref);
    SetEvent(capture->stopEvent);
    WaitForSingleObject(capture->thread, INFINITE);
    CloseHandle(capture->thread);
    CloseHandle(capture->stopEvent);
    CloseHandle(capture->readyEvent);
    delete capture;
}

extern "C" char *WebrtpUsbWinDeviceList(char **errOut) {
    HRESULT hr = MfStartupScoped();
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = WideToUtf8Dup(CaptureErrorMessage(hr, "initialize media foundation"));
        }
        return nullptr;
    }
    std::string result;
    hr = DeviceListString(&result);
    MfShutdownScoped();
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = WideToUtf8Dup(CaptureErrorMessage(hr, "list usb devices"));
        }
        return nullptr;
    }
    return StringDup(result);
}

extern "C" char *WebrtpUsbWinDeviceCapabilities(const char *device, char **errOut) {
    HRESULT hr = MfStartupScoped();
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = WideToUtf8Dup(CaptureErrorMessage(hr, "initialize media foundation"));
        }
        return nullptr;
    }

    IMFActivate *found = nullptr;
    hr = FindDevice(Utf8ToWide(device), &found);
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = WideToUtf8Dup(CaptureErrorMessage(hr, "find usb device"));
        }
        MfShutdownScoped();
        return nullptr;
    }

    std::string result;
    hr = DeviceCapabilitiesJson(found, &result);
    SafeRelease(&found);
    MfShutdownScoped();
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = WideToUtf8Dup(CaptureErrorMessage(hr, "query usb capabilities"));
        }
        return nullptr;
    }
    return StringDup(result);
}

namespace {

struct DshowMonikerEntry {
    IMoniker *moniker;
    std::wstring displayName;
    std::wstring friendlyName;
    bool devicePathReadable;
};

GUID DshowFourccSubtype(DWORD fourcc) {
    GUID guid = {fourcc, 0x0000, 0x0010, {0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}};
    return guid;
}

std::string DshowFormatLabel(const GUID &subtype) {
    if (GuidEqual(subtype, MEDIASUBTYPE_YUY2)) {
        return "yuyv422";
    }
    if (GuidEqual(subtype, MEDIASUBTYPE_UYVY)) {
        return "uyvy422";
    }
    if (GuidEqual(subtype, MEDIASUBTYPE_NV12)) {
        return "nv12";
    }
    if (GuidEqual(subtype, MEDIASUBTYPE_MJPG)) {
        return "mjpeg";
    }
    if (GuidEqual(subtype, MEDIASUBTYPE_RGB24)) {
        return "bgr24";
    }
    if (GuidEqual(subtype, MEDIASUBTYPE_RGB32)) {
        return "rgb32";
    }
    if (GuidEqual(subtype, DshowFourccSubtype(MAKEFOURCC('H', '2', '6', '4')))) {
        return "h264";
    }
    if (GuidEqual(subtype, DshowFourccSubtype(MAKEFOURCC('I', '4', '2', '0')))) {
        return "yuv420p";
    }
    return "";
}

void DshowClearMediaType(AM_MEDIA_TYPE *mediaType) {
    if (mediaType == nullptr) {
        return;
    }
    if (mediaType->cbFormat != 0 && mediaType->pbFormat != nullptr) {
        CoTaskMemFree(mediaType->pbFormat);
    }
    if (mediaType->pUnk != nullptr) {
        mediaType->pUnk->Release();
    }
    memset(mediaType, 0, sizeof(*mediaType));
}

void DshowFreeMediaType(AM_MEDIA_TYPE *mediaType) {
    if (mediaType == nullptr) {
        return;
    }
    DshowClearMediaType(mediaType);
    CoTaskMemFree(mediaType);
}

HRESULT DshowCopyMediaType(AM_MEDIA_TYPE *dst, const AM_MEDIA_TYPE *src) {
    if (dst == nullptr || src == nullptr) {
        return E_POINTER;
    }
    *dst = *src;
    dst->pbFormat = nullptr;
    if (src->cbFormat != 0 && src->pbFormat != nullptr) {
        dst->pbFormat = static_cast<BYTE *>(CoTaskMemAlloc(src->cbFormat));
        if (dst->pbFormat == nullptr) {
            dst->cbFormat = 0;
            return E_OUTOFMEMORY;
        }
        memcpy(dst->pbFormat, src->pbFormat, src->cbFormat);
    }
    if (dst->pUnk != nullptr) {
        dst->pUnk->AddRef();
    }
    return S_OK;
}

bool DshowMediaTypeGeometry(const AM_MEDIA_TYPE *mediaType, UINT32 *widthOut, UINT32 *heightOut, REFERENCE_TIME *avgTimePerFrameOut) {
    if (mediaType == nullptr || mediaType->pbFormat == nullptr) {
        return false;
    }
    LONG width = 0;
    LONG height = 0;
    REFERENCE_TIME avgTimePerFrame = 0;
    if (GuidEqual(mediaType->formattype, FORMAT_VideoInfo) && mediaType->cbFormat >= sizeof(VIDEOINFOHEADER)) {
        const VIDEOINFOHEADER *header = reinterpret_cast<const VIDEOINFOHEADER *>(mediaType->pbFormat);
        width = header->bmiHeader.biWidth;
        height = header->bmiHeader.biHeight;
        avgTimePerFrame = header->AvgTimePerFrame;
    } else if (GuidEqual(mediaType->formattype, FORMAT_VideoInfo2) && mediaType->cbFormat >= sizeof(VIDEOINFOHEADER2)) {
        const VIDEOINFOHEADER2 *header = reinterpret_cast<const VIDEOINFOHEADER2 *>(mediaType->pbFormat);
        width = header->bmiHeader.biWidth;
        height = header->bmiHeader.biHeight;
        avgTimePerFrame = header->AvgTimePerFrame;
    } else {
        return false;
    }
    if (widthOut != nullptr) {
        *widthOut = static_cast<UINT32>(width < 0 ? -width : width);
    }
    if (heightOut != nullptr) {
        // * yuv frames are top down regardless of sign, so only the magnitude matters here
        *heightOut = static_cast<UINT32>(height < 0 ? -height : height);
    }
    if (avgTimePerFrameOut != nullptr) {
        *avgTimePerFrameOut = avgTimePerFrame;
    }
    return true;
}

void DshowMergeMediaType(const AM_MEDIA_TYPE *mediaType, std::vector<CapabilityModeEntry> *modes) {
    if (mediaType == nullptr || modes == nullptr || !GuidEqual(mediaType->majortype, MEDIATYPE_Video)) {
        return;
    }
    UINT32 width = 0;
    UINT32 height = 0;
    REFERENCE_TIME avgTimePerFrame = 0;
    if (!DshowMediaTypeGeometry(mediaType, &width, &height, &avgTimePerFrame)) {
        return;
    }
    double fps = (avgTimePerFrame > 0) ? 10000000.0 / static_cast<double>(avgTimePerFrame) : 0.0;
    MergeMode(modes, width, height, fps, DshowFormatLabel(mediaType->subtype));
}

HRESULT DshowCollectMonikers(std::vector<DshowMonikerEntry> *entriesOut) {
    ICreateDevEnum *devEnum = nullptr;
    HRESULT hr = CoCreateInstance(CLSID_SystemDeviceEnum, nullptr, CLSCTX_INPROC_SERVER, IID_ICreateDevEnum, reinterpret_cast<void **>(&devEnum));
    if (FAILED(hr)) {
        return hr;
    }
    IEnumMoniker *enumMoniker = nullptr;
    hr = devEnum->CreateClassEnumerator(CLSID_VideoInputDeviceCategory, &enumMoniker, 0);
    SafeRelease(&devEnum);
    if (hr != S_OK) {
        return FAILED(hr) ? hr : S_OK;
    }
    IMoniker *moniker = nullptr;
    while (enumMoniker->Next(1, &moniker, nullptr) == S_OK) {
        DshowMonikerEntry entry;
        entry.moniker = moniker;
        entry.devicePathReadable = false;
        LPOLESTR displayName = nullptr;
        if (SUCCEEDED(moniker->GetDisplayName(nullptr, nullptr, &displayName)) && displayName != nullptr) {
            entry.displayName = displayName;
            CoTaskMemFree(displayName);
        }
        IPropertyBag *bag = nullptr;
        if (SUCCEEDED(moniker->BindToStorage(nullptr, nullptr, IID_IPropertyBag, reinterpret_cast<void **>(&bag))) && bag != nullptr) {
            VARIANT value;
            VariantInit(&value);
            if (SUCCEEDED(bag->Read(L"FriendlyName", &value, nullptr)) && value.vt == VT_BSTR && value.bstrVal != nullptr) {
                entry.friendlyName = value.bstrVal;
            }
            VariantClear(&value);
            VariantInit(&value);
            entry.devicePathReadable = SUCCEEDED(bag->Read(L"DevicePath", &value, nullptr));
            VariantClear(&value);
            SafeRelease(&bag);
        }
        entriesOut->push_back(entry);
        moniker = nullptr;
    }
    SafeRelease(&enumMoniker);
    return S_OK;
}

void DshowReleaseMonikers(std::vector<DshowMonikerEntry> *entries) {
    if (entries == nullptr) {
        return;
    }
    for (auto &entry : *entries) {
        SafeRelease(&entry.moniker);
    }
}

void DshowPinMediaTypes(IPin *pin, std::vector<CapabilityModeEntry> *modes) {
    if (pin == nullptr || modes == nullptr) {
        return;
    }
    IAMStreamConfig *config = nullptr;
    if (SUCCEEDED(pin->QueryInterface(IID_IAMStreamConfig, reinterpret_cast<void **>(&config))) && config != nullptr) {
        int count = 0;
        int size = 0;
        if (SUCCEEDED(config->GetNumberOfCapabilities(&count, &size)) && size == static_cast<int>(sizeof(VIDEO_STREAM_CONFIG_CAPS))) {
            std::vector<BYTE> capsBuffer(static_cast<size_t>(size));
            for (int idx = 0; idx < count; idx++) {
                AM_MEDIA_TYPE *mediaType = nullptr;
                if (FAILED(config->GetStreamCaps(idx, &mediaType, capsBuffer.data())) || mediaType == nullptr) {
                    continue;
                }
                DshowMergeMediaType(mediaType, modes);
                DshowFreeMediaType(mediaType);
            }
        }
        SafeRelease(&config);
        if (!modes->empty()) {
            return;
        }
    }
    IEnumMediaTypes *enumTypes = nullptr;
    if (SUCCEEDED(pin->EnumMediaTypes(&enumTypes)) && enumTypes != nullptr) {
        AM_MEDIA_TYPE *mediaType = nullptr;
        while (enumTypes->Next(1, &mediaType, nullptr) == S_OK) {
            DshowMergeMediaType(mediaType, modes);
            DshowFreeMediaType(mediaType);
            mediaType = nullptr;
        }
        SafeRelease(&enumTypes);
    }
}

HRESULT DshowCapabilitiesJson(IMoniker *moniker, const std::wstring &id, const std::wstring &name, std::string *resultOut) {
    if (moniker == nullptr || resultOut == nullptr) {
        return E_POINTER;
    }
    IBaseFilter *filter = nullptr;
    HRESULT hr = moniker->BindToObject(nullptr, nullptr, IID_IBaseFilter, reinterpret_cast<void **>(&filter));
    if (FAILED(hr) || filter == nullptr) {
        return FAILED(hr) ? hr : E_FAIL;
    }
    std::vector<CapabilityModeEntry> modes;
    IEnumPins *enumPins = nullptr;
    hr = filter->EnumPins(&enumPins);
    if (SUCCEEDED(hr) && enumPins != nullptr) {
        IPin *pin = nullptr;
        while (enumPins->Next(1, &pin, nullptr) == S_OK) {
            PIN_DIRECTION direction;
            if (SUCCEEDED(pin->QueryDirection(&direction)) && direction == PINDIR_OUTPUT) {
                DshowPinMediaTypes(pin, &modes);
            }
            SafeRelease(&pin);
        }
        SafeRelease(&enumPins);
    }
    SafeRelease(&filter);
    SortModeEntries(&modes);
    std::vector<std::string> codecs;
    codecs.push_back("h264");
    *resultOut = CapabilitiesJsonBuild(id, name, codecs, "target", modes);
    return S_OK;
}

const GUID DshowSinkFilterClsid = {0x7f9a4f80, 0x2f3b, 0x4e59, {0x9c, 0x1d, 0x51, 0x1e, 0x0b, 0x9a, 0x33, 0x21}};

struct DshowFrameContext {
    WinCapture *capture;
    H264EncoderContext *encoder;
    CRITICAL_SECTION lock;
    HANDLE eosEvent;
    LONGLONG frameIndex;
    LONGLONG frameDuration;
    bool running;
};

bool DshowSinkSubtypeAccepted(const GUID &subtype) {
    return GuidEqual(subtype, MEDIASUBTYPE_NV12) ||
           GuidEqual(subtype, MEDIASUBTYPE_YUY2) ||
           GuidEqual(subtype, MEDIASUBTYPE_YV12) ||
           GuidEqual(subtype, DshowFourccSubtype(MAKEFOURCC('I', '4', '2', '0'))) ||
           GuidEqual(subtype, DshowFourccSubtype(MAKEFOURCC('I', 'Y', 'U', 'V')));
}

bool DshowSinkTypeAccepted(const AM_MEDIA_TYPE *mediaType) {
    if (mediaType == nullptr || !GuidEqual(mediaType->majortype, MEDIATYPE_Video) || !DshowSinkSubtypeAccepted(mediaType->subtype)) {
        return false;
    }
    UINT32 width = 0;
    UINT32 height = 0;
    return DshowMediaTypeGeometry(mediaType, &width, &height, nullptr) && width > 0 && height > 0;
}

double DshowCaptureFormatScore(const GUID &subtype, UINT32 width, UINT32 height, double fps, int widthHint, int heightHint, double fpsHint) {
    double score = 0.0;
    if (widthHint > 0) score += fabs(static_cast<double>(width) - widthHint) * 1000.0;
    if (heightHint > 0) score += fabs(static_cast<double>(height) - heightHint) * 1000.0;
    if (fpsHint > 0) {
        if (fps > 0) score += fabs(fps - fpsHint) * 100.0;
        else score += 1000000.0;
    }
    if (GuidEqual(subtype, MEDIASUBTYPE_NV12)) score -= 20.0;
    else if (GuidEqual(subtype, MEDIASUBTYPE_YV12) ||
             GuidEqual(subtype, DshowFourccSubtype(MAKEFOURCC('I', '4', '2', '0'))) ||
             GuidEqual(subtype, DshowFourccSubtype(MAKEFOURCC('I', 'Y', 'U', 'V')))) score -= 15.0;
    else if (GuidEqual(subtype, MEDIASUBTYPE_YUY2)) score -= 10.0;
    return score;
}

void DshowFrameDeliver(DshowFrameContext *ctx, const BYTE *data, DWORD length, REFERENCE_TIME startTime, bool haveTime) {
    if (ctx == nullptr || data == nullptr || length == 0) {
        return;
    }
    EnterCriticalSection(&ctx->lock);
    if (!ctx->running || ctx->encoder == nullptr) {
        LeaveCriticalSection(&ctx->lock);
        return;
    }
    LONGLONG duration = ctx->frameDuration > 0 ? ctx->frameDuration : 333333;
    LONGLONG sampleTime = haveTime ? startTime : ctx->frameIndex * duration;
    ctx->frameIndex++;

    IMFSample *sample = nullptr;
    IMFMediaBuffer *buffer = nullptr;
    HRESULT hr = MFCreateSample(&sample);
    if (SUCCEEDED(hr)) hr = MFCreateMemoryBuffer(length, &buffer);
    if (SUCCEEDED(hr)) {
        BYTE *dst = nullptr;
        DWORD maxLength = 0;
        hr = buffer->Lock(&dst, &maxLength, nullptr);
        if (SUCCEEDED(hr)) {
            memcpy(dst, data, length);
            buffer->Unlock();
            hr = buffer->SetCurrentLength(length);
        }
    }
    if (SUCCEEDED(hr)) hr = sample->AddBuffer(buffer);
    if (SUCCEEDED(hr)) hr = sample->SetSampleTime(sampleTime);
    if (SUCCEEDED(hr)) hr = sample->SetSampleDuration(duration);

    std::vector<EncodedPacket> packets;
    if (SUCCEEDED(hr)) hr = EncodeH264Sample(ctx->encoder, sample, &packets);
    SafeRelease(&buffer);
    SafeRelease(&sample);
    if (FAILED(hr)) {
        // * record the failure and wake the capture thread, which reports it exactly once
        ctx->running = false;
        ctx->capture->error = WideToUtf8String(CaptureErrorMessage(hr, "encode directshow sample"));
        LeaveCriticalSection(&ctx->lock);
        SetEvent(ctx->eosEvent);
        return;
    }
    LeaveCriticalSection(&ctx->lock);
    for (const auto &packet : packets) {
        if (!packet.annexb.empty()) {
            uint32_t pts90k = static_cast<uint32_t>((packet.sampleTime * 9) / 1000);
            WebrtpUsbWinPacket(ctx->capture->handle, const_cast<uint8_t *>(packet.annexb.data()), static_cast<int>(packet.annexb.size()), pts90k);
        }
    }
}

class DshowSinkEnumMediaTypes : public IEnumMediaTypes {
public:
    DshowSinkEnumMediaTypes() : refs(1) {}
    virtual ~DshowSinkEnumMediaTypes() {}

    STDMETHODIMP QueryInterface(REFIID riid, void **out) {
        if (out == nullptr) return E_POINTER;
        if (riid == IID_IUnknown || riid == IID_IEnumMediaTypes) {
            *out = static_cast<IEnumMediaTypes *>(this);
            AddRef();
            return S_OK;
        }
        *out = nullptr;
        return E_NOINTERFACE;
    }
    STDMETHODIMP_(ULONG) AddRef() { return InterlockedIncrement(&refs); }
    STDMETHODIMP_(ULONG) Release() {
        LONG value = InterlockedDecrement(&refs);
        if (value == 0) delete this;
        return value;
    }
    STDMETHODIMP Next(ULONG count, AM_MEDIA_TYPE **types, ULONG *fetched) {
        if (types == nullptr) return E_POINTER;
        if (count > 1 && fetched == nullptr) return E_INVALIDARG;
        if (fetched != nullptr) *fetched = 0;
        return S_FALSE;
    }
    STDMETHODIMP Skip(ULONG) { return S_FALSE; }
    STDMETHODIMP Reset() { return S_OK; }
    STDMETHODIMP Clone(IEnumMediaTypes **out) {
        if (out == nullptr) return E_POINTER;
        *out = new DshowSinkEnumMediaTypes();
        return S_OK;
    }

private:
    LONG refs;
};

class DshowSinkEnumPins : public IEnumPins {
public:
    DshowSinkEnumPins(IPin *pin, ULONG index) : refs(1), pin(pin), index(index) {
        if (pin != nullptr) pin->AddRef();
    }
    virtual ~DshowSinkEnumPins() { SafeRelease(&pin); }

    STDMETHODIMP QueryInterface(REFIID riid, void **out) {
        if (out == nullptr) return E_POINTER;
        if (riid == IID_IUnknown || riid == IID_IEnumPins) {
            *out = static_cast<IEnumPins *>(this);
            AddRef();
            return S_OK;
        }
        *out = nullptr;
        return E_NOINTERFACE;
    }
    STDMETHODIMP_(ULONG) AddRef() { return InterlockedIncrement(&refs); }
    STDMETHODIMP_(ULONG) Release() {
        LONG value = InterlockedDecrement(&refs);
        if (value == 0) delete this;
        return value;
    }
    STDMETHODIMP Next(ULONG count, IPin **out, ULONG *fetched) {
        if (out == nullptr) return E_POINTER;
        if (count > 1 && fetched == nullptr) return E_INVALIDARG;
        ULONG delivered = 0;
        if (index == 0 && count > 0 && pin != nullptr) {
            out[0] = pin;
            pin->AddRef();
            index = 1;
            delivered = 1;
        }
        if (fetched != nullptr) *fetched = delivered;
        return delivered == count ? S_OK : S_FALSE;
    }
    STDMETHODIMP Skip(ULONG count) {
        index += count;
        return index <= 1 ? S_OK : S_FALSE;
    }
    STDMETHODIMP Reset() {
        index = 0;
        return S_OK;
    }
    STDMETHODIMP Clone(IEnumPins **out) {
        if (out == nullptr) return E_POINTER;
        *out = new DshowSinkEnumPins(pin, index);
        return S_OK;
    }

private:
    LONG refs;
    IPin *pin;
    ULONG index;
};

class DshowSinkFilter;

class DshowSinkPin : public IPin, public IMemInputPin {
public:
    DshowSinkPin(DshowSinkFilter *filter, DshowFrameContext *ctx) : filter(filter), ctx(ctx), connectedPin(nullptr) {
        memset(&mediaType, 0, sizeof(mediaType));
    }
    virtual ~DshowSinkPin() { ResetConnection(); }

    STDMETHODIMP QueryInterface(REFIID riid, void **out) {
        if (out == nullptr) return E_POINTER;
        if (riid == IID_IUnknown || riid == IID_IPin) {
            *out = static_cast<IPin *>(this);
            AddRef();
            return S_OK;
        }
        if (riid == IID_IMemInputPin) {
            *out = static_cast<IMemInputPin *>(this);
            AddRef();
            return S_OK;
        }
        *out = nullptr;
        return E_NOINTERFACE;
    }
    STDMETHODIMP_(ULONG) AddRef();
    STDMETHODIMP_(ULONG) Release();

    STDMETHODIMP Connect(IPin *, const AM_MEDIA_TYPE *) { return E_UNEXPECTED; }
    STDMETHODIMP ReceiveConnection(IPin *connector, const AM_MEDIA_TYPE *pmt);
    STDMETHODIMP Disconnect();
    STDMETHODIMP ConnectedTo(IPin **out) {
        if (out == nullptr) return E_POINTER;
        if (connectedPin == nullptr) {
            *out = nullptr;
            return VFW_E_NOT_CONNECTED;
        }
        *out = connectedPin;
        connectedPin->AddRef();
        return S_OK;
    }
    STDMETHODIMP ConnectionMediaType(AM_MEDIA_TYPE *pmt) {
        if (pmt == nullptr) return E_POINTER;
        if (connectedPin == nullptr) {
            memset(pmt, 0, sizeof(*pmt));
            return VFW_E_NOT_CONNECTED;
        }
        return DshowCopyMediaType(pmt, &mediaType);
    }
    STDMETHODIMP QueryPinInfo(PIN_INFO *info);
    STDMETHODIMP QueryDirection(PIN_DIRECTION *dir) {
        if (dir == nullptr) return E_POINTER;
        *dir = PINDIR_INPUT;
        return S_OK;
    }
    STDMETHODIMP QueryId(LPWSTR *id) {
        if (id == nullptr) return E_POINTER;
        *id = static_cast<LPWSTR>(CoTaskMemAlloc(sizeof(L"input")));
        if (*id == nullptr) return E_OUTOFMEMORY;
        memcpy(*id, L"input", sizeof(L"input"));
        return S_OK;
    }
    STDMETHODIMP QueryAccept(const AM_MEDIA_TYPE *pmt) { return DshowSinkTypeAccepted(pmt) ? S_OK : S_FALSE; }
    STDMETHODIMP EnumMediaTypes(IEnumMediaTypes **out) {
        if (out == nullptr) return E_POINTER;
        *out = new DshowSinkEnumMediaTypes();
        return S_OK;
    }
    STDMETHODIMP QueryInternalConnections(IPin **, ULONG *) { return E_NOTIMPL; }
    STDMETHODIMP EndOfStream() {
        if (ctx != nullptr) SetEvent(ctx->eosEvent);
        return S_OK;
    }
    STDMETHODIMP BeginFlush() { return S_OK; }
    STDMETHODIMP EndFlush() { return S_OK; }
    STDMETHODIMP NewSegment(REFERENCE_TIME, REFERENCE_TIME, double) { return S_OK; }

    STDMETHODIMP GetAllocator(IMemAllocator **out) {
        if (out != nullptr) *out = nullptr;
        // * the upstream pin must provide its own allocator
        return VFW_E_NO_ALLOCATOR;
    }
    STDMETHODIMP NotifyAllocator(IMemAllocator *, BOOL) { return S_OK; }
    STDMETHODIMP GetAllocatorRequirements(ALLOCATOR_PROPERTIES *) { return E_NOTIMPL; }
    STDMETHODIMP Receive(IMediaSample *sample) {
        if (sample == nullptr) return E_POINTER;
        BYTE *data = nullptr;
        if (FAILED(sample->GetPointer(&data)) || data == nullptr) return S_OK;
        long length = sample->GetActualDataLength();
        if (length <= 0) return S_OK;
        REFERENCE_TIME startTime = 0;
        REFERENCE_TIME endTime = 0;
        bool haveTime = SUCCEEDED(sample->GetTime(&startTime, &endTime));
        DshowFrameDeliver(ctx, data, static_cast<DWORD>(length), startTime, haveTime);
        return S_OK;
    }
    STDMETHODIMP ReceiveMultiple(IMediaSample **samples, long count, long *processed) {
        if (samples == nullptr || processed == nullptr) return E_POINTER;
        long done = 0;
        for (long idx = 0; idx < count; idx++) {
            if (FAILED(Receive(samples[idx]))) break;
            done++;
        }
        *processed = done;
        return S_OK;
    }
    STDMETHODIMP ReceiveCanBlock() { return S_OK; }

    const AM_MEDIA_TYPE *ConnectedType() const { return connectedPin != nullptr ? &mediaType : nullptr; }
    void ResetConnection() {
        SafeRelease(&connectedPin);
        DshowClearMediaType(&mediaType);
    }

private:
    DshowSinkFilter *filter;
    DshowFrameContext *ctx;
    IPin *connectedPin;
    AM_MEDIA_TYPE mediaType;
};

class DshowSinkFilter : public IBaseFilter {
public:
    DshowSinkFilter(DshowFrameContext *ctx) : refs(1), state(State_Stopped), graph(nullptr), clock(nullptr) {
        InitializeCriticalSection(&lock);
        name[0] = L'\0';
        pin = new DshowSinkPin(this, ctx);
    }
    virtual ~DshowSinkFilter() {
        delete pin;
        SafeRelease(&clock);
        DeleteCriticalSection(&lock);
    }

    STDMETHODIMP QueryInterface(REFIID riid, void **out) {
        if (out == nullptr) return E_POINTER;
        if (riid == IID_IUnknown || riid == IID_IPersist || riid == IID_IMediaFilter || riid == IID_IBaseFilter) {
            *out = static_cast<IBaseFilter *>(this);
            AddRef();
            return S_OK;
        }
        *out = nullptr;
        return E_NOINTERFACE;
    }
    STDMETHODIMP_(ULONG) AddRef() { return InterlockedIncrement(&refs); }
    STDMETHODIMP_(ULONG) Release() {
        LONG value = InterlockedDecrement(&refs);
        if (value == 0) delete this;
        return value;
    }

    STDMETHODIMP GetClassID(CLSID *out) {
        if (out == nullptr) return E_POINTER;
        *out = DshowSinkFilterClsid;
        return S_OK;
    }
    STDMETHODIMP Stop() { return StateSet(State_Stopped); }
    STDMETHODIMP Pause() { return StateSet(State_Paused); }
    STDMETHODIMP Run(REFERENCE_TIME) { return StateSet(State_Running); }
    STDMETHODIMP GetState(DWORD, FILTER_STATE *out) {
        if (out == nullptr) return E_POINTER;
        EnterCriticalSection(&lock);
        *out = state;
        LeaveCriticalSection(&lock);
        return S_OK;
    }
    STDMETHODIMP SetSyncSource(IReferenceClock *newClock) {
        EnterCriticalSection(&lock);
        SafeRelease(&clock);
        clock = newClock;
        if (clock != nullptr) clock->AddRef();
        LeaveCriticalSection(&lock);
        return S_OK;
    }
    STDMETHODIMP GetSyncSource(IReferenceClock **out) {
        if (out == nullptr) return E_POINTER;
        EnterCriticalSection(&lock);
        *out = clock;
        if (*out != nullptr) (*out)->AddRef();
        LeaveCriticalSection(&lock);
        return S_OK;
    }
    STDMETHODIMP EnumPins(IEnumPins **out) {
        if (out == nullptr) return E_POINTER;
        *out = new DshowSinkEnumPins(static_cast<IPin *>(pin), 0);
        return S_OK;
    }
    STDMETHODIMP FindPin(LPCWSTR id, IPin **out) {
        if (out == nullptr) return E_POINTER;
        if (id != nullptr && wcscmp(id, L"input") == 0) {
            *out = static_cast<IPin *>(pin);
            (*out)->AddRef();
            return S_OK;
        }
        *out = nullptr;
        return VFW_E_NOT_FOUND;
    }
    STDMETHODIMP QueryFilterInfo(FILTER_INFO *info) {
        if (info == nullptr) return E_POINTER;
        wcsncpy(info->achName, name, MAX_FILTER_NAME - 1);
        info->achName[MAX_FILTER_NAME - 1] = L'\0';
        info->pGraph = graph;
        if (info->pGraph != nullptr) info->pGraph->AddRef();
        return S_OK;
    }
    STDMETHODIMP JoinFilterGraph(IFilterGraph *newGraph, LPCWSTR newName) {
        // * the graph pointer is deliberately not addrefed, per the documented contract
        graph = newGraph;
        if (newName != nullptr) {
            wcsncpy(name, newName, MAX_FILTER_NAME - 1);
            name[MAX_FILTER_NAME - 1] = L'\0';
        } else {
            name[0] = L'\0';
        }
        return S_OK;
    }
    STDMETHODIMP QueryVendorInfo(LPWSTR *) { return E_NOTIMPL; }

    FILTER_STATE StateGet() {
        EnterCriticalSection(&lock);
        FILTER_STATE current = state;
        LeaveCriticalSection(&lock);
        return current;
    }
    DshowSinkPin *SinkPin() { return pin; }

private:
    HRESULT StateSet(FILTER_STATE next) {
        EnterCriticalSection(&lock);
        state = next;
        LeaveCriticalSection(&lock);
        return S_OK;
    }

    LONG refs;
    FILTER_STATE state;
    IFilterGraph *graph;
    IReferenceClock *clock;
    DshowSinkPin *pin;
    CRITICAL_SECTION lock;
    WCHAR name[MAX_FILTER_NAME];
};

STDMETHODIMP_(ULONG) DshowSinkPin::AddRef() {
    return filter->AddRef();
}

STDMETHODIMP_(ULONG) DshowSinkPin::Release() {
    return filter->Release();
}

STDMETHODIMP DshowSinkPin::ReceiveConnection(IPin *connector, const AM_MEDIA_TYPE *pmt) {
    if (connector == nullptr || pmt == nullptr) return E_POINTER;
    if (connectedPin != nullptr) return VFW_E_ALREADY_CONNECTED;
    if (filter->StateGet() != State_Stopped) return VFW_E_NOT_STOPPED;
    if (!DshowSinkTypeAccepted(pmt)) return VFW_E_TYPE_NOT_ACCEPTED;
    HRESULT hr = DshowCopyMediaType(&mediaType, pmt);
    if (FAILED(hr)) return hr;
    connectedPin = connector;
    connectedPin->AddRef();
    return S_OK;
}

STDMETHODIMP DshowSinkPin::Disconnect() {
    if (filter->StateGet() != State_Stopped) return VFW_E_NOT_STOPPED;
    if (connectedPin == nullptr) return S_FALSE;
    ResetConnection();
    return S_OK;
}

STDMETHODIMP DshowSinkPin::QueryPinInfo(PIN_INFO *info) {
    if (info == nullptr) return E_POINTER;
    memset(info, 0, sizeof(*info));
    wcsncpy(info->achName, L"input", MAX_PIN_NAME - 1);
    info->dir = PINDIR_INPUT;
    info->pFilter = filter;
    filter->AddRef();
    return S_OK;
}

IPin *DshowFindCapturePin(IBaseFilter *filter) {
    IEnumPins *enumPins = nullptr;
    if (FAILED(filter->EnumPins(&enumPins)) || enumPins == nullptr) {
        return nullptr;
    }
    IPin *preferred = nullptr;
    IPin *fallback = nullptr;
    IPin *pin = nullptr;
    while (enumPins->Next(1, &pin, nullptr) == S_OK) {
        PIN_DIRECTION direction;
        if (FAILED(pin->QueryDirection(&direction)) || direction != PINDIR_OUTPUT) {
            SafeRelease(&pin);
            continue;
        }
        bool isCapture = false;
        IKsPropertySet *properties = nullptr;
        if (SUCCEEDED(pin->QueryInterface(IID_IKsPropertySet, reinterpret_cast<void **>(&properties))) && properties != nullptr) {
            GUID category = GUID_NULL;
            DWORD returned = 0;
            if (SUCCEEDED(properties->Get(AMPROPSETID_Pin, AMPROPERTY_PIN_CATEGORY, nullptr, 0, &category, sizeof(category), &returned)) && GuidEqual(category, PIN_CATEGORY_CAPTURE)) {
                isCapture = true;
            }
            SafeRelease(&properties);
        }
        if (isCapture && preferred == nullptr) {
            preferred = pin;
            pin = nullptr;
            continue;
        }
        if (fallback == nullptr) {
            fallback = pin;
            pin = nullptr;
            continue;
        }
        SafeRelease(&pin);
    }
    SafeRelease(&enumPins);
    if (preferred != nullptr) {
        SafeRelease(&fallback);
        return preferred;
    }
    // * a minimal software filter may not implement pin categories at all
    return fallback;
}

HRESULT DshowChooseCaptureFormat(IPin *pin, int widthHint, int heightHint, double fpsHint, AM_MEDIA_TYPE **chosenOut, IAMStreamConfig **configOut) {
    if (pin == nullptr || chosenOut == nullptr || configOut == nullptr) {
        return E_POINTER;
    }
    *chosenOut = nullptr;
    *configOut = nullptr;
    AM_MEDIA_TYPE *best = nullptr;
    double bestScore = 0.0;
    bool bestSet = false;
    auto consider = [&](AM_MEDIA_TYPE *mediaType) {
        if (!DshowSinkTypeAccepted(mediaType)) {
            DshowFreeMediaType(mediaType);
            return;
        }
        UINT32 width = 0;
        UINT32 height = 0;
        REFERENCE_TIME avgTimePerFrame = 0;
        DshowMediaTypeGeometry(mediaType, &width, &height, &avgTimePerFrame);
        double fps = (avgTimePerFrame > 0) ? 10000000.0 / static_cast<double>(avgTimePerFrame) : 0.0;
        double score = DshowCaptureFormatScore(mediaType->subtype, width, height, fps, widthHint, heightHint, fpsHint);
        if (!bestSet || score < bestScore) {
            if (best != nullptr) DshowFreeMediaType(best);
            best = mediaType;
            bestScore = score;
            bestSet = true;
            return;
        }
        DshowFreeMediaType(mediaType);
    };

    IAMStreamConfig *config = nullptr;
    if (SUCCEEDED(pin->QueryInterface(IID_IAMStreamConfig, reinterpret_cast<void **>(&config))) && config != nullptr) {
        int count = 0;
        int size = 0;
        if (SUCCEEDED(config->GetNumberOfCapabilities(&count, &size)) && size == static_cast<int>(sizeof(VIDEO_STREAM_CONFIG_CAPS))) {
            std::vector<BYTE> capsBuffer(static_cast<size_t>(size));
            for (int idx = 0; idx < count; idx++) {
                AM_MEDIA_TYPE *mediaType = nullptr;
                if (FAILED(config->GetStreamCaps(idx, &mediaType, capsBuffer.data())) || mediaType == nullptr) {
                    continue;
                }
                consider(mediaType);
            }
        }
    }
    if (!bestSet) {
        IEnumMediaTypes *enumTypes = nullptr;
        if (SUCCEEDED(pin->EnumMediaTypes(&enumTypes)) && enumTypes != nullptr) {
            AM_MEDIA_TYPE *mediaType = nullptr;
            while (enumTypes->Next(1, &mediaType, nullptr) == S_OK) {
                consider(mediaType);
                mediaType = nullptr;
            }
            SafeRelease(&enumTypes);
        }
    }
    if (!bestSet) {
        SafeRelease(&config);
        return VFW_E_INVALIDMEDIATYPE;
    }
    *chosenOut = best;
    *configOut = config;
    return S_OK;
}

bool DshowCaptureRun(WinCapture *capture) {
    if (_wcsicmp(capture->codec.c_str(), L"h264") != 0) {
        if (!capture->useDshow) {
            return false;
        }
        capture->error = "directshow virtual devices support codec h264 only";
        return true;
    }

    std::vector<DshowMonikerEntry> entries;
    HRESULT hr = DshowCollectMonikers(&entries);
    if (FAILED(hr)) {
        DshowReleaseMonikers(&entries);
        if (!capture->useDshow) {
            return false;
        }
        capture->error = WideToUtf8String(CaptureErrorMessage(hr, "list directshow devices"));
        return true;
    }
    IMoniker *moniker = nullptr;
    for (auto &entry : entries) {
        if (entry.devicePathReadable || entry.moniker == nullptr) {
            continue;
        }
        if (_wcsicmp(entry.displayName.c_str(), capture->device.c_str()) == 0 || _wcsicmp(entry.friendlyName.c_str(), capture->device.c_str()) == 0) {
            moniker = entry.moniker;
            moniker->AddRef();
            break;
        }
    }
    DshowReleaseMonikers(&entries);
    if (moniker == nullptr) {
        if (!capture->useDshow) {
            return false;
        }
        capture->error = "directshow device not found";
        return true;
    }

    IBaseFilter *sourceFilter = nullptr;
    IPin *sourcePin = nullptr;
    IGraphBuilder *graph = nullptr;
    IMediaControl *control = nullptr;
    DshowSinkFilter *sink = nullptr;
    AM_MEDIA_TYPE *chosen = nullptr;
    IAMStreamConfig *config = nullptr;
    DshowFrameContext ctx = {};
    H264EncoderContext encoder = {};
    bool ctxReady = false;
    bool encoderReady = false;
    bool controlRunning = false;

    do {
        hr = moniker->BindToObject(nullptr, nullptr, IID_IBaseFilter, reinterpret_cast<void **>(&sourceFilter));
        if (FAILED(hr) || sourceFilter == nullptr) {
            capture->error = WideToUtf8String(CaptureErrorMessage(FAILED(hr) ? hr : E_FAIL, "bind directshow source filter"));
            break;
        }
        sourcePin = DshowFindCapturePin(sourceFilter);
        if (sourcePin == nullptr) {
            capture->error = "directshow source exposes no output pin";
            break;
        }

        InitializeCriticalSection(&ctx.lock);
        ctx.capture = capture;
        ctx.eosEvent = CreateEventW(nullptr, TRUE, FALSE, nullptr);
        ctxReady = true;
        if (ctx.eosEvent == nullptr) {
            capture->error = "create directshow stream event failed";
            break;
        }

        hr = CoCreateInstance(CLSID_FilterGraph, nullptr, CLSCTX_INPROC_SERVER, IID_IGraphBuilder, reinterpret_cast<void **>(&graph));
        if (FAILED(hr) || graph == nullptr) {
            capture->error = WideToUtf8String(CaptureErrorMessage(FAILED(hr) ? hr : E_FAIL, "create directshow graph"));
            break;
        }
        hr = graph->AddFilter(sourceFilter, L"source");
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "add directshow source filter"));
            break;
        }
        sink = new DshowSinkFilter(&ctx);
        hr = graph->AddFilter(sink, L"webrtpSink");
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "add directshow sink filter"));
            break;
        }

        HRESULT chooseHr = DshowChooseCaptureFormat(sourcePin, capture->width, capture->height, capture->fps, &chosen, &config);
        if (SUCCEEDED(chooseHr) && config != nullptr && chosen != nullptr) {
            // * a failed set format is not fatal, the connect chain below decides
            config->SetFormat(chosen);
        }
        IPin *sinkPin = static_cast<IPin *>(sink->SinkPin());
        hr = E_FAIL;
        if (chosen != nullptr) {
            hr = graph->ConnectDirect(sourcePin, sinkPin, chosen);
        }
        if (FAILED(hr)) {
            hr = graph->ConnectDirect(sourcePin, sinkPin, nullptr);
        }
        if (FAILED(hr)) {
            hr = graph->Connect(sourcePin, sinkPin);
        }
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "connect directshow source, no supported raw format (nv12, i420, yv12, yuy2) was negotiated, check that the virtual camera is started"));
            break;
        }

        const AM_MEDIA_TYPE *connectedType = sink->SinkPin()->ConnectedType();
        UINT32 width = 0;
        UINT32 height = 0;
        REFERENCE_TIME avgTimePerFrame = 0;
        if (connectedType == nullptr || !DshowMediaTypeGeometry(connectedType, &width, &height, &avgTimePerFrame) || width == 0 || height == 0) {
            capture->error = "read negotiated directshow media type failed";
            break;
        }
        UINT32 fpsNum = 30;
        UINT32 fpsDen = 1;
        if (avgTimePerFrame > 0) {
            if (FAILED(MFAverageTimePerFrameToFrameRate(static_cast<UINT64>(avgTimePerFrame), &fpsNum, &fpsDen)) || fpsNum == 0) {
                fpsNum = 30;
                fpsDen = 1;
            }
        } else if (capture->fps > 0) {
            fpsNum = static_cast<UINT32>(capture->fps * 1000.0 + 0.5);
            fpsDen = 1000;
        }
        GUID inputSubtype = connectedType->subtype;
        if (GuidEqual(inputSubtype, DshowFourccSubtype(MAKEFOURCC('I', '4', '2', '0')))) {
            // * identical memory layout, and the encoder documents iyuv rather than i420
            inputSubtype = MFVideoFormat_IYUV;
        }
        UINT32 bitrate = capture->bitrateKbps > 0 ? static_cast<UINT32>(capture->bitrateKbps) * 1000U : 0U;
        const UINT32 profile = H264ProfileValue(capture->h264Profile);
        hr = CreateH264Encoder(width, height, fpsNum, fpsDen, bitrate, profile, inputSubtype, &encoder);
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "create h264 encoder"));
            break;
        }
        encoderReady = true;
        ctx.encoder = &encoder;
        ctx.frameDuration = avgTimePerFrame > 0 ? avgTimePerFrame : (fpsNum > 0 ? static_cast<LONGLONG>((10000000.0 * fpsDen) / fpsNum) : 333333);
        ctx.frameIndex = 0;
        ctx.running = true;

        hr = graph->QueryInterface(IID_IMediaControl, reinterpret_cast<void **>(&control));
        if (FAILED(hr) || control == nullptr) {
            capture->error = WideToUtf8String(CaptureErrorMessage(FAILED(hr) ? hr : E_FAIL, "query directshow media control"));
            break;
        }
        hr = control->Run();
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(CaptureErrorMessage(hr, "run directshow graph"));
            break;
        }
        controlRunning = true;

        capture->started = true;
        SetEvent(capture->readyEvent);

        HANDLE waits[2] = {capture->stopEvent, ctx.eosEvent};
        DWORD waitResult = WaitForMultipleObjects(2, waits, FALSE, INFINITE);
        if (waitResult == WAIT_OBJECT_0 + 1) {
            WebrtpUsbWinError(capture->handle, StringDup(capture->error.empty() ? std::string("directshow stream ended") : capture->error));
        }
    } while (false);

    // * flag the streaming thread off before stopping the graph, and never stop while holding the lock
    if (ctxReady) {
        EnterCriticalSection(&ctx.lock);
        ctx.running = false;
        LeaveCriticalSection(&ctx.lock);
    }
    if (control != nullptr && controlRunning) {
        control->Stop();
    }
    SafeRelease(&control);
    if (encoderReady) {
        CloseH264Encoder(&encoder);
    }
    if (chosen != nullptr) {
        DshowFreeMediaType(chosen);
    }
    SafeRelease(&config);
    SafeRelease(&graph);
    if (sink != nullptr) {
        sink->Release();
        sink = nullptr;
    }
    SafeRelease(&sourcePin);
    SafeRelease(&sourceFilter);
    SafeRelease(&moniker);
    if (ctxReady) {
        if (ctx.eosEvent != nullptr) {
            CloseHandle(ctx.eosEvent);
        }
        DeleteCriticalSection(&ctx.lock);
    }
    return true;
}

}  // namespace

extern "C" char *WebrtpUsbWinDshowDeviceList(char **errOut) {
    HRESULT initHr = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    bool uninitialize = SUCCEEDED(initHr);
    std::vector<DshowMonikerEntry> entries;
    HRESULT hr = DshowCollectMonikers(&entries);
    if (FAILED(hr)) {
        DshowReleaseMonikers(&entries);
        if (uninitialize) {
            CoUninitialize();
        }
        if (errOut != nullptr) {
            *errOut = WideToUtf8Dup(CaptureErrorMessage(hr, "list directshow devices"));
        }
        return nullptr;
    }
    std::string result;
    for (auto &entry : entries) {
        if (entry.displayName.empty()) {
            continue;
        }
        char *idUtf8 = WideToUtf8Dup(entry.displayName);
        char *nameUtf8 = WideToUtf8Dup(entry.friendlyName);
        if (idUtf8 != nullptr) {
            if (!result.empty()) {
                result.push_back('\n');
            }
            result.append(idUtf8);
            result.push_back('\t');
            if (nameUtf8 != nullptr) {
                result.append(nameUtf8);
            }
            result.push_back('\t');
            result.append(entry.devicePathReadable ? "1" : "0");
        }
        free(idUtf8);
        free(nameUtf8);
    }
    DshowReleaseMonikers(&entries);
    if (uninitialize) {
        CoUninitialize();
    }
    return StringDup(result);
}

extern "C" char *WebrtpUsbWinDshowDeviceCapabilities(const char *device, char **errOut) {
    HRESULT initHr = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    bool uninitialize = SUCCEEDED(initHr);
    std::wstring needle = Utf8ToWide(device);
    std::vector<DshowMonikerEntry> entries;
    HRESULT hr = DshowCollectMonikers(&entries);
    if (FAILED(hr)) {
        DshowReleaseMonikers(&entries);
        if (uninitialize) {
            CoUninitialize();
        }
        if (errOut != nullptr) {
            *errOut = WideToUtf8Dup(CaptureErrorMessage(hr, "list directshow devices"));
        }
        return nullptr;
    }
    std::string result;
    hr = HRESULT_FROM_WIN32(ERROR_NOT_FOUND);
    for (auto &entry : entries) {
        if (entry.devicePathReadable) {
            continue;
        }
        if (_wcsicmp(entry.displayName.c_str(), needle.c_str()) != 0 && _wcsicmp(entry.friendlyName.c_str(), needle.c_str()) != 0) {
            continue;
        }
        hr = DshowCapabilitiesJson(entry.moniker, entry.displayName, entry.friendlyName, &result);
        break;
    }
    DshowReleaseMonikers(&entries);
    if (uninitialize) {
        CoUninitialize();
    }
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = WideToUtf8Dup(CaptureErrorMessage(hr, "find directshow device"));
        }
        return nullptr;
    }
    return StringDup(result);
}
