import AppKit
import SwiftUI
import UserNotifications

@main
struct HomenetBridgeApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @ObservedObject private var model = AppModel.shared

    var body: some Scene {
        MenuBarExtra {
            MenuContent(model: model)
        } label: {
            Image(nsImage: Glyph.image(active: model.status != nil, pending: model.pendingCount > 0))
            if let s = model.status {
                Text(model.remaining(until: s.until))
            }
        }
        .menuBarExtraStyle(.menu)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        AppModel.shared.start()
    }

    func applicationWillTerminate(_ notification: Notification) {
        // A bridge started from the menu stops with the app; one from the terminal keeps going.
        AppModel.shared.runner.terminate()
    }
}

final class AppModel: ObservableObject {
    static let shared = AppModel()

    @Published private(set) var status: BridgeStatus?
    @Published private(set) var starting = false
    @Published private(set) var failure: String?
    @Published private(set) var services: [Config.Service] = []
    @Published private(set) var now = Date()

    let runner = BridgeRunner()
    private let client = BridgeClient()
    private let notifier = Notifier()
    private var clock: Timer?

    var pendingCount: Int { status?.devices.filter(\.isPending).count ?? 0 }

    func start() {
        services = Config.load()
        notifier.setUp { [weak self] ip, allow in self?.decide(ip, allow: allow) }
        client.onStatus = { [weak self] status in
            guard let self else { return }
            self.status = status
            if status != nil { self.starting = false }
            self.notifier.update(status)
        }
        client.onError = { [weak self] error in self?.failure = error }
        runner.onExit = { [weak self] failure in
            self?.starting = false
            self?.failure = failure
        }
        client.start()
        clock = Timer.scheduledTimer(withTimeInterval: 15, repeats: true) { [weak self] _ in
            self?.now = Date()
        }
    }

    func reloadServices() { services = Config.load() }

    func share(_ service: String, hours: Int) {
        failure = nil
        do {
            try runner.start(service: service, hours: hours)
            starting = true
        } catch {
            failure = BridgeRunner.binary == nil
                ? "homenet-bridge not found. Install it with Homebrew."
                : error.localizedDescription
        }
    }

    func decide(_ ip: String, allow: Bool) { client.send(["cmd": allow ? "approve" : "deny", "ip": ip]) }
    func extend(minutes: Int) { client.send(["cmd": "extend", "minutes": minutes]) }
    func stop() { client.send(["cmd": "stop"]) }

    func remaining(until: Date) -> String {
        let minutes = max(0, Int(until.timeIntervalSince(now) / 60))
        return String(format: "%d:%02d", minutes / 60, minutes % 60)
    }
}

struct MenuContent: View {
    @ObservedObject var model: AppModel

    var body: some View {
        if let s = model.status {
            Text("Sharing \(s.service) until \(s.until.formatted(date: .omitted, time: .shortened))")
            Button("Copy \(s.address)") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(s.address, forType: .string)
            }
            Divider()
            Section("Devices") {
                if s.devices.isEmpty {
                    Text("None yet: open the address on the TV")
                }
                ForEach(s.devices) { d in DeviceMenu(device: d, model: model) }
            }
            Divider()
            Button("Extend by 1 hour") { model.extend(minutes: 60) }
            Button("Stop sharing") { model.stop() }
        } else {
            if model.starting {
                Text("Starting…")
            }
            if let f = model.failure {
                Text("Couldn't start: \(f)")
            }
            if BridgeRunner.binary == nil {
                Text("Install the CLI: brew install frigori/tap/homenet-bridge")
            } else if model.services.isEmpty {
                Button("Add services to config.json…") {
                    try? FileManager.default.createDirectory(at: Paths.configDir, withIntermediateDirectories: true)
                    NSWorkspace.shared.open(Paths.configDir)
                }
                Button("Reload config") { model.reloadServices() }
            } else {
                Menu("Share") {
                    ForEach(model.services, id: \.name) { svc in
                        Menu(svc.name) {
                            ForEach([1, 3, 4, 8], id: \.self) { h in
                                Button(h == 1 ? "For 1 hour" : "For \(h) hours") { model.share(svc.name, hours: h) }
                            }
                        }
                    }
                }
                .disabled(model.starting)
            }
        }
        Divider()
        Button("Show log") { NSWorkspace.shared.open(Paths.log) }
            .disabled(!FileManager.default.fileExists(atPath: Paths.log.path))
        Button("Quit homenet-bridge") { NSApp.terminate(nil) }
            .keyboardShortcut("q")
    }
}

struct DeviceMenu: View {
    let device: BridgeDevice
    @ObservedObject var model: AppModel

    var body: some View {
        switch device.state {
        case "pending":
            Menu("Waiting: \(device.ip) · code \(device.code)") {
                Button("Allow") { model.decide(device.ip, allow: true) }
                Button("Deny") { model.decide(device.ip, allow: false) }
            }
        case "approved":
            Menu("\(device.ip) · allowed") {
                Button("Remove access") { model.decide(device.ip, allow: false) }
            }
        default:
            Menu("\(device.ip) · denied") {
                Button("Allow") { model.decide(device.ip, allow: true) }
            }
        }
    }
}

/// The app icon's arch over a deck (assets/icon/glyph.svg), drawn as a template image so
/// the menu bar tints it. Dimmed when idle; a dot marks devices waiting for approval.
enum Glyph {
    static func image(active: Bool, pending: Bool) -> NSImage {
        // glyph.svg's viewBox is 104 x 80, from (8, 16).
        let size = NSSize(width: 21, height: 16)
        let img = NSImage(size: size, flipped: true) { _ in
            let scale = size.height / 80
            let t = NSAffineTransform()
            t.scale(by: scale)
            t.translateX(by: -8, yBy: -16)
            t.concat()
            NSColor.black.withAlphaComponent(active ? 1 : 0.55).setStroke()

            let deck = NSBezierPath()
            deck.move(to: NSPoint(x: 14, y: 82))
            deck.line(to: NSPoint(x: 106, y: 82))
            deck.lineWidth = 9
            deck.lineCapStyle = .round
            deck.stroke()

            let arch = NSBezierPath()
            arch.move(to: NSPoint(x: 26, y: 82))
            // Quadratic Q60,22 as a cubic.
            arch.curve(to: NSPoint(x: 94, y: 82),
                       controlPoint1: NSPoint(x: 26 + 2.0 / 3 * 34, y: 82 - 2.0 / 3 * 60),
                       controlPoint2: NSPoint(x: 94 - 2.0 / 3 * 34, y: 82 - 2.0 / 3 * 60))
            arch.lineWidth = 12
            arch.lineCapStyle = .round
            arch.stroke()

            if pending {
                NSColor.black.setFill()
                NSBezierPath(ovalIn: NSRect(x: 92, y: 16, width: 20, height: 20)).fill()
            }
            return true
        }
        img.isTemplate = true
        return img
    }
}

/// Posts a notification with Allow and Deny buttons for each device that starts waiting.
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    private var decide: ((String, Bool) -> Void)?
    private var notified = Set<String>()
    private var service = ""
    private let center = UNUserNotificationCenter.current()

    func setUp(decide: @escaping (String, Bool) -> Void) {
        self.decide = decide
        center.delegate = self
        let allow = UNNotificationAction(identifier: "allow", title: "Allow", options: [])
        let deny = UNNotificationAction(identifier: "deny", title: "Deny", options: [.destructive])
        center.setNotificationCategories([
            UNNotificationCategory(identifier: "device", actions: [allow, deny], intentIdentifiers: [], options: [])
        ])
        center.requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    func update(_ status: BridgeStatus?) {
        guard let status else {
            // Session over: a new one starts with new codes.
            notified.removeAll()
            center.removeAllDeliveredNotifications()
            return
        }
        service = status.service
        for d in status.devices where d.isPending && !notified.contains(d.ip) {
            notified.insert(d.ip)
            post(d)
        }
        let decided = status.devices.filter { !$0.isPending }.map { "device-" + $0.ip }
        center.removeDeliveredNotifications(withIdentifiers: decided)
    }

    private func post(_ d: BridgeDevice) {
        let content = UNMutableNotificationContent()
        content.title = "A device wants \(service)"
        var body = "\(d.ip) · code \(d.code) on its screen"
        if let hint = d.hint, !hint.isEmpty {
            body += "\n" + String(hint.prefix(60))
        }
        content.body = body
        content.categoryIdentifier = "device"
        content.userInfo = ["ip": d.ip]
        content.sound = .default
        center.add(UNNotificationRequest(identifier: "device-" + d.ip, content: content, trigger: nil))
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        if let ip = response.notification.request.content.userInfo["ip"] as? String {
            switch response.actionIdentifier {
            case "allow": DispatchQueue.main.async { self.decide?(ip, true) }
            case "deny": DispatchQueue.main.async { self.decide?(ip, false) }
            default: break
            }
        }
        completionHandler()
    }
}
