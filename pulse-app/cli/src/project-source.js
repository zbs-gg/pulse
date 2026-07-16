import { createHash, randomBytes } from 'node:crypto';
import {
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  writeFileSync,
} from 'node:fs';
import { dirname, isAbsolute, relative, resolve, sep } from 'node:path';

const SOURCE_MAX_BYTES = 8 * 1024 * 1024;
const WINDOW_MIN_BYTES = 64;
const WINDOW_MAX_BYTES = 32 * 1024;
const DIGEST = /^[a-f0-9]{64}$/;
const REPOSITORY_ID = /^repository_[A-Za-z0-9._:-]{1,240}$/;
const PORTABLE_PROJECT_ID = /^project_[a-f0-9]{32}$/;
const SAFE_EXTENSION = /\.(?:md|markdown|txt)$/i;
const CONTROL = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/;
const CREDENTIAL = /(?:\bsk-[A-Za-z0-9_-]{12,}\b|authorization\s*:\s*bearer\s+\S{12,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b)/i;
const LOCAL_PATH = /(?:^|[\s"'(=])(?:\/(?:Users|home|private|etc|tmp|var|opt|usr|Library|Volumes)(?:\/[A-Za-z0-9._~@%+,:=-]+)+|[A-Za-z]:\\|\\\\[^\\\s]+\\)/i;
const TRANSCRIPT = /^\s*(?:user|assistant|human|system|ai)\s*:/i;
const INSTRUCTION = /\b(?:ignore|disregard|override)\b.{0,40}\b(?:previous|prior|system|developer)\b.{0,20}\b(?:instruction|prompt|rule)s?\b|\byou are now\b/i;

function exactKeys(value, allowed, code) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).some((key) => !allowed.includes(key))) throw new Error(code);
}

function boundIdentity(resolved) {
  const binding = resolved?.binding;
  const workspace = binding?.workspace;
  if (!DIGEST.test(binding?.binding_digest ?? '') || !Number.isSafeInteger(binding?.resolver_epoch) ||
      binding.resolver_epoch < 1 || typeof workspace?.canonical_path !== 'string' ||
      !isAbsolute(workspace.canonical_path) || !REPOSITORY_ID.test(workspace?.repository_id ?? '') ||
      typeof resolved?.runtime?.data_dir !== 'string' || !isAbsolute(resolved.runtime.data_dir)) {
    throw new Error('project_source_authority_unavailable');
  }
  const root = realpathSync(workspace.canonical_path);
  return { binding, root, repositoryID: workspace.repository_id };
}

function safeLocator(locator) {
  if (typeof locator !== 'string' || locator.length < 1 || locator.length > 512 ||
      locator.trim() !== locator || locator.normalize('NFC') !== locator || isAbsolute(locator) ||
      locator.includes('\\') || CONTROL.test(locator) || !SAFE_EXTENSION.test(locator)) {
    throw new Error('project_source_locator_unsafe');
  }
  const parts = locator.split('/');
  if (parts.some((part) => part === '' || part === '.' || part === '..' || part === '.git') ||
      parts.join('/') !== locator) throw new Error('project_source_locator_unsafe');
  return locator;
}

function pathInside(root, target) {
  const rel = relative(root, target);
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`) && !isAbsolute(rel);
}

function readSafeSource(root, locator) {
  const requested = resolve(root, ...locator.split('/'));
  if (!pathInside(root, requested)) throw new Error('project_source_locator_unsafe');
  const info = lstatSync(requested);
  if (!info.isFile() || info.isSymbolicLink() || info.size > SOURCE_MAX_BYTES) {
    throw new Error('project_source_file_unsafe');
  }
  const canonical = realpathSync(requested);
  if (canonical !== requested || !pathInside(root, canonical)) throw new Error('project_source_file_unsafe');
  const bytes = readFileSync(canonical);
  if (bytes.includes(0) || bytes.some((value) => value < 0x09 || (value > 0x0d && value < 0x20))) {
    throw new Error('project_source_binary_unsupported');
  }
  let text;
  try { text = new TextDecoder('utf-8', { fatal: true }).decode(bytes); } catch {
    throw new Error('project_source_binary_unsupported');
  }
  return { bytes, info, text };
}

function unsafeReason(line) {
  if (CREDENTIAL.test(line)) return 'credential';
  if (LOCAL_PATH.test(line)) return 'path';
  if (TRANSCRIPT.test(line)) return 'transcript';
  if (INSTRUCTION.test(line)) return 'instruction';
  return undefined;
}

function sanitizeLine(line, lineNumber) {
  const reason = unsafeReason(line);
  if (!reason) return { text: line };
  return {
    text: `[withheld:${reason}]`,
    withheld: { line: lineNumber, reason },
  };
}

export function ensureBoundPortableProjectID(resolved, { random = randomBytes } = {}) {
  const { binding, repositoryID } = boundIdentity(resolved);
  const dataDir = resolve(resolved.runtime.data_dir);
  const statePath = resolve(dataDir, 'git-team-memory-project.json');
  if (!pathInside(dataDir, statePath)) throw new Error('project_identity_path_unsafe');
  if (existsSync(statePath)) {
    const info = lstatSync(statePath);
    const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
    if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
        (info.mode & 0o077) !== 0 || info.size > 2048) throw new Error('project_identity_file_unsafe');
    const value = JSON.parse(readFileSync(statePath, 'utf8'));
    if (value?.schema !== 'pulse.git_team_memory_project_identity.v1' ||
        !PORTABLE_PROJECT_ID.test(value.portable_project_id ?? '') ||
        value.repository_id !== repositoryID || value.binding_digest !== binding.binding_digest ||
        Object.keys(value).sort().join('\0') !== 'binding_digest\0portable_project_id\0repository_id\0schema') {
      throw new Error('project_identity_mismatch');
    }
    return value.portable_project_id;
  }
  const entropy = random(16);
  if (!Buffer.isBuffer(entropy) || entropy.length !== 16) throw new Error('project_identity_random_invalid');
  const value = {
    schema: 'pulse.git_team_memory_project_identity.v1',
    portable_project_id: `project_${entropy.toString('hex')}`,
    repository_id: repositoryID,
    binding_digest: binding.binding_digest,
  };
  mkdirSync(dirname(statePath), { recursive: true, mode: 0o700 });
  try {
    writeFileSync(statePath, `${JSON.stringify(value)}\n`, { mode: 0o600, flag: 'wx' });
  } catch (error) {
    if (error?.code === 'EEXIST') return ensureBoundPortableProjectID(resolved, { random });
    throw error;
  }
  return value.portable_project_id;
}

export function readBoundProjectSourceWindow(resolved, input) {
  exactKeys(input, ['locator', 'cursor', 'max_bytes', 'expected_version_digest'], 'project_source_window_invalid');
  if (!Number.isSafeInteger(input.cursor) || input.cursor < 0 || !Number.isSafeInteger(input.max_bytes) ||
      input.max_bytes < WINDOW_MIN_BYTES || input.max_bytes > WINDOW_MAX_BYTES ||
      (input.expected_version_digest !== undefined && !DIGEST.test(input.expected_version_digest))) {
    throw new Error('project_source_window_invalid');
  }
  const { root, repositoryID } = boundIdentity(resolved);
  const locator = safeLocator(input.locator);
  const source = readSafeSource(root, locator);
  const versionDigest = createHash('sha256').update(source.bytes).digest('hex');
  if (input.expected_version_digest !== undefined && input.expected_version_digest !== versionDigest) {
    throw new Error('project_source_version_changed');
  }
  const lines = source.text.split('\n');
  if (input.cursor > lines.length) throw new Error('project_source_window_invalid');
  const output = [];
  const withheld = [];
  let used = 0;
  let cursor = input.cursor;
  while (cursor < lines.length) {
    const sanitized = sanitizeLine(lines[cursor], cursor + 1);
    let rendered = sanitized.text;
    let size = Buffer.byteLength(rendered) + (output.length === 0 ? 0 : 1);
    if (size > input.max_bytes && output.length === 0) {
      rendered = '[withheld:oversized_line]';
      size = Buffer.byteLength(rendered);
      sanitized.withheld = { line: cursor + 1, reason: 'oversized_line' };
    }
    if (output.length > 0 && used + size > input.max_bytes) break;
    output.push(rendered);
    if (sanitized.withheld) withheld.push(sanitized.withheld);
    used += size;
    cursor += 1;
  }
  return {
    schema: 'pulse.project_source.window.v1',
    portable_project_id: ensureBoundPortableProjectID(resolved),
    repository_id: repositoryID,
    source_kind: 'repository_text',
    locator,
    version_digest: versionDigest,
    byte_count: source.bytes.length,
    cursor: input.cursor,
    next_cursor: cursor,
    status: cursor < lines.length ? 'more' : 'complete',
    content: output.join('\n'),
    withheld,
  };
}
