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

#include "windows_d3d11_mf_shared.h"
#include <mfobjects.h>

namespace {

struct MFH264Decoder {
    IMFTransform *transform;
    IMFMediaType *inputType;
    IMFMediaType *outputType;
    IMFSample *inputSample;
    IMFMediaBuffer *inputBuffer;
    DWORD inputBufferSize;
    IMFSample *outputSample;
    IMFMediaBuffer *outputBuffer;
    DWORD outputBufferSize;
    std::string sps;
    std::string pps;
    bool streamingBegun;
    MFD3D11Resources *d3d11;
    bool lastOutputUsedDXGI;
    GUID lastOutputSubtypeGUID;
    std::string lastOutputSubtype;
    std::string configuredOutputSubtype;
    std::string lastAccessPath;
    std::string lastConversionPath;
    UINT32 lastOutputWidth;
    UINT32 lastOutputHeight;
    LONG lastOutputStride;
};

char *StringDup(const std::string &src) {
    char *dst = static_cast<char *>(malloc(src.size() + 1));
    if (dst == nullptr) {
        return nullptr;
    }
    memcpy(dst, src.c_str(), src.size() + 1);
    return dst;
}

void SetError(char **errOut, const std::string &msg) {
    if (errOut != nullptr) {
        *errOut = StringDup(msg);
    }
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
    if (FAILED(hr)) {
        if (hr != MF_E_ALREADY_INITIALIZED) {
            CoUninitialize();
        }
        return hr;
    }
    return S_OK;
}

void ShutdownMF() {
    MFShutdown();
    CoUninitialize();
}

std::string GuidName(const GUID &guid) {
    if (guid == MFVideoFormat_ARGB32) return "ARGB32";
    if (guid == MFVideoFormat_RGB32) return "RGB32";
    if (guid == MFVideoFormat_NV12) return "NV12";
    if (guid == MFVideoFormat_H264) return "H264";
    char buf[64];
    snprintf(buf, sizeof(buf), "{%08lx-%04x-%04x}", static_cast<unsigned long>(guid.Data1), guid.Data2, guid.Data3);
    return std::string(buf);
}

inline uint8_t ClampByte(int v) {
    if (v < 0) return 0;
    if (v > 255) return 255;
    return static_cast<uint8_t>(v);
}

std::string MakeAvcc(const uint8_t *sps, int spsLen, const uint8_t *pps, int ppsLen) {
    if (sps == nullptr || pps == nullptr || spsLen < 4 || ppsLen < 1) {
        return std::string();
    }
    std::string out;
    out.reserve(static_cast<size_t>(11 + spsLen + ppsLen));
    out.push_back(1);
    out.push_back(static_cast<char>(sps[1]));
    out.push_back(static_cast<char>(sps[2]));
    out.push_back(static_cast<char>(sps[3]));
    out.push_back(static_cast<char>(0xFF));
    out.push_back(static_cast<char>(0xE1));
    out.push_back(static_cast<char>((spsLen >> 8) & 0xFF));
    out.push_back(static_cast<char>(spsLen & 0xFF));
    out.append(reinterpret_cast<const char *>(sps), static_cast<size_t>(spsLen));
    out.push_back(1);
    out.push_back(static_cast<char>((ppsLen >> 8) & 0xFF));
    out.push_back(static_cast<char>(ppsLen & 0xFF));
    out.append(reinterpret_cast<const char *>(pps), static_cast<size_t>(ppsLen));
    return out;
}

HRESULT ConfigureDecoder(MFH264Decoder *decoder, const uint8_t *sps, int spsLen, const uint8_t *pps, int ppsLen) {
    if (decoder == nullptr) {
        return E_POINTER;
    }
    std::string nextSPS(reinterpret_cast<const char *>(sps), static_cast<size_t>(spsLen));
    std::string nextPPS(reinterpret_cast<const char *>(pps), static_cast<size_t>(ppsLen));
    if (decoder->inputType != nullptr && decoder->outputType != nullptr &&
        decoder->sps == nextSPS && decoder->pps == nextPPS) {
        return S_OK;
    }

    decoder->transform->ProcessMessage(MFT_MESSAGE_COMMAND_FLUSH, 0);
    decoder->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
    decoder->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
    decoder->streamingBegun = false;
    SafeRelease(&decoder->outputBuffer);
    SafeRelease(&decoder->outputSample);
    decoder->outputBufferSize = 0;
    SafeRelease(&decoder->inputType);
    SafeRelease(&decoder->outputType);

    IMFMediaType *inputType = nullptr;
    HRESULT hr = MFCreateMediaType(&inputType);
    if (FAILED(hr)) {
        return hr;
    }
    hr = inputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    if (SUCCEEDED(hr)) hr = inputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_H264);
    if (SUCCEEDED(hr)) {
        std::string avcc = MakeAvcc(sps, spsLen, pps, ppsLen);
        if (avcc.empty()) {
            hr = E_INVALIDARG;
        } else {
            hr = inputType->SetBlob(MF_MT_MPEG_SEQUENCE_HEADER, reinterpret_cast<const UINT8 *>(avcc.data()), static_cast<UINT32>(avcc.size()));
        }
    }
    if (SUCCEEDED(hr)) {
        hr = decoder->transform->SetInputType(0, inputType, 0);
    }
    if (FAILED(hr)) {
        SafeRelease(&inputType);
        return hr;
    }

    IMFMediaType *outputType = nullptr;
    std::string configuredOutputSubtype;
    hr = MFCreateMediaType(&outputType);
    if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_ARGB32);
    if (SUCCEEDED(hr)) hr = decoder->transform->SetOutputType(0, outputType, 0);
    if (SUCCEEDED(hr)) configuredOutputSubtype = "ARGB32";
    if (FAILED(hr)) {
        SafeRelease(&outputType);
        outputType = nullptr;
        hr = MFCreateMediaType(&outputType);
        if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
        if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_RGB32);
        if (SUCCEEDED(hr)) hr = decoder->transform->SetOutputType(0, outputType, 0);
        if (SUCCEEDED(hr)) configuredOutputSubtype = "RGB32";
    }
    if (FAILED(hr)) {
        SafeRelease(&inputType);
        SafeRelease(&outputType);
        return hr;
    }

    decoder->inputType = inputType;
    decoder->outputType = outputType;
    decoder->sps = nextSPS;
    decoder->pps = nextPPS;
    decoder->configuredOutputSubtype = configuredOutputSubtype;
    GUID subtype = GUID_NULL;
    if (SUCCEEDED(outputType->GetGUID(MF_MT_SUBTYPE, &subtype))) {
        decoder->lastOutputSubtypeGUID = subtype;
        decoder->lastOutputSubtype = GuidName(subtype);
    } else {
        decoder->lastOutputSubtypeGUID = GUID_NULL;
        decoder->lastOutputSubtype = "unreported";
    }
    return S_OK;
}

HRESULT EnsureStreaming(MFH264Decoder *decoder) {
    if (decoder->streamingBegun) {
        return S_OK;
    }
    HRESULT hr = decoder->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
    if (SUCCEEDED(hr)) hr = decoder->transform->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
    if (SUCCEEDED(hr)) decoder->streamingBegun = true;
    return hr;
}

HRESULT EnsureInputSampleCapacity(MFH264Decoder *decoder, DWORD sampleLen) {
    if (decoder == nullptr) {
        return E_POINTER;
    }
    if (decoder->inputSample != nullptr && decoder->inputBuffer != nullptr && decoder->inputBufferSize >= sampleLen) {
        return S_OK;
    }
    SafeRelease(&decoder->inputBuffer);
    SafeRelease(&decoder->inputSample);
    decoder->inputBufferSize = 0;

    HRESULT hr = MFCreateSample(&decoder->inputSample);
    if (FAILED(hr)) {
        return hr;
    }
    hr = MFCreateMemoryBuffer(sampleLen, &decoder->inputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&decoder->inputSample);
        return hr;
    }
    hr = decoder->inputSample->AddBuffer(decoder->inputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&decoder->inputBuffer);
        SafeRelease(&decoder->inputSample);
        return hr;
    }
    decoder->inputBufferSize = sampleLen;
    return S_OK;
}

HRESULT EnsureOutputSample(MFH264Decoder *decoder) {
    if (decoder == nullptr) {
        return E_POINTER;
    }
    MFT_OUTPUT_STREAM_INFO streamInfo = {};
    HRESULT hr = decoder->transform->GetOutputStreamInfo(0, &streamInfo);
    if (FAILED(hr)) {
        return hr;
    }
    DWORD needed = streamInfo.cbSize > 0 ? streamInfo.cbSize : 1024 * 1024;
    if (decoder->outputSample != nullptr && decoder->outputBuffer != nullptr && decoder->outputBufferSize >= needed) {
        return S_OK;
    }
    SafeRelease(&decoder->outputBuffer);
    SafeRelease(&decoder->outputSample);
    decoder->outputBufferSize = 0;

    hr = MFCreateSample(&decoder->outputSample);
    if (FAILED(hr)) {
        return hr;
    }
    hr = MFCreateMemoryBuffer(needed, &decoder->outputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&decoder->outputSample);
        return hr;
    }
    hr = decoder->outputSample->AddBuffer(decoder->outputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&decoder->outputBuffer);
        SafeRelease(&decoder->outputSample);
        return hr;
    }
    decoder->outputBufferSize = needed;
    return S_OK;
}

HRESULT CopyOutputBuffer(MFH264Decoder *decoder, IMFSample *sample, IMFMediaType *outputType, void **outData, int *outWidth, int *outHeight, int *outStride) {
    IMFMediaBuffer *buffer = nullptr;
    IMF2DBuffer *buffer2D = nullptr;
    HRESULT hr = sample->GetBufferByIndex(0, &buffer);
    if (FAILED(hr)) {
        return hr;
    }
    LONG stride = 0;
    BYTE *scanline0 = nullptr;
    BYTE *raw = nullptr;
    DWORD maxLen = 0;
    DWORD curLen = 0;
    ID3D11Texture2D *stagingTexture = nullptr;
    D3D11_MAPPED_SUBRESOURCE mapped = {};

    IMFDXGIBuffer *dxgiBuffer = nullptr;
    bool usedDXGI = false;
    std::string accessPath = "none";
    std::string conversionPath = "unknown";
    hr = buffer->QueryInterface(IID_PPV_ARGS(&dxgiBuffer));
    if (SUCCEEDED(hr) && dxgiBuffer != nullptr && decoder != nullptr && decoder->d3d11 != nullptr && decoder->d3d11->device != nullptr && decoder->d3d11->context != nullptr) {
        ID3D11Texture2D *texture = nullptr;
        UINT subresource = 0;
        hr = dxgiBuffer->GetResource(IID_ID3D11Texture2D, reinterpret_cast<void **>(&texture));
        if (SUCCEEDED(hr) && texture != nullptr) {
            dxgiBuffer->GetSubresourceIndex(&subresource);
            D3D11_TEXTURE2D_DESC desc = {};
            texture->GetDesc(&desc);
            D3D11_TEXTURE2D_DESC stagingDesc = desc;
            stagingDesc.BindFlags = 0;
            stagingDesc.MiscFlags = 0;
            stagingDesc.Usage = D3D11_USAGE_STAGING;
            stagingDesc.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
            stagingDesc.ArraySize = 1;
            stagingDesc.MipLevels = 1;
            hr = decoder->d3d11->device->CreateTexture2D(&stagingDesc, nullptr, &stagingTexture);
            if (SUCCEEDED(hr) && stagingTexture != nullptr) {
                decoder->d3d11->context->CopySubresourceRegion(stagingTexture, 0, 0, 0, 0, texture, subresource, nullptr);
                hr = decoder->d3d11->context->Map(stagingTexture, 0, D3D11_MAP_READ, 0, &mapped);
                if (SUCCEEDED(hr)) {
                    scanline0 = static_cast<BYTE *>(mapped.pData);
                    stride = static_cast<LONG>(mapped.RowPitch);
                    usedDXGI = true;
                    accessPath = "dxgi-staging-map";
                }
            }
        }
        SafeRelease(&texture);
        SafeRelease(&dxgiBuffer);
    }
    if (decoder != nullptr) {
        decoder->lastOutputUsedDXGI = usedDXGI;
    }

    if (scanline0 == nullptr) {
        hr = buffer->QueryInterface(IID_PPV_ARGS(&buffer2D));
        if (SUCCEEDED(hr) && buffer2D != nullptr) {
            hr = buffer2D->Lock2D(&scanline0, &stride);
            if (SUCCEEDED(hr)) {
                accessPath = "imf2dbuffer-lock2d";
            }
        } else {
            hr = buffer->Lock(&raw, &maxLen, &curLen);
            if (SUCCEEDED(hr)) {
                scanline0 = raw;
                stride = 0;
                accessPath = "imfmediabuffer-lock";
            }
        }
    }
    if (FAILED(hr) || scanline0 == nullptr) {
        if (buffer2D != nullptr) {
            SafeRelease(&buffer2D);
        }
        if (stagingTexture != nullptr && decoder != nullptr && decoder->d3d11 != nullptr && decoder->d3d11->context != nullptr) {
            decoder->d3d11->context->Unmap(stagingTexture, 0);
            SafeRelease(&stagingTexture);
        }
        SafeRelease(&buffer);
        return FAILED(hr) ? hr : E_FAIL;
    }

    UINT32 width = 0;
    UINT32 height = 0;
    if (outputType != nullptr) {
        MFGetAttributeSize(outputType, MF_MT_FRAME_SIZE, &width, &height);
    }
    if (width == 0 || height == 0) {
        // Fallback for many decoders: read from contiguous buffer length as tightly packed BGRA.
        if (curLen == 0) {
            buffer->GetCurrentLength(&curLen);
        }
        width = 1;
        height = curLen / 4;
    }
    int outBytesPerRow = static_cast<int>(width) * 4;
    size_t total = static_cast<size_t>(outBytesPerRow) * static_cast<size_t>(height);
    BYTE *copy = static_cast<BYTE *>(malloc(total));
    if (copy == nullptr) {
        if (buffer2D != nullptr) {
            buffer2D->Unlock2D();
            SafeRelease(&buffer2D);
        } else {
            buffer->Unlock();
        }
        SafeRelease(&buffer);
        return E_OUTOFMEMORY;
    }
    if (decoder != nullptr && decoder->lastOutputSubtypeGUID == MFVideoFormat_NV12) {
        conversionPath = "nv12-to-rgba";
        const BYTE *srcY = scanline0;
        const BYTE *srcUV = scanline0 + static_cast<size_t>(stride) * height;
        LONG uvStride = stride;
        for (UINT32 y = 0; y < height; y++) {
            const BYTE *yRow = srcY + static_cast<size_t>(y) * stride;
            const BYTE *uvRow = srcUV + static_cast<size_t>(y / 2) * uvStride;
            BYTE *dstRow = copy + static_cast<size_t>(y) * outBytesPerRow;
            for (UINT32 x = 0; x < width; x++) {
                int Y = yRow[x];
                int U = uvRow[(x / 2) * 2 + 0];
                int V = uvRow[(x / 2) * 2 + 1];
                int c = Y - 16;
                int d = U - 128;
                int e = V - 128;
                dstRow[x*4+0] = ClampByte((298*c + 409*e + 128) >> 8);
                dstRow[x*4+1] = ClampByte((298*c - 100*d - 208*e + 128) >> 8);
                dstRow[x*4+2] = ClampByte((298*c + 516*d + 128) >> 8);
                dstRow[x*4+3] = 0xFF;
            }
        }
    } else if (decoder != nullptr && decoder->lastOutputSubtypeGUID == MFVideoFormat_YUY2) {
        conversionPath = "yuy2-to-rgba";
        if (stride == 0) {
            stride = static_cast<LONG>(width * 2);
        }
        for (UINT32 y = 0; y < height; y++) {
            const BYTE *srcRow = scanline0 + static_cast<size_t>(y) * stride;
            BYTE *dstRow = copy + static_cast<size_t>(y) * outBytesPerRow;
            for (UINT32 x = 0; x < width; x += 2) {
                int Y0 = srcRow[x*2 + 0];
                int U  = srcRow[x*2 + 1];
                int Y1 = srcRow[x*2 + 2];
                int V  = srcRow[x*2 + 3];
                int c0 = Y0 - 16, c1 = Y1 - 16, d = U - 128, e = V - 128;
                dstRow[(x+0)*4+0] = ClampByte((298*c0 + 409*e + 128) >> 8);
                dstRow[(x+0)*4+1] = ClampByte((298*c0 - 100*d - 208*e + 128) >> 8);
                dstRow[(x+0)*4+2] = ClampByte((298*c0 + 516*d + 128) >> 8);
                dstRow[(x+0)*4+3] = 0xFF;
                if (x + 1 < width) {
                    dstRow[(x+1)*4+0] = ClampByte((298*c1 + 409*e + 128) >> 8);
                    dstRow[(x+1)*4+1] = ClampByte((298*c1 - 100*d - 208*e + 128) >> 8);
                    dstRow[(x+1)*4+2] = ClampByte((298*c1 + 516*d + 128) >> 8);
                    dstRow[(x+1)*4+3] = 0xFF;
                }
            }
        }
    } else {
        conversionPath = "bgra-rgba-swap";
        if (stride == 0) {
            stride = outBytesPerRow;
        }
        for (UINT32 y = 0; y < height; y++) {
            const BYTE *srcRow = scanline0 + static_cast<size_t>(y) * stride;
            BYTE *dstRow = copy + static_cast<size_t>(y) * outBytesPerRow;
            for (UINT32 x = 0; x < width; x++) {
                const BYTE *srcPix = srcRow + static_cast<size_t>(x) * 4;
                BYTE *dstPix = dstRow + static_cast<size_t>(x) * 4;
                dstPix[0] = srcPix[2];
                dstPix[1] = srcPix[1];
                dstPix[2] = srcPix[0];
                dstPix[3] = srcPix[3];
            }
        }
    }

    if (stagingTexture != nullptr && decoder != nullptr && decoder->d3d11 != nullptr && decoder->d3d11->context != nullptr) {
        decoder->d3d11->context->Unmap(stagingTexture, 0);
        SafeRelease(&stagingTexture);
    } else if (buffer2D != nullptr) {
        buffer2D->Unlock2D();
        SafeRelease(&buffer2D);
    } else {
        buffer->Unlock();
    }
    SafeRelease(&buffer);

    *outData = copy;
    *outWidth = static_cast<int>(width);
    *outHeight = static_cast<int>(height);
    *outStride = outBytesPerRow;
    if (decoder != nullptr) {
        decoder->lastAccessPath = accessPath;
        decoder->lastConversionPath = conversionPath;
        decoder->lastOutputWidth = width;
        decoder->lastOutputHeight = height;
        decoder->lastOutputStride = outBytesPerRow;
    }
    return S_OK;
}

} // namespace

extern "C" void *WebrtpMFH264DecoderCreate(char **errOut) {
    HRESULT hr = StartupMF();
    if (FAILED(hr)) {
        SetError(errOut, "Media Foundation startup failed");
        return nullptr;
    }
    IMFTransform *transform = nullptr;
    hr = CoCreateInstance(CLSID_CMSH264DecoderMFT, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(&transform));
    if (FAILED(hr) || transform == nullptr) {
        ShutdownMF();
        SetError(errOut, "create H264 decoder MFT failed");
        return nullptr;
    }
    MFH264Decoder *decoder = new MFH264Decoder();
    decoder->transform = transform;
    decoder->inputType = nullptr;
    decoder->outputType = nullptr;
    decoder->d3d11 = nullptr;
    decoder->inputSample = nullptr;
    decoder->inputBuffer = nullptr;
    decoder->inputBufferSize = 0;
    decoder->outputSample = nullptr;
    decoder->outputBuffer = nullptr;
    decoder->outputBufferSize = 0;
    decoder->streamingBegun = false;
    decoder->lastOutputUsedDXGI = false;
    decoder->lastOutputSubtypeGUID = GUID_NULL;
    decoder->lastOutputSubtype.clear();
    decoder->configuredOutputSubtype.clear();
    decoder->lastAccessPath = "uninitialized";
    decoder->lastConversionPath = "uninitialized";
    decoder->lastOutputWidth = 0;
    decoder->lastOutputHeight = 0;
    decoder->lastOutputStride = 0;
    std::string d3dErr;
    if (SUCCEEDED(AcquireMFD3D11Resources(&decoder->d3d11, &d3dErr)) && decoder->d3d11 != nullptr) {
        BindTransformToD3D11(transform, decoder->d3d11);
    }
    IMFAttributes *attrs = nullptr;
    if (SUCCEEDED(transform->GetAttributes(&attrs)) && attrs != nullptr) {
        attrs->SetUINT32(MF_LOW_LATENCY, TRUE);
        attrs->Release();
    }
    ICodecAPI *codecApi = nullptr;
    if (SUCCEEDED(transform->QueryInterface(IID_ICodecAPI, reinterpret_cast<void **>(&codecApi))) && codecApi != nullptr) {
        VARIANT value;
        VariantInit(&value);
        value.vt = VT_UI4;
        value.ulVal = 1;
        codecApi->SetValue(&CODECAPI_AVLowLatencyMode, &value);
        VariantClear(&value);
        codecApi->Release();
    }
    return decoder;
}

extern "C" void WebrtpMFH264DecoderClose(void *ref) {
    if (ref == nullptr) {
        return;
    }
    MFH264Decoder *decoder = static_cast<MFH264Decoder *>(ref);
    ReleaseMFD3D11Resources(decoder->d3d11);
    decoder->d3d11 = nullptr;
    SafeRelease(&decoder->outputBuffer);
    SafeRelease(&decoder->outputSample);
    SafeRelease(&decoder->inputBuffer);
    SafeRelease(&decoder->inputSample);
    SafeRelease(&decoder->outputType);
    SafeRelease(&decoder->inputType);
    SafeRelease(&decoder->transform);
    delete decoder;
    ShutdownMF();
}

extern "C" int WebrtpMFH264DecoderDecodeH264(void *ref, const void *sample, int sampleLen, const void *sps, int spsLen, const void *pps, int ppsLen, void **outData, int *outWidth, int *outHeight, int *outStride, char **errOut) {
    if (outData != nullptr) *outData = nullptr;
    if (outWidth != nullptr) *outWidth = 0;
    if (outHeight != nullptr) *outHeight = 0;
    if (outStride != nullptr) *outStride = 0;
    if (ref == nullptr || sample == nullptr || sampleLen <= 0 || sps == nullptr || spsLen <= 0 || pps == nullptr || ppsLen <= 0) {
        SetError(errOut, "invalid Media Foundation decode input");
        return 0;
    }

    MFH264Decoder *decoder = static_cast<MFH264Decoder *>(ref);
    HRESULT hr = ConfigureDecoder(decoder, static_cast<const uint8_t *>(sps), spsLen, static_cast<const uint8_t *>(pps), ppsLen);
    if (FAILED(hr)) {
        SetError(errOut, "configure Media Foundation decoder failed");
        return 0;
    }
    hr = EnsureStreaming(decoder);
    if (FAILED(hr)) {
        SetError(errOut, "start Media Foundation stream failed");
        return 0;
    }

    hr = EnsureInputSampleCapacity(decoder, static_cast<DWORD>(sampleLen));
    if (FAILED(hr)) {
        SetError(errOut, "create Media Foundation input buffer failed");
        return 0;
    }
    BYTE *dst = nullptr;
    DWORD maxLen = 0;
    hr = decoder->inputBuffer->Lock(&dst, &maxLen, nullptr);
    if (SUCCEEDED(hr)) {
        memcpy(dst, sample, static_cast<size_t>(sampleLen));
        hr = decoder->inputBuffer->Unlock();
    }
    if (SUCCEEDED(hr)) {
        hr = decoder->inputBuffer->SetCurrentLength(static_cast<DWORD>(sampleLen));
    }
    if (FAILED(hr)) {
        SetError(errOut, "fill Media Foundation input buffer failed");
        return 0;
    }
    hr = decoder->transform->ProcessInput(0, decoder->inputSample, 0);
    if (FAILED(hr)) {
        SetError(errOut, "Media Foundation ProcessInput failed");
        return 0;
    }

    hr = EnsureOutputSample(decoder);
    if (FAILED(hr)) {
        SetError(errOut, "create Media Foundation output sample failed");
        return 0;
    }
    decoder->outputBuffer->SetCurrentLength(0);

    MFT_OUTPUT_DATA_BUFFER output = {};
    output.dwStreamID = 0;
    output.pSample = decoder->outputSample;
    DWORD statusFlags = 0;
    hr = decoder->transform->ProcessOutput(0, 1, &output, &statusFlags);
    if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) {
        SetError(errOut, "Media Foundation decoder needs more input");
        return 0;
    }
    if (FAILED(hr)) {
        SetError(errOut, "Media Foundation ProcessOutput failed");
        return 0;
    }

    hr = CopyOutputBuffer(decoder, decoder->outputSample, decoder->outputType, outData, outWidth, outHeight, outStride);
    if (FAILED(hr)) {
        SetError(errOut, "copy Media Foundation output failed");
        return 0;
    }
    return 1;
}

extern "C" void WebrtpMFH264DecoderFreeFrame(void *ptr) {
    if (ptr != nullptr) {
        free(ptr);
    }
}

extern "C" char *WebrtpMFH264DecoderDebugInfo(void *ref) {
    if (ref == nullptr) {
        return nullptr;
    }
    MFH264Decoder *decoder = static_cast<MFH264Decoder *>(ref);
    std::string info = "mf_decoder";
    info += " d3d11=";
    info += (decoder->d3d11 != nullptr ? "true" : "false");
    info += " configured_output=";
    info += decoder->configuredOutputSubtype.empty() ? "unknown" : decoder->configuredOutputSubtype;
    info += " actual_output=";
    info += decoder->lastOutputSubtype.empty() ? "unknown" : decoder->lastOutputSubtype;
    info += " access_path=";
    info += decoder->lastAccessPath.empty() ? "unknown" : decoder->lastAccessPath;
    info += " conversion=";
    info += decoder->lastConversionPath.empty() ? "unknown" : decoder->lastConversionPath;
    info += " dxgi_output=";
    info += (decoder->lastOutputUsedDXGI ? "true" : "false");
    info += " size=";
    info += std::to_string(decoder->lastOutputWidth);
    info += "x";
    info += std::to_string(decoder->lastOutputHeight);
    info += " stride=";
    info += std::to_string(decoder->lastOutputStride);
    return StringDup(info);
}
