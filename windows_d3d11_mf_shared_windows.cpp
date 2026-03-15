#include "windows_d3d11_mf_shared.h"

#include <dxgi1_2.h>
#include <mfobjects.h>

namespace {

template <typename T>
void SafeReleaseShared(T **ptr) {
    if (ptr != nullptr && *ptr != nullptr) {
        (*ptr)->Release();
        *ptr = nullptr;
    }
}

SRWLOCK g_d3d11_lock = SRWLOCK_INIT;
MFD3D11Resources *g_d3d11 = nullptr;

void FreeResources(MFD3D11Resources *res) {
    if (res == nullptr) {
        return;
    }
    SafeReleaseShared(&res->manager);
    SafeReleaseShared(&res->context);
    SafeReleaseShared(&res->device);
    delete res;
}

} // namespace

HRESULT AcquireMFD3D11Resources(MFD3D11Resources **out, std::string *errOut) {
    if (out == nullptr) {
        return E_POINTER;
    }
    *out = nullptr;

    AcquireSRWLockExclusive(&g_d3d11_lock);
    if (g_d3d11 != nullptr) {
        g_d3d11->refs++;
        *out = g_d3d11;
        ReleaseSRWLockExclusive(&g_d3d11_lock);
        return S_OK;
    }

    MFD3D11Resources *res = new MFD3D11Resources();
    res->device = nullptr;
    res->context = nullptr;
    res->manager = nullptr;
    res->resetToken = 0;
    res->refs = 1;

    UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT | D3D11_CREATE_DEVICE_VIDEO_SUPPORT;
    D3D_FEATURE_LEVEL levels[] = {
        D3D_FEATURE_LEVEL_11_1,
        D3D_FEATURE_LEVEL_11_0,
        D3D_FEATURE_LEVEL_10_1,
        D3D_FEATURE_LEVEL_10_0,
    };
    D3D_FEATURE_LEVEL createdLevel = D3D_FEATURE_LEVEL_11_0;
    HRESULT hr = D3D11CreateDevice(
        nullptr,
        D3D_DRIVER_TYPE_HARDWARE,
        nullptr,
        flags,
        levels,
        ARRAYSIZE(levels),
        D3D11_SDK_VERSION,
        &res->device,
        &createdLevel,
        &res->context
    );
    if (FAILED(hr)) {
        hr = D3D11CreateDevice(
            nullptr,
            D3D_DRIVER_TYPE_WARP,
            nullptr,
            D3D11_CREATE_DEVICE_BGRA_SUPPORT,
            levels,
            ARRAYSIZE(levels),
            D3D11_SDK_VERSION,
            &res->device,
            &createdLevel,
            &res->context
        );
    }
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = "D3D11CreateDevice failed";
        }
        FreeResources(res);
        ReleaseSRWLockExclusive(&g_d3d11_lock);
        return hr;
    }

    hr = MFCreateDXGIDeviceManager(&res->resetToken, &res->manager);
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = "MFCreateDXGIDeviceManager failed";
        }
        FreeResources(res);
        ReleaseSRWLockExclusive(&g_d3d11_lock);
        return hr;
    }
    hr = res->manager->ResetDevice(res->device, res->resetToken);
    if (FAILED(hr)) {
        if (errOut != nullptr) {
            *errOut = "IMFDXGIDeviceManager::ResetDevice failed";
        }
        FreeResources(res);
        ReleaseSRWLockExclusive(&g_d3d11_lock);
        return hr;
    }

    g_d3d11 = res;
    *out = res;
    ReleaseSRWLockExclusive(&g_d3d11_lock);
    return S_OK;
}

void ReleaseMFD3D11Resources(MFD3D11Resources *res) {
    if (res == nullptr) {
        return;
    }
    AcquireSRWLockExclusive(&g_d3d11_lock);
    if (g_d3d11 == res) {
        g_d3d11->refs--;
        if (g_d3d11->refs <= 0) {
            MFD3D11Resources *toFree = g_d3d11;
            g_d3d11 = nullptr;
            ReleaseSRWLockExclusive(&g_d3d11_lock);
            FreeResources(toFree);
            return;
        }
    }
    ReleaseSRWLockExclusive(&g_d3d11_lock);
}

HRESULT BindTransformToD3D11(IMFTransform *transform, MFD3D11Resources *res) {
    if (transform == nullptr || res == nullptr || res->manager == nullptr) {
        return E_POINTER;
    }
    IMFAttributes *attrs = nullptr;
    HRESULT hr = transform->GetAttributes(&attrs);
    if (SUCCEEDED(hr) && attrs != nullptr) {
        attrs->SetUINT32(MF_LOW_LATENCY, TRUE);
        attrs->SetUINT32(MF_SA_D3D11_BINDFLAGS, D3D11_BIND_SHADER_RESOURCE | D3D11_BIND_RENDER_TARGET);
        attrs->Release();
    }
    hr = transform->ProcessMessage(MFT_MESSAGE_SET_D3D_MANAGER, reinterpret_cast<ULONG_PTR>(res->manager));
    return hr;
}
