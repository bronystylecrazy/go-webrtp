import Foundation
import CoreMedia
import CoreMediaIO
import CoreVideo
import IOKit.audio
import os

private let frameRate: Int32 = 30
private let frameWidth: Int32 = 1280
private let frameHeight: Int32 = 720
private let stripeHeight = 18

final class GoWebRTPCameraDeviceSource: NSObject, CMIOExtensionDeviceSource {
    private let logger = Logger(subsystem: "go.webrtp.virtualcam", category: "device")
    private let timerQueue = DispatchQueue(label: "go.webrtp.virtualcam.timer", qos: .userInteractive)
    private var timer: DispatchSourceTimer?
    private var streamingClients: UInt32 = 0
    private var stripeStartRow: Int = 0
    private var stripeAscending = false

    private(set) var device: CMIOExtensionDevice!

    private var streamSource: GoWebRTPCameraStreamSource!
    private var videoDescription: CMFormatDescription!
    private var bufferPool: CVPixelBufferPool!
    private var bufferAuxAttributes: NSDictionary!

    init(localizedName: String) {
        super.init()

        let deviceID = UUID()
        device = CMIOExtensionDevice(
            localizedName: localizedName,
            deviceID: deviceID,
            legacyDeviceID: "go-webrtp.virtualcam.\(deviceID.uuidString.lowercased())",
            source: self
        )

        CMVideoFormatDescriptionCreate(
            allocator: kCFAllocatorDefault,
            codecType: kCVPixelFormatType_32BGRA,
            width: frameWidth,
            height: frameHeight,
            extensions: nil,
            formatDescriptionOut: &videoDescription
        )

        let pixelBufferAttributes: NSDictionary = [
            kCVPixelBufferWidthKey: Int(frameWidth),
            kCVPixelBufferHeightKey: Int(frameHeight),
            kCVPixelBufferPixelFormatTypeKey: kCVPixelFormatType_32BGRA,
            kCVPixelBufferIOSurfacePropertiesKey: [:] as NSDictionary,
        ]
        CVPixelBufferPoolCreate(kCFAllocatorDefault, nil, pixelBufferAttributes, &bufferPool)
        bufferAuxAttributes = [kCVPixelBufferPoolAllocationThresholdKey: 6]

        let streamFormat = CMIOExtensionStreamFormat(
            formatDescription: videoDescription,
            maxFrameDuration: CMTime(value: 1, timescale: frameRate),
            minFrameDuration: CMTime(value: 1, timescale: frameRate),
            validFrameDurations: nil
        )

        streamSource = GoWebRTPCameraStreamSource(
            localizedName: "GoWebRTP Virtual Camera Video",
            streamID: UUID(),
            streamFormat: streamFormat,
            device: device
        )
        do {
            try device.addStream(streamSource.stream)
        } catch {
            fatalError("failed to add virtual camera stream: \(error)")
        }
    }

    var availableProperties: Set<CMIOExtensionProperty> {
        [.deviceTransportType, .deviceModel]
    }

    func deviceProperties(forProperties properties: Set<CMIOExtensionProperty>) throws -> CMIOExtensionDeviceProperties {
        let deviceProperties = CMIOExtensionDeviceProperties(dictionary: [:])
        if properties.contains(.deviceTransportType) {
            deviceProperties.transportType = kIOAudioDeviceTransportTypeVirtual
        }
        if properties.contains(.deviceModel) {
            deviceProperties.model = "GoWebRTP Virtual Camera"
        }
        return deviceProperties
    }

    func setDeviceProperties(_ deviceProperties: CMIOExtensionDeviceProperties) throws {
        _ = deviceProperties
    }

    func startStreaming() {
        streamingClients += 1
        guard timer == nil else {
            return
        }

        let timer = DispatchSource.makeTimerSource(flags: .strict, queue: timerQueue)
        timer.schedule(deadline: .now(), repeating: .nanoseconds(Int(1_000_000_000 / frameRate)), leeway: .nanoseconds(0))
        timer.setEventHandler { [weak self] in
            self?.emitFrame()
        }
        self.timer = timer
        timer.resume()
        logger.info("virtual camera started streaming")
    }

    func stopStreaming() {
        if streamingClients > 1 {
            streamingClients -= 1
            return
        }

        streamingClients = 0
        timer?.cancel()
        timer = nil
        logger.info("virtual camera stopped streaming")
    }

    private func emitFrame() {
        var pixelBuffer: CVPixelBuffer?
        let status = CVPixelBufferPoolCreatePixelBufferWithAuxAttributes(
            kCFAllocatorDefault,
            bufferPool,
            bufferAuxAttributes,
            &pixelBuffer
        )
        guard status == kCVReturnSuccess, let pixelBuffer else {
            logger.error("failed to allocate pixel buffer: \(status)")
            return
        }

        renderFrame(into: pixelBuffer)

        var timing = CMSampleTimingInfo(
            duration: CMTime(value: 1, timescale: frameRate),
            presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()),
            decodeTimeStamp: .invalid
        )
        var sampleBuffer: CMSampleBuffer?
        let createStatus = CMSampleBufferCreateForImageBuffer(
            allocator: kCFAllocatorDefault,
            imageBuffer: pixelBuffer,
            dataReady: true,
            makeDataReadyCallback: nil,
            refcon: nil,
            formatDescription: videoDescription,
            sampleTiming: &timing,
            sampleBufferOut: &sampleBuffer
        )
        guard createStatus == noErr, let sampleBuffer else {
            logger.error("failed to create sample buffer: \(createStatus)")
            return
        }

        let hostTime = UInt64(timing.presentationTimeStamp.seconds * Double(NSEC_PER_SEC))
        streamSource.stream.send(sampleBuffer, discontinuity: [], hostTimeInNanoseconds: hostTime)
    }

    // Replace this test pattern renderer with a real frame bridge from go-webrtp.
    private func renderFrame(into pixelBuffer: CVPixelBuffer) {
        CVPixelBufferLockBaseAddress(pixelBuffer, [])
        defer { CVPixelBufferUnlockBaseAddress(pixelBuffer, []) }

        guard let base = CVPixelBufferGetBaseAddress(pixelBuffer) else {
            return
        }

        let width = CVPixelBufferGetWidth(pixelBuffer)
        let height = CVPixelBufferGetHeight(pixelBuffer)
        let bytesPerRow = CVPixelBufferGetBytesPerRow(pixelBuffer)
        memset(base, 0x12, bytesPerRow * height)

        for row in 0..<height {
            let line = base.advanced(by: row * bytesPerRow).assumingMemoryBound(to: UInt8.self)
            for column in 0..<width {
                let offset = column * 4
                line[offset + 0] = UInt8((column * 255) / max(width, 1))
                line[offset + 1] = UInt8((row * 255) / max(height, 1))
                line[offset + 2] = 0x28
                line[offset + 3] = 0xFF
            }
        }

        if stripeAscending {
            stripeStartRow -= 1
            stripeAscending = stripeStartRow > 0
        } else {
            stripeStartRow += 1
            stripeAscending = stripeStartRow >= max(height - stripeHeight, 0)
        }

        for row in stripeStartRow..<min(stripeStartRow + stripeHeight, height) {
            let line = base.advanced(by: row * bytesPerRow).assumingMemoryBound(to: UInt32.self)
            for column in 0..<width {
                line[column] = 0xFFFFFFFF
            }
        }
    }
}

final class GoWebRTPCameraStreamSource: NSObject, CMIOExtensionStreamSource {
    private(set) var stream: CMIOExtensionStream!
    private let streamFormat: CMIOExtensionStreamFormat
    private let device: CMIOExtensionDevice

    init(localizedName: String, streamID: UUID, streamFormat: CMIOExtensionStreamFormat, device: CMIOExtensionDevice) {
        self.streamFormat = streamFormat
        self.device = device
        super.init()
        stream = CMIOExtensionStream(
            localizedName: localizedName,
            streamID: streamID,
            direction: .source,
            clockType: .hostTime,
            source: self
        )
    }

    var formats: [CMIOExtensionStreamFormat] {
        [streamFormat]
    }

    var availableProperties: Set<CMIOExtensionProperty> {
        [.streamFrameDuration]
    }

    func streamProperties(forProperties properties: Set<CMIOExtensionProperty>) throws -> CMIOExtensionStreamProperties {
        let streamProperties = CMIOExtensionStreamProperties(dictionary: [:])
        if properties.contains(.streamFrameDuration) {
            streamProperties.frameDuration = CMTime(value: 1, timescale: frameRate)
        }
        return streamProperties
    }

    func setStreamProperties(_ streamProperties: CMIOExtensionStreamProperties) throws {
        _ = streamProperties
    }

    func authorizedToStartStream(for client: CMIOExtensionClient) -> Bool {
        _ = client
        return true
    }

    func startStream() throws {
        guard let source = device.source as? GoWebRTPCameraDeviceSource else {
            fatalError("unexpected CMIO device source type")
        }
        source.startStreaming()
    }

    func stopStream() throws {
        guard let source = device.source as? GoWebRTPCameraDeviceSource else {
            fatalError("unexpected CMIO device source type")
        }
        source.stopStreaming()
    }
}

final class GoWebRTPCameraProviderSource: NSObject, CMIOExtensionProviderSource {
    private(set) var provider: CMIOExtensionProvider!
    private var deviceSource: GoWebRTPCameraDeviceSource!

    init(clientQueue: DispatchQueue?) {
        super.init()

        provider = CMIOExtensionProvider(source: self, clientQueue: clientQueue)
        deviceSource = GoWebRTPCameraDeviceSource(localizedName: "GoWebRTP Virtual Camera")
        do {
            try provider.addDevice(deviceSource.device)
        } catch {
            fatalError("failed to add virtual camera device: \(error)")
        }
    }

    func connect(to client: CMIOExtensionClient) throws {
        _ = client
    }

    func disconnect(from client: CMIOExtensionClient) {
        _ = client
    }

    var availableProperties: Set<CMIOExtensionProperty> {
        [.providerManufacturer]
    }

    func providerProperties(forProperties properties: Set<CMIOExtensionProperty>) throws -> CMIOExtensionProviderProperties {
        let providerProperties = CMIOExtensionProviderProperties(dictionary: [:])
        if properties.contains(.providerManufacturer) {
            providerProperties.manufacturer = "GoWebRTP"
        }
        return providerProperties
    }

    func setProviderProperties(_ providerProperties: CMIOExtensionProviderProperties) throws {
        _ = providerProperties
    }
}
