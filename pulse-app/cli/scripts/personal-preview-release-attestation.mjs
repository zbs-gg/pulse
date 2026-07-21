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
import {
  contextQueryHasNoInfluence, exactTarballPulseInvocation, physicalHostLifecycleEvidence,
  unassignedAssignmentTurnRef,
} from '../src/release-attestation.js';
import { SUPPORTED_HOST_IDS } from '../src/supported-hosts.js';
import { attestSelectedTarget } from '../src/target-release-attestation.js';
import { inspectPresenceTrust } from '../src/trust-helper.js';
import { readUnassignedInbox, unassignedInboxPath } from '../src/unassigned-inbox.js';
import { resolveWorkspaceBinding } from '../src/workspace-binding.js';

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
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
    stdio: options.stdio ?? ['inherit', 'pipe', 'pipe'],
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

function installedArtifactAttestationInputs(committed, release) {
  const artifacts = {};
  for (const [kind, descriptor] of Object.entries(release.artifacts)) {
    const activation = committed.activations[kind];
    if (!activation) fail('physical_attestation_activation_missing', kind);
    const metadata = exactJSON(readFileSync(join(activation.version_path, 'activation.json'), 'utf8'),
      'physical_attestation_activation_metadata_invalid');
    artifacts[kind] = Object.freeze({ descriptor, root: activation.version_path, tree: metadata.tree });
  }
  return Object.freeze(artifacts);
}

async function productJSON(runtime, secret, path, { method = 'GET', body } = {}) {
  const response = await fetch(`${runtime.base_url}${path}`, {
    method,
    headers: {
      Accept: 'application/json', 'X-Pulse-Key': secret,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(5000),
  });
  if (!response.ok) fail('physical_attestation_product_query_failed', `${path}: ${response.status}`);
  return response.json();
}

function verifiedTarball(path) {
  if (!path || !isAbsolute(path) || resolve(path) !== path) {
    fail('physical_attestation_tarball_required',
      'set PULSE_PERSONAL_ATTEST_TARBALL to the exact absolute .tgz installed on this Mac');
  }
  let info;
  try { info = lstatSync(path); } catch { fail('physical_attestation_tarball_unsafe'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1) {
    fail('physical_attestation_tarball_unsafe');
  }
  const bytes = readFileSync(path);
  const packed = exactJSON(run('/usr/bin/tar', ['-xOf', path, 'package/package.json'], {
    code: 'physical_attestation_tarball_invalid',
  }).stdout, 'physical_attestation_tarball_package_invalid');
  if (packed?.name !== '@zbs-gg/pulse' || typeof packed.version !== 'string') {
    fail('physical_attestation_tarball_package_invalid');
  }
  return {
    path,
    bytes: bytes.byteLength,
    sha256: createHash('sha256').update(bytes).digest('hex'),
    version: packed.version,
  };
}

function verifiedNpmExecPath() {
  const path = process.env.npm_execpath;
  if (!path || !isAbsolute(path) || resolve(path) !== path) {
    fail('physical_attestation_npm_execpath_invalid',
      'run this gate through npm run attest:personal-preview');
  }
  let info;
  try { info = lstatSync(path); } catch { fail('physical_attestation_npm_execpath_invalid'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1) {
    fail('physical_attestation_npm_execpath_invalid');
  }
  return path;
}

function installExactTarball(tarball, workspace) {
  runPackedPulse(tarball, ['install'], {
    cwd: workspace, stdio: 'inherit', timeout: 15 * 60_000,
    code: 'physical_attestation_exact_tarball_install_failed',
  });
}

function runPackedPulse(tarball, args, options = {}) {
  const npmExecPath = verifiedNpmExecPath();
  return run(process.execPath, [npmExecPath, ...exactTarballPulseInvocation(tarball.path, args)], options);
}

function selectedHost() {
  const host = process.env.PULSE_PERSONAL_ATTEST_HOST;
  if (!SUPPORTED_HOST_IDS.includes(host)) {
    fail('physical_attestation_host_required',
      'set PULSE_PERSONAL_ATTEST_HOST to claude-code, codex, or cursor');
  }
  return host;
}

async function publicPreviewVersion() {
  const url = 'https://registry.npmjs.org/-/package/@zbs-gg%2Fpulse/dist-tags';
  const response = await fetch(url, {
    method: 'GET', redirect: 'error', headers: { Accept: 'application/json' },
    signal: AbortSignal.timeout(10_000),
  }).catch(() => fail('physical_attestation_public_dist_tags_unavailable'));
  if (!response.ok || response.url !== url) fail('physical_attestation_public_dist_tags_unavailable');
  const text = await response.text();
  if (Buffer.byteLength(text) > 64 * 1024) fail('physical_attestation_public_dist_tags_invalid');
  const tags = exactJSON(text, 'physical_attestation_public_dist_tags_invalid');
  if (typeof tags?.preview !== 'string' || tags.preview.length < 1) {
    fail('physical_attestation_public_preview_missing');
  }
  return tags.preview;
}

function readyVerdict(host) {
  return {
    'claude-code': 'Pulse Claude Code automatic lifecycle ready.',
    codex: 'Pulse Codex automatic lifecycle ready.',
    cursor: 'Pulse Cursor automatic lifecycle ready.',
  }[host];
}

function exactAssignedCandidate(inbox, binding, tray) {
  const assigned = [...inbox.receipts].reverse().find((receipt) =>
    receipt.action === 'assign' && receipt.status === 'assigned' &&
    receipt.binding_digest === binding.binding_digest &&
    receipt.repository_id === binding.workspace.repository_id &&
    receipt.store_id === (binding.personal?.store_id ?? binding.desk?.store_id));
  if (!assigned) fail('physical_attestation_unassigned_assignment_missing');
  const turnRef = unassignedAssignmentTurnRef(assigned.content_digest);
  const candidate = tray?.candidates?.find((value) =>
    value.state === 'committed' && value.current === true &&
    value.receipt_history?.some((receipt) => receipt.safe_provenance?.turn_id === turnRef) &&
    typeof value.canonical_object_id === 'string' &&
    value.canonical_object_id.length > 0 &&
    ['created', 'updated', 'deduplicated'].includes(value.latest_receipt?.status));
  if (!candidate) fail('physical_attestation_assigned_candidate_not_saved');
  const summary = candidate.candidate?.capsule?.items?.[0]?.redacted_summary;
  if (typeof summary !== 'string' || summary.trim().length < 1) {
    fail('physical_attestation_assigned_candidate_invalid');
  }
  return { assigned, candidate, summary };
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
const betaWorkspaceValue = process.env.PULSE_PERSONAL_ATTEST_BETA_WORKSPACE;
if (!betaWorkspaceValue || !isAbsolute(betaWorkspaceValue)) {
  fail('physical_attestation_beta_workspace_required',
    'set PULSE_PERSONAL_ATTEST_BETA_WORKSPACE to a clean, separately bound project');
}
const betaWorkspace = resolve(betaWorkspaceValue);
if (betaWorkspace === workspace) fail('physical_attestation_beta_workspace_not_distinct');
const host = selectedHost();
const tarball = verifiedTarball(process.env.PULSE_PERSONAL_ATTEST_TARBALL);
const publicPreview = await publicPreviewVersion();
installExactTarball(tarball, workspace);
const releaseInspection = inspectPersonalRelease({ dataDir });
const committed = readCommittedArtifactSet({ installRoot: join(dataDir, 'artifacts') });
if (committed.record.manifest_digest !== releaseInspection.release.manifest_digest ||
    committed.record.version !== packageJSON.version || tarball.version !== packageJSON.version ||
    committed.record.epoch !== releaseInspection.release.epoch) {
  fail('physical_attestation_release_identity_mismatch');
}

const doctor = exactJSON(runPackedPulse(tarball, ['doctor', host, '--json'], {
  cwd: workspace,
  code: 'physical_attestation_doctor_failed',
}).stdout, 'physical_attestation_doctor_invalid');
if (doctor.verdict !== readyVerdict(host) ||
    Object.values(doctor.checks ?? {}).some((check) => check?.ok !== true) ||
    doctor.personal_live_readiness?.outcome !== 'ready' ||
    doctor.trust?.authority_mode !== 'production' ||
    doctor.trust?.release_manifest_digest !== releaseInspection.release.manifest_digest ||
    doctor.trust?.release_version !== packageJSON.version ||
    doctor.trust?.release_epoch !== releaseInspection.release.epoch ||
    doctor.trust?.full_retrieval !== true || doctor.trust?.external_embedding_api !== false ||
    doctor.trust?.raw_transcript_capture !== false || doctor.trust?.backend_llm_enabled !== false) {
  fail('physical_attestation_product_not_ready');
}
if (host === 'codex' && (doctor.trust?.native_hook_trusted !== true ||
    doctor.trust?.trusted_hook_observed !== true)) {
  fail('physical_attestation_codex_lifecycle_not_ready');
}

const presence = inspectPresenceTrust({ probePublicKey: true, probeCapabilities: true });
if (!presence.ready) fail('physical_attestation_presence_not_ready', presence.issues.join(', '));
const nativeAttestation = attestSelectedTarget({
  artifacts: installedArtifactAttestationInputs(committed, releaseInspection.release),
  catalogVerified: releaseInspection.release.historical_only === false,
  manifestDigest: releaseInspection.release.manifest_digest,
  mode: 'production',
  platform: process.platform,
  target: {
    platform: process.platform,
    verification_profile: releaseInspection.release.verification_profile,
  },
});
const nativeExecutablesVerified = nativeAttestation.executables_verified;

const binding = resolveWorkspaceBinding({ cwd: workspace });
if (binding.mode !== 'personal' || binding.fallback !== false) fail('physical_attestation_binding_invalid');
const runtime = vaultRuntimeFromBinding(binding);
const secret = readFileSync(join(runtime.data_dir, 'secret.key'), 'utf8').trim();
if (!/^[a-f0-9]{64}$/.test(secret)) fail('physical_attestation_ipc_secret_invalid');
const status = await productJSON(runtime, secret, '/memory/status');
const viewer = await productJSON(runtime, secret,
  `/viewer/data?thread_id=release-attestation&host=${host}&token_budget=900`);
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

const inbox = readUnassignedInbox(unassignedInboxPath(homedir()));
const tray = await productJSON(runtime, secret, '/memory/tray?limit=200');
const assignment = exactAssignedCandidate(inbox, binding, tray);
const recallRequest = {
  query: assignment.summary, scope: 'project', limit: 10, privacy_ceiling: 'private',
};
const alphaRecall = await productJSON(runtime, secret, '/memory/recall', {
  method: 'POST', body: recallRequest,
});
if (!alphaRecall?.items?.some((item) => item.id === assignment.candidate.canonical_object_id)) {
  fail('physical_attestation_assigned_object_not_recalled');
}

const betaBinding = resolveWorkspaceBinding({ cwd: betaWorkspace });
if (betaBinding.mode !== 'personal' || betaBinding.fallback !== false ||
    betaBinding.binding_digest === binding.binding_digest ||
    betaBinding.workspace.repository_id === binding.workspace.repository_id) {
  fail('physical_attestation_beta_binding_invalid');
}
const betaRuntime = vaultRuntimeFromBinding(betaBinding);
if (betaRuntime.store_id === runtime.store_id || betaRuntime.data_dir === runtime.data_dir) {
  fail('physical_attestation_beta_vault_not_distinct');
}
const betaSecret = readFileSync(join(betaRuntime.data_dir, 'secret.key'), 'utf8').trim();
if (!/^[a-f0-9]{64}$/.test(betaSecret)) fail('physical_attestation_beta_ipc_secret_invalid');
const betaStatus = await productJSON(betaRuntime, betaSecret, '/memory/status');
const betaViewer = await productJSON(betaRuntime, betaSecret,
  `/viewer/data?thread_id=release-attestation-beta&host=${host}&token_budget=900`);
const betaRecall = await productJSON(betaRuntime, betaSecret, '/memory/recall', {
  method: 'POST', body: recallRequest,
});
const betaContext = await productJSON(betaRuntime, betaSecret, '/context/query', {
  method: 'POST',
  body: { query: assignment.summary, mode: 'auto', top_k: 10, include_trace: true, graph_mode: 'anchored' },
});
if (betaStatus.item_count !== 0 || !Array.isArray(betaRecall?.items) || betaRecall.items.length !== 0 ||
    betaViewer?.first_memory?.status !== 'pending' ||
    !Array.isArray(betaViewer?.next_resume?.included_object_ids) ||
    betaViewer.next_resume.included_object_ids.length !== 0 || !contextQueryHasNoInfluence(betaContext)) {
  fail('physical_attestation_cross_project_influence_detected');
}

runPackedPulse(tarball, ['home', '--host', host], {
  cwd: workspace,
  stdio: 'inherit',
  timeout: 180_000,
  code: 'physical_attestation_home_failed',
});

const workspaceFingerprint = createHash('sha256').update(JSON.stringify({
  repository_id: binding.workspace.repository_id,
  workspace_id: binding.workspace.workspace_id,
})).digest('hex');
const receipt = {
  schema: 'pulse.personal_preview_release_attestation.v2',
  content_free: true,
  production_install_proof: true,
  authority: 'production',
  target_host: host,
  package_version: packageJSON.version,
  packed_tarball_sha256: tarball.sha256,
  packed_tarball_bytes: tarball.bytes,
  exact_tarball_install_executed: true,
  public_preview_version: publicPreview,
  public_preview_matches_artifact: publicPreview === packageJSON.version,
  release_epoch: releaseInspection.release.epoch,
  release_manifest_digest: releaseInspection.release.manifest_digest,
  platform: 'darwin',
  architecture: 'arm64',
  workspace_fingerprint: workspaceFingerprint,
  native_executables_verified: nativeExecutablesVerified,
  host_lifecycle_evidence: physicalHostLifecycleEvidence(host),
  full_retrieval: true,
  external_embedding_api: false,
  backend_model_calls: false,
  raw_transcript_capture: false,
  memory_item_count: status.item_count,
  first_memory_state: 'saved',
  continuity_object_count: viewer.next_resume.included_object_ids.length,
  unassigned_assignment_receipt: true,
  assigned_object_recalled_in_alpha: true,
  assigned_object_absent_from_beta: true,
  beta_context_trace_empty: true,
  beta_memory_item_count: betaStatus.item_count,
  token_economy_state: economy.state,
  token_economy_method: economy.method_id,
  pulse_context_tokens: economy.pulse_tokens,
  home_opened_with_os_presence: true,
  attested_at: new Date().toISOString(),
};
writeReceipt(process.env.PULSE_PERSONAL_ATTESTATION_RECEIPT, receipt);
process.stdout.write(`${JSON.stringify(receipt)}\n`);
process.stdout.write('Pulse Personal physical release attestation passed.\n');
