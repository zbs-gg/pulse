import { recoverWorkspaceBindingTransaction } from './binding-admin.js';

const RUNNERS = Object.freeze({
  'claude-code': async () => (await import('./claude-hooks.js')).runClaudeHookCLI,
  codex: async () => (await import('./codex-hooks.js')).runCodexHookCLI,
  cursor: async () => (await import('./cursor-hooks.js')).runCursorHookCLI,
});

async function recoverProductBindingAuthority(env = process.env, dependencies = {}) {
  const recover = dependencies.recoverBinding ?? recoverWorkspaceBindingTransaction;
  if (env.PULSE_TRUST_MODE === 'test') {
    const registryPath = env.PULSE_BINDING_REGISTRY_PATH;
    const publicKeyPath = env.PULSE_BINDING_PUBLIC_KEY_PATH;
    const anchorPath = env.PULSE_BINDING_ANCHOR_PATH;
    if (!registryPath || !publicKeyPath || !anchorPath) {
      throw new Error('synthetic test authority requires registry, public key, and anti-rollback anchor paths');
    }
    await recover({ registryPath, publicKeyPath, anchorPath, rootPublicKey: false, rootAnchor: false });
    return;
  }
  await recover();
}

function requiresProductBindingRecovery(eventName) {
  return eventName === 'SessionStart';
}

export async function runProductHookEntrypoint(host, eventName, dependencies = {}) {
  const loadRunner = RUNNERS[host];
  if (!loadRunner || typeof eventName !== 'string' || eventName.length < 1 || eventName.length > 64) {
    throw new Error('Pulse product hook entrypoint is invalid');
  }
  if (requiresProductBindingRecovery(eventName)) {
    await recoverProductBindingAuthority(dependencies.environment ?? process.env, dependencies);
  }
  const runner = await loadRunner();
  await runner(eventName, dependencies);
}

export const __productHookEntrypointTest = Object.freeze({
  recoverProductBindingAuthority,
  requiresProductBindingRecovery,
});
