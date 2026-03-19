import Foundation
import CoreMediaIO

let providerSource = GoWebRTPCameraProviderSource(clientQueue: nil)
CMIOExtensionProvider.startService(provider: providerSource.provider)
CFRunLoopRun()
