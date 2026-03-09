#include <mfapi.h>
#include <mfidl.h>
#include <mfreadwrite.h>
#include <mferror.h>
#include <propvarutil.h>
#include <windows.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

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
    double fps;
    bool started;
    std::string error;
};

struct MediaTypeSelection {
    IMFMediaType *type;
    UINT32 width;
    UINT32 height;
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

HRESULT SelectCompressedMediaType(IMFSourceReader *reader, const GUID &subtype, double fpsHint, MediaTypeSelection *selectionOut) {
    IMFMediaType *best = nullptr;
    UINT32 bestWidth = 0;
    UINT32 bestHeight = 0;
    UINT64 bestPixels = 0;

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
        UINT64 pixels = static_cast<UINT64>(width) * static_cast<UINT64>(height);

        bool better = false;
        if (best == nullptr) {
            better = true;
        } else if (fpsHint > 0 && fps > 0) {
            double bestFrNum = 0;
            UINT32 oldNum = 0;
            UINT32 oldDen = 0;
            if (SUCCEEDED(MFGetAttributeRatio(best, MF_MT_FRAME_RATE, &oldNum, &oldDen)) && oldDen != 0) {
                bestFrNum = static_cast<double>(oldNum) / static_cast<double>(oldDen);
            }
            double curScore = fabs(fps - fpsHint);
            double bestScore = bestFrNum > 0 ? fabs(bestFrNum - fpsHint) : 1000000.0;
            if (curScore < bestScore || (curScore == bestScore && pixels > bestPixels)) {
                better = true;
            }
        } else if (pixels > bestPixels) {
            better = true;
        }

        if (better) {
            SafeRelease(&best);
            best = mediaType;
            bestWidth = width;
            bestHeight = height;
            bestPixels = pixels;
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
        hr = SelectCompressedMediaType(reader, subtype, capture->fps, &selection);
        if (FAILED(hr)) {
            capture->error = WideToUtf8String(L"device does not expose native " + capture->codec + L" output");
            break;
        }

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
                    UINT32 cleanPoint = 0;
                    bool isKeyFrame = sample->GetUINT32(MFSampleExtension_CleanPoint, &cleanPoint) == S_OK && cleanPoint != 0;
                    std::vector<uint8_t> annexb = ToAnnexB(raw, curLen, nalLengthSize, codecConfig, isKeyFrame);
                    if (!annexb.empty()) {
                        uint32_t pts90k = static_cast<uint32_t>((sampleTime * 9) / 1000);
                        WebrtpUsbWinPacket(capture->handle, annexb.data(), static_cast<int>(annexb.size()), pts90k);
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
    if (source != nullptr) {
        source->Shutdown();
    }
    SafeRelease(&source);
    SafeRelease(&device);
    MfShutdownScoped();
    return 0;
}

}  // namespace

extern "C" void *WebrtpUsbWinCaptureStart(const char *device, const char *codec, double fps, uintptr_t handle, char **errOut) {
    WinCapture *capture = new WinCapture();
    capture->thread = nullptr;
    capture->stopEvent = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    capture->readyEvent = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    capture->handle = handle;
    capture->device = Utf8ToWide(device);
    capture->codec = Utf8ToWide(codec);
    capture->fps = fps;
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
