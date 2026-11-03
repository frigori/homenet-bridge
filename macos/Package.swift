// swift-tools-version: 6.0
// The menu bar app. Build the .app with `make app` from the repository root.
import PackageDescription

let package = Package(
    name: "HomenetBridge",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(name: "HomenetBridge", path: "Sources/HomenetBridge")
    ],
    swiftLanguageModes: [.v5]
)
