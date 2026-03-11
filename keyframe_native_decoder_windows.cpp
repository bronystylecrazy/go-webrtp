#include <mfapi.h>
#include <mfidl.h>
#include <mftransform.h>
#include <mferror.h>
#include <wmcodecdsp.h>
#include <windows.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <string>

namespace {

struct MFH264Decoder {
    IMFTransform *transform;
    IMFMediaType *inputType;
    IMFMediaType *outputType;
    std::string sps;
    std::string pps;
    bool streamingBegun;
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
    hr = MFCreateMediaType(&outputType);
    if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_ARGB32);
    if (SUCCEEDED(hr)) hr = decoder->transform->SetOutputType(0, outputType, 0);
    if (FAILED(hr)) {
        SafeRelease(&outputType);
        outputType = nullptr;
        hr = MFCreateMediaType(&outputType);
        if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
        if (SUCCEEDED(hr)) hr = outputType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_RGB32);
        if (SUCCEEDED(hr)) hr = decoder->transform->SetOutputType(0, outputType, 0);
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

HRESULT CopyOutputBuffer(IMFSample *sample, IMFMediaType *outputType, void **outData, int *outWidth, int *outHeight, int *outStride) {
    IMFMediaBuffer *buffer = nullptr;
    HRESULT hr = sample->ConvertToContiguousBuffer(&buffer);
    if (FAILED(hr)) {
        return hr;
    }

    IMF2DBuffer *buffer2D = nullptr;
    LONG stride = 0;
    BYTE *scanline0 = nullptr;
    BYTE *raw = nullptr;
    DWORD maxLen = 0;
    DWORD curLen = 0;

    hr = sample->GetBufferByIndex(0, &buffer);
    if (FAILED(hr)) {
        SafeRelease(&buffer);
        return hr;
    }
    hr = buffer->QueryInterface(IID_PPV_ARGS(&buffer2D));
    if (SUCCEEDED(hr) && buffer2D != nullptr) {
        hr = buffer2D->Lock2D(&scanline0, &stride);
    } else {
        hr = buffer->Lock(&raw, &maxLen, &curLen);
        if (SUCCEEDED(hr)) {
            scanline0 = raw;
            stride = 0;
        }
    }
    if (FAILED(hr) || scanline0 == nullptr) {
        if (buffer2D != nullptr) {
            SafeRelease(&buffer2D);
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
    if (stride == 0) {
        stride = outBytesPerRow;
    }
    for (UINT32 y = 0; y < height; y++) {
        memcpy(copy + static_cast<size_t>(y) * outBytesPerRow, scanline0 + static_cast<size_t>(y) * stride, outBytesPerRow);
    }

    if (buffer2D != nullptr) {
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
    decoder->streamingBegun = false;
    return decoder;
}

extern "C" void WebrtpMFH264DecoderClose(void *ref) {
    if (ref == nullptr) {
        return;
    }
    MFH264Decoder *decoder = static_cast<MFH264Decoder *>(ref);
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

    IMFMediaBuffer *buffer = nullptr;
    hr = MFCreateMemoryBuffer(static_cast<DWORD>(sampleLen), &buffer);
    if (FAILED(hr)) {
        SetError(errOut, "create Media Foundation input buffer failed");
        return 0;
    }
    BYTE *dst = nullptr;
    DWORD maxLen = 0;
    hr = buffer->Lock(&dst, &maxLen, nullptr);
    if (SUCCEEDED(hr)) {
        memcpy(dst, sample, static_cast<size_t>(sampleLen));
        hr = buffer->Unlock();
    }
    if (SUCCEEDED(hr)) {
        hr = buffer->SetCurrentLength(static_cast<DWORD>(sampleLen));
    }
    if (FAILED(hr)) {
        SafeRelease(&buffer);
        SetError(errOut, "fill Media Foundation input buffer failed");
        return 0;
    }

    IMFSample *inputSample = nullptr;
    hr = MFCreateSample(&inputSample);
    if (SUCCEEDED(hr)) {
        hr = inputSample->AddBuffer(buffer);
    }
    SafeRelease(&buffer);
    if (FAILED(hr)) {
        SafeRelease(&inputSample);
        SetError(errOut, "create Media Foundation input sample failed");
        return 0;
    }

    hr = decoder->transform->ProcessInput(0, inputSample, 0);
    SafeRelease(&inputSample);
    if (FAILED(hr)) {
        SetError(errOut, "Media Foundation ProcessInput failed");
        return 0;
    }

    MFT_OUTPUT_STREAM_INFO streamInfo = {};
    hr = decoder->transform->GetOutputStreamInfo(0, &streamInfo);
    if (FAILED(hr)) {
        SetError(errOut, "Media Foundation GetOutputStreamInfo failed");
        return 0;
    }

    IMFMediaBuffer *outputBuffer = nullptr;
    IMFSample *outputSample = nullptr;
    hr = MFCreateSample(&outputSample);
    if (SUCCEEDED(hr)) {
        hr = MFCreateMemoryBuffer(streamInfo.cbSize > 0 ? streamInfo.cbSize : static_cast<DWORD>(sampleLen * 8), &outputBuffer);
    }
    if (SUCCEEDED(hr)) {
        hr = outputSample->AddBuffer(outputBuffer);
    }
    SafeRelease(&outputBuffer);
    if (FAILED(hr)) {
        SafeRelease(&outputSample);
        SetError(errOut, "create Media Foundation output sample failed");
        return 0;
    }

    MFT_OUTPUT_DATA_BUFFER output = {};
    output.dwStreamID = 0;
    output.pSample = outputSample;
    DWORD statusFlags = 0;
    hr = decoder->transform->ProcessOutput(0, 1, &output, &statusFlags);
    if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) {
        SafeRelease(&outputSample);
        SetError(errOut, "Media Foundation decoder needs more input");
        return 0;
    }
    if (FAILED(hr)) {
        SafeRelease(&outputSample);
        SetError(errOut, "Media Foundation ProcessOutput failed");
        return 0;
    }

    hr = CopyOutputBuffer(outputSample, decoder->outputType, outData, outWidth, outHeight, outStride);
    SafeRelease(&outputSample);
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
