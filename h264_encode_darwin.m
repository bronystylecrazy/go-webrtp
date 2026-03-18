#import <CoreFoundation/CoreFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <Foundation/Foundation.h>
#import <VideoToolbox/VideoToolbox.h>
#import <dispatch/dispatch.h>
#import <string.h>

@interface WebrtpVTH264Encoder : NSObject
@property(nonatomic, assign) VTCompressionSessionRef session;
@property(nonatomic, assign) int width;
@property(nonatomic, assign) int height;
@property(nonatomic, assign) int64_t frameIndex;
@property(nonatomic, assign) BOOL includeParameterSets;
@property(nonatomic, assign) BOOL forceNextKeyFrame;
@property(nonatomic, strong) NSData *outputData;
@property(nonatomic, assign) OSStatus outputStatus;
@property(nonatomic, strong) dispatch_semaphore_t outputSemaphore;
@end

static void WebrtpVTEncodeSetError(char **errOut, NSString *message) {
    if (errOut == NULL) {
        return;
    }
    const char *utf8 = message != nil ? message.UTF8String : "unknown error";
    *errOut = utf8 != NULL ? strdup(utf8) : strdup("unknown error");
}

static void WebrtpVTEncodeInvalidateSession(WebrtpVTH264Encoder *encoder) {
    if (encoder.session != NULL) {
        VTCompressionSessionCompleteFrames(encoder.session, kCMTimeInvalid);
        VTCompressionSessionInvalidate(encoder.session);
        CFRelease(encoder.session);
        encoder.session = NULL;
    }
}

static void WebrtpVTH264EncoderOutput(void *outputCallbackRefCon,
                                      void *sourceFrameRefCon,
                                      OSStatus status,
                                      VTEncodeInfoFlags infoFlags,
                                      CMSampleBufferRef sampleBuffer) {
    (void)sourceFrameRefCon;
    (void)infoFlags;
    WebrtpVTH264Encoder *encoder = (__bridge WebrtpVTH264Encoder *)outputCallbackRefCon;
    encoder.outputStatus = status;
    encoder.outputData = nil;

    if (status == noErr && sampleBuffer != NULL && CMSampleBufferDataIsReady(sampleBuffer)) {
        BOOL isKeyFrame = NO;
        CFArrayRef attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, false);
        if (attachments != NULL && CFArrayGetCount(attachments) > 0) {
            CFDictionaryRef attachment = CFArrayGetValueAtIndex(attachments, 0);
            isKeyFrame = !CFDictionaryContainsKey(attachment, kCMSampleAttachmentKey_NotSync);
        }

        NSMutableData *annexb = [NSMutableData data];
        static const uint8_t startCode[] = {0x00, 0x00, 0x00, 0x01};
        CMFormatDescriptionRef format = CMSampleBufferGetFormatDescription(sampleBuffer);
        if (isKeyFrame || encoder.includeParameterSets) {
            encoder.includeParameterSets = NO;
            size_t count = 0;
            if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(format, 0, NULL, NULL, &count, NULL) == noErr) {
                for (size_t idx = 0; idx < count; idx++) {
                    const uint8_t *param = NULL;
                    size_t paramSize = 0;
                    if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(format, idx, &param, &paramSize, NULL, NULL) == noErr && param != NULL && paramSize > 0) {
                        [annexb appendBytes:startCode length:sizeof(startCode)];
                        [annexb appendBytes:param length:paramSize];
                    }
                }
            }
        }

        CMBlockBufferRef blockBuffer = CMSampleBufferGetDataBuffer(sampleBuffer);
        if (blockBuffer != NULL) {
            size_t totalLength = 0;
            char *dataPointer = NULL;
            if (CMBlockBufferGetDataPointer(blockBuffer, 0, NULL, &totalLength, &dataPointer) != noErr) {
                NSMutableData *contiguous = [NSMutableData dataWithLength:CMBlockBufferGetDataLength(blockBuffer)];
                if (CMBlockBufferCopyDataBytes(blockBuffer, 0, contiguous.length, contiguous.mutableBytes) == noErr) {
                    dataPointer = contiguous.mutableBytes;
                    totalLength = contiguous.length;
                }
            }
            size_t offset = 0;
            while (dataPointer != NULL && offset + 4 <= totalLength) {
                uint32_t nalLength = 0;
                memcpy(&nalLength, dataPointer + offset, 4);
                nalLength = CFSwapInt32BigToHost(nalLength);
                offset += 4;
                if (nalLength == 0 || offset + nalLength > totalLength) {
                    break;
                }
                [annexb appendBytes:startCode length:sizeof(startCode)];
                [annexb appendBytes:dataPointer + offset length:nalLength];
                offset += nalLength;
            }
        }
        if (annexb.length > 0) {
            encoder.outputData = [annexb copy];
        }
    }

    if (encoder.outputSemaphore != nil) {
        dispatch_semaphore_signal(encoder.outputSemaphore);
    }
}

static BOOL WebrtpVTH264EnsureSession(WebrtpVTH264Encoder *encoder, int width, int height, char **errOut) {
    if (encoder.session != NULL && encoder.width == width && encoder.height == height) {
        return YES;
    }

    WebrtpVTEncodeInvalidateSession(encoder);

    VTCompressionSessionRef session = NULL;
    OSStatus status = VTCompressionSessionCreate(
        kCFAllocatorDefault,
        (int32_t)width,
        (int32_t)height,
        kCMVideoCodecType_H264,
        NULL,
        NULL,
        NULL,
        WebrtpVTH264EncoderOutput,
        (__bridge void *)encoder,
        &session);
    if (status != noErr || session == NULL) {
        WebrtpVTEncodeSetError(errOut, [NSString stringWithFormat:@"create VT compression session failed: %d", (int)status]);
        return NO;
    }

    VTSessionSetProperty(session, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
    VTSessionSetProperty(session, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
    VTSessionSetProperty(session, kVTCompressionPropertyKey_MaxKeyFrameInterval, (__bridge CFTypeRef)@30);
    VTSessionSetProperty(session, kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, (__bridge CFTypeRef)@1.0);
    VTSessionSetProperty(session, kVTCompressionPropertyKey_ProfileLevel, kVTProfileLevel_H264_Main_AutoLevel);

    status = VTCompressionSessionPrepareToEncodeFrames(session);
    if (status != noErr) {
        CFRelease(session);
        WebrtpVTEncodeSetError(errOut, [NSString stringWithFormat:@"prepare VT compression session failed: %d", (int)status]);
        return NO;
    }

    encoder.session = session;
    encoder.width = width;
    encoder.height = height;
    encoder.frameIndex = 0;
    encoder.includeParameterSets = YES;
    encoder.forceNextKeyFrame = YES;
    return YES;
}

static BOOL WebrtpVTCopyRGBAIntoPixelBuffer(const void *rgba, int width, int height, int stride, CVPixelBufferRef pixelBuffer, char **errOut) {
    if (rgba == NULL || width <= 0 || height <= 0 || stride <= 0 || pixelBuffer == NULL) {
        WebrtpVTEncodeSetError(errOut, @"invalid RGBA frame");
        return NO;
    }
    CVReturn cvStatus = CVPixelBufferLockBaseAddress(pixelBuffer, 0);
    if (cvStatus != kCVReturnSuccess) {
        WebrtpVTEncodeSetError(errOut, [NSString stringWithFormat:@"lock pixel buffer failed: %d", (int)cvStatus]);
        return NO;
    }
    uint8_t *dstBase = CVPixelBufferGetBaseAddress(pixelBuffer);
    size_t dstStride = CVPixelBufferGetBytesPerRow(pixelBuffer);
    const uint8_t *srcBase = (const uint8_t *)rgba;
    for (int y = 0; y < height; y++) {
        const uint8_t *srcRow = srcBase + (size_t)y * (size_t)stride;
        uint8_t *dstRow = dstBase + (size_t)y * dstStride;
        for (int x = 0; x < width; x++) {
            const uint8_t *srcPix = srcRow + (size_t)x * 4;
            uint8_t *dstPix = dstRow + (size_t)x * 4;
            dstPix[0] = srcPix[2];
            dstPix[1] = srcPix[1];
            dstPix[2] = srcPix[0];
            dstPix[3] = srcPix[3];
        }
    }
    CVPixelBufferUnlockBaseAddress(pixelBuffer, 0);
    return YES;
}

@implementation WebrtpVTH264Encoder

- (instancetype)init {
    self = [super init];
    if (self == nil) {
        return nil;
    }
    _outputSemaphore = dispatch_semaphore_create(0);
    _includeParameterSets = YES;
    _forceNextKeyFrame = YES;
    return self;
}

- (void)dealloc {
    WebrtpVTEncodeInvalidateSession(self);
}

@end

void *WebrtpVTH264EncoderCreate(char **errOut) {
    @autoreleasepool {
        WebrtpVTH264Encoder *encoder = [[WebrtpVTH264Encoder alloc] init];
        if (encoder == nil) {
            WebrtpVTEncodeSetError(errOut, @"allocate VT encoder failed");
            return NULL;
        }
        return (__bridge_retained void *)encoder;
    }
}

void WebrtpVTH264EncoderClose(void *ref) {
    if (ref == NULL) {
        return;
    }
    @autoreleasepool {
        WebrtpVTH264Encoder *encoder = (__bridge_transfer WebrtpVTH264Encoder *)ref;
        (void)encoder;
    }
}

int WebrtpVTH264EncoderEncodeRGBA(void *ref, const void *rgba, int width, int height, int stride, void **outData, int *outLen, char **errOut) {
    if (outData != NULL) {
        *outData = NULL;
    }
    if (outLen != NULL) {
        *outLen = 0;
    }
    if (ref == NULL || rgba == NULL || width <= 0 || height <= 0 || stride <= 0) {
        WebrtpVTEncodeSetError(errOut, @"invalid VT encode input");
        return 0;
    }

    @autoreleasepool {
        WebrtpVTH264Encoder *encoder = (__bridge WebrtpVTH264Encoder *)ref;
        if (!WebrtpVTH264EnsureSession(encoder, width, height, errOut)) {
            return 0;
        }

        CVPixelBufferRef pixelBuffer = NULL;
        NSDictionary *attrs = @{
            (__bridge NSString *)kCVPixelBufferCGImageCompatibilityKey: @YES,
            (__bridge NSString *)kCVPixelBufferCGBitmapContextCompatibilityKey: @YES,
        };
        CVReturn cvStatus = CVPixelBufferCreate(kCFAllocatorDefault, width, height, kCVPixelFormatType_32BGRA, (__bridge CFDictionaryRef)attrs, &pixelBuffer);
        if (cvStatus != kCVReturnSuccess || pixelBuffer == NULL) {
            WebrtpVTEncodeSetError(errOut, [NSString stringWithFormat:@"create pixel buffer failed: %d", (int)cvStatus]);
            return 0;
        }
        if (!WebrtpVTCopyRGBAIntoPixelBuffer(rgba, width, height, stride, pixelBuffer, errOut)) {
            CFRelease(pixelBuffer);
            return 0;
        }

        encoder.outputData = nil;
        encoder.outputStatus = noErr;
        encoder.outputSemaphore = dispatch_semaphore_create(0);

        CFDictionaryRef frameProps = NULL;
        if (encoder.forceNextKeyFrame) {
            encoder.forceNextKeyFrame = NO;
            frameProps = (__bridge CFDictionaryRef)@{(__bridge NSString *)kVTEncodeFrameOptionKey_ForceKeyFrame: @YES};
        }
        CMTime pts = CMTimeMake(encoder.frameIndex++, 30);
        VTEncodeInfoFlags infoFlags = 0;
        OSStatus status = VTCompressionSessionEncodeFrame(encoder.session, pixelBuffer, pts, kCMTimeInvalid, frameProps, NULL, &infoFlags);
        CFRelease(pixelBuffer);
        if (status != noErr) {
            WebrtpVTEncodeSetError(errOut, [NSString stringWithFormat:@"encode frame failed: %d", (int)status]);
            return 0;
        }

        status = VTCompressionSessionCompleteFrames(encoder.session, pts);
        if (status != noErr) {
            WebrtpVTEncodeSetError(errOut, [NSString stringWithFormat:@"complete frames failed: %d", (int)status]);
            return 0;
        }

        dispatch_semaphore_wait(encoder.outputSemaphore, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
        if (encoder.outputStatus != noErr) {
            WebrtpVTEncodeSetError(errOut, [NSString stringWithFormat:@"encode callback failed: %d", (int)encoder.outputStatus]);
            return 0;
        }
        if (encoder.outputData == nil || encoder.outputData.length == 0) {
            WebrtpVTEncodeSetError(errOut, @"empty encoded frame");
            return 0;
        }

        void *copy = malloc(encoder.outputData.length);
        if (copy == NULL) {
            WebrtpVTEncodeSetError(errOut, @"allocate encoded frame buffer failed");
            return 0;
        }
        memcpy(copy, encoder.outputData.bytes, encoder.outputData.length);
        if (outData != NULL) {
            *outData = copy;
        }
        if (outLen != NULL) {
            *outLen = (int)encoder.outputData.length;
        }
        return 1;
    }
}

void WebrtpVTH264EncoderFreeBuffer(void *ptr) {
    if (ptr != NULL) {
        free(ptr);
    }
}
