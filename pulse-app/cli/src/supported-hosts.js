import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, realpathSync, statSync } from 'node:fs';
import { homedir } from 'node:os';
import { isAbsolute, join, resolve } from 'node:path';

export const SUPPORTED_HOST_IDS = Object.freeze(['claude-code', 'codex', 'cursor']);

const DEFAULT_CODEX_CANDIDATES = Object.freeze([
  '/opt/homebrew/bin/codex',
  '/usr/local/bin/codex',
  '/usr/bin/codex',
]);

function claudeCandidates(home) {
  return [
    join(resolve(home), '.local', 'bin', 'claude'),
    '/opt/homebrew/bin/claude',
    '/usr/local/bin/claude',
    '/usr/bin/claude',
  ];
}

function canonicalExecutable(path) {
  try {
    const target = realpathSync(path);
    const stat = statSync(target);
    if (!stat.isFile() || (stat.mode & 0o111) === 0) return null;
    return {
      path: target,
      sha256: createHash('sha256').update(readFileSync(target)).digest('hex'),
    };
  } catch {
    return null;
  }
}

function detectCLI({ candidates, executablePath, label, versionProbe }) {
  if (!Array.isArray(candidates) || candidates.length > 32 ||
      candidates.some((candidate) => typeof candidate !== 'string' || !isAbsolute(candidate) || resolve(candidate) !== candidate) ||
      (executablePath !== undefined && (typeof executablePath !== 'string' || !isAbsolute(executablePath) || resolve(executablePath) !== executablePath)) ||
      (versionProbe !== undefined && (typeof versionProbe !== 'function' || executablePath === undefined))) {
    throw new TypeError(`${label}_path_invalid`);
  }
  const selected = executablePath
    ? canonicalExecutable(executablePath)
    : candidates.map(canonicalExecutable).find(Boolean);
  if (!selected) {
    return {
      available: false,
      executable_path: null,
      executable_sha256: null,
      version: null,
      reason_code: `${label}_missing`,
    };
  }
  const identity = { executable_path: selected.path, executable_sha256: selected.sha256 };
  if (!versionProbe) return { available: true, ...identity, version: null, reason_code: null };

  let result;
  try { result = versionProbe(selected.path); } catch {
    return { available: false, ...identity, version: null, reason_code: `${label}_probe_failed` };
  }
  if (result?.status !== 0) {
    return { available: false, ...identity, version: null, reason_code: `${label}_probe_failed` };
  }
  const output = `${result.stdout ?? ''}\n${result.stderr ?? ''}`.slice(0, 4096);
  const match = output.match(/(?:^|\s)v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)(?:\s|$)/);
  if (!match) return { available: false, ...identity, version: null, reason_code: `${label}_version_invalid` };
  return { available: true, ...identity, version: match[1], reason_code: null };
}

export function detectCodexCLI({
  candidates = DEFAULT_CODEX_CANDIDATES,
  codexPath,
  versionProbe,
} = {}) {
  return detectCLI({ candidates, executablePath: codexPath, label: 'codex', versionProbe });
}

export function detectClaudeCodeCLI({
  candidates,
  claudePath,
  home = homedir(),
  versionProbe,
} = {}) {
  return detectCLI({
    candidates: candidates ?? claudeCandidates(home),
    executablePath: claudePath,
    label: 'claude',
    versionProbe,
  });
}

export function detectCursorInstallation({
  appCandidates,
  home = homedir(),
} = {}) {
  const candidates = appCandidates ?? [
    '/Applications/Cursor.app',
    join(resolve(home), 'Applications', 'Cursor.app'),
  ];
  if (!Array.isArray(candidates) || candidates.length > 16 ||
      candidates.some((candidate) => typeof candidate !== 'string' || !isAbsolute(candidate) || resolve(candidate) !== candidate)) {
    throw new TypeError('cursor_path_invalid');
  }
  for (const candidate of candidates) {
    if (!existsSync(candidate)) continue;
    try {
      const path = realpathSync(candidate);
      if (statSync(path).isDirectory()) return { available: true, app_path: path, reason_code: null };
    } catch { /* try the next bounded app location */ }
  }
  return { available: false, app_path: null, reason_code: 'cursor_missing' };
}

function hostRecord(host, result) {
  if (!result || typeof result !== 'object' || Array.isArray(result)) {
    throw new TypeError('supported_host_detection_invalid');
  }
  const detected = result.detected === true || result.available === true ||
    typeof result.executable_path === 'string' || typeof result.app_path === 'string';
  const compatible = result.compatible === true || result.available === true;
  return Object.freeze({
    ...result,
    host,
    detected,
    compatible,
    activation_target: compatible,
  });
}

export function detectSupportedHosts({
  home = homedir(),
  detectClaude = () => detectClaudeCodeCLI({ home }),
  detectCodex = () => detectCodexCLI(),
  detectCursor = () => detectCursorInstallation({ home }),
} = {}) {
  if (![detectClaude, detectCodex, detectCursor].every((detector) => typeof detector === 'function')) {
    throw new TypeError('supported_host_detector_invalid');
  }
  return Object.freeze([
    hostRecord('claude-code', detectClaude()),
    hostRecord('codex', detectCodex()),
    hostRecord('cursor', detectCursor()),
  ]);
}
