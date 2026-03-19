import Foundation
import SystemExtensions

enum InstallerAction: String {
    case activate
    case deactivate
    case properties
}

final class InstallerDelegate: NSObject, OSSystemExtensionRequestDelegate {
    private let completion: (Int32) -> Void

    init(completion: @escaping (Int32) -> Void) {
        self.completion = completion
    }

    func request(
        _ request: OSSystemExtensionRequest,
        actionForReplacingExtension existing: OSSystemExtensionProperties,
        withExtension ext: OSSystemExtensionProperties
    ) -> OSSystemExtensionRequest.ReplacementAction {
        fputs("Replacing existing extension \(existing.bundleIdentifier) with \(ext.bundleIdentifier)\n", stderr)
        return .replace
    }

    func requestNeedsUserApproval(_ request: OSSystemExtensionRequest) {
        fputs("System extension approval required for \(request.identifier)\n", stderr)
    }

    func request(_ request: OSSystemExtensionRequest, didFinishWithResult result: OSSystemExtensionRequest.Result) {
        print("System extension request finished: \(result.rawValue)")
        completion(EXIT_SUCCESS)
    }

    func request(_ request: OSSystemExtensionRequest, didFailWithError error: Error) {
        fputs("System extension request failed: \(error)\n", stderr)
        completion(EXIT_FAILURE)
    }

    func request(_ request: OSSystemExtensionRequest, foundProperties properties: [OSSystemExtensionProperties]) {
        if properties.isEmpty {
            print("No matching system extensions found for \(request.identifier)")
            return
        }
        for item in properties {
            print("bundleIdentifier=\(item.bundleIdentifier) enabled=\(item.isEnabled) awaitingApproval=\(item.isAwaitingUserApproval) uninstalling=\(item.isUninstalling)")
        }
    }
}

func configuredExtensionBundleIdentifier() -> String {
    if let value = Bundle.main.object(forInfoDictionaryKey: "VirtualCameraExtensionBundleIdentifier") as? String, !value.isEmpty {
        return value
    }
    if let value = ProcessInfo.processInfo.environment["VIRTUAL_CAMERA_EXTENSION_BUNDLE_ID"], !value.isEmpty {
        return value
    }
    fatalError("VirtualCameraExtensionBundleIdentifier is not configured")
}

func parseAction() -> InstallerAction {
    guard CommandLine.arguments.count > 1 else {
        return .activate
    }
    guard let action = InstallerAction(rawValue: CommandLine.arguments[1].lowercased()) else {
        fatalError("expected one of: activate, deactivate, properties")
    }
    return action
}

let extensionBundleIdentifier = configuredExtensionBundleIdentifier()
let action = parseAction()
let queue = DispatchQueue(label: "go.webrtp.virtualcam.installer")
let delegate = InstallerDelegate { code in
    exit(code)
}

let request: OSSystemExtensionRequest
switch action {
case .activate:
    request = OSSystemExtensionRequest.activationRequest(forExtensionWithIdentifier: extensionBundleIdentifier, queue: queue)
case .deactivate:
    request = OSSystemExtensionRequest.deactivationRequest(forExtensionWithIdentifier: extensionBundleIdentifier, queue: queue)
case .properties:
    request = OSSystemExtensionRequest.propertiesRequest(forExtensionWithIdentifier: extensionBundleIdentifier, queue: queue)
}

request.delegate = delegate
OSSystemExtensionManager.shared.submitRequest(request)
RunLoop.main.run()
