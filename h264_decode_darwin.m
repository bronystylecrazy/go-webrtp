#import <CoreFoundation/CoreFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <Foundation/Foundation.h>
#import <VideoToolbox/VideoToolbox.h>
#import <string.h>

@interface WebrtpVTDecoder : NSObject
@property(nonatomic, assign) VTDecompressionSessionRef session;
@property(nonatomic, assign) CMVideoFormatDescriptionRef format;
@property(nonatomic, strong) NSData *currentSPS;
@property(nonatomic, strong) NSData *currentPPS;
@end

typedef struct {
    CVImageBufferRef imageBuffer;
    OSStatus status;
} WebrtpVTDecodeOutput;

static void WebrtpVTSetError(char **errOut, NSString *message) {
    if (errOut == NULL) {
        return;
    }
    const char *utf8 = message != nil ? message.UTF8String : "unknown error";
    *errOut = utf8 != NULL ? strdup(utf8) : strdup("unknown error");
}

static void WebrtpVTInvalidateSession(WebrtpVTDecoder *decoder) {
    if (decoder.session != NULL) {
        VTDecompressionSessionInvalidate(decoder.session);
        CFRelease(decoder.session);
        decoder.session = NULL;
    }
    if (decoder.format != NULL) {
        CFRelease(decoder.format);
        decoder.format = NULL;
    }
}

static void WebrtpVTDecodeOutputCallback(void *decompressionOutputRefCon,
                                         void *sourceFrameRefCon,
                                         OSStatus status,
                                         VTDecodeInfoFlags infoFlags,
                                         CVImageBufferRef imageBuffer,
                                         CMTime presentationTimeStamp,
                                         CMTime presentationDuration) {
    (void) sourceFrameRefCon;
    (void) infoFlags;
    (void) presentationTimeStamp;
    (void) presentationDuration;
    (void) decompressionOutputRefCon;
    WebrtpVTDecodeOutput *output = (WebrtpVTDecodeOutput *)sourceFrameRefCon;
    output->status = status;
    if (status == noErr && imageBuffer != NULL) {
        output->imageBuffer = CVBufferRetain(imageBuffer);
    }
}

static BOOL WebrtpVTEnsureSession(WebrtpVTDecoder *decoder,
                                  const uint8_t *sps, size_t spsLen,
                                  const uint8_t *pps, size_t ppsLen,
                                  char **errOut) {
    NSData *spsData = [NSData dataWithBytes:sps length:spsLen];
    NSData *ppsData = [NSData dataWithBytes:pps length:ppsLen];
    BOOL formatChanged = decoder.session == NULL ||
        decoder.currentSPS == nil || ![decoder.currentSPS isEqualToData:spsData] ||
        decoder.currentPPS == nil || ![decoder.currentPPS isEqualToData:ppsData];
    if (!formatChanged) {
        return YES;
    }

    WebrtpVTInvalidateSession(decoder);

    const uint8_t *parameterSetPointers[2] = {sps, pps};
    size_t parameterSetSizes[2] = {spsLen, ppsLen};
    CMVideoFormatDescriptionRef format = NULL;
    OSStatus status = CMVideoFormatDescriptionCreateFromH264ParameterSets(
        kCFAllocatorDefault,
        2,
        parameterSetPointers,
        parameterSetSizes,
        4,
        &format);
    if (status != noErr || format == NULL) {
        WebrtpVTSetError(errOut, [NSString stringWithFormat:@"create H264 format description failed: %d", (int)status]);
        return NO;
    }

    NSDictionary *attrs = @{
        (__bridge NSString *)kCVPixelBufferPixelFormatTypeKey: @(kCVPixelFormatType_32BGRA),
        (__bridge NSString *)kCVPixelBufferIOSurfacePropertiesKey: @{}
    };
    VTDecompressionOutputCallbackRecord callback = {
        .decompressionOutputCallback = WebrtpVTDecodeOutputCallback,
        .decompressionOutputRefCon = NULL,
    };
    VTDecompressionSessionRef session = NULL;
    status = VTDecompressionSessionCreate(
        kCFAllocatorDefault,
        format,
        NULL,
        (__bridge CFDictionaryRef)attrs,
        &callback,
        &session);
    if (status != noErr || session == NULL) {
        if (format != NULL) {
            CFRelease(format);
        }
        WebrtpVTSetError(errOut, [NSString stringWithFormat:@"create VT decompression session failed: %d", (int)status]);
        return NO;
    }

    decoder.format = format;
    decoder.session = session;
    decoder.currentSPS = [spsData copy];
    decoder.currentPPS = [ppsData copy];
    return YES;
}

@implementation WebrtpVTDecoder

- (void)dealloc {
    WebrtpVTInvalidateSession(self);
}

@end

void *WebrtpVTDecoderCreate(char **errOut) {
    @autoreleasepool {
        WebrtpVTDecoder *decoder = [[WebrtpVTDecoder alloc] init];
        if (decoder == nil) {
            WebrtpVTSetError(errOut, @"allocate VT decoder failed");
            return NULL;
        }
        return (__bridge_retained void *)decoder;
    }
}

void WebrtpVTDecoderClose(void *ref) {
    if (ref == NULL) {
        return;
    }
    @autoreleasepool {
        WebrtpVTDecoder *decoder = (__bridge_transfer WebrtpVTDecoder *)ref;
        (void)decoder;
    }
}

int WebrtpVTDecoderDecodeH264(void *ref,
                              const void *sample, int sampleLen,
                              const void *sps, int spsLen,
                              const void *pps, int ppsLen,
                              void **outData, int *outWidth, int *outHeight, int *outStride,
                              char **errOut) {
    if (outData != NULL) {
        *outData = NULL;
    }
    if (outWidth != NULL) {
        *outWidth = 0;
    }
    if (outHeight != NULL) {
        *outHeight = 0;
    }
    if (outStride != NULL) {
        *outStride = 0;
    }
    if (ref == NULL || sample == NULL || sampleLen <= 0 || sps == NULL || spsLen <= 0 || pps == NULL || ppsLen <= 0) {
        WebrtpVTSetError(errOut, @"invalid VT decode input");
        return 0;
    }

    @autoreleasepool {
        WebrtpVTDecoder *decoder = (__bridge WebrtpVTDecoder *)ref;
        if (!WebrtpVTEnsureSession(decoder, sps, (size_t)spsLen, pps, (size_t)ppsLen, errOut)) {
            return 0;
        }

        CMBlockBufferRef blockBuffer = NULL;
        OSStatus status = CMBlockBufferCreateWithMemoryBlock(
            kCFAllocatorDefault,
            NULL,
            sampleLen,
            kCFAllocatorDefault,
            NULL,
            0,
            sampleLen,
            0,
            &blockBuffer);
        if (status != noErr || blockBuffer == NULL) {
            WebrtpVTSetError(errOut, [NSString stringWithFormat:@"create block buffer failed: %d", (int)status]);
            return 0;
        }
        status = CMBlockBufferReplaceDataBytes(sample, blockBuffer, 0, sampleLen);
        if (status != noErr) {
            CFRelease(blockBuffer);
            WebrtpVTSetError(errOut, [NSString stringWithFormat:@"fill block buffer failed: %d", (int)status]);
            return 0;
        }

        CMSampleBufferRef sampleBuffer = NULL;
        size_t sampleSize = (size_t)sampleLen;
        status = CMSampleBufferCreateReady(
            kCFAllocatorDefault,
            blockBuffer,
            decoder.format,
            1,
            0,
            NULL,
            1,
            &sampleSize,
            &sampleBuffer);
        CFRelease(blockBuffer);
        if (status != noErr || sampleBuffer == NULL) {
            WebrtpVTSetError(errOut, [NSString stringWithFormat:@"create sample buffer failed: %d", (int)status]);
            return 0;
        }

        WebrtpVTDecodeOutput output = {0};
        VTDecodeFrameFlags flags = kVTDecodeFrame_EnableAsynchronousDecompression;
        VTDecodeInfoFlags infoFlags = 0;
        status = VTDecompressionSessionDecodeFrame(decoder.session, sampleBuffer, flags, &output, &infoFlags);
        if (status == noErr) {
            status = VTDecompressionSessionWaitForAsynchronousFrames(decoder.session);
        }
        CFRelease(sampleBuffer);
        if (status != noErr || output.status != noErr || output.imageBuffer == NULL) {
            if (output.imageBuffer != NULL) {
                CVBufferRelease(output.imageBuffer);
            }
            WebrtpVTInvalidateSession(decoder);
            WebrtpVTSetError(errOut, [NSString stringWithFormat:@"VT decode failed: session=%d output=%d", (int)status, (int)output.status]);
            return 0;
        }

        CVPixelBufferRef pixelBuffer = (CVPixelBufferRef)output.imageBuffer;
        CVPixelBufferLockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
        size_t width = CVPixelBufferGetWidth(pixelBuffer);
        size_t height = CVPixelBufferGetHeight(pixelBuffer);
        size_t bytesPerRow = CVPixelBufferGetBytesPerRow(pixelBuffer);
        uint8_t *base = (uint8_t *)CVPixelBufferGetBaseAddress(pixelBuffer);
        if (base == NULL || width == 0 || height == 0) {
            CVPixelBufferUnlockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
            CVBufferRelease(pixelBuffer);
            WebrtpVTSetError(errOut, @"VT decode returned empty pixel buffer");
            return 0;
        }

        size_t outBytesPerRow = width * 4;
        size_t outLen = outBytesPerRow * height;
        uint8_t *copy = malloc(outLen);
        if (copy == NULL) {
            CVPixelBufferUnlockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
            CVBufferRelease(pixelBuffer);
            WebrtpVTSetError(errOut, @"allocate output frame failed");
            return 0;
        }
        for (size_t y = 0; y < height; y++) {
            memcpy(copy + y * outBytesPerRow, base + y * bytesPerRow, outBytesPerRow);
        }
        CVPixelBufferUnlockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
        CVBufferRelease(pixelBuffer);

        if (outData != NULL) {
            *outData = copy;
        }
        if (outWidth != NULL) {
            *outWidth = (int)width;
        }
        if (outHeight != NULL) {
            *outHeight = (int)height;
        }
        if (outStride != NULL) {
            *outStride = (int)outBytesPerRow;
        }
        return 1;
    }
}

void WebrtpVTDecoderFreeFrame(void *ptr) {
    if (ptr != NULL) {
        free(ptr);
    }
}
