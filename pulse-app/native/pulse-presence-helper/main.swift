import AppKit
import CryptoKit
import Foundation
import LocalAuthentication
import Security

private let applicationTag = Data("gg.zbs.pulse.userpresence.v1".utf8)
private let dpopApplicationTagPrefix = Data("gg.zbs.pulse.dpop.v1.".utf8)
private let dpopMetadataService = "gg.zbs.pulse.dpop.metadata.v1"
private let helperContractVersion = 2
private let helperCapabilities = [
    "dpop-create", "dpop-delete", "dpop-proof", "dpop-public",
    "prove", "public-key", "self-test", "sign-binding-registry",
]
private let allowedActions: Set<String> = [
    "binding.change", "vault.wipe", "airlock.approve",
    "mandatory.activate", "membership.change",
]
private let allowedDPoPOwnerPaths: Set<String> = [
    "/owner/v1/approval",
    "/owner/v1/bootstrap",
    "/owner/v1/activate",
    "/owner/v1/members",
    "/owner/v1/bindings",
    "/owner/v1/services",
    "/owner/v1/projects",
    "/owner/v1/project-grants",
    "/owner/v1/shared-delete",
    "/owner/v1/audit",
    "/owner/v1/deletion-status",
]

private enum HelperFailure: Error {
    case invalidRequest
    case canceled
    case keyUnavailable
    case signingFailed
}

private let safeDPoPKeyRef = try! NSRegularExpression(pattern: "^[A-Za-z0-9][A-Za-z0-9._:/-]{0,511}$")
private let safeDPoPIdentity = try! NSRegularExpression(pattern: "^[A-Za-z0-9][A-Za-z0-9._|:@/-]{0,255}$")
private let safeBase64URL = try! NSRegularExpression(pattern: "^[A-Za-z0-9_-]+$")
private let safeNonce = try! NSRegularExpression(pattern: "^[A-Za-z0-9._~-]{1,512}$")

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

private func base64URL(_ data: Data) -> String {
    data.base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
}

private func decodeBase64URL(_ value: Any?, maximumBytes: Int) throws -> Data {
    guard let string = value as? String, !string.isEmpty,
          string.count <= ((maximumBytes + 2) / 3) * 4,
          string.unicodeScalars.allSatisfy({
              ($0.value >= 48 && $0.value <= 57) ||
              ($0.value >= 65 && $0.value <= 90) ||
              ($0.value >= 97 && $0.value <= 122) || $0 == "-" || $0 == "_"
          }) else { throw HelperFailure.invalidRequest }
    let remainder = string.count % 4
    guard remainder != 1 else { throw HelperFailure.invalidRequest }
    let standard = string.replacingOccurrences(of: "-", with: "+")
        .replacingOccurrences(of: "_", with: "/") + String(repeating: "=", count: (4 - remainder) % 4)
    guard let decoded = Data(base64Encoded: standard), decoded.count <= maximumBytes,
          base64URL(decoded) == string else { throw HelperFailure.invalidRequest }
    return decoded
}

private func validatedDPoPKeyRef(_ value: Any?) throws -> String {
    guard let keyRef = value as? String, keyRef.utf8.count <= 512,
          safeDPoPKeyRef.firstMatch(
              in: keyRef, range: NSRange(keyRef.startIndex..<keyRef.endIndex, in: keyRef)
          ) != nil else { throw HelperFailure.invalidRequest }
    return keyRef
}

private func matches(_ expression: NSRegularExpression, _ value: String) -> Bool {
    expression.firstMatch(
        in: value, range: NSRange(value.startIndex..<value.endIndex, in: value)
    ) != nil
}

private func validatedIdentity(_ value: Any?) throws -> String {
    guard let text = value as? String, text.utf8.count <= 256,
          matches(safeDPoPIdentity, text) else { throw HelperFailure.invalidRequest }
    return text
}

private func validatedHTTPSURL(_ value: Any?, requireMCP: Bool) throws -> String {
    guard let text = value as? String, text.utf8.count <= 2048,
          let components = URLComponents(string: text), components.scheme == "https",
          components.user == nil, components.password == nil,
          components.query == nil, components.fragment == nil,
          let host = components.host, !host.isEmpty,
          let url = components.url, url.absoluteString == text,
          (!requireMCP || components.path == "/mcp") else {
        throw HelperFailure.invalidRequest
    }
    return text
}

// Installation metadata pins the OAuth audience to the exact /mcp resource.
// Owner administration shares that resource's origin, but only these exact,
// query-free public routes may receive a proof from the same installation key.
private func validatedDPoPResourceTarget(_ value: Any?, pinnedResource: String) throws -> String {
    let resource = try validatedHTTPSURL(pinnedResource, requireMCP: true)
    let target = try validatedHTTPSURL(value, requireMCP: false)
    if target == resource { return target }

    guard let resourceComponents = URLComponents(string: resource),
          let targetComponents = URLComponents(string: target),
          resourceComponents.scheme == targetComponents.scheme,
          resourceComponents.host?.caseInsensitiveCompare(targetComponents.host ?? "") == .orderedSame,
          (resourceComponents.port ?? 443) == (targetComponents.port ?? 443),
          allowedDPoPOwnerPaths.contains(targetComponents.percentEncodedPath) else {
        throw HelperFailure.invalidRequest
    }
    return target
}

private func validatedBase64URL(_ value: Any?, exactCount: Int? = nil, maximumCount: Int = 512) throws -> String {
    guard let text = value as? String, !text.isEmpty, text.count <= maximumCount,
          exactCount == nil || text.count == exactCount,
          matches(safeBase64URL, text) else { throw HelperFailure.invalidRequest }
    return text
}

private func dpopApplicationTag(_ keyRef: String) -> Data {
    dpopApplicationTagPrefix + Data(SHA256.hash(data: Data(keyRef.utf8)))
}

private func dpopPrivateKey(keyRef: String, create: Bool) throws -> SecKey {
    let tag = dpopApplicationTag(keyRef)
    let query: [String: Any] = [
        kSecClass as String: kSecClassKey,
        kSecAttrApplicationTag as String: tag,
        kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
        kSecReturnRef as String: true,
        kSecUseDataProtectionKeychain as String: true,
    ]
    var item: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &item)
    if status == errSecSuccess, let key = item as! SecKey? { return key }
    guard status == errSecItemNotFound, create else { throw HelperFailure.keyUnavailable }

    var accessError: Unmanaged<CFError>?
    guard let control = SecAccessControlCreateWithFlags(
        nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, [.privateKeyUsage], &accessError
    ) else { throw HelperFailure.keyUnavailable }
    let attributes: [String: Any] = [
        kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
        kSecAttrKeySizeInBits as String: 256,
        kSecAttrTokenID as String: kSecAttrTokenIDSecureEnclave,
        kSecUseDataProtectionKeychain as String: true,
        kSecPrivateKeyAttrs as String: [
            kSecAttrIsPermanent as String: true,
            kSecAttrApplicationTag as String: tag,
            kSecAttrAccessControl as String: control,
        ],
    ]
    var keyError: Unmanaged<CFError>?
    guard let key = SecKeyCreateRandomKey(attributes as CFDictionary, &keyError) else {
        throw HelperFailure.keyUnavailable
    }
    return key
}

private func dpopMetadataQuery(_ keyRef: String) -> [String: Any] {
    [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: dpopMetadataService,
        kSecAttrAccount as String: keyRef,
        kSecUseDataProtectionKeychain as String: true,
    ]
}

private func dpopMetadata(_ keyRef: String) throws -> [String: Any] {
    var query = dpopMetadataQuery(keyRef)
    query[kSecReturnData as String] = true
    query[kSecMatchLimit as String] = kSecMatchLimitOne
    var item: CFTypeRef?
    guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
          let data = item as? Data else { throw HelperFailure.keyUnavailable }
    return try exactDictionary(
        data, keys: ["client_id", "resource", "schema", "subject", "token_endpoint"]
    )
}

private func storeDPoPMetadata(_ keyRef: String, _ metadata: [String: Any]) throws {
    let data = try JSONSerialization.data(withJSONObject: metadata, options: [.sortedKeys])
    var query = dpopMetadataQuery(keyRef)
    query[kSecValueData as String] = data
    query[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
    let status = SecItemAdd(query as CFDictionary, nil)
    if status == errSecDuplicateItem {
        let existing = try dpopMetadata(keyRef)
        let existingData = try JSONSerialization.data(withJSONObject: existing, options: [.sortedKeys])
        guard existingData == data else { throw HelperFailure.invalidRequest }
        return
    }
    guard status == errSecSuccess else { throw HelperFailure.keyUnavailable }
}

private func dpopPublicJWK(_ privateKey: SecKey) throws -> [String: String] {
    guard let publicKey = SecKeyCopyPublicKey(privateKey) else { throw HelperFailure.keyUnavailable }
    var error: Unmanaged<CFError>?
    guard let raw = SecKeyCopyExternalRepresentation(publicKey, &error) as Data?,
          raw.count == 65, raw.first == 0x04 else { throw HelperFailure.keyUnavailable }
    return [
        "crv": "P-256",
        "kty": "EC",
        "x": base64URL(raw.subdata(in: 1..<33)),
        "y": base64URL(raw.subdata(in: 33..<65)),
    ]
}

private func emitJSON(_ value: [String: Any]) throws {
    let bytes = try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
    FileHandle.standardOutput.write(bytes)
    FileHandle.standardOutput.write(Data("\n".utf8))
}

private func dpopPublicResult(keyRef: String, privateKey: SecKey) throws -> [String: Any] {
    [
        "key_ref": keyRef,
        "public_jwk": try dpopPublicJWK(privateKey),
        "schema": "pulse.dpop.public.v1",
    ]
}

private func readDERLength(_ bytes: [UInt8], _ index: inout Int) throws -> Int {
    guard index < bytes.count else { throw HelperFailure.signingFailed }
    let first = Int(bytes[index]); index += 1
    if first < 0x80 { return first }
    let count = first & 0x7f
    guard count > 0, count <= 2, index + count <= bytes.count, bytes[index] != 0 else {
        throw HelperFailure.signingFailed
    }
    var length = 0
    for _ in 0..<count { length = (length << 8) | Int(bytes[index]); index += 1 }
    guard length >= 0x80 else { throw HelperFailure.signingFailed }
    return length
}

private func readDERInteger(_ bytes: [UInt8], _ index: inout Int) throws -> [UInt8] {
    guard index < bytes.count, bytes[index] == 0x02 else { throw HelperFailure.signingFailed }
    index += 1
    let length = try readDERLength(bytes, &index)
    guard length > 0, length <= 33, index + length <= bytes.count else { throw HelperFailure.signingFailed }
    var value = Array(bytes[index..<(index + length)]); index += length
    guard (value[0] & 0x80) == 0 else { throw HelperFailure.signingFailed }
    if value.count == 33 {
        guard value[0] == 0, (value[1] & 0x80) != 0 else { throw HelperFailure.signingFailed }
        value.removeFirst()
    } else if value.count > 1, value[0] == 0, (value[1] & 0x80) == 0 {
        throw HelperFailure.signingFailed
    }
    return Array(repeating: 0, count: 32 - value.count) + value
}

private func p1363Signature(_ der: Data) throws -> Data {
    let bytes = [UInt8](der)
    var index = 0
    guard index < bytes.count, bytes[index] == 0x30 else { throw HelperFailure.signingFailed }
    index += 1
    let sequenceLength = try readDERLength(bytes, &index)
    guard index + sequenceLength == bytes.count else { throw HelperFailure.signingFailed }
    let r = try readDERInteger(bytes, &index)
    let s = try readDERInteger(bytes, &index)
    guard index == bytes.count else { throw HelperFailure.signingFailed }
    return Data(r + s)
}

private func runPureSelfTest() throws {
    let small = Data([0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x02])
    let smallExpected = Data(Array(repeating: 0, count: 31) + [0x01] +
        Array(repeating: 0, count: 31) + [0x02])
    guard try p1363Signature(small) == smallExpected else { throw HelperFailure.signingFailed }

    let highR = [UInt8]([0x30, 0x45, 0x02, 0x21, 0x00, 0x80] +
        Array(repeating: 0, count: 31) + [0x02, 0x20, 0x7f] +
        Array(repeating: 0, count: 31))
    let highExpected = Data([0x80] + Array(repeating: 0, count: 31) +
        [0x7f] + Array(repeating: 0, count: 31))
    guard try p1363Signature(Data(highR)) == highExpected else { throw HelperFailure.signingFailed }

    let malformed = [
        Data([0x30, 0x06, 0x02, 0x01, 0x80, 0x02, 0x01, 0x01]),
        Data([0x30, 0x07, 0x02, 0x02, 0x00, 0x01, 0x02, 0x01, 0x01]),
        Data([0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x02, 0x00]),
    ]
    for value in malformed {
        var rejected = false
        do {
            _ = try p1363Signature(value)
        } catch HelperFailure.signingFailed {
            rejected = true
        }
        guard rejected else { throw HelperFailure.signingFailed }
    }

	let policyNow: Int64 = 1_784_064_000
	let metadata: [String: Any] = [
		"client_id": "client-pulse",
		"resource": "https://team.example/mcp",
		"schema": "pulse.dpop.metadata.v1",
		"subject": "principal-nik",
		"token_endpoint": "https://issuer.example/oauth/token",
	]
	func policyData(_ value: [String: Any]) throws -> Data {
		try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
	}
	func policyRejects(_ value: [String: Any]) -> Bool {
		do {
			_ = try validateDPoPProofPolicy(try policyData(value), metadata: metadata, now: policyNow)
			return false
		} catch HelperFailure.invalidRequest {
			return true
		} catch {
			return false
		}
	}
	let resourceRequest: [String: Any] = [
		"ath": String(repeating: "a", count: 43),
		"client_id": "client-pulse",
		"enrollment_generation": 1,
		"enrollment_id": "enrollment-1",
		"htm": "POST",
		"htu": "https://team.example/mcp",
		"iat": policyNow,
		"jti": "resource-jti",
		"key_ref": "team/deployment/client/principal",
		"nonce": "",
		"purpose": "resource",
		"schema": "pulse.dpop.proof.v1",
		"sub": "principal-nik",
	]
	let resource = try validateDPoPProofPolicy(
		try policyData(resourceRequest), metadata: metadata, now: policyNow
	)
	guard resource.keyRef == "team/deployment/client/principal",
		  resource.payload["htu"] as? String == "https://team.example/mcp" else {
		throw HelperFailure.signingFailed
	}
	for path in allowedDPoPOwnerPaths.sorted() {
		var ownerRequest = resourceRequest
		ownerRequest["htu"] = "https://team.example\(path)"
		let owner = try validateDPoPProofPolicy(
			try policyData(ownerRequest), metadata: metadata, now: policyNow
		)
		guard owner.payload["htu"] as? String == "https://team.example\(path)" else {
			throw HelperFailure.signingFailed
		}
	}
	var tokenRequest = resourceRequest
	tokenRequest["ath"] = ""
	tokenRequest["enrollment_generation"] = 0
	tokenRequest["enrollment_id"] = ""
	tokenRequest["htu"] = "https://issuer.example/oauth/token"
	tokenRequest["nonce"] = "token-nonce"
	tokenRequest["purpose"] = "token"
	let token = try validateDPoPProofPolicy(
		try policyData(tokenRequest), metadata: metadata, now: policyNow
	)
	guard token.payload["htu"] as? String == "https://issuer.example/oauth/token",
		  token.payload["nonce"] as? String == "token-nonce" else {
		throw HelperFailure.signingFailed
	}

	var invalid = resourceRequest
	invalid["htu"] = "https://other.example/mcp"
	guard policyRejects(invalid) else { throw HelperFailure.signingFailed }
	invalid = tokenRequest
	invalid["htu"] = "https://issuer.example/other"
	guard policyRejects(invalid) else { throw HelperFailure.signingFailed }
	invalid = resourceRequest
	invalid["client_id"] = "client-other"
	guard policyRejects(invalid) else { throw HelperFailure.signingFailed }
	invalid = resourceRequest
	invalid["sub"] = "principal-other"
	guard policyRejects(invalid) else { throw HelperFailure.signingFailed }
	invalid = resourceRequest
	invalid["htm"] = "PATCH"
	guard policyRejects(invalid) else { throw HelperFailure.signingFailed }
	invalid = tokenRequest
	invalid["htm"] = "GET"
	guard policyRejects(invalid) else { throw HelperFailure.signingFailed }
	for target in [
		"https://other.example/owner/v1/members",
		"https://team.example/owner/v1/members/",
		"https://team.example/owner/v1/members?debug=1",
		"https://team.example/owner/v1/unknown",
	] {
		invalid = resourceRequest
		invalid["htu"] = target
		guard policyRejects(invalid) else { throw HelperFailure.signingFailed }
	}
	invalid = resourceRequest
	invalid["htu"] = "https://team.example/owner/v1/members"
	invalid["htm"] = "GET"
	guard policyRejects(invalid) else { throw HelperFailure.signingFailed }
}

private func deleteDPoPKey(_ keyRef: String) throws {
    let query: [String: Any] = [
        kSecClass as String: kSecClassKey,
        kSecAttrApplicationTag as String: dpopApplicationTag(keyRef),
        kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
        kSecUseDataProtectionKeychain as String: true,
    ]
    let status = SecItemDelete(query as CFDictionary)
    guard status == errSecSuccess || status == errSecItemNotFound else { throw HelperFailure.keyUnavailable }
    let metadataStatus = SecItemDelete(dpopMetadataQuery(keyRef) as CFDictionary)
    guard metadataStatus == errSecSuccess || metadataStatus == errSecItemNotFound else {
        throw HelperFailure.keyUnavailable
    }
}

private struct ValidatedDPoPProof {
    let keyRef: String
    let payload: [String: Any]
}

// validateDPoPProofPolicy is deliberately pure: native self-tests can exercise
// the exact resource, token-endpoint, client, subject, and method pins without
// touching Keychain or Secure Enclave state.
private func validateDPoPProofPolicy(
    _ data: Data,
    metadata: [String: Any],
    now: Int64
) throws -> ValidatedDPoPProof {
    let keys: Set<String> = [
        "ath", "client_id", "enrollment_generation", "enrollment_id", "htm", "htu",
        "iat", "jti", "key_ref", "nonce", "purpose", "schema", "sub",
    ]
    let object = try exactDictionary(data, keys: keys)
    guard object["schema"] as? String == "pulse.dpop.proof.v1" else {
        throw HelperFailure.invalidRequest
    }
    let keyRef = try validatedDPoPKeyRef(object["key_ref"])
    guard metadata["schema"] as? String == "pulse.dpop.metadata.v1",
          let pinnedResource = metadata["resource"] as? String,
          let pinnedTokenEndpoint = metadata["token_endpoint"] as? String,
          let pinnedClientID = metadata["client_id"] as? String,
          let pinnedSubject = metadata["subject"] as? String else {
        throw HelperFailure.invalidRequest
    }
    let clientID = try validatedIdentity(object["client_id"])
    let subject = try validatedIdentity(object["sub"])
    guard clientID == pinnedClientID, subject == pinnedSubject,
          let purpose = object["purpose"] as? String,
          let method = object["htm"] as? String,
          let issuedAt = object["iat"] as? NSNumber,
          CFGetTypeID(issuedAt) != CFBooleanGetTypeID(),
          issuedAt.doubleValue == Double(issuedAt.int64Value),
          abs(now - issuedAt.int64Value) <= 300 else {
        throw HelperFailure.invalidRequest
    }
    let jti = try validatedBase64URL(object["jti"], maximumCount: 128)
    var payload: [String: Any] = [
        "client_id": clientID,
        "htm": method,
        "iat": issuedAt.int64Value,
        "jti": jti,
        "sub": subject,
    ]
    if purpose == "resource" {
        let target = try validatedDPoPResourceTarget(object["htu"], pinnedResource: pinnedResource)
        let isPinnedMCPResource = target == pinnedResource
        guard (isPinnedMCPResource && (method == "GET" || method == "POST" || method == "DELETE")) ||
              (!isPinnedMCPResource && method == "POST"),
              object["nonce"] as? String == "" else { throw HelperFailure.invalidRequest }
        payload["htu"] = target
        payload["ath"] = try validatedBase64URL(object["ath"], exactCount: 43)
        payload["enrollment_id"] = try validatedIdentity(object["enrollment_id"])
        guard let generation = object["enrollment_generation"] as? NSNumber,
              CFGetTypeID(generation) != CFBooleanGetTypeID(),
              generation.doubleValue == Double(generation.uint64Value), generation.uint64Value >= 1 else {
            throw HelperFailure.invalidRequest
        }
        payload["enrollment_generation"] = generation.uint64Value
    } else if purpose == "token" {
        guard method == "POST", object["htu"] as? String == pinnedTokenEndpoint,
              object["ath"] as? String == "", object["enrollment_id"] as? String == "",
              (object["enrollment_generation"] as? NSNumber)?.intValue == 0 else {
            throw HelperFailure.invalidRequest
        }
        payload["htu"] = pinnedTokenEndpoint
        if let nonce = object["nonce"] as? String, !nonce.isEmpty {
            guard matches(safeNonce, nonce) else { throw HelperFailure.invalidRequest }
            payload["nonce"] = nonce
        }
    } else {
        throw HelperFailure.invalidRequest
    }
    return ValidatedDPoPProof(keyRef: keyRef, payload: payload)
}

private func dpopProof(_ data: Data) throws -> [String: Any] {
    let keys: Set<String> = [
        "ath", "client_id", "enrollment_generation", "enrollment_id", "htm", "htu",
        "iat", "jti", "key_ref", "nonce", "purpose", "schema", "sub",
    ]
    let object = try exactDictionary(data, keys: keys)
    let keyRef = try validatedDPoPKeyRef(object["key_ref"])
    let metadata = try dpopMetadata(keyRef)
    let validated = try validateDPoPProofPolicy(
        data, metadata: metadata, now: Int64(Date().timeIntervalSince1970)
    )
    let key = try dpopPrivateKey(keyRef: validated.keyRef, create: false)
    let publicJWK = try dpopPublicJWK(key)
    let header: [String: Any] = ["alg": "ES256", "jwk": publicJWK, "typ": "dpop+jwt"]
    let headerBytes = try JSONSerialization.data(withJSONObject: header, options: [.sortedKeys])
    let payloadBytes = try JSONSerialization.data(withJSONObject: validated.payload, options: [.sortedKeys])
    let signingInput = "\(base64URL(headerBytes)).\(base64URL(payloadBytes))"
    var error: Unmanaged<CFError>?
    guard let der = SecKeyCreateSignature(
        key, .ecdsaSignatureMessageX962SHA256, Data(signingInput.utf8) as CFData, &error
    ) as Data? else { throw HelperFailure.signingFailed }
    let proof = "\(signingInput).\(base64URL(try p1363Signature(der)))"
    return ["key_ref": validated.keyRef, "proof": proof, "schema": "pulse.dpop.proof_result.v1"]
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
    if command == "contract" {
        try emitJSON([
            "capabilities": helperCapabilities,
            "schema": "pulse.presence_helper.contract.v1",
            "version": helperContractVersion,
        ])
        return
    }
    if command == "self-test" {
        try runPureSelfTest()
        try emitJSON([
            "schema": "pulse.presence_helper.self_test.v1",
            "status": "pass",
            "vectors": 29,
        ])
        return
    }
    if command == "public-key" {
        let key = try privateKey(reason: "Install the Pulse user-presence trust key")
        guard let publicKey = SecKeyCopyPublicKey(key) else { throw HelperFailure.keyUnavailable }
        FileHandle.standardOutput.write(Data(pem(try subjectPublicKeyInfo(publicKey)).utf8))
        return
    }

    let data = try readPayload()
    if command == "dpop-create" || command == "dpop-public" || command == "dpop-delete" {
        let expectedSchema = command == "dpop-create"
            ? "pulse.dpop.create.v2"
            : command == "dpop-public" ? "pulse.dpop.key_ref.v1" : "pulse.dpop.delete.v1"
        let expectedKeys: Set<String> = command == "dpop-create"
            ? ["client_id", "key_ref", "resource", "schema", "subject", "token_endpoint"]
            : ["key_ref", "schema"]
        let object = try exactDictionary(data, keys: expectedKeys)
        guard object["schema"] as? String == expectedSchema else { throw HelperFailure.invalidRequest }
        let keyRef = try validatedDPoPKeyRef(object["key_ref"])
        if command == "dpop-delete" {
            try deleteDPoPKey(keyRef)
            try emitJSON(["key_ref": keyRef, "schema": "pulse.dpop.deleted.v1"])
        } else {
            let key = try dpopPrivateKey(keyRef: keyRef, create: command == "dpop-create")
            if command == "dpop-create" {
                let metadata: [String: Any] = [
                    "client_id": try validatedIdentity(object["client_id"]),
                    "resource": try validatedHTTPSURL(object["resource"], requireMCP: true),
                    "schema": "pulse.dpop.metadata.v1",
                    "subject": try validatedIdentity(object["subject"]),
                    "token_endpoint": try validatedHTTPSURL(object["token_endpoint"], requireMCP: false),
                ]
                try storeDPoPMetadata(keyRef, metadata)
            }
            try emitJSON(dpopPublicResult(keyRef: keyRef, privateKey: key))
        }
        return
    }
    if command == "dpop-proof" {
        try emitJSON(dpopProof(data))
        return
    }
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
