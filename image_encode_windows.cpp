#include <windows.h>
#include <wincodec.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <string>

namespace {

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

template <typename T>
void SafeRelease(T **ptr) {
    if (ptr != nullptr && *ptr != nullptr) {
        (*ptr)->Release();
        *ptr = nullptr;
    }
}

} // namespace

extern "C" int WebrtpWICEncodeJPEG(const void *rgba, int width, int height, int stride, int quality, void **outData, int *outLen, char **errOut) {
    if (outData != nullptr) *outData = nullptr;
    if (outLen != nullptr) *outLen = 0;
    if (rgba == nullptr || width <= 0 || height <= 0 || stride < width * 4) {
        SetError(errOut, "invalid WIC jpeg input");
        return 0;
    }

    HRESULT hr = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    bool coInit = SUCCEEDED(hr);
    if (FAILED(hr) && hr != RPC_E_CHANGED_MODE) {
        SetError(errOut, "initialize COM failed");
        return 0;
    }

    IWICImagingFactory *factory = nullptr;
    IWICBitmap *bitmap = nullptr;
    IStream *stream = nullptr;
    IWICBitmapEncoder *encoder = nullptr;
    IWICBitmapFrameEncode *frame = nullptr;
    IPropertyBag2 *props = nullptr;

    hr = CoCreateInstance(CLSID_WICImagingFactory, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(&factory));
    if (SUCCEEDED(hr)) {
        hr = factory->CreateBitmapFromMemory(
            static_cast<UINT>(width),
            static_cast<UINT>(height),
            GUID_WICPixelFormat32bppRGBA,
            static_cast<UINT>(stride),
            static_cast<UINT>(stride * height),
            static_cast<BYTE *>(const_cast<void *>(rgba)),
            &bitmap
        );
    }
    if (SUCCEEDED(hr)) {
        hr = CreateStreamOnHGlobal(nullptr, TRUE, &stream);
    }
    if (SUCCEEDED(hr)) {
        hr = factory->CreateEncoder(GUID_ContainerFormatJpeg, nullptr, &encoder);
    }
    if (SUCCEEDED(hr)) {
        hr = encoder->Initialize(stream, WICBitmapEncoderNoCache);
    }
    if (SUCCEEDED(hr)) {
        hr = encoder->CreateNewFrame(&frame, &props);
    }
    if (SUCCEEDED(hr) && props != nullptr) {
        PROPBAG2 option = {};
        option.pstrName = const_cast<LPOLESTR>(L"ImageQuality");
        VARIANT value;
        VariantInit(&value);
        value.vt = VT_R4;
        value.fltVal = static_cast<float>(quality < 1 ? 1 : (quality > 100 ? 100 : quality)) / 100.0f;
        props->Write(1, &option, &value);
        VariantClear(&value);
    }
    if (SUCCEEDED(hr)) {
        hr = frame->Initialize(props);
    }
    if (SUCCEEDED(hr)) {
        hr = frame->SetSize(static_cast<UINT>(width), static_cast<UINT>(height));
    }
    if (SUCCEEDED(hr)) {
        WICPixelFormatGUID pixelFormat = GUID_WICPixelFormat24bppBGR;
        hr = frame->SetPixelFormat(&pixelFormat);
    }
    if (SUCCEEDED(hr)) {
        hr = frame->WriteSource(bitmap, nullptr);
    }
    if (SUCCEEDED(hr)) {
        hr = frame->Commit();
    }
    if (SUCCEEDED(hr)) {
        hr = encoder->Commit();
    }

    if (SUCCEEDED(hr)) {
        HGLOBAL handle = nullptr;
        hr = GetHGlobalFromStream(stream, &handle);
        if (SUCCEEDED(hr) && handle != nullptr) {
            SIZE_T size = GlobalSize(handle);
            void *src = GlobalLock(handle);
            if (src == nullptr || size == 0) {
                hr = E_FAIL;
            } else {
                void *copy = malloc(size);
                if (copy == nullptr) {
                    hr = E_OUTOFMEMORY;
                } else {
                    memcpy(copy, src, size);
                    if (outData != nullptr) *outData = copy;
                    if (outLen != nullptr) *outLen = static_cast<int>(size);
                }
                GlobalUnlock(handle);
            }
        }
    }

    SafeRelease(&props);
    SafeRelease(&frame);
    SafeRelease(&encoder);
    SafeRelease(&stream);
    SafeRelease(&bitmap);
    SafeRelease(&factory);
    if (coInit) {
        CoUninitialize();
    }

    if (FAILED(hr)) {
        SetError(errOut, "WIC jpeg encode failed");
        return 0;
    }
    return 1;
}

extern "C" void WebrtpWICFreeBuffer(void *ptr) {
    if (ptr != nullptr) {
        free(ptr);
    }
}
