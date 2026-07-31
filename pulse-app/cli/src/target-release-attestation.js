import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { lstatSync, openSync, closeSync, readSync, fstatSync, constants } from 'node:fs';
import { isAbsolute, join, relative, resolve, sep } from 'node:path';

const SHA256 = /^[a-f0-9]{64}$/;
const MACH_O_MAGICS = new Set([
  'feedface', 'feedfacf', 'cefaedfe', 'cffaedfe',
  'cafebabe', 'bebafeca', 'cafebabf', 'bfbafeca',
]);

export class TargetReleaseAttestationError extends Error {
  constructor(code) {
    super(code);
    this.name = 'TargetReleaseAttestationError';
    this.code = code;
  }
}

function fail(code) { throw new TargetReleaseAttestationError(code); }

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number' && Number.isSafeInteger(value) && !Object.is(value, -0)) return String(value);
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  fail('release_attestation_value_invalid');
}

function defaultRun(command, args, options = {}) {
  return spawnSync(command, args, {
    encoding: 'utf8', env: { ...process.env, ...options.env },
    maxBuffer: 1024 * 1024, stdio: ['ignore', 'pipe', 'pipe'], timeout: 30_000,
  });
}

function safeFile(root, relativePath) {
  if (!isAbsolute(root) || resolve(root) !== root || typeof relativePath !== 'string' ||
      relativePath.length < 1 || relativePath.includes('\\') || relativePath.split('/').some((part) => !part || part === '.' || part === '..')) {
    fail('release_attestation_path_invalid');
  }
  const path = join(root, relativePath);
  const back = relative(root, path).split(sep).join('/');
  if (back !== relativePath) fail('release_attestation_path_invalid');
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.nlink !== 1) fail('release_attestation_file_invalid');
  return { path, stat };
}

function digestFile(path, expectedBytes) {
  let descriptor;
  try {
    descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    const before = fstatSync(descriptor);
    if (!before.isFile() || before.nlink !== 1 || before.size !== expectedBytes) fail('release_attestation_file_changed');
    const hash = createHash('sha256');
    const buffer = Buffer.allocUnsafe(1024 * 1024);
    let bytes = 0;
    for (;;) {
      const count = readSync(descriptor, buffer, 0, buffer.length, null);
      if (count === 0) break;
      bytes += count;
      hash.update(buffer.subarray(0, count));
    }
    const after = fstatSync(descriptor);
    if (bytes !== expectedBytes || after.size !== before.size || after.mtimeMs !== before.mtimeMs) fail('release_attestation_file_changed');
    return hash.digest('hex');
  } finally {
    if (descriptor !== undefined) closeSync(descriptor);
  }
}

function nativeCodeForPlatform(path, platform) {
  let descriptor;
  try {
    descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    const header = Buffer.alloc(4);
    const count = readSync(descriptor, header, 0, header.length, 0);
    if (count < 2) return false;
    if (platform === 'darwin') return count === 4 && MACH_O_MAGICS.has(header.toString('hex'));
    if (platform === 'win32') return header.subarray(0, 2).toString('ascii') === 'MZ';
    return false;
  } finally {
    if (descriptor !== undefined) closeSync(descriptor);
  }
}

function verifyArtifactTrees(artifacts, platform) {
  const executables = [];
  for (const [kind, artifact] of Object.entries(artifacts).sort(([left], [right]) => left.localeCompare(right))) {
    if (!artifact || !artifact.descriptor || !artifact.tree || !Array.isArray(artifact.tree.files)) fail('release_attestation_artifact_invalid');
    const treeDigest = createHash('sha256').update(canonical(artifact.tree)).digest('hex');
    if (artifact.descriptor.tree_digest !== treeDigest) fail('release_attestation_tree_digest_mismatch');
    const executableFiles = artifact.tree.files.filter((file) => file?.executable === true);
    if (typeof artifact.descriptor.executable !== 'boolean' ||
        artifact.descriptor.executable !== (executableFiles.length > 0)) {
      fail('release_attestation_executable_contract_mismatch');
    }
    for (const file of artifact.tree.files) {
      if (!Number.isSafeInteger(file.bytes) || file.bytes < 0 || typeof file.sha256 !== 'string' || !SHA256.test(file.sha256) ||
          ![0o600, 0o700].includes(file.mode) || typeof file.executable !== 'boolean') fail('release_attestation_tree_invalid');
      const actual = safeFile(artifact.root, file.path);
      if (digestFile(actual.path, file.bytes) !== file.sha256) fail('release_attestation_file_digest_mismatch');
      const primary = artifact.descriptor.executable === true && file.executable;
      if (primary || nativeCodeForPlatform(actual.path, platform)) {
        executables.push({ artifact, kind, path: actual.path, primary });
      }
    }
  }
  return executables;
}

function validateArtifactSet(artifacts) {
  if (!artifacts || Array.isArray(artifacts) || typeof artifacts !== 'object') fail('release_attestation_artifact_set_invalid');
  const kinds = Object.keys(artifacts).sort();
  const required = ['daemon', 'embedder-runtime', 'model', 'plugin-runtime'];
  const allowed = [...required, 'presence-helper'];
  if (required.some((kind) => !kinds.includes(kind)) || kinds.some((kind) => !allowed.includes(kind))) {
    fail('release_attestation_artifact_set_invalid');
  }
}

function requireSuccess(result, code) {
  if (!result || result.status !== 0) fail(code);
  return `${result.stdout ?? ''}\n${result.stderr ?? ''}`;
}

function attestApple(executables, profile, run) {
  if (profile.stapled === true) fail('apple_stapling_claim_invalid');
  if (executables.length < 1 || profile.gatekeeper !== true || profile.notarized !== true || profile.stapled !== false ||
      typeof profile.team_id !== 'string') fail('apple_release_policy_invalid');
  for (const executable of executables) {
    requireSuccess(run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=4', executable.path]), 'apple_codesign_invalid');
    const details = requireSuccess(run('/usr/bin/codesign', ['-d', '--verbose=4', executable.path]), 'apple_codesign_identity_invalid');
    const expectedIdentifier = executable.primary ? executable.artifact.descriptor.signing?.identifier : null;
    const identifierLine = details.split(/\r?\n/).find((line) => line.startsWith('Identifier='));
    if (executable.artifact.descriptor.signing?.scheme !== 'apple-developer-id' ||
        executable.artifact.descriptor.signing?.team_id !== profile.team_id ||
        (executable.primary && (!expectedIdentifier || identifierLine !== `Identifier=${expectedIdentifier}`)) ||
        (!executable.primary && !/^Identifier=[A-Za-z0-9][A-Za-z0-9._-]{0,254}$/.test(identifierLine ?? '')) ||
        !details.split(/\r?\n/).includes(`TeamIdentifier=${profile.team_id}`) ||
        !details.includes(`Authority=Developer ID Application:`)) fail('apple_codesign_identity_mismatch');
    requireSuccess(
      run('/usr/bin/codesign', ['-vvvv', '-R=notarized', '--check-notarization', executable.path]),
      'apple_notarization_evidence_missing',
    );
  }
}

function windowsPowerShellScript() {
  return `$s=Get-AuthenticodeSignature -LiteralPath $env:PULSE_ATTEST_PATH;[ordered]@{status=[string]$s.Status;signature_type=[string]$s.SignatureType;signer_subject=$s.SignerCertificate.Subject;timestamper_subject=$s.TimeStamperCertificate.Subject}|ConvertTo-Json -Compress`;
}

function attestWindows(executables, profile, run) {
  if (executables.length < 1 || profile.timestamped !== true || typeof profile.publisher !== 'string') fail('windows_release_policy_invalid');
  const encoded = Buffer.from(windowsPowerShellScript(), 'utf16le').toString('base64');
  for (const executable of executables) {
    if (executable.artifact.descriptor.signing?.scheme !== 'windows-authenticode') {
      fail('windows_authenticode_policy_mismatch');
    }
    const result = run('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-EncodedCommand', encoded], {
      env: { PULSE_ATTEST_PATH: executable.path },
    });
    requireSuccess(result, 'windows_authenticode_invalid');
    let evidence;
    try { evidence = JSON.parse(String(result.stdout ?? '').trim()); } catch { fail('windows_authenticode_evidence_invalid'); }
    if (evidence.status !== 'Valid' || evidence.signature_type !== 'Authenticode') fail('windows_authenticode_invalid');
    if (evidence.signer_subject !== profile.publisher) fail('windows_authenticode_publisher_mismatch');
    if (typeof evidence.timestamper_subject !== 'string' || evidence.timestamper_subject.length < 1) {
      fail('windows_authenticode_timestamp_missing');
    }
  }
}

export function attestSelectedTarget({
  artifacts, catalogVerified = false, manifestDigest = null, mode = 'production', platform = process.platform,
  run = defaultRun, target,
} = {}) {
  if (!target || target.platform !== platform || !target.verification_profile || !['fixture', 'production'].includes(mode)) {
    fail('release_attestation_input_invalid');
  }
  const profile = target.verification_profile;
  if (profile.kind === 'fixture') {
    if (mode !== 'fixture') fail('release_fixture_cannot_attest_production');
    if (profile.production !== false || typeof profile.fixture_id !== 'string') fail('release_fixture_policy_invalid');
    return Object.freeze({ fixture_id: profile.fixture_id, policy: 'fixture', production: false, schema: 'pulse.target_release_attestation.v1' });
  }
  if (mode !== 'production' || catalogVerified !== true || typeof manifestDigest !== 'string' || !SHA256.test(manifestDigest)) {
    fail(platform === 'linux' ? 'linux_signed_catalog_required' : 'release_signed_catalog_required');
  }
  validateArtifactSet(artifacts);
  const executables = verifyArtifactTrees(artifacts ?? {}, platform);
  if (profile.kind === 'apple' && platform === 'darwin') attestApple(executables, profile, run);
  else if (profile.kind === 'windows' && platform === 'win32') attestWindows(executables, profile, run);
  else if (profile.kind === 'linux' && platform === 'linux') {
    if (profile.policy !== 'signed-catalog-tree-v1') fail('linux_release_policy_invalid');
  } else fail('release_attestation_profile_mismatch');
  return Object.freeze({
    executables_verified: executables.length,
    manifest_digest: manifestDigest,
    policy: profile.kind === 'linux' ? profile.policy : profile.kind,
    production: true,
    schema: 'pulse.target_release_attestation.v1',
  });
}
