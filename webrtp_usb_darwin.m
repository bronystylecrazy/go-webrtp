#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <Foundation/Foundation.h>
#import <VideoToolbox/VideoToolbox.h>
#import <math.h>

extern void WebrtpUsbMacPacket(uintptr_t handle, void *data, int length, uint32_t pts90k);
extern void WebrtpUsbMacError(uintptr_t handle, char *msg);

static uint32_t WebrtpUsbPts90k(CMTime pts) {
    if (!CMTIME_IS_VALID(pts)) {
        return 0;
    }
    Float64 seconds = CMTimeGetSeconds(pts);
    if (!isfinite(seconds) || seconds < 0) {
        return 0;
    }
    return (uint32_t) llround(seconds * 90000.0);
}

@interface WebrtpUsbMacCapture : NSObject<AVCaptureVideoDataOutputSampleBufferDelegate>
@property(nonatomic, assign) uintptr_t handle;
@property(nonatomic, strong) AVCaptureSession *session;
@property(nonatomic, strong) dispatch_queue_t queue;
@property(nonatomic, assign) VTCompressionSessionRef compression;
@property(nonatomic, assign) CMVideoCodecType codecType;
@property(nonatomic, assign) Float64 fps;
@property(nonatomic, assign) int bitrateKbps;
@property(nonatomic, assign) BOOL includeParameterSets;
@end

static void WebrtpUsbMacCompressionOutput(void *outputCallbackRefCon, void *sourceFrameRefCon, OSStatus status, VTEncodeInfoFlags infoFlags, CMSampleBufferRef sampleBuffer) {
    WebrtpUsbMacCapture *capture = (__bridge WebrtpUsbMacCapture *) outputCallbackRefCon;
    if (status != noErr) {
        WebrtpUsbMacError(capture.handle, (char *) "VideoToolbox encode failed");
        return;
    }
    if (sampleBuffer == NULL || !CMSampleBufferDataIsReady(sampleBuffer)) {
        return;
    }

    BOOL isKeyFrame = NO;
    CFArrayRef attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, false);
    if (attachments != NULL && CFArrayGetCount(attachments) > 0) {
        CFDictionaryRef attachment = CFArrayGetValueAtIndex(attachments, 0);
        isKeyFrame = !CFDictionaryContainsKey(attachment, kCMSampleAttachmentKey_NotSync);
    }

    NSMutableData *annexb = [NSMutableData data];
    static const uint8_t startCode[] = {0x00, 0x00, 0x00, 0x01};

    CMFormatDescriptionRef format = CMSampleBufferGetFormatDescription(sampleBuffer);
    if (isKeyFrame || capture.includeParameterSets) {
        capture.includeParameterSets = NO;
        if (capture.codecType == kCMVideoCodecType_H264) {
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
        } else if (capture.codecType == kCMVideoCodecType_HEVC) {
            size_t count = 0;
            if (CMVideoFormatDescriptionGetHEVCParameterSetAtIndex(format, 0, NULL, NULL, &count, NULL) == noErr) {
                for (size_t idx = 0; idx < count; idx++) {
                    const uint8_t *param = NULL;
                    size_t paramSize = 0;
                    if (CMVideoFormatDescriptionGetHEVCParameterSetAtIndex(format, idx, &param, &paramSize, NULL, NULL) == noErr && param != NULL && paramSize > 0) {
                        [annexb appendBytes:startCode length:sizeof(startCode)];
                        [annexb appendBytes:param length:paramSize];
                    }
                }
            }
        }
    }

    CMBlockBufferRef blockBuffer = CMSampleBufferGetDataBuffer(sampleBuffer);
    if (blockBuffer == NULL) {
        return;
    }

    size_t totalLength = 0;
    char *dataPointer = NULL;
    if (CMBlockBufferGetDataPointer(blockBuffer, 0, NULL, &totalLength, &dataPointer) != noErr) {
        NSMutableData *contiguous = [NSMutableData dataWithLength:CMBlockBufferGetDataLength(blockBuffer)];
        if (CMBlockBufferCopyDataBytes(blockBuffer, 0, contiguous.length, contiguous.mutableBytes) != noErr) {
            return;
        }
        dataPointer = contiguous.mutableBytes;
        totalLength = contiguous.length;
    }

    size_t offset = 0;
    while (offset + 4 <= totalLength) {
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

    if (annexb.length == 0) {
        return;
    }

    WebrtpUsbMacPacket(capture.handle, annexb.mutableBytes, (int) annexb.length, WebrtpUsbPts90k(CMSampleBufferGetPresentationTimeStamp(sampleBuffer)));
}

@implementation WebrtpUsbMacCapture

- (instancetype)initWithHandle:(uintptr_t)handle codec:(NSString *)codec fps:(double)fps bitrateKbps:(int)bitrateKbps {
    self = [super init];
    if (self == nil) {
        return nil;
    }
    _handle = handle;
    _fps = fps;
    _bitrateKbps = bitrateKbps;
    _queue = dispatch_queue_create("go.webrtp.usb.capture", DISPATCH_QUEUE_SERIAL);
    _includeParameterSets = YES;
    if ([[codec lowercaseString] isEqualToString:@"h265"]) {
        _codecType = kCMVideoCodecType_HEVC;
    } else {
        _codecType = kCMVideoCodecType_H264;
    }
    return self;
}

- (BOOL)startWithDevice:(NSString *)deviceName error:(NSError **)error {
    AVAuthorizationStatus auth = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
    if (auth == AVAuthorizationStatusDenied || auth == AVAuthorizationStatusRestricted) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"go-webrtp" code:1 userInfo:@{NSLocalizedDescriptionKey: @"camera access is denied"}];
        }
        return NO;
    }

    AVCaptureDevice *device = nil;
    if ([deviceName length] == 0 || [[deviceName lowercaseString] isEqualToString:@"default"]) {
        device = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeVideo];
    } else {
        AVCaptureDeviceDiscoverySession *discovery = [AVCaptureDeviceDiscoverySession discoverySessionWithDeviceTypes:@[AVCaptureDeviceTypeBuiltInWideAngleCamera, AVCaptureDeviceTypeExternal] mediaType:AVMediaTypeVideo position:AVCaptureDevicePositionUnspecified];
        NSArray<AVCaptureDevice *> *devices = discovery.devices;
        for (AVCaptureDevice *candidate in devices) {
            if ([candidate.uniqueID isEqualToString:deviceName] || [candidate.localizedName isEqualToString:deviceName]) {
                device = candidate;
                break;
            }
        }
    }
    if (device == nil) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"go-webrtp" code:2 userInfo:@{NSLocalizedDescriptionKey: @"video device not found"}];
        }
        return NO;
    }

    AVCaptureDeviceInput *input = [AVCaptureDeviceInput deviceInputWithDevice:device error:error];
    if (input == nil) {
        return NO;
    }

    AVCaptureVideoDataOutput *output = [[AVCaptureVideoDataOutput alloc] init];
    output.alwaysDiscardsLateVideoFrames = YES;
    output.videoSettings = @{(id)kCVPixelBufferPixelFormatTypeKey: @(kCVPixelFormatType_420YpCbCr8BiPlanarFullRange)};
    [output setSampleBufferDelegate:self queue:self.queue];

    AVCaptureSession *session = [[AVCaptureSession alloc] init];
    if ([session canSetSessionPreset:AVCaptureSessionPresetHigh]) {
        session.sessionPreset = AVCaptureSessionPresetHigh;
    }
    if (![session canAddInput:input]) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"go-webrtp" code:3 userInfo:@{NSLocalizedDescriptionKey: @"cannot add capture input"}];
        }
        return NO;
    }
    if (![session canAddOutput:output]) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"go-webrtp" code:4 userInfo:@{NSLocalizedDescriptionKey: @"cannot add capture output"}];
        }
        return NO;
    }

    [session beginConfiguration];
    [session addInput:input];
    [session addOutput:output];
    AVCaptureConnection *connection = [output connectionWithMediaType:AVMediaTypeVideo];
    if (self.fps > 0 && connection != nil && connection.isVideoMinFrameDurationSupported) {
        connection.videoMinFrameDuration = CMTimeMake(1, (int32_t) llround(self.fps));
    }
    [session commitConfiguration];

    self.session = session;
    [self.session startRunning];
    return YES;
}

- (BOOL)setupCompressionForSampleBuffer:(CMSampleBufferRef)sampleBuffer error:(NSError **)error {
    if (self.compression != NULL) {
        return YES;
    }

    CVImageBufferRef imageBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (imageBuffer == NULL) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"go-webrtp" code:5 userInfo:@{NSLocalizedDescriptionKey: @"missing image buffer"}];
        }
        return NO;
    }

    size_t width = CVPixelBufferGetWidth(imageBuffer);
    size_t height = CVPixelBufferGetHeight(imageBuffer);
    OSStatus status = VTCompressionSessionCreate(kCFAllocatorDefault, (int32_t) width, (int32_t) height, self.codecType, NULL, NULL, NULL, WebrtpUsbMacCompressionOutput, (__bridge void *) self, &_compression);
    if (status != noErr || self.compression == NULL) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"go-webrtp" code:6 userInfo:@{NSLocalizedDescriptionKey: @"failed to create encoder"}];
        }
        return NO;
    }

    VTSessionSetProperty(self.compression, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
    VTSessionSetProperty(self.compression, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
    if (self.fps > 0) {
        VTSessionSetProperty(self.compression, kVTCompressionPropertyKey_ExpectedFrameRate, (__bridge CFTypeRef) @(self.fps));
    }
    if (self.bitrateKbps > 0) {
        int bitrate = self.bitrateKbps * 1000;
        VTSessionSetProperty(self.compression, kVTCompressionPropertyKey_AverageBitRate, (__bridge CFTypeRef) @(bitrate));
        VTSessionSetProperty(self.compression, kVTCompressionPropertyKey_DataRateLimits, (__bridge CFArrayRef) @[@(bitrate * 2 / 8), @1]);
    }
    VTSessionSetProperty(self.compression, kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, (__bridge CFTypeRef) @(2.0));
    VTSessionSetProperty(self.compression, kVTCompressionPropertyKey_ProfileLevel, self.codecType == kCMVideoCodecType_HEVC ? kVTProfileLevel_HEVC_Main_AutoLevel : kVTProfileLevel_H264_Main_AutoLevel);

    status = VTCompressionSessionPrepareToEncodeFrames(self.compression);
    if (status != noErr) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"go-webrtp" code:7 userInfo:@{NSLocalizedDescriptionKey: @"failed to prepare encoder"}];
        }
        return NO;
    }
    return YES;
}

- (void)stop {
    if (self.session != nil) {
        [self.session stopRunning];
        self.session = nil;
    }
    if (self.compression != NULL) {
        VTCompressionSessionCompleteFrames(self.compression, kCMTimeInvalid);
        VTCompressionSessionInvalidate(self.compression);
        CFRelease(self.compression);
        self.compression = NULL;
    }
}

- (void)captureOutput:(AVCaptureOutput *)output didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer fromConnection:(AVCaptureConnection *)connection {
    (void) output;
    (void) connection;

    NSError *error = nil;
    if (![self setupCompressionForSampleBuffer:sampleBuffer error:&error]) {
        WebrtpUsbMacError(self.handle, (char *) error.localizedDescription.UTF8String);
        return;
    }

    VTEncodeInfoFlags flags = 0;
    OSStatus status = VTCompressionSessionEncodeFrame(self.compression, CMSampleBufferGetImageBuffer(sampleBuffer), CMSampleBufferGetPresentationTimeStamp(sampleBuffer), kCMTimeInvalid, NULL, NULL, &flags);
    if (status != noErr) {
        WebrtpUsbMacError(self.handle, (char *) "failed to encode video frame");
    }
}

@end

void *WebrtpUsbMacCaptureStart(const char *device, const char *codec, double fps, int bitrateKbps, uintptr_t handle, char **errOut) {
    @autoreleasepool {
        NSString *deviceName = device != NULL ? [NSString stringWithUTF8String:device] : @"default";
        NSString *codecName = codec != NULL ? [NSString stringWithUTF8String:codec] : @"h264";
        WebrtpUsbMacCapture *capture = [[WebrtpUsbMacCapture alloc] initWithHandle:handle codec:codecName fps:fps bitrateKbps:bitrateKbps];
        NSError *error = nil;
        if (![capture startWithDevice:deviceName error:&error]) {
            if (errOut != NULL) {
                NSString *msg = error.localizedDescription ?: @"failed to start capture";
                *errOut = strdup(msg.UTF8String);
            }
            return NULL;
        }
        return (__bridge_retained void *) capture;
    }
}

void WebrtpUsbMacCaptureStop(void *ref) {
    @autoreleasepool {
        if (ref == NULL) {
            return;
        }
        WebrtpUsbMacCapture *capture = (__bridge_transfer WebrtpUsbMacCapture *) ref;
        [capture stop];
    }
}

char *WebrtpUsbMacDeviceList(char **errOut) {
    @autoreleasepool {
        AVAuthorizationStatus auth = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
        if (auth == AVAuthorizationStatusDenied || auth == AVAuthorizationStatusRestricted) {
            if (errOut != NULL) {
                *errOut = strdup("camera access is denied");
            }
            return NULL;
        }

        AVCaptureDeviceDiscoverySession *discovery = [AVCaptureDeviceDiscoverySession discoverySessionWithDeviceTypes:@[AVCaptureDeviceTypeBuiltInWideAngleCamera, AVCaptureDeviceTypeExternal] mediaType:AVMediaTypeVideo position:AVCaptureDevicePositionUnspecified];
        NSArray<AVCaptureDevice *> *devices = discovery.devices;
        NSMutableArray<NSString *> *lines = [NSMutableArray arrayWithCapacity:devices.count];
        for (AVCaptureDevice *device in devices) {
            NSString *line = [NSString stringWithFormat:@"%@\t%@", device.uniqueID ?: @"", device.localizedName ?: @""];
            [lines addObject:line];
        }
        NSString *joined = [lines componentsJoinedByString:@"\n"];
        return strdup(joined.UTF8String);
    }
}
