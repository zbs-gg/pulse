const PERSONAL_SCHEMA = 'pulse.personal_authority_profile.v1';
const ENHANCED_SCHEMA = 'pulse.enhanced_presence.profile.v1';
const PROTECTED_ACTIONS = Object.freeze(['binding.change', 'vault.wipe']);
const ENHANCED_KINDS = new Set(['unavailable', 'webauthn', 'macos_native']);
const SAFE_REASON_CODE = /^[a-z][a-z0-9_]{0,63}$/;

function unavailableEnhancedPresence(reasonCode = 'enhanced_presence_unavailable') {
  return {
    schema: ENHANCED_SCHEMA,
    version: 1,
    kind: 'unavailable',
    available: false,
    protected_actions: [],
    reason_code: SAFE_REASON_CODE.test(reasonCode)
      ? reasonCode
      : 'enhanced_presence_unavailable',
  };
}

function normalizeEnhancedPresence(value) {
  if (value === undefined) return unavailableEnhancedPresence();
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      value.schema !== ENHANCED_SCHEMA || value.version !== 1 ||
      !ENHANCED_KINDS.has(value.kind) || typeof value.available !== 'boolean' ||
      !Array.isArray(value.protected_actions)) {
    throw new TypeError('personal authority enhanced-presence profile is invalid');
  }
  if (!value.available) {
    if (value.protected_actions.length !== 0 || !SAFE_REASON_CODE.test(value.reason_code ?? '')) {
      throw new TypeError('personal authority enhanced-presence profile is invalid');
    }
  } else if (value.kind === 'unavailable' || value.reason_code !== '' ||
      value.protected_actions.length !== PROTECTED_ACTIONS.length ||
      value.protected_actions.some((action, index) => action !== PROTECTED_ACTIONS[index])) {
    throw new TypeError('personal authority enhanced-presence profile is invalid');
  }
  return {
    schema: ENHANCED_SCHEMA,
    version: 1,
    kind: value.kind,
    available: value.available,
    protected_actions: [...value.protected_actions],
    reason_code: value.reason_code,
  };
}

export function portablePersonalAuthorityProfile(enhancedPresence) {
  return {
    schema: PERSONAL_SCHEMA,
    version: 1,
    kind: 'portable',
    ordinary_ready: true,
    enhanced_presence: normalizeEnhancedPresence(enhancedPresence),
  };
}

export function normalizePersonalAuthorityProfile(value) {
  if (value === undefined) return portablePersonalAuthorityProfile();
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      value.schema !== PERSONAL_SCHEMA || value.version !== 1 ||
      value.kind !== 'portable' || value.ordinary_ready !== true) {
    throw new TypeError('personal authority profile is invalid');
  }
  return portablePersonalAuthorityProfile(value.enhanced_presence);
}

export const PERSONAL_AUTHORITY_PROFILE_SCHEMA = PERSONAL_SCHEMA;
export const ENHANCED_PRESENCE_PROFILE_SCHEMA = ENHANCED_SCHEMA;
export const PERSONAL_PROTECTED_ACTIONS = PROTECTED_ACTIONS;
