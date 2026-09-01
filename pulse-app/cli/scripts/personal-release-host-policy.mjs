import { loadNativeUniversalMatrix } from './native-universal-matrix.mjs';

const OPENCODE_HARNESS = Object.freeze({
  host: 'opencode',
  vendor: 'Anomaly',
  distribution: 'npm',
  identity: 'opencode-ai',
  version: '1.18.15',
  executable: 'opencode',
  vendor_source: 'https://registry.npmjs.org/opencode-ai',
  downloads: Object.freeze({
    'darwin-arm64': 'https://registry.npmjs.org/opencode-ai/-/opencode-ai-1.18.15.tgz',
  }),
  executable_digest_policy: 'native_evidence_sha256',
  supported_targets: Object.freeze(['darwin-arm64']),
});

export function loadPersonalReleaseHostPolicy(matrix = loadNativeUniversalMatrix()) {
  return Object.freeze([
    ...matrix.harnesses,
    OPENCODE_HARNESS,
  ]);
}

export function personalReleaseHostTargetCount(harnesses, targetIDs) {
  const selected = new Set(targetIDs);
  return harnesses.reduce((count, harness) => count +
    harness.supported_targets.filter((targetID) => selected.has(targetID)).length, 0);
}
