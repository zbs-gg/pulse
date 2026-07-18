const TARGETS = Object.freeze({
  'darwin-arm64': Object.freeze({ id: 'darwin-arm64', platform: 'darwin', architecture: 'arm64', libc: null }),
  'darwin-x64': Object.freeze({ id: 'darwin-x64', platform: 'darwin', architecture: 'x64', libc: null }),
  'linux-arm64-gnu': Object.freeze({ id: 'linux-arm64-gnu', platform: 'linux', architecture: 'arm64', libc: 'gnu' }),
  'linux-x64-gnu': Object.freeze({ id: 'linux-x64-gnu', platform: 'linux', architecture: 'x64', libc: 'gnu' }),
  'win32-arm64': Object.freeze({ id: 'win32-arm64', platform: 'win32', architecture: 'arm64', libc: null }),
  'win32-x64': Object.freeze({ id: 'win32-x64', platform: 'win32', architecture: 'x64', libc: null }),
});

export const DESKTOP_TARGET_IDS = Object.freeze(Object.keys(TARGETS).sort());

export class DesktopTargetError extends Error {
  constructor(code) {
    super(code);
    this.name = 'DesktopTargetError';
    this.code = code;
  }
}

function unavailable() {
  throw new DesktopTargetError('release_target_unavailable');
}

export function desktopTargetDefinition(id) {
  if (typeof id !== 'string' || !Object.hasOwn(TARGETS, id)) unavailable();
  return TARGETS[id];
}

export function resolveDesktopTarget({ platform, architecture, libc = null } = {}) {
  if (!['arm64', 'x64'].includes(architecture)) unavailable();
  const suffix = platform === 'linux'
    ? (libc === 'gnu' ? `-${libc}` : unavailable())
    : (libc === null || libc === undefined ? '' : unavailable());
  return desktopTargetDefinition(`${platform}-${architecture}${suffix}`);
}

export function validateDesktopTargetCatalog(targets) {
  if (!targets || Array.isArray(targets) || typeof targets !== 'object' ||
      Object.keys(targets).sort().join('\0') !== DESKTOP_TARGET_IDS.join('\0')) {
    throw new DesktopTargetError('release_target_catalog_invalid');
  }
  return targets;
}

export function selectDesktopTarget(targets, host) {
  const target = resolveDesktopTarget(host);
  if (!targets || Array.isArray(targets) || typeof targets !== 'object' || !Object.hasOwn(targets, target.id)) unavailable();
  return targets[target.id];
}
