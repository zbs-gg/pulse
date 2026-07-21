import { existsSync, realpathSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join, resolve } from 'node:path';

import { recoverWorkspaceBindingTransaction } from './binding-admin.js';
import { resolveBoundCodexRuntime } from './codex-runtime.js';
import { defaultBindingPaths } from './workspace-binding.js';
import { createHookWorkerRuntimeResolver, serveProductHookWorker } from './product-hook-worker.js';

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
  return eventName === 'SessionStart' || eventName === 'sessionStart';
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

function productHookWitnessPaths(resolved, environment = process.env) {
  const defaults = defaultBindingPaths();
  const authority = environment.PULSE_TRUST_MODE === 'test'
    ? [
        environment.PULSE_BINDING_REGISTRY_PATH,
        environment.PULSE_BINDING_PUBLIC_KEY_PATH,
        environment.PULSE_BINDING_ANCHOR_PATH,
      ]
    : [defaults.registryPath, defaults.publicKeyPath, defaults.anchorPath];
  const dataDir = resolved.runtime.data_dir;
  return [
    ...authority,
    join(dataDir, 'capture-state.json'),
    join(dataDir, 'runtime', 'product-daemon.json'),
    join(dataDir, 'secret.key'),
    resolved.runtime.pid_file,
  ].filter((path) => typeof path === 'string' && existsSync(path));
}

function syntheticHookDiagnostic(error) {
  const value = typeof error?.code === 'string' ? error.code : error?.message;
  return typeof value === 'string' && /^[A-Za-z0-9_:-]{1,128}$/.test(value)
    ? value
    : 'hook_failure_unclassified';
}

export async function runProductHookWorker(host, environment = process.env, dependencies = {}) {
  const runtimeResolver = createHookWorkerRuntimeResolver({
    host,
    resolveRuntime: (input) => resolveBoundCodexRuntime(input, { host }),
    witnessPaths: (resolved) => productHookWitnessPaths(resolved, environment),
  });
  await (dependencies.serveWorker ?? serveProductHookWorker)({
    host,
    token: environment.PULSE_HOOK_WORKER_TOKEN,
    receiptPath: environment.PULSE_HOOK_WORKER_RECEIPT,
    workspaceDigest: environment.PULSE_HOOK_WORKER_WORKSPACE_DIGEST,
    runtimeDigest: environment.PULSE_RUNTIME_DIGEST,
    pluginDigest: environment.PULSE_PLUGIN_TREE_DIGEST,
    hookDigest: environment.PULSE_HOOK_BUNDLE_DIGEST,
    handleRequest: async (eventName, input) => {
      runtimeResolver.begin(eventName, input);
      let output = '';
      await runProductHookEntrypoint(host, eventName, {
        input,
        resolveRuntime: runtimeResolver.resolve,
        writeOutput: async (serialized) => { output += serialized; },
        ...(environment.PULSE_TRUST_MODE === 'test' ? { degradedReason: syntheticHookDiagnostic } : {}),
      });
      return output;
    },
  });
}

export const __productHookEntrypointTest = Object.freeze({
  invokedAsMain,
  productHookWitnessPaths,
  recoverProductBindingAuthority,
  requiresProductBindingRecovery,
  syntheticHookDiagnostic,
});

function invokedAsMain(entrypoint = process.argv[1], moduleURL = import.meta.url, dependencies = {}) {
  if (!entrypoint) return false;
  const canonical = dependencies.realpath ?? realpathSync;
  try {
    return canonical(resolve(entrypoint)) === canonical(fileURLToPath(moduleURL));
  } catch {
    return false;
  }
}

if (invokedAsMain() &&
    process.argv[2] === '--pulse-hook-worker') {
  await runProductHookWorker(process.argv[3]);
}
