#pragma once

#include <mfapi.h>
#include <mfidl.h>
#include <mftransform.h>
#include <d3d11.h>

#include <string>

struct MFD3D11Resources {
    ID3D11Device *device;
    ID3D11DeviceContext *context;
    IMFDXGIDeviceManager *manager;
    UINT resetToken;
    long refs;
};

HRESULT AcquireMFD3D11Resources(MFD3D11Resources **out, std::string *errOut);
void ReleaseMFD3D11Resources(MFD3D11Resources *res);
HRESULT BindTransformToD3D11(IMFTransform *transform, MFD3D11Resources *res);
