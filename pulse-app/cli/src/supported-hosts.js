import { homedir } from 'node:os';

import { createPlatformServices, PlatformServicesError } from './platform-services.js';

export const SUPPORTED_HOST_IDS = Object.freeze(['claude-code', 'codex', 'cursor']);

function detectCLI({ candidates, executablePath, label, platformServices, versionProbe }) {
  if (!Array.isArray(candidates) || candidates.length > 32 ||
      candidates.some((candidate) => !platformServices.isAbsolutePath(candidate)) ||
      (executablePath !== undefined && !platformServices.isAbsolutePath(executablePath)) ||
      (versionProbe !== undefined && (typeof versionProbe !== 'function' || executablePath === undefined))) {
    throw new TypeError(`${label}_path_invalid`);
  }
  let selected;
  try {
    selected = executablePath
      ? platformServices.inspectExecutable(executablePath)
      : candidates.map((candidate) => platformServices.inspectExecutable(candidate)).find(Boolean);
  } catch (error) {
    if (!(error instanceof PlatformServicesError)) throw error;
    return {
      available: false, executable_path: null, executable_sha256: null, version: null,
      reason_code: `${label}_${error.code}`,
    };
  }
  if (!selected) {
    return {
      available: false,
      executable_path: null,
      executable_sha256: null,
      version: null,
      reason_code: `${label}_missing`,
    };
  }
  const identity = { executable_path: selected.canonical_path, executable_sha256: selected.sha256 };
  if (!versionProbe) return { available: true, ...identity, version: null, reason_code: null };

  let result;
  try { result = versionProbe(selected.canonical_path); } catch {
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
  candidates,
  codexPath,
  platformServices = createPlatformServices(),
  versionProbe,
} = {}) {
  return detectCLI({
    candidates: candidates ?? platformServices.hostCandidates().codex,
    executablePath: codexPath, label: 'codex', platformServices, versionProbe,
  });
}

export function detectClaudeCodeCLI({
  candidates,
  claudePath,
  home = homedir(),
  platformServices = createPlatformServices({ home }),
  versionProbe,
} = {}) {
  return detectCLI({
    candidates: candidates ?? platformServices.hostCandidates(home).claude,
    executablePath: claudePath,
    label: 'claude',
    platformServices,
    versionProbe,
  });
}

export function detectCursorInstallation({
  appCandidates,
  home = homedir(),
  platformServices = createPlatformServices({ home }),
} = {}) {
  const candidates = appCandidates ?? platformServices.hostCandidates(home).cursor;
  if (!Array.isArray(candidates) || candidates.length > 16 ||
      candidates.some((candidate) => !platformServices.isAbsolutePath(candidate))) {
    throw new TypeError('cursor_path_invalid');
  }
  for (const candidate of candidates) {
    try {
      const proof = platformServices.inspectApplication(candidate);
      if (proof) return { available: true, app_path: proof.canonical_path, reason_code: null };
    } catch (error) {
      if (error instanceof PlatformServicesError && error.code === 'platform_native_adapter_unavailable') {
        return { available: false, app_path: null, reason_code: `cursor_${error.code}` };
      }
      if (!(error instanceof PlatformServicesError)) throw error;
    }
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
  platformServices = createPlatformServices({ home }),
  detectClaude = () => detectClaudeCodeCLI({ home, platformServices }),
  detectCodex = () => detectCodexCLI({ platformServices }),
  detectCursor = () => detectCursorInstallation({ home, platformServices }),
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
