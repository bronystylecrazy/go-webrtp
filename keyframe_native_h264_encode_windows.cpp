#include <mfapi.h>
#include <mfidl.h>
#include <mftransform.h>
#include <mferror.h>
#include <wmcodecdsp.h>
#include <codecapi.h>
#include <windows.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <string>
#include <vector>
#include <stdio.h>

#include "windows_d3d11_mf_shared.h"

namespace {

struct MFH264FrameEncoder {
    IMFTransform *transform;
    IMFMediaType *inputType;
    IMFMediaType *outputType;
    ICodecAPI *codecApi;
    IMFSample *inputSample;
    IMFMediaBuffer *inputBuffer;
    DWORD inputBufferSize;
    IMFSample *outputSample;
    IMFMediaBuffer *outputBuffer;
    DWORD outputBufferSize;
    UINT32 width;
    UINT32 height;
    UINT32 bitrate;
    GUID inputSubtype;
    std::vector<std::vector<uint8_t>> codecConfig;
    uint32_t nalLengthSize;
    bool streamingBegun;
    MFD3D11Resources *d3d11;
};

char *StringDup(const std::string &src) {
    char *dst = static_cast<char *>(malloc(src.size() + 1));
    if (dst != nullptr) {
        memcpy(dst, src.c_str(), src.size() + 1);
    }
    return dst;
}

void SetError(char **errOut, const std::string &msg) {
    if (errOut != nullptr) {
        *errOut = StringDup(msg);
    }
}

std::string HrString(const char *step, HRESULT hr) {
    char buf[128];
    snprintf(buf, sizeof(buf), "%s (hr=0x%08lx)", step, static_cast<unsigned long>(hr));
    return std::string(buf);
}

template <typename T>
void SafeRelease(T **ptr) {
    if (ptr != nullptr && *ptr != nullptr) {
        (*ptr)->Release();
        *ptr = nullptr;
    }
}

HRESULT StartupMF() {
    HRESULT hr = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    if (FAILED(hr) && hr != RPC_E_CHANGED_MODE) {
        return hr;
    }
    hr = MFStartup(MF_VERSION, MFSTARTUP_LITE);
    if (FAILED(hr) && hr != MF_E_ALREADY_INITIALIZED) {
        CoUninitialize();
        return hr;
    }
    return S_OK;
}

void ShutdownMF() {
    MFShutdown();
    CoUninitialize();
}

UINT32 DefaultBitrate(UINT32 width, UINT32 height) {
    double bits = static_cast<double>(width) * static_cast<double>(height) * 0.12;
    if (bits < 500000.0) bits = 500000.0;
    if (bits > 12000000.0) bits = 12000000.0;
    return static_cast<UINT32>(bits);
}

inline uint8_t ClampByte(int v) {
    if (v < 0) return 0;
    if (v > 255) return 255;
    return static_cast<uint8_t>(v);
}

void RGBAtoNV12(const uint8_t *src, int width, int height, int stride, uint8_t *dstY, uint8_t *dstUV, int yStride, int uvStride) {
    for (int y = 0; y < height; y += 2) {
        const uint8_t *row0 = src + static_cast<size_t>(y) * stride;
        const uint8_t *row1 = src + static_cast<size_t>(std::min(y + 1, height - 1)) * stride;
        uint8_t *y0 = dstY + static_cast<size_t>(y) * yStride;
        uint8_t *y1 = dstY + static_cast<size_t>(std::min(y + 1, height - 1)) * yStride;
        uint8_t *uv = dstUV + static_cast<size_t>(y / 2) * uvStride;
        for (int x = 0; x < width; x += 2) {
            int uAcc = 0;
            int vAcc = 0;
            for (int dy = 0; dy < 2; dy++) {
                const uint8_t *row = dy == 0 ? row0 : row1;
                uint8_t *yRow = dy == 0 ? y0 : y1;
                for (int dx = 0; dx < 2; dx++) {
                    int px = std::min(x + dx, width - 1);
                    const uint8_t *pix = row + static_cast<size_t>(px) * 4;
                    int r = pix[0];
                    int g = pix[1];
                    int b = pix[2];
                    int yv = ((66 * r + 129 * g + 25 * b + 128) >> 8) + 16;
                    int uvv = ((-38 * r - 74 * g + 112 * b + 128) >> 8) + 128;
                    int vv = ((112 * r - 94 * g - 18 * b + 128) >> 8) + 128;
                    yRow[px] = ClampByte(yv);
                    uAcc += uvv;
                    vAcc += vv;
                }
            }
            uv[x] = ClampByte((uAcc + 2) / 4);
            if (x + 1 < uvStride) {
                uv[x+1] = ClampByte((vAcc + 2) / 4);
            }
        }
    }
}

HRESULT LoadCodecConfig(IMFMediaType *mediaType, std::vector<std::vector<uint8_t>> *codecConfig, uint32_t *nalLengthSize) {
    if (codecConfig == nullptr || nalLengthSize == nullptr) {
        return E_POINTER;
    }
    codecConfig->clear();
    *nalLengthSize = 4;
    UINT32 blobSize = 0;
    HRESULT hr = mediaType->GetBlobSize(MF_MT_MPEG_SEQUENCE_HEADER, &blobSize);
    if (FAILED(hr) || blobSize == 0) {
        return FAILED(hr) ? hr : MF_E_ATTRIBUTENOTFOUND;
    }
    std::vector<uint8_t> blob(blobSize);
    hr = mediaType->GetBlob(MF_MT_MPEG_SEQUENCE_HEADER, blob.data(), blobSize, &blobSize);
    if (FAILED(hr) || blobSize < 7) {
        return FAILED(hr) ? hr : E_FAIL;
    }
    *nalLengthSize = (blob[4] & 0x03) + 1;
    size_t offset = 5;
    const uint8_t spsCount = blob[offset++] & 0x1F;
    for (uint8_t i = 0; i < spsCount && offset + 2 <= blob.size(); i++) {
        uint16_t size = static_cast<uint16_t>(blob[offset] << 8 | blob[offset + 1]);
        offset += 2;
        if (offset + size > blob.size()) {
            return E_FAIL;
        }
        codecConfig->push_back(std::vector<uint8_t>(blob.begin() + static_cast<long>(offset), blob.begin() + static_cast<long>(offset + size)));
        offset += size;
    }
    if (offset >= blob.size()) {
        return E_FAIL;
    }
    const uint8_t ppsCount = blob[offset++];
    for (uint8_t i = 0; i < ppsCount && offset + 2 <= blob.size(); i++) {
        uint16_t size = static_cast<uint16_t>(blob[offset] << 8 | blob[offset + 1]);
        offset += 2;
        if (offset + size > blob.size()) {
            return E_FAIL;
        }
        codecConfig->push_back(std::vector<uint8_t>(blob.begin() + static_cast<long>(offset), blob.begin() + static_cast<long>(offset + size)));
        offset += size;
    }
    return codecConfig->empty() ? E_FAIL : S_OK;
}

std::vector<uint8_t> ToAnnexB(const uint8_t *data, size_t size, uint32_t nalLengthSize, const std::vector<std::vector<uint8_t>> &codecConfig) {
    std::vector<uint8_t> out;
    static const uint8_t startCode[] = {0, 0, 0, 1};
    for (const auto &nalu : codecConfig) {
        out.insert(out.end(), startCode, startCode + sizeof(startCode));
        out.insert(out.end(), nalu.begin(), nalu.end());
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

HRESULT EnsureConfigured(MFH264FrameEncoder *enc, UINT32 width, UINT32 height, std::string *stepOut) {
    if (enc == nullptr) {
        return E_POINTER;
    }
    if (stepOut != nullptr) {
        stepOut->clear();
    }
    if (enc->inputType != nullptr && enc->outputType != nullptr && enc->width == width && enc->height == height) {
        return S_OK;
    }
    if (enc->transform != nullptr && enc->streamingBegun) {
        enc->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
        enc->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
        enc->transform->ProcessMessage(MFT_MESSAGE_COMMAND_FLUSH, 0);
        enc->streamingBegun = false;
    }
    SafeRelease(&enc->inputBuffer);
    SafeRelease(&enc->inputSample);
    enc->inputBufferSize = 0;
    SafeRelease(&enc->outputBuffer);
    SafeRelease(&enc->outputSample);
    enc->outputBufferSize = 0;
    SafeRelease(&enc->inputType);
    SafeRelease(&enc->outputType);
    enc->codecConfig.clear();
    enc->nalLengthSize = 4;

    enc->width = width;
    enc->height = height;
    enc->bitrate = DefaultBitrate(width, height);
    enc->inputSubtype = GUID_NULL;

    IMFMediaType *outputType = nullptr;
    HRESULT hr = MFCreateMediaType(&outputType);
    if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "output SetGUID major";
    if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_H264);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "output SetGUID subtype h264";
    if (SUCCEEDED(hr)) hr = MFSetAttributeSize(outputType, MF_MT_FRAME_SIZE, width, height);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "output SetAttributeSize frame size";
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(outputType, MF_MT_FRAME_RATE, 30, 1);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "output SetAttributeRatio frame rate";
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(outputType, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "output SetAttributeRatio pixel aspect";
    if (SUCCEEDED(hr)) hr = outputType->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "output SetUINT32 interlace";
    if (SUCCEEDED(hr)) hr = outputType->SetUINT32(MF_MT_AVG_BITRATE, enc->bitrate);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "output SetUINT32 bitrate";
    if (SUCCEEDED(hr)) hr = enc->transform->SetOutputType(0, outputType, 0);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "transform SetOutputType h264";
    if (FAILED(hr)) {
        SafeRelease(&outputType);
        return hr;
    }
    enc->outputType = outputType;

    IMFMediaType *inputType = nullptr;
    hr = MFCreateMediaType(&inputType);
    if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "input SetGUID major";
    if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_ARGB32);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "input SetGUID subtype argb32";
    if (SUCCEEDED(hr)) hr = MFSetAttributeSize(inputType, MF_MT_FRAME_SIZE, width, height);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "input SetAttributeSize frame size";
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(inputType, MF_MT_FRAME_RATE, 30, 1);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "input SetAttributeRatio frame rate";
    if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(inputType, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "input SetAttributeRatio pixel aspect";
    if (SUCCEEDED(hr)) hr = inputType->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "input SetUINT32 interlace";
    if (SUCCEEDED(hr)) hr = enc->transform->SetInputType(0, inputType, 0);
    if (FAILED(hr) && stepOut != nullptr) *stepOut = "transform SetInputType argb32";
    if (FAILED(hr)) {
        SafeRelease(&inputType);
        inputType = nullptr;
        hr = MFCreateMediaType(&inputType);
        if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback SetGUID major";
        if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_RGB32);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback SetGUID subtype rgb32";
        if (SUCCEEDED(hr)) hr = MFSetAttributeSize(inputType, MF_MT_FRAME_SIZE, width, height);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback SetAttributeSize frame size";
        if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(inputType, MF_MT_FRAME_RATE, 30, 1);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback SetAttributeRatio frame rate";
        if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(inputType, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback SetAttributeRatio pixel aspect";
        if (SUCCEEDED(hr)) hr = inputType->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback SetUINT32 interlace";
        if (SUCCEEDED(hr)) hr = enc->transform->SetInputType(0, inputType, 0);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "transform SetInputType rgb32";
    }
    if (FAILED(hr)) {
        SafeRelease(&inputType);
        inputType = nullptr;
        hr = MFCreateMediaType(&inputType);
        if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback2 SetGUID major";
        if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_NV12);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback2 SetGUID subtype nv12";
        if (SUCCEEDED(hr)) hr = MFSetAttributeSize(inputType, MF_MT_FRAME_SIZE, width, height);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback2 SetAttributeSize frame size";
        if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(inputType, MF_MT_FRAME_RATE, 30, 1);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback2 SetAttributeRatio frame rate";
        if (SUCCEEDED(hr)) hr = MFSetAttributeRatio(inputType, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback2 SetAttributeRatio pixel aspect";
        if (SUCCEEDED(hr)) hr = inputType->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "input fallback2 SetUINT32 interlace";
        if (SUCCEEDED(hr)) hr = enc->transform->SetInputType(0, inputType, 0);
        if (FAILED(hr) && stepOut != nullptr) *stepOut = "transform SetInputType nv12";
    }
    if (FAILED(hr)) {
        SafeRelease(&inputType);
        return hr;
    }
    enc->inputType = inputType;
    inputType->GetGUID(MF_MT_SUBTYPE, &enc->inputSubtype);

    if (enc->codecApi != nullptr) {
        VARIANT value;
        VariantInit(&value);
        value.vt = VT_UI4;
        value.ulVal = eAVEncCommonRateControlMode_CBR;
        enc->codecApi->SetValue(&CODECAPI_AVEncCommonRateControlMode, &value);
        value.ulVal = enc->bitrate;
        enc->codecApi->SetValue(&CODECAPI_AVEncCommonMeanBitRate, &value);
        value.ulVal = 1;
        enc->codecApi->SetValue(&CODECAPI_AVEncMPVGOPSize, &value);
        value.ulVal = 0;
        enc->codecApi->SetValue(&CODECAPI_AVEncMPVDefaultBPictureCount, &value);
        value.ulVal = 1;
        enc->codecApi->SetValue(&CODECAPI_AVLowLatencyMode, &value);
        VariantClear(&value);
    }

    hr = LoadCodecConfig(enc->outputType, &enc->codecConfig, &enc->nalLengthSize);
    if (FAILED(hr)) {
        enc->codecConfig.clear();
        enc->nalLengthSize = 4;
    }

    hr = enc->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
    if (SUCCEEDED(hr)) hr = enc->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
    if (SUCCEEDED(hr)) enc->streamingBegun = true;
    return hr;
}

HRESULT EnsureInputSample(MFH264FrameEncoder *enc) {
    if (enc == nullptr) return E_POINTER;
    DWORD needed = enc->width * enc->height * 4;
    if (IsEqualGUID(enc->inputSubtype, MFVideoFormat_NV12)) {
        needed = enc->width * enc->height * 3 / 2;
    }
    if (enc->inputSample != nullptr && enc->inputBuffer != nullptr && enc->inputBufferSize >= needed) {
        return S_OK;
    }
    SafeRelease(&enc->inputBuffer);
    SafeRelease(&enc->inputSample);
    enc->inputBufferSize = 0;
    HRESULT hr = MFCreateSample(&enc->inputSample);
    if (FAILED(hr)) return hr;
    hr = MFCreateMemoryBuffer(needed, &enc->inputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&enc->inputSample);
        return hr;
    }
    hr = enc->inputSample->AddBuffer(enc->inputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&enc->inputBuffer);
        SafeRelease(&enc->inputSample);
        return hr;
    }
    enc->inputBufferSize = needed;
    return S_OK;
}

HRESULT EnsureOutputSample(MFH264FrameEncoder *enc) {
    if (enc == nullptr) return E_POINTER;
    MFT_OUTPUT_STREAM_INFO streamInfo = {};
    HRESULT hr = enc->transform->GetOutputStreamInfo(0, &streamInfo);
    if (FAILED(hr)) return hr;
    DWORD needed = streamInfo.cbSize > 0 ? streamInfo.cbSize : 1024 * 1024;
    if (enc->outputSample != nullptr && enc->outputBuffer != nullptr && enc->outputBufferSize >= needed) {
        return S_OK;
    }
    SafeRelease(&enc->outputBuffer);
    SafeRelease(&enc->outputSample);
    enc->outputBufferSize = 0;
    hr = MFCreateSample(&enc->outputSample);
    if (FAILED(hr)) return hr;
    hr = MFCreateMemoryBuffer(needed, &enc->outputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&enc->outputSample);
        return hr;
    }
    hr = enc->outputSample->AddBuffer(enc->outputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&enc->outputBuffer);
        SafeRelease(&enc->outputSample);
        return hr;
    }
    enc->outputBufferSize = needed;
    return S_OK;
}

} // namespace

extern "C" void *WebrtpMFH264EncoderCreate(char **errOut) {
    HRESULT hr = StartupMF();
    if (FAILED(hr)) {
        SetError(errOut, "Media Foundation startup failed");
        return nullptr;
    }
    IMFTransform *transform = nullptr;
    hr = CoCreateInstance(CLSID_CMSH264EncoderMFT, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(&transform));
    if (FAILED(hr) || transform == nullptr) {
        ShutdownMF();
        SetError(errOut, "create H264 encoder MFT failed");
        return nullptr;
    }
    MFH264FrameEncoder *enc = new MFH264FrameEncoder();
    enc->transform = nullptr;
    enc->inputType = nullptr;
    enc->outputType = nullptr;
    enc->codecApi = nullptr;
    enc->inputSample = nullptr;
    enc->inputBuffer = nullptr;
    enc->inputBufferSize = 0;
    enc->outputSample = nullptr;
    enc->outputBuffer = nullptr;
    enc->outputBufferSize = 0;
    enc->width = 0;
    enc->height = 0;
    enc->bitrate = 0;
    enc->inputSubtype = GUID_NULL;
    enc->nalLengthSize = 4;
    enc->streamingBegun = false;
    enc->d3d11 = nullptr;
    enc->transform = transform;
    std::string d3dErr;
    if (SUCCEEDED(AcquireMFD3D11Resources(&enc->d3d11, &d3dErr)) && enc->d3d11 != nullptr) {
        BindTransformToD3D11(transform, enc->d3d11);
    }
    transform->QueryInterface(IID_ICodecAPI, reinterpret_cast<void **>(&enc->codecApi));
    IMFAttributes *attrs = nullptr;
    if (SUCCEEDED(transform->GetAttributes(&attrs)) && attrs != nullptr) {
        attrs->SetUINT32(MF_LOW_LATENCY, TRUE);
        attrs->Release();
    }
    return enc;
}

extern "C" void WebrtpMFH264EncoderClose(void *ref) {
    if (ref == nullptr) return;
    MFH264FrameEncoder *enc = static_cast<MFH264FrameEncoder *>(ref);
    ReleaseMFD3D11Resources(enc->d3d11);
    enc->d3d11 = nullptr;
    if (enc->transform != nullptr && enc->streamingBegun) {
        enc->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
        enc->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
        enc->transform->ProcessMessage(MFT_MESSAGE_COMMAND_FLUSH, 0);
    }
    SafeRelease(&enc->outputBuffer);
    SafeRelease(&enc->outputSample);
    SafeRelease(&enc->inputBuffer);
    SafeRelease(&enc->inputSample);
    SafeRelease(&enc->codecApi);
    SafeRelease(&enc->outputType);
    SafeRelease(&enc->inputType);
    SafeRelease(&enc->transform);
    delete enc;
    ShutdownMF();
}

extern "C" int WebrtpMFH264EncoderEncodeRGBA(void *ref, const void *rgba, int width, int height, int stride, void **outData, int *outLen, char **errOut) {
    if (outData != nullptr) *outData = nullptr;
    if (outLen != nullptr) *outLen = 0;
    if (ref == nullptr || rgba == nullptr || width <= 0 || height <= 0 || stride < width * 4) {
        SetError(errOut, "invalid Media Foundation encode input");
        return 0;
    }
    MFH264FrameEncoder *enc = static_cast<MFH264FrameEncoder *>(ref);
    std::string configStep;
    HRESULT hr = EnsureConfigured(enc, static_cast<UINT32>(width), static_cast<UINT32>(height), &configStep);
    if (FAILED(hr)) {
        SetError(errOut, HrString(("configure Media Foundation encoder failed at " + configStep).c_str(), hr));
        return 0;
    }
    hr = EnsureInputSample(enc);
    if (FAILED(hr)) {
        SetError(errOut, HrString("create Media Foundation input sample failed", hr));
        return 0;
    }
    BYTE *dst = nullptr;
    DWORD maxLen = 0;
    hr = enc->inputBuffer->Lock(&dst, &maxLen, nullptr);
    if (FAILED(hr)) {
        SetError(errOut, HrString("lock Media Foundation input buffer failed", hr));
        return 0;
    }
    const BYTE *srcBase = static_cast<const BYTE *>(rgba);
    DWORD currentLen = 0;
    if (IsEqualGUID(enc->inputSubtype, MFVideoFormat_NV12)) {
        uint8_t *yPlane = dst;
        uint8_t *uvPlane = dst + static_cast<size_t>(width) * height;
        RGBAtoNV12(srcBase, width, height, stride, yPlane, uvPlane, width, width);
        currentLen = static_cast<DWORD>(width * height * 3 / 2);
    } else {
        const UINT32 rowBytes = static_cast<UINT32>(width) * 4;
        for (int y = 0; y < height; y++) {
            const BYTE *srcRow = srcBase + static_cast<size_t>(y) * stride;
            BYTE *dstRow = dst + static_cast<size_t>(y) * rowBytes;
            for (int x = 0; x < width; x++) {
                const BYTE *srcPix = srcRow + static_cast<size_t>(x) * 4;
                BYTE *dstPix = dstRow + static_cast<size_t>(x) * 4;
                dstPix[0] = srcPix[2];
                dstPix[1] = srcPix[1];
                dstPix[2] = srcPix[0];
                dstPix[3] = srcPix[3];
            }
        }
        currentLen = rowBytes * static_cast<DWORD>(height);
    }
    enc->inputBuffer->Unlock();
    enc->inputBuffer->SetCurrentLength(currentLen);
    enc->inputSample->SetSampleTime(0);
    enc->inputSample->SetSampleDuration(10 * 1000 * 1000);

    if (enc->codecApi != nullptr) {
        VARIANT value;
        VariantInit(&value);
        value.vt = VT_UI4;
        value.ulVal = 1;
        enc->codecApi->SetValue(&CODECAPI_AVEncVideoForceKeyFrame, &value);
        VariantClear(&value);
    }

    hr = enc->transform->ProcessInput(0, enc->inputSample, 0);
    if (FAILED(hr)) {
        SetError(errOut, HrString("Media Foundation encoder ProcessInput failed", hr));
        return 0;
    }
    hr = EnsureOutputSample(enc);
    if (FAILED(hr)) {
        SetError(errOut, HrString("create Media Foundation output sample failed", hr));
        return 0;
    }
    enc->outputBuffer->SetCurrentLength(0);
    MFT_OUTPUT_DATA_BUFFER output = {};
    output.dwStreamID = 0;
    output.pSample = enc->outputSample;
    DWORD status = 0;
    hr = enc->transform->ProcessOutput(0, 1, &output, &status);
    if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) {
        SetError(errOut, HrString("Media Foundation encoder needs more input", hr));
        return 0;
    }
    if (FAILED(hr)) {
        SetError(errOut, HrString("Media Foundation encoder ProcessOutput failed", hr));
        return 0;
    }
    IMFMediaBuffer *buffer = nullptr;
    hr = enc->outputSample->ConvertToContiguousBuffer(&buffer);
    if (FAILED(hr) || buffer == nullptr) {
        SafeRelease(&buffer);
        SetError(errOut, HrString("read Media Foundation output buffer failed", FAILED(hr) ? hr : E_FAIL));
        return 0;
    }
    BYTE *raw = nullptr;
    DWORD outputMaxLen = 0;
    DWORD curLen = 0;
    hr = buffer->Lock(&raw, &outputMaxLen, &curLen);
    if (FAILED(hr) || raw == nullptr || curLen == 0) {
        if (SUCCEEDED(hr)) {
            buffer->Unlock();
        }
        SafeRelease(&buffer);
        SetError(errOut, HrString("lock Media Foundation output buffer failed", FAILED(hr) ? hr : E_FAIL));
        return 0;
    }
    std::vector<uint8_t> annexb = ToAnnexB(raw, curLen, enc->nalLengthSize, enc->codecConfig);
    buffer->Unlock();
    SafeRelease(&buffer);
    if (annexb.empty()) {
        SetError(errOut, "Media Foundation encoder produced empty frame");
        return 0;
    }
    void *copy = malloc(annexb.size());
    if (copy == nullptr) {
        SetError(errOut, "allocate h264 output failed");
        return 0;
    }
    memcpy(copy, annexb.data(), annexb.size());
    if (outData != nullptr) *outData = copy;
    if (outLen != nullptr) *outLen = static_cast<int>(annexb.size());
    return 1;
}

extern "C" void WebrtpMFH264EncoderFreeBuffer(void *ptr) {
    if (ptr != nullptr) {
        free(ptr);
    }
}
