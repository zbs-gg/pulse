import { mkdtempSync, rmSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';

import { createPlatformServices, PlatformServicesError } from './platform-services.js';

export const SUPPORTED_HOST_IDS = Object.freeze(['claude-code', 'codex', 'cursor', 'opencode']);

function detectCLI({ candidates, executablePath, label, platformServices, versionProbe }) {
  if (!Array.isArray(candidates) || candidates.length > 32 ||
      candidates.some((candidate) => !platformServices.isAbsolutePath(candidate)) ||
      (executablePath !== undefined && !platformServices.isAbsolutePath(executablePath)) ||
      (versionProbe !== undefined && typeof versionProbe !== 'function')) {
    throw new TypeError(`${label}_path_invalid`);
  }
	let detectedFailure;
	const paths = executablePath ? [executablePath] : candidates;
	for (const candidate of paths) {
		let selected;
		try { selected = platformServices.inspectExecutable(candidate); } catch (error) {
			if (!(error instanceof PlatformServicesError)) throw error;
			return {
				available: false, executable_path: null, executable_sha256: null, version: null,
				reason_code: `${label}_${error.code}`,
			};
		}
		if (!selected) continue;
		const identity = { executable_path: selected.canonical_path, executable_sha256: selected.sha256 };
		if (!versionProbe) return { available: true, ...identity, version: null, reason_code: null };
		let result;
		try { result = versionProbe(selected.canonical_path); } catch {
			detectedFailure ??= { ...identity, reason_code: `${label}_probe_failed` };
			continue;
		}
		if (result?.status !== 0) {
			detectedFailure ??= { ...identity, reason_code: `${label}_probe_failed` };
			continue;
		}
		const output = `${result.stdout ?? ''}\n${result.stderr ?? ''}`.slice(0, 4096);
		const match = output.match(/(?:^|\s)v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)(?:\s|$)/);
		if (!match) {
			detectedFailure ??= { ...identity, reason_code: `${label}_version_invalid` };
			continue;
		}
		return { available: true, ...identity, version: match[1], reason_code: null };
	}
	if (detectedFailure) return { available: false, ...detectedFailure, version: null };
	return {
		available: false, executable_path: null, executable_sha256: null, version: null,
		reason_code: `${label}_missing`,
	};
}

export function probeHostVersion(executable, args = ['--version'], timeout = 5000) {
	const probeHome = mkdtempSync(join(tmpdir(), 'pulse-host-probe.'));
	try {
		return spawnSync(executable, args, {
			encoding: 'utf8',
			env: {
				...process.env,
				HOME: probeHome,
				USERPROFILE: probeHome,
				XDG_CONFIG_HOME: join(probeHome, '.config'),
				XDG_DATA_HOME: join(probeHome, '.local', 'share'),
				XDG_CACHE_HOME: join(probeHome, '.cache'),
				CODEX_HOME: join(probeHome, '.codex'),
			},
			stdio: ['ignore', 'pipe', 'pipe'],
			timeout,
			killSignal: 'SIGTERM',
		});
	} finally {
		rmSync(probeHome, { recursive: true, force: true });
	}
}

export function detectCodexCLI({
  candidates,
  codexPath,
  platformServices = createPlatformServices(),
  versionProbe,
} = {}) {
  return detectCLI({
    candidates: candidates ?? platformServices.hostCandidates().codex,
    executablePath: codexPath, label: 'codex', platformServices,
	versionProbe: versionProbe ?? probeHostVersion,
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
      if (proof) return {
        available: true,
        app_path: proof.canonical_path,
        executable_path: proof.executable_path,
        executable_sha256: proof.executable_sha256,
        reason_code: null,
      };
    } catch (error) {
      if (error instanceof PlatformServicesError && error.code === 'platform_native_adapter_unavailable') {
        return {
          available: false, app_path: null, executable_path: null, executable_sha256: null,
          reason_code: `cursor_${error.code}`,
        };
      }
      if (!(error instanceof PlatformServicesError)) throw error;
    }
  }
  return {
    available: false, app_path: null, executable_path: null, executable_sha256: null,
    reason_code: 'cursor_missing',
  };
}

export function detectOpenCodeCLI({
  candidates,
  opencodePath,
  platformServices = createPlatformServices(),
  versionProbe,
} = {}) {
  const detected = detectCLI({
    candidates: candidates ?? platformServices.hostCandidates().opencode,
    executablePath: opencodePath,
    label: 'opencode',
    platformServices,
    versionProbe: versionProbe ?? probeHostVersion,
  });
  if (!detected.available) return detected;
  const compatiblePlatform = platformServices.platform === 'darwin' && platformServices.architecture === 'arm64';
  const compatibleVersion = /^1\.18\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(detected.version ?? '');
  if (compatiblePlatform && compatibleVersion) return { ...detected, compatible: true };
  return {
    ...detected,
    available: false,
    detected: true,
    compatible: false,
    reason_code: compatiblePlatform ? 'opencode_version_incompatible' : 'opencode_platform_incompatible',
  };
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
  detectOpenCode = () => detectOpenCodeCLI({ platformServices }),
} = {}) {
  if (![detectClaude, detectCodex, detectCursor, detectOpenCode].every((detector) => typeof detector === 'function')) {
    throw new TypeError('supported_host_detector_invalid');
  }
  return Object.freeze([
    hostRecord('claude-code', detectClaude()),
    hostRecord('codex', detectCodex()),
    hostRecord('cursor', detectCursor()),
    hostRecord('opencode', detectOpenCode()),
  ]);
}
