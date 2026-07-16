#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, lstatSync, mkdirSync, readFileSync, renameSync, writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { readCommittedArtifactSet } from '../src/artifact-installer.js';
import { vaultRuntimeFromBinding } from '../src/local-supervisor.js';
import { inspectPersonalRelease } from '../src/personal-runtime-installer.js';
import { inspectPresenceTrust } from '../src/trust-helper.js';
import { resolveWorkspaceBinding } from '../src/workspace-binding.js';

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const cli = join(packageRoot, 'src', 'cli.js');
const packageJSON = JSON.parse(readFileSync(join(packageRoot, 'package.json'), 'utf8'));
const dataDir = resolve(process.env.PULSE_DATA_DIR || join(homedir(), '.pulse'));

function fail(code, detail = '') {
  const error = new Error(detail ? `${code}: ${detail}` : code);
  error.code = code;
  throw error;
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd ?? process.cwd(),
    env: options.env ?? process.env,
    encoding: 'utf8',
    stdio: ['inherit', 'pipe', 'pipe'],
    timeout: options.timeout ?? 120_000,
    maxBuffer: 8 * 1024 * 1024,
  });
  if (result.status !== 0) {
    fail(options.code ?? 'physical_attestation_command_failed',
      `${command} ${args.join(' ')} exited ${result.status}: ${String(result.stderr).trim()}`);
  }
  return result;
}

function rejectSyntheticAuthority() {
  const forbidden = Object.keys(process.env).filter((name) =>
    name === 'PULSE_TRUST_MODE' || name === 'PULSE_RELEASE_TEST_MODE' ||
    name.startsWith('PULSE_RELEASE_TEST_') || name.startsWith('PULSE_TEST_'));
  if (forbidden.length > 0) fail('synthetic_authority_forbidden', forbidden.sort().join(', '));
}

function exactJSON(stdout, code) {
  try { return JSON.parse(stdout); } catch { fail(code); }
}

function verifyInstalledNativeExecutables(committed) {
  let verified = 0;
  for (const kind of ['daemon', 'embedder-runtime', 'presence-helper']) {
    const activation = committed.activations[kind];
    if (!activation) fail('physical_attestation_native_activation_missing', kind);
    const metadata = exactJSON(readFileSync(join(activation.version_path, 'activation.json'), 'utf8'),
      'physical_attestation_activation_metadata_invalid');
    const executables = metadata?.tree?.files?.filter((entry) => entry.executable === true) ?? [];
    if (executables.length < 1) fail('physical_attestation_native_executable_missing', kind);
    for (const entry of executables) {
      const executable = join(activation.version_path, entry.path);
      run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', executable], {
        code: 'physical_attestation_codesign_failed',
      });
      run('/usr/sbin/spctl', ['-a', '-t', 'execute', '-vv', executable], {
        code: 'physical_attestation_gatekeeper_failed',
      });
      verified += 1;
    }
  }
  return verified;
}

async function productJSON(runtime, secret, path) {
  const response = await fetch(`${runtime.base_url}${path}`, {
    method: 'GET',
    headers: { Accept: 'application/json', 'X-Pulse-Key': secret },
    signal: AbortSignal.timeout(5000),
  });
  if (!response.ok) fail('physical_attestation_product_query_failed', `${path}: ${response.status}`);
  return response.json();
}

function writeReceipt(path, receipt) {
  if (!path) return;
  if (!isAbsolute(path) || resolve(path) !== path) fail('physical_attestation_receipt_path_invalid');
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const directory = lstatSync(dirname(path));
  if (!directory.isDirectory() || directory.isSymbolicLink() || (directory.mode & 0o077) !== 0) {
    fail('physical_attestation_receipt_directory_unsafe');
  }
  const temporary = `${path}.new-${process.pid}`;
  writeFileSync(temporary, `${JSON.stringify(receipt)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
  chmodSync(temporary, 0o600);
  renameSync(temporary, path);
}

rejectSyntheticAuthority();
if (process.env.PULSE_PERSONAL_ATTESTATION !== 'physical-apple-silicon') {
  fail('physical_attestation_confirmation_required',
    'set PULSE_PERSONAL_ATTESTATION=physical-apple-silicon on the clean release Mac');
}
if (process.platform !== 'darwin' || process.arch !== 'arm64') {
  fail('physical_attestation_platform_unsupported', `${process.platform}/${process.arch}`);
}
if (!process.stdin.isTTY || !process.stdout.isTTY) {
  fail('physical_attestation_interactive_terminal_required');
}

const workspace = process.env.PULSE_PERSONAL_ATTEST_WORKSPACE
  ? resolve(process.env.PULSE_PERSONAL_ATTEST_WORKSPACE)
  : process.cwd();
const releaseInspection = inspectPersonalRelease({ dataDir });
const committed = readCommittedArtifactSet({ installRoot: join(dataDir, 'artifacts') });
if (committed.record.manifest_digest !== releaseInspection.release.manifest_digest ||
    committed.record.version !== packageJSON.version ||
    committed.record.epoch !== releaseInspection.release.epoch) {
  fail('physical_attestation_release_identity_mismatch');
}

const doctor = exactJSON(run(process.execPath, [cli, 'doctor', 'codex', '--json'], {
  cwd: workspace,
  code: 'physical_attestation_doctor_failed',
}).stdout, 'physical_attestation_doctor_invalid');
if (doctor.verdict !== 'Pulse Codex automatic lifecycle ready.' ||
    doctor.personal_live_readiness?.outcome !== 'ready' ||
    doctor.trust?.authority_mode !== 'production' ||
    doctor.trust?.release_manifest_digest !== releaseInspection.release.manifest_digest ||
    doctor.trust?.release_version !== packageJSON.version ||
    doctor.trust?.full_retrieval !== true || doctor.trust?.external_embedding_api !== false ||
    doctor.trust?.raw_transcript_capture !== false || doctor.trust?.backend_llm_enabled !== false ||
    doctor.trust?.native_hook_trusted !== true || doctor.trust?.trusted_hook_observed !== true) {
  fail('physical_attestation_product_not_ready');
}

const presence = inspectPresenceTrust({ probePublicKey: true, probeCapabilities: true });
if (!presence.ready) fail('physical_attestation_presence_not_ready', presence.issues.join(', '));
const nativeExecutablesVerified = verifyInstalledNativeExecutables(committed);

const binding = resolveWorkspaceBinding({ cwd: workspace });
if (binding.mode !== 'personal' || binding.fallback !== false) fail('physical_attestation_binding_invalid');
const runtime = vaultRuntimeFromBinding(binding);
const secret = readFileSync(join(runtime.data_dir, 'secret.key'), 'utf8').trim();
if (!/^[a-f0-9]{64}$/.test(secret)) fail('physical_attestation_ipc_secret_invalid');
const status = await productJSON(runtime, secret, '/memory/status');
const viewer = await productJSON(runtime, secret,
  '/viewer/data?thread_id=release-attestation&host=codex&token_budget=900');
const economy = viewer?.next_resume?.token_economy;
if (!Number.isInteger(status.item_count) || status.item_count < 1 ||
    viewer?.first_memory?.status !== 'saved' ||
    !Array.isArray(viewer?.next_resume?.included_object_ids) ||
    viewer.next_resume.included_object_ids.length < 1 ||
    !economy || !['collecting_baseline', 'estimated', 'measured'].includes(economy.state) ||
    typeof economy.method_id !== 'string' || economy.method_id.length < 1 ||
    !Number.isInteger(economy.pulse_tokens) || economy.pulse_tokens < 1) {
  fail('physical_attestation_continuity_evidence_missing');
}

run(process.execPath, [cli, 'home'], {
  cwd: workspace,
  timeout: 180_000,
  code: 'physical_attestation_home_failed',
});

const workspaceFingerprint = createHash('sha256').update(JSON.stringify({
  repository_id: binding.workspace.repository_id,
  workspace_id: binding.workspace.workspace_id,
})).digest('hex');
const receipt = {
  schema: 'pulse.personal_preview_release_attestation.v1',
  content_free: true,
  production_install_proof: true,
  authority: 'production',
  package_version: packageJSON.version,
  release_epoch: releaseInspection.release.epoch,
  release_manifest_digest: releaseInspection.release.manifest_digest,
  platform: 'darwin',
  architecture: 'arm64',
  workspace_fingerprint: workspaceFingerprint,
  native_executables_verified: nativeExecutablesVerified,
  native_hook_trusted: true,
  trusted_hook_observed: true,
  full_retrieval: true,
  external_embedding_api: false,
  backend_model_calls: false,
  raw_transcript_capture: false,
  memory_item_count: status.item_count,
  first_memory_state: 'saved',
  continuity_object_count: viewer.next_resume.included_object_ids.length,
  token_economy_state: economy.state,
  token_economy_method: economy.method_id,
  pulse_context_tokens: economy.pulse_tokens,
  home_opened_with_os_presence: true,
  attested_at: new Date().toISOString(),
};
writeReceipt(process.env.PULSE_PERSONAL_ATTESTATION_RECEIPT, receipt);
process.stdout.write(`${JSON.stringify(receipt)}\n`);
process.stdout.write('Pulse Personal physical release attestation passed.\n');
