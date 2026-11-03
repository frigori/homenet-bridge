import Foundation

// What the bridge reports on its control socket (internal/control in the Go code).
struct BridgeDevice: Decodable, Identifiable, Equatable {
    let ip: String
    let code: String
    let state: String
    let hint: String?
    var id: String { ip }

    var isPending: Bool { state == "pending" }
}

struct BridgeStatus: Decodable, Equatable {
    let service: String
    let upstream: String
    let address: String
    let bridge: String
    let until: Date
    let devices: [BridgeDevice]
}

private struct Message: Decodable {
    let type: String
    let status: BridgeStatus?
    let error: String?
}

enum Paths {
    /// Same as the Go side's config.Dir().
    static let configDir = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Application Support/homenet-bridge")

    /// Same rule as control.DefaultPath: fall back to the temp dir past the socket path limit.
    static var controlSocket: String {
        let p = configDir.appendingPathComponent("control.sock").path
        if p.utf8.count > 103 {
            return (NSTemporaryDirectory() as NSString).appendingPathComponent("homenet-bridge-control.sock")
        }
        return p
    }

    static let log = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Logs/homenet-bridge.log")
}

/// Services from config.json, offered in the Share menu.
struct Config: Decodable {
    struct Service: Decodable {
        let name: String
        let upstream: String
    }
    let services: [Service]?

    static func load() -> [Service] {
        let url = Paths.configDir.appendingPathComponent("config.json")
        guard let data = try? Data(contentsOf: url),
              let cfg = try? JSONDecoder().decode(Config.self, from: data) else { return [] }
        return cfg.services ?? []
    }
}

/// Connects to the bridge's control socket, reconnecting while none is running, so it also
/// picks up a bridge started from the terminal.
final class BridgeClient {
    var onStatus: ((BridgeStatus?) -> Void)?
    var onError: ((String) -> Void)?

    private var socket: LineSocket?
    private var timer: Timer?
    private let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            guard let date = parseRFC3339(s) else {
                throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: "bad date \(s)"))
            }
            return date
        }
        return d
    }()

    func start() {
        connect()
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            self?.connect()
        }
    }

    func send(_ command: [String: Any]) {
        guard let socket, var data = try? JSONSerialization.data(withJSONObject: command) else { return }
        data.append(0x0a)
        socket.send(data)
    }

    private func connect() {
        guard socket == nil, let s = LineSocket(path: Paths.controlSocket) else { return }
        socket = s
        s.readLines(onLine: { [weak self] line in
            DispatchQueue.main.async { self?.handle(line) }
        }, onClose: { [weak self] in
            DispatchQueue.main.async {
                self?.socket = nil
                self?.onStatus?(nil)
            }
        })
    }

    private func handle(_ line: Data) {
        guard let msg = try? decoder.decode(Message.self, from: line) else { return }
        if let status = msg.status {
            onStatus?(status)
        } else if let error = msg.error {
            onError?(error)
        }
    }
}

/// Go writes RFC 3339 with up to nine fractional digits, more than ISO8601DateFormatter reads.
func parseRFC3339(_ s: String) -> Date? {
    let trimmed = s.replacingOccurrences(of: #"\.\d+"#, with: "", options: .regularExpression)
    return ISO8601DateFormatter().date(from: trimmed)
}

/// A Unix stream socket carrying newline-delimited messages.
final class LineSocket {
    private let fd: Int32
    private let lock = NSLock()
    private var closed = false

    init?(path: String) {
        fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return nil }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else {
            Darwin.close(fd)
            return nil
        }
        withUnsafeMutableBytes(of: &addr.sun_path) { buf in
            buf.copyBytes(from: bytes)
            buf[bytes.count] = 0
        }
        let len = socklen_t(MemoryLayout<sockaddr_un>.size)
        addr.sun_len = UInt8(len)
        let ok = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, len) }
        }
        guard ok == 0 else {
            Darwin.close(fd)
            return nil
        }
        var on: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
    }

    func readLines(onLine: @escaping (Data) -> Void, onClose: @escaping () -> Void) {
        let fd = self.fd
        Thread.detachNewThread { [self] in
            var buf = [UInt8](repeating: 0, count: 65536)
            var pending = Data()
            while true {
                let n = read(fd, &buf, buf.count)
                if n <= 0 { break }
                pending.append(buf, count: n)
                while let i = pending.firstIndex(of: 0x0a) {
                    onLine(pending.subdata(in: pending.startIndex..<i))
                    pending = pending.subdata(in: (i + 1)..<pending.endIndex)
                }
            }
            lock.lock()
            closed = true
            Darwin.close(fd)
            lock.unlock()
            onClose()
        }
    }

    func send(_ data: Data) {
        lock.lock()
        defer { lock.unlock() }
        guard !closed else { return }
        data.withUnsafeBytes { _ = write(fd, $0.baseAddress, data.count) }
    }
}

/// Runs `homenet-bridge share` for the Share menu. Output goes to ~/Library/Logs.
final class BridgeRunner {
    var onExit: ((String?) -> Void)?
    private(set) var process: Process?

    /// The CLI bundled in the app, else a Homebrew or install.sh one.
    static var binary: URL? {
        if let u = Bundle.main.url(forAuxiliaryExecutable: "homenet-bridge") { return u }
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return ["/opt/homebrew/bin", "/usr/local/bin", home + "/.local/bin"]
            .map { URL(fileURLWithPath: $0).appendingPathComponent("homenet-bridge") }
            .first { FileManager.default.isExecutableFile(atPath: $0.path) }
    }

    func start(service: String, hours: Int) throws {
        guard let bin = Self.binary else { throw NSError(domain: "homenet-bridge", code: 1) }
        let fm = FileManager.default
        try fm.createDirectory(at: Paths.log.deletingLastPathComponent(), withIntermediateDirectories: true)
        if !fm.fileExists(atPath: Paths.log.path) {
            fm.createFile(atPath: Paths.log.path, contents: nil)
        }
        let log = try FileHandle(forWritingTo: Paths.log)
        log.seekToEndOfFile()
        log.write("\n--- \(Date()) share \(service) for \(hours)h\n".data(using: .utf8)!)
        let start = log.offsetInFile

        let p = Process()
        p.executableURL = bin
        p.arguments = ["share", service, "--for", "\(hours)h", "--control", Paths.controlSocket]
        p.standardInput = FileHandle.nullDevice
        p.standardOutput = log
        p.standardError = log
        p.terminationHandler = { [weak self] proc in
            let failure = proc.terminationStatus == 0 ? nil : Self.error(since: start)
            DispatchQueue.main.async {
                self?.process = nil
                self?.onExit?(failure)
            }
        }
        try p.run()
        process = p
    }

    func terminate() { process?.terminate() }

    /// The CLI's "error: ..." message and hint lines from this run's log.
    private static func error(since offset: UInt64) -> String {
        guard let h = try? FileHandle(forReadingFrom: Paths.log) else { return "see the log" }
        h.seek(toFileOffset: offset)
        let text = String(decoding: h.readDataToEndOfFile(), as: UTF8.self)
        if let r = text.range(of: "error: ", options: .backwards) {
            return String(text[r.upperBound...]).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        return "see the log"
    }
}
