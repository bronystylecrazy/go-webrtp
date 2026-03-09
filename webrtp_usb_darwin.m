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
@property(nonatomic, assign) int targetWidth;
@property(nonatomic, assign) int targetHeight;
@property(nonatomic, assign) Float64 fps;
@property(nonatomic, assign) int bitrateKbps;
@property(nonatomic, assign) BOOL includeParameterSets;
@property(nonatomic, assign) BOOL forceNextKeyFrame;
@end

static NSString *WebrtpUsbMacSessionPresetForSize(int width, int height) {
    if (width == 640 && height == 480) {
        return AVCaptureSessionPreset640x480;
    }
    if (width == 1280 && height == 720) {
        return AVCaptureSessionPreset1280x720;
    }
    if (width == 1920 && height == 1080) {
        return AVCaptureSessionPreset1920x1080;
    }
    return nil;
}

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

- (instancetype)initWithHandle:(uintptr_t)handle codec:(NSString *)codec width:(int)width height:(int)height fps:(double)fps bitrateKbps:(int)bitrateKbps {
    self = [super init];
    if (self == nil) {
        return nil;
    }
    _handle = handle;
    _targetWidth = width;
    _targetHeight = height;
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

    AVCaptureDeviceFormat *bestFormat = nil;
    if ((self.targetWidth > 0 && self.targetHeight > 0) || self.fps > 0) {
        double bestScore = DBL_MAX;
        for (AVCaptureDeviceFormat *format in device.formats) {
            CMVideoDimensions dims = CMVideoFormatDescriptionGetDimensions(format.formatDescription);
            if (dims.width <= 0 || dims.height <= 0) {
                continue;
            }
            if (self.targetWidth > 0 && self.targetHeight > 0 &&
                (dims.width != self.targetWidth || dims.height != self.targetHeight)) {
                continue;
            }
            double sizeScore = 0;
            if (self.targetWidth > 0 && self.targetHeight > 0) {
                sizeScore = fabs((double)dims.width - self.targetWidth) + fabs((double)dims.height - self.targetHeight);
            }
            double fpsScore = 0;
            if (self.fps > 0) {
                double candidate = 0;
                for (AVFrameRateRange *range in format.videoSupportedFrameRateRanges) {
                    if (range.maxFrameRate >= self.fps) {
                        candidate = self.fps;
                        break;
                    }
                    candidate = MAX(candidate, range.maxFrameRate);
                }
                fpsScore = candidate > 0 ? fabs(candidate - self.fps) * 10.0 : 1000000.0;
            }
            double score = sizeScore + fpsScore;
            if (score < bestScore) {
                bestScore = score;
                bestFormat = format;
            }
        }
        if (bestFormat == nil && self.targetWidth > 0 && self.targetHeight > 0) {
            if (error != NULL) {
                NSString *msg = [NSString stringWithFormat:@"requested mode %dx%d is not supported by device", self.targetWidth, self.targetHeight];
                *error = [NSError errorWithDomain:@"go-webrtp" code:8 userInfo:@{NSLocalizedDescriptionKey: msg}];
            }
            return NO;
        }
    }

    AVCaptureVideoDataOutput *output = [[AVCaptureVideoDataOutput alloc] init];
    output.alwaysDiscardsLateVideoFrames = YES;
    output.videoSettings = @{(id)kCVPixelBufferPixelFormatTypeKey: @(kCVPixelFormatType_420YpCbCr8BiPlanarFullRange)};
    [output setSampleBufferDelegate:self queue:self.queue];

    AVCaptureSession *session = [[AVCaptureSession alloc] init];
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
    NSString *requestedPreset = nil;
    if (self.targetWidth > 0 && self.targetHeight > 0) {
        requestedPreset = WebrtpUsbMacSessionPresetForSize(self.targetWidth, self.targetHeight);
    }
    if (requestedPreset != nil && [session canSetSessionPreset:requestedPreset]) {
        session.sessionPreset = requestedPreset;
    } else if (!((self.targetWidth > 0 && self.targetHeight > 0) || self.fps > 0) &&
               [session canSetSessionPreset:AVCaptureSessionPresetHigh]) {
        session.sessionPreset = AVCaptureSessionPresetHigh;
    }
    if (bestFormat != nil) {
        if ([device lockForConfiguration:error]) {
            device.activeFormat = bestFormat;
            if (self.fps > 0) {
                CMTime frameDuration = CMTimeMake(1, (int32_t) llround(self.fps));
                device.activeVideoMinFrameDuration = frameDuration;
                device.activeVideoMaxFrameDuration = frameDuration;
            }
            CMVideoDimensions dims = CMVideoFormatDescriptionGetDimensions(device.activeFormat.formatDescription);
            NSLog(@"go-webrtp usb capture selected activeFormat %dx%d @ %.2f fps", dims.width, dims.height, self.fps);
            [device unlockForConfiguration];
        } else {
            [session commitConfiguration];
            return NO;
        }
    }
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
    CFDictionaryRef frameProperties = NULL;
    if (self.forceNextKeyFrame) {
        self.forceNextKeyFrame = NO;
        frameProperties = (__bridge CFDictionaryRef) @{(__bridge NSString *) kVTEncodeFrameOptionKey_ForceKeyFrame: @YES};
    }
    OSStatus status = VTCompressionSessionEncodeFrame(self.compression, CMSampleBufferGetImageBuffer(sampleBuffer), CMSampleBufferGetPresentationTimeStamp(sampleBuffer), kCMTimeInvalid, frameProperties, NULL, &flags);
    if (status != noErr) {
        WebrtpUsbMacError(self.handle, (char *) "failed to encode video frame");
    }
}

@end

void *WebrtpUsbMacCaptureStart(const char *device, const char *codec, int width, int height, double fps, int bitrateKbps, uintptr_t handle, char **errOut) {
    @autoreleasepool {
        NSString *deviceName = device != NULL ? [NSString stringWithUTF8String:device] : @"default";
        NSString *codecName = codec != NULL ? [NSString stringWithUTF8String:codec] : @"h264";
        WebrtpUsbMacCapture *capture = [[WebrtpUsbMacCapture alloc] initWithHandle:handle codec:codecName width:width height:height fps:fps bitrateKbps:bitrateKbps];
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

void WebrtpUsbMacCaptureForceKeyFrame(void *ref) {
    @autoreleasepool {
        if (ref == NULL) {
            return;
        }
        WebrtpUsbMacCapture *capture = (__bridge WebrtpUsbMacCapture *) ref;
        capture.forceNextKeyFrame = YES;
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

char *WebrtpUsbMacDeviceCapabilities(const char *device, char **errOut) {
    @autoreleasepool {
        NSString *deviceName = device != NULL ? [NSString stringWithUTF8String:device] : @"default";
        AVCaptureDevice *selected = nil;
        AVCaptureDeviceDiscoverySession *discovery = [AVCaptureDeviceDiscoverySession discoverySessionWithDeviceTypes:@[AVCaptureDeviceTypeBuiltInWideAngleCamera, AVCaptureDeviceTypeExternal] mediaType:AVMediaTypeVideo position:AVCaptureDevicePositionUnspecified];
        NSArray<AVCaptureDevice *> *devices = discovery.devices;
        for (AVCaptureDevice *candidate in devices) {
            if ([deviceName length] == 0 || [[deviceName lowercaseString] isEqualToString:@"default"]) {
                selected = candidate;
                break;
            }
            if ([candidate.uniqueID isEqualToString:deviceName] || [candidate.localizedName isEqualToString:deviceName]) {
                selected = candidate;
                break;
            }
        }
        if (selected == nil) {
            if (errOut != NULL) {
                *errOut = strdup("video device not found");
            }
            return NULL;
        }

        NSMutableDictionary *result = [NSMutableDictionary dictionary];
        result[@"device"] = @{
            @"id": selected.uniqueID ?: @"",
            @"name": selected.localizedName ?: @""
        };
        result[@"codecs"] = @[@"h264", @"h265"];
        result[@"bitrateControl"] = @"target";

        NSMutableDictionary<NSString *, NSMutableOrderedSet<NSNumber *> *> *modeMap = [NSMutableDictionary dictionary];
        for (AVCaptureDeviceFormat *format in selected.formats) {
            CMFormatDescriptionRef desc = format.formatDescription;
            CMVideoDimensions dims = CMVideoFormatDescriptionGetDimensions(desc);
            if (dims.width <= 0 || dims.height <= 0) {
                continue;
            }
            NSString *key = [NSString stringWithFormat:@"%d x %d", dims.width, dims.height];
            NSMutableOrderedSet<NSNumber *> *fpsSet = modeMap[key];
            if (fpsSet == nil) {
                fpsSet = [NSMutableOrderedSet orderedSet];
                modeMap[key] = fpsSet;
            }
            for (AVFrameRateRange *range in format.videoSupportedFrameRateRanges) {
                double maxFps = range.maxFrameRate;
                if (isfinite(maxFps) && maxFps > 0) {
                    [fpsSet addObject:@(maxFps)];
                }
            }
        }

        NSArray<NSString *> *sortedKeys = [[modeMap allKeys] sortedArrayUsingSelector:@selector(compare:)];
        NSMutableArray *modes = [NSMutableArray arrayWithCapacity:sortedKeys.count];
        for (NSString *key in sortedKeys) {
            NSArray<NSString *> *parts = [key componentsSeparatedByString:@" x "];
            if (parts.count != 2) {
                continue;
            }
            NSMutableArray *fps = [NSMutableArray array];
            for (NSNumber *value in modeMap[key]) {
                [fps addObject:value];
            }
            [fps sortUsingComparator:^NSComparisonResult(NSNumber *a, NSNumber *b) {
                return [a compare:b];
            }];
            [modes addObject:@{
                @"width": @([parts[0] intValue]),
                @"height": @([parts[1] intValue]),
                @"fps": fps
            }];
        }
        result[@"modes"] = modes;

        NSError *jsonError = nil;
        NSData *data = [NSJSONSerialization dataWithJSONObject:result options:0 error:&jsonError];
        if (data == nil) {
            if (errOut != NULL) {
                NSString *msg = jsonError.localizedDescription ?: @"failed to encode capabilities";
                *errOut = strdup(msg.UTF8String);
            }
            return NULL;
        }
        return strdup(((NSString *)[[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]).UTF8String);
    }
}
