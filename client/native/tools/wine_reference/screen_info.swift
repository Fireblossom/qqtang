import ApplicationServices
import CoreGraphics
import Foundation

var result: [String: Any] = [
    "screen_capture_authorized": CGPreflightScreenCaptureAccess(),
    "accessibility_authorized": AXIsProcessTrusted()
]
if CommandLine.arguments.contains("--request") {
    result["request_result"] = CGRequestScreenCaptureAccess()
}
var windows: [[String: Any]] = []
if let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] {
    for w in list {
        let owner = w[kCGWindowOwnerName as String] as? String ?? ""
        if owner == "Client.exe" {
            windows.append([
                "id": w[kCGWindowNumber as String] ?? 0,
                "pid": w[kCGWindowOwnerPID as String] ?? 0,
                "owner": owner,
                "bounds": w[kCGWindowBounds as String] ?? [:]
            ])
        }
    }
}
result["windows"] = windows
let data = try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys])
print(String(data: data, encoding: .utf8)!)
