import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';

const REPORT_SCHEMA = 'pulse.storage_report.v1';
const CLEAN_RESULT_SCHEMA = 'pulse.storage_clean_result.v1';
const AUTHORITY_SCHEMA = 'pulse.artifact_generation_authority.v1';
const SAFE_ARTIFACT = /^pulse-[A-Za-z0-9._-]+$/;
const SAFE_VERSION = /^[a-f0-9]{64}$/;

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function inside(path, root) {
  const rel = relative(resolve(root), resolve(path));
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`) && !rel.startsWith(sep);
}

function privateMode(mode, platform = process.platform) {
  // POSIX mode bits do not describe Windows ACLs. Node reports synthetic
  // permissions there, so applying Unix write bits rejects otherwise valid
  // Personal vault files and directories on Windows.
  return platform === 'win32' || (mode & 0o022) === 0;
}

function privateRegular(path) {
  const info = lstatSync(path);
  return info.isFile() && !info.isSymbolicLink() && privateMode(info.mode);
}

function readAuthority(artifactsRoot) {
  const path = join(artifactsRoot, 'artifact-generation-authority.json');
  if (!existsSync(path) || !privateRegular(path)) fail('storage_release_authority_unavailable');
  const value = JSON.parse(readFileSync(path, 'utf8'));
  if (value?.schema !== AUTHORITY_SCHEMA || !value.active?.activations ||
      (value.previous !== null && !value.previous?.activations)) {
    fail('storage_release_authority_invalid');
  }
  return value;
}

function activationPaths(generation, artifactsRoot) {
  const paths = [];
  for (const activation of Object.values(generation?.activations ?? {})) {
    const path = activation?.version_path;
    if (typeof path !== 'string' || !inside(path, artifactsRoot) || basename(path).length !== 64) {
      fail('storage_release_authority_invalid');
    }
    const canonical = resolve(path);
    if (!existsSync(canonical) || lstatSync(canonical).isSymbolicLink() || !statSync(canonical).isDirectory()) {
      fail('storage_protected_artifact_missing');
    }
    paths.push(canonical);
  }
  return paths;
}

function pathBytes(path, { skipSymlinks = false } = {}) {
  const info = lstatSync(path);
  if (info.isSymbolicLink()) {
    if (skipSymlinks) return 0;
    fail('storage_symlink_refused');
  }
  if (info.isFile()) return info.size;
  if (!info.isDirectory()) fail('storage_special_file_refused');
  let total = 0;
  for (const entry of readdirSync(path)) total += pathBytes(join(path, entry), { skipSymlinks });
  return total;
}

function processCommands() {
  try {
    return execFileSync('/bin/ps', ['-axo', 'command='], {
      encoding: 'utf8', timeout: 2000, maxBuffer: 4 * 1024 * 1024,
    });
  } catch {
    return '';
  }
}

function candidate(path, category, dataDir, commands) {
  const canonical = resolve(path);
  if (!inside(canonical, dataDir) || !existsSync(canonical)) fail('storage_candidate_outside_data_dir');
  const info = lstatSync(canonical);
  if (info.isSymbolicLink() || (!info.isDirectory() && !info.isFile())) fail('storage_candidate_unsafe');
  const inUse = commands.includes(canonical);
  return {
    category,
    path: canonical,
    relative_path: relative(dataDir, canonical),
    // Generated trees may contain ordinary package-manager links (for example
    // node_modules/.bin). Count only materialized files and never follow links
    // outside the candidate tree. The candidate root itself must still be a
    // real directory or regular file, as checked above.
    bytes: pathBytes(canonical, { skipSymlinks: true }),
    in_use: inUse,
    action: inUse ? 'skip' : 'clean',
    reason: inUse ? 'running_process_uses_path' : 'generated_and_unreferenced',
  };
}

function listArtifactCandidates(dataDir, authority, commands) {
  const artifactsRoot = join(dataDir, 'artifacts');
  const protectedPaths = new Set([
    ...activationPaths(authority.active, artifactsRoot),
    ...activationPaths(authority.previous, artifactsRoot),
  ]);
  const protectedArtifactRoots = new Set([...protectedPaths].map((path) => dirname(dirname(path))));
  const candidates = [];
  for (const name of readdirSync(artifactsRoot)) {
    if (!SAFE_ARTIFACT.test(name)) continue;
    const artifactRoot = join(artifactsRoot, name);
    const rootInfo = lstatSync(artifactRoot);
    if (rootInfo.isSymbolicLink() || !rootInfo.isDirectory()) continue;
    if (!protectedArtifactRoots.has(resolve(artifactRoot))) {
      candidates.push(candidate(artifactRoot, 'obsolete_release', dataDir, commands));
      continue;
    }
    const versionsRoot = join(artifactRoot, 'versions');
    if (!existsSync(versionsRoot) || lstatSync(versionsRoot).isSymbolicLink()) continue;
    for (const version of readdirSync(versionsRoot)) {
      if (!SAFE_VERSION.test(version)) continue;
      const versionPath = resolve(join(versionsRoot, version));
      if (!protectedPaths.has(versionPath)) {
        candidates.push(candidate(versionPath, 'obsolete_release', dataDir, commands));
      }
    }
  }
  const downloads = join(artifactsRoot, 'downloads');
  if (existsSync(downloads)) candidates.push(candidate(downloads, 'verified_downloads', dataDir, commands));
  const devBuilds = join(dataDir, 'dev-builds');
  if (existsSync(devBuilds)) candidates.push(candidate(devBuilds, 'developer_builds', dataDir, commands));
  return { candidates, protectedPaths: [...protectedPaths] };
}

function digestReport(report) {
  const canonical = JSON.stringify({
    candidates: report.candidates.map(({ relative_path, bytes, action }) => ({ relative_path, bytes, action })),
    protected_release_paths: report.protected_release_paths.map((path) => relative(report.data_dir, path)).sort(),
  });
  return createHash('sha256').update('pulse-storage-plan-v1\0').update(canonical).digest('hex');
}

export function inspectPulseStorage({ dataDir, commands = processCommands() } = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir) || !existsSync(dataDir)) {
    fail('storage_data_dir_invalid');
  }
  const root = resolve(dataDir);
  if (lstatSync(root).isSymbolicLink() || !statSync(root).isDirectory()) fail('storage_data_dir_unsafe');
  const artifactsRoot = join(root, 'artifacts');
  const authority = readAuthority(artifactsRoot);
  const { candidates, protectedPaths } = listArtifactCandidates(root, authority, commands);
  candidates.sort((a, b) => a.relative_path.localeCompare(b.relative_path));
  const report = {
    schema: REPORT_SCHEMA,
    data_dir: root,
    generated_at: new Date().toISOString(),
    active_release: { version: authority.active.version, epoch: authority.active.epoch },
    previous_release: authority.previous
      ? { version: authority.previous.version, epoch: authority.previous.epoch }
      : null,
    total_bytes: pathBytes(root, { skipSymlinks: true }),
    protected_release_bytes: protectedPaths.reduce((sum, path) => sum + pathBytes(path), 0),
    reclaimable_bytes: candidates.filter((value) => value.action === 'clean').reduce((sum, value) => sum + value.bytes, 0),
    skipped_bytes: candidates.filter((value) => value.action === 'skip').reduce((sum, value) => sum + value.bytes, 0),
    protected_release_paths: protectedPaths.sort(),
    candidates,
  };
  report.plan_digest = digestReport(report);
  return report;
}

function rollbackMoves(moves) {
  const failures = [];
  for (const move of [...moves].reverse()) {
    try {
      mkdirSync(dirname(move.source), { recursive: true, mode: 0o700 });
      renameSync(move.quarantine, move.source);
    } catch (error) {
      failures.push(error);
    }
  }
  return failures;
}

export async function cleanPulseStorage({ dataDir, planDigest, verify = async () => true, commands } = {}) {
  const report = inspectPulseStorage({ dataDir, commands });
  if (!/^[a-f0-9]{64}$/.test(planDigest ?? '') || planDigest !== report.plan_digest) {
    fail('storage_plan_changed');
  }
  const selected = report.candidates.filter((value) => value.action === 'clean');
  const quarantineRoot = join(resolve(dataDir), 'quarantine', `storage-${report.plan_digest}`);
  if (existsSync(quarantineRoot)) fail('storage_quarantine_exists');
  mkdirSync(quarantineRoot, { recursive: true, mode: 0o700 });
  const moves = [];
  try {
    for (const item of selected) {
      const target = join(quarantineRoot, item.relative_path);
      mkdirSync(dirname(target), { recursive: true, mode: 0o700 });
      renameSync(item.path, target);
      moves.push({ source: item.path, quarantine: target });
    }
    if (await verify(report) !== true) fail('storage_post_clean_verification_failed');
    rmSync(quarantineRoot, { recursive: true, force: false });
  } catch (error) {
    const rollbackFailures = rollbackMoves(moves);
    if (existsSync(quarantineRoot)) rmSync(quarantineRoot, { recursive: true, force: true });
    if (rollbackFailures.length > 0) fail('storage_clean_rollback_failed');
    throw error;
  }
  const receipt = {
    schema: CLEAN_RESULT_SCHEMA,
    plan_digest: report.plan_digest,
    cleaned_at: new Date().toISOString(),
    freed_bytes: selected.reduce((sum, item) => sum + item.bytes, 0),
    cleaned_count: selected.length,
    skipped_count: report.candidates.length - selected.length,
  };
  const receipts = join(resolve(dataDir), 'receipts');
  mkdirSync(receipts, { recursive: true, mode: 0o700 });
  writeFileSync(join(receipts, 'latest-storage-clean.json'), `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o600 });
  return receipt;
}

export function writeStorageHomeSnapshot({ dataDir, vaultDataDir, archiveReceiptPath } = {}) {
  const root = resolve(dataDir ?? '');
  const vault = resolve(vaultDataDir ?? '');
  if (typeof vaultDataDir !== 'string' || !isAbsolute(vaultDataDir) ||
      !existsSync(vault) || resolve(vaultDataDir) !== vault) {
    fail('storage_home_vault_invalid');
  }
  const vaultInfo = lstatSync(vault);
  if (vaultInfo.isSymbolicLink() || !vaultInfo.isDirectory() || !privateMode(vaultInfo.mode)) {
    fail('storage_home_vault_invalid');
  }
  const report = inspectPulseStorage({ dataDir: root });
  let archive = null;
  const receiptPath = archiveReceiptPath ?? join(root, 'receipts', 'external-archive.json');
  if (existsSync(receiptPath) && privateRegular(receiptPath)) {
    try {
      const value = JSON.parse(readFileSync(receiptPath, 'utf8'));
      if (value?.schema === 'pulse.external_archive_receipt.v1' &&
          typeof value.archive_path === 'string' && /^[a-f0-9]{64}$/.test(value.sha256 ?? '')) {
        archive = { path: value.archive_path, verified_at: value.verified_at, sha256: value.sha256 };
      }
    } catch { /* an invalid optional archive receipt is omitted */ }
  }
  const snapshot = {
    schema: 'pulse.storage_home.v1', generated_at: new Date().toISOString(),
    total_bytes: report.total_bytes, protected_release_bytes: report.protected_release_bytes,
    reclaimable_bytes: report.reclaimable_bytes, skipped_bytes: report.skipped_bytes,
    active_epoch: report.active_release.epoch, previous_epoch: report.previous_release?.epoch ?? null,
    archive,
  };
  writeFileSync(join(vault, 'storage-home.json'), `${JSON.stringify(snapshot, null, 2)}\n`, { mode: 0o600 });
  return snapshot;
}

export const __storageMaintenanceTest = Object.freeze({ digestReport, pathBytes, privateMode });
