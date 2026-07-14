import AppKit
import CryptoKit
import Foundation
import LocalAuthentication
import Security

private let applicationTag = Data("gg.zbs.pulse.userpresence.v1".utf8)
private let accessGroup = "44N4NZ86S5.gg.zbs.pulse.userpresence"
private let allowedActions: Set<String> = [
    "binding.change", "vault.wipe", "airlock.approve",
    "mandatory.activate", "membership.change",
]

private enum HelperFailure: Error {
    case invalidRequest
    case canceled
    case keyUnavailable
    case signingFailed
}

private func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("pulse-presence-helper: \(message)\n".utf8))
    exit(1)
}

private func payloadPath() -> String {
    guard let index = CommandLine.arguments.firstIndex(of: "--payload"),
          index + 1 < CommandLine.arguments.count else {
        fail("missing payload")
    }
    return CommandLine.arguments[index + 1]
}

private func readPayload() throws -> Data {
    let path = payloadPath()
    let attributes = try FileManager.default.attributesOfItem(atPath: path)
    guard attributes[.type] as? FileAttributeType == .typeRegular,
          let size = attributes[.size] as? NSNumber,
          size.intValue > 1, size.intValue <= 1_048_576 else {
        throw HelperFailure.invalidRequest
    }
    return try Data(contentsOf: URL(fileURLWithPath: path), options: [.mappedIfSafe])
}

private func exactDictionary(_ data: Data, keys: Set<String>) throws -> [String: Any] {
    let value = try JSONSerialization.jsonObject(with: data, options: [])
    guard let object = value as? [String: Any], Set(object.keys) == keys else {
        throw HelperFailure.invalidRequest
    }
    return object
}

private func validLowerHex(_ value: Any?, count: ClosedRange<Int>) -> Bool {
    guard let string = value as? String, count.contains(string.count) else { return false }
    return string.unicodeScalars.allSatisfy { scalar in
        (scalar.value >= 48 && scalar.value <= 57) || (scalar.value >= 97 && scalar.value <= 102)
    }
}

private func validatePresenceChallenge(_ data: Data) throws -> String {
    let object = try exactDictionary(data, keys: ["action", "digest", "expires_at", "nonce", "policy_epoch", "schema"])
    guard object["schema"] as? String == "pulse.user-presence.challenge.v1",
          let action = object["action"] as? String, allowedActions.contains(action),
          validLowerHex(object["digest"], count: 64...64),
          validLowerHex(object["nonce"], count: 32...128),
          let epoch = object["policy_epoch"] as? NSNumber,
          CFGetTypeID(epoch) != CFBooleanGetTypeID(),
          epoch.doubleValue == Double(epoch.uint64Value), epoch.uint64Value >= 1,
          let expiry = object["expires_at"] as? String,
          ISO8601DateFormatter().date(from: expiry) != nil else {
        throw HelperFailure.invalidRequest
    }
    return action
}

private func validateBindingRegistry(_ data: Data) throws -> Int {
    let object = try exactDictionary(data, keys: ["bindings", "epoch", "schema"])
    guard object["schema"] as? String == "pulse.workspace-binding-registry.v1",
          let epoch = object["epoch"] as? NSNumber,
          CFGetTypeID(epoch) != CFBooleanGetTypeID(),
          epoch.doubleValue == Double(epoch.uint64Value), epoch.uint64Value >= 1,
          let bindings = object["bindings"] as? [[String: Any]], !bindings.isEmpty, bindings.count <= 128 else {
        throw HelperFailure.invalidRequest
    }
    for binding in bindings {
        guard binding["binding_id"] is String,
              binding["receipt_id"] is String,
              binding["workspace"] is [String: Any],
              let mode = binding["mode"] as? String,
              mode == "personal" || mode == "team" else {
            throw HelperFailure.invalidRequest
        }
    }
    return bindings.count
}

private func digestHex(_ data: Data) -> String {
    SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
}

@MainActor
private func reviewExactBytes(title: String, summary: String, data: Data) throws {
    let alert = NSAlert()
    alert.messageText = title
    alert.informativeText = summary
    alert.alertStyle = .warning
    alert.addButton(withTitle: "Approve exact bytes")
    alert.addButton(withTitle: "Cancel")

    let scroll = NSScrollView(frame: NSRect(x: 0, y: 0, width: 640, height: 320))
    scroll.hasVerticalScroller = true
    scroll.borderType = .bezelBorder
    let text = NSTextView(frame: scroll.bounds)
    text.isEditable = false
    text.isSelectable = true
    text.font = NSFont.monospacedSystemFont(ofSize: 11, weight: .regular)
    if let value = try? JSONSerialization.jsonObject(with: data),
       let pretty = try? JSONSerialization.data(withJSONObject: value, options: [.prettyPrinted, .sortedKeys]),
       let rendered = String(data: pretty, encoding: .utf8) {
        text.string = rendered
    } else {
        text.string = data.base64EncodedString()
    }
    scroll.documentView = text
    alert.accessoryView = scroll
    NSApplication.shared.activate(ignoringOtherApps: true)
    guard alert.runModal() == .alertFirstButtonReturn else {
        throw HelperFailure.canceled
    }
}

private func privateKey(reason: String) throws -> SecKey {
    let context = LAContext()
    context.localizedReason = reason
    let query: [String: Any] = [
        kSecClass as String: kSecClassKey,
        kSecAttrApplicationTag as String: applicationTag,
        kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
        kSecAttrAccessGroup as String: accessGroup,
        kSecReturnRef as String: true,
        kSecUseAuthenticationContext as String: context,
    ]
    var item: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &item)
    if status == errSecSuccess, let key = item as! SecKey? {
        return key
    }
    guard status == errSecItemNotFound else { throw HelperFailure.keyUnavailable }

    var accessError: Unmanaged<CFError>?
    guard let control = SecAccessControlCreateWithFlags(
        nil,
        kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
        [.privateKeyUsage, .userPresence],
        &accessError
    ) else { throw HelperFailure.keyUnavailable }
    let attributes: [String: Any] = [
        kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
        kSecAttrKeySizeInBits as String: 256,
        kSecAttrTokenID as String: kSecAttrTokenIDSecureEnclave,
        kSecPrivateKeyAttrs as String: [
            kSecAttrIsPermanent as String: true,
            kSecAttrApplicationTag as String: applicationTag,
            kSecAttrAccessGroup as String: accessGroup,
            kSecAttrAccessControl as String: control,
        ],
    ]
    var keyError: Unmanaged<CFError>?
    guard let key = SecKeyCreateRandomKey(attributes as CFDictionary, &keyError) else {
        throw HelperFailure.keyUnavailable
    }
    return key
}

private func sign(_ data: Data, reason: String) throws -> (signature: Data, publicKey: SecKey) {
    let key = try privateKey(reason: reason)
    var error: Unmanaged<CFError>?
    guard let signature = SecKeyCreateSignature(
        key,
        .ecdsaSignatureMessageX962SHA256,
        data as CFData,
        &error
    ) as Data?, let publicKey = SecKeyCopyPublicKey(key) else {
        throw HelperFailure.signingFailed
    }
    return (signature, publicKey)
}

private func subjectPublicKeyInfo(_ key: SecKey) throws -> Data {
    var error: Unmanaged<CFError>?
    guard let raw = SecKeyCopyExternalRepresentation(key, &error) as Data?, raw.count == 65 else {
        throw HelperFailure.keyUnavailable
    }
    let prefix: [UInt8] = [
        0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01,
        0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00,
    ]
    return Data(prefix) + raw
}

private func pem(_ der: Data) -> String {
    let base64 = der.base64EncodedString(options: [.lineLength64Characters, .endLineWithLineFeed])
    return "-----BEGIN PUBLIC KEY-----\n\(base64)-----END PUBLIC KEY-----\n"
}

private func emitResult(signature: Data, publicKey: SecKey) throws {
    let der = try subjectPublicKeyInfo(publicKey)
    let result: [String: Any] = [
        "algorithm": "es256",
        "key_id": digestHex(der),
        "signature": signature.base64EncodedString(),
    ]
    let bytes = try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys])
    FileHandle.standardOutput.write(bytes)
    FileHandle.standardOutput.write(Data("\n".utf8))
}

private func run() async throws {
    guard CommandLine.arguments.count >= 2 else { throw HelperFailure.invalidRequest }
    let command = CommandLine.arguments[1]
    if command == "public-key" {
        let key = try privateKey(reason: "Install the Pulse user-presence trust key")
        guard let publicKey = SecKeyCopyPublicKey(key) else { throw HelperFailure.keyUnavailable }
        FileHandle.standardOutput.write(Data(pem(try subjectPublicKeyInfo(publicKey)).utf8))
        return
    }

    let data = try readPayload()
    let digest = digestHex(data)
    switch command {
    case "prove":
        let action = try validatePresenceChallenge(data)
        try await reviewExactBytes(
            title: "Pulse privileged action",
            summary: "Action: \(action)\nExact SHA-256: \(digest)",
            data: data
        )
        let proof = try sign(data, reason: "Approve Pulse action \(action), digest \(digest.prefix(16))")
        try emitResult(signature: proof.signature, publicKey: proof.publicKey)
    case "sign-binding-registry":
        let count = try validateBindingRegistry(data)
        try await reviewExactBytes(
            title: "Change Pulse workspace bindings",
            summary: "Bindings: \(count)\nExact SHA-256: \(digest)",
            data: data
        )
        let proof = try sign(data, reason: "Approve \(count) Pulse bindings, digest \(digest.prefix(16))")
        try emitResult(signature: proof.signature, publicKey: proof.publicKey)
    default:
        throw HelperFailure.invalidRequest
    }
}

@main
private enum PulsePresenceHelper {
    static func main() async {
        do {
            try await run()
        } catch HelperFailure.canceled {
            fail("canceled")
        } catch {
            fail("request denied")
        }
    }
}
