#include <mfapi.h>
#include <mfidl.h>
#include <mfreadwrite.h>
#include <mftransform.h>
#include <mferror.h>
#include <propvarutil.h>
#include <wmcodecdsp.h>
#include <codecapi.h>
#include <windows.h>
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
    int width;
    int height;
    double fps;
    int bitrateKbps;
    bool started;
    std::string error;
};

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
};

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

void MergeMode(std::vector<CapabilityModeEntry> *modes, UINT32 width, UINT32 height, double fps) {
    if (modes == nullptr || width == 0 || height == 0) {
        return;
    }
    for (auto &mode : *modes) {
        if (mode.width == width && mode.height == height) {
            AppendUniqueFps(&mode.fps, fps);
            return;
        }
    }
    CapabilityModeEntry mode = {};
    mode.width = width;
    mode.height = height;
    AppendUniqueFps(&mode.fps, fps);
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

HRESULT DeviceCapabilitiesJson(IMFActivate *device, std::string *resultOut) {
    if (device == nullptr || resultOut == nullptr) {
        return E_POINTER;
    }
    IMFMediaSource *source = nullptr;
    IMFSourceReader *reader = nullptr;
    std::vector<CapabilityModeEntry> h264Modes;
    std::vector<CapabilityModeEntry> h265Modes;
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
        if (GuidEqual(subtype, MFVideoFormat_H264)) {
            MergeMode(&h264Modes, width, height, fps);
        } else if (GuidEqual(subtype, MFVideoFormat_HEVC)) {
            MergeMode(&h265Modes, width, height, fps);
        }
        SafeRelease(&mediaType);
    }
    SafeRelease(&reader);
    SafeRelease(&source);
    if (FAILED(hr)) {
        return hr;
    }

    auto sortModes = [](std::vector<CapabilityModeEntry> *modes) {
        if (modes == nullptr) return;
        for (auto &mode : *modes) {
            SortFps(&mode.fps);
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
    };
    sortModes(&h264Modes);
    sortModes(&h265Modes);

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
    }
    sortModes(&mergedModes);

    std::string json = "{";
    json += "\"device\":{\"id\":\"" + JsonEscape(WideToUtf8String(id)) + "\",\"name\":\"" + JsonEscape(WideToUtf8String(name)) + "\"},";
    json += "\"codecs\":[";
    for (size_t i = 0; i < codecs.size(); i++) {
        if (i > 0) json += ",";
        json += "\"" + codecs[i] + "\"";
    }
    json += "],";
    json += "\"bitrateControl\":\"native\",";
    json += "\"modes\":[";
    for (size_t i = 0; i < mergedModes.size(); i++) {
        if (i > 0) json += ",";
        const auto &mode = mergedModes[i];
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
        json += "}";
    }
    json += "]}";
    *resultOut = json;
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

HRESULT CreateH264Encoder(UINT32 width, UINT32 height, UINT32 fpsNum, UINT32 fpsDen, UINT32 bitrate, GUID inputSubtype, H264EncoderContext *ctx) {
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

        MediaTypeSelection selection = {};
        subtype = (_wcsicmp(capture->codec.c_str(), L"h265") == 0) ? MFVideoFormat_HEVC : MFVideoFormat_H264;
        hr = SelectCompressedMediaType(reader, subtype, capture->width, capture->height, capture->fps, &selection);
        if (FAILED(hr)) {
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
            hr = CreateH264Encoder(rawSelection.width, rawSelection.height, rawSelection.fpsNum, rawSelection.fpsDen, bitrate, rawSelection.outputSubtype, &encoder);
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

extern "C" void *WebrtpUsbWinCaptureStart(const char *device, const char *codec, int width, int height, double fps, int bitrateKbps, uintptr_t handle, char **errOut) {
    WinCapture *capture = new WinCapture();
    capture->thread = nullptr;
    capture->stopEvent = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    capture->readyEvent = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    capture->handle = handle;
    capture->device = Utf8ToWide(device);
    capture->codec = Utf8ToWide(codec);
    capture->width = width;
    capture->height = height;
    capture->fps = fps;
    capture->bitrateKbps = bitrateKbps;
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
