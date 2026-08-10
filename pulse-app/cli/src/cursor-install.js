import { createHash } from 'node:crypto';
import {
  chmodSync, cpSync, existsSync, lstatSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync,
  writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';

export function cursorHomePath(env = process.env) {
  return resolve(env.CURSOR_HOME || join(homedir(), '.cursor'));
}

function cursorPluginPath(cursorHome) {
  return join(resolve(cursorHome), 'plugins', 'local', 'pulse');
}

const PULSE_CURSOR_HOOKS = ['beforeSubmitPrompt', 'preToolUse', 'postToolUse'];

function cursorConfigPaths(cursorHome) {
  const root = resolve(cursorHome);
  return { hooks: join(root, 'hooks.json'), mcp: join(root, 'mcp.json') };
}

function readJSONFile(path, fallback) {
  if (!existsSync(path)) return structuredClone(fallback);
  const value = JSON.parse(readFileSync(path, 'utf8'));
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('cursor_config_invalid');
  return value;
}

function atomicWriteJSON(path, value) {
  const parent = resolve(path, '..');
  mkdirSync(parent, { recursive: true, mode: 0o700 });
  const next = join(parent, `.${path.split('/').pop()}.pulse-next-${process.pid}-${Date.now()}`);
  try {
    writeFileSync(next, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
    chmodSync(next, 0o600);
    renameSync(next, path);
  } finally {
    rmSync(next, { force: true });
  }
}

function pulseHookCommand(pluginRoot, event) {
  return `node ${JSON.stringify(join(resolve(pluginRoot), 'hooks', 'cursor-hook.mjs'))} ${event}`;
}

function isPulseHook(entry) {
  return typeof entry?.command === 'string' && entry.command.includes('/hooks/cursor-hook.mjs');
}

function mergedCursorHooks(current, pluginRoot) {
  const hooks = current.hooks && typeof current.hooks === 'object' && !Array.isArray(current.hooks)
    ? structuredClone(current.hooks) : {};
  for (const [event, entries] of Object.entries(hooks)) {
    if (!Array.isArray(entries)) throw new Error('cursor_hooks_invalid');
    hooks[event] = entries.filter((entry) => !isPulseHook(entry));
  }
  for (const event of PULSE_CURSOR_HOOKS) {
    hooks[event] ??= [];
    hooks[event].push({ command: pulseHookCommand(pluginRoot, event), timeout: 10 });
  }
  return { ...current, version: current.version ?? 1, hooks };
}

function mergedCursorMCP(current, pluginRoot) {
  const mcpServers = current.mcpServers && typeof current.mcpServers === 'object' && !Array.isArray(current.mcpServers)
    ? structuredClone(current.mcpServers) : {};
  delete mcpServers.pulse_local;
  mcpServers['pulse-product'] = {
    command: 'node', args: [join(resolve(pluginRoot), 'mcp', 'cursor-server.mjs')],
  };
  return { ...current, mcpServers };
}

function restoreFile(path, existed, bytes) {
  if (!existed) {
    rmSync(path, { force: true });
    return;
  }
  writeFileSync(path, bytes, { mode: 0o600 });
  chmodSync(path, 0o600);
}

function exactTreeDigest(root, label) {
  const base = resolve(root);
  const rootInfo = lstatSync(base);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : rootInfo.uid;
  if (!rootInfo.isDirectory() || rootInfo.isSymbolicLink() || rootInfo.uid !== currentUID ||
      (rootInfo.mode & 0o022) !== 0) {
    throw new Error(`${label}_unsafe`);
  }
  const hash = createHash('sha256');
  const visit = (directory, prefix = '') => {
    for (const name of readdirSync(directory).sort()) {
      const path = join(directory, name);
      const relative = prefix ? `${prefix}/${name}` : name;
      const info = lstatSync(path);
      if (info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o022) !== 0 ||
          (info.isFile() && info.nlink !== 1)) {
        throw new Error(`${label}_unsafe`);
      }
      if (info.isDirectory()) visit(path, relative);
      else if (info.isFile()) {
        hash.update(relative);
        hash.update('\x00');
        hash.update(readFileSync(path));
        hash.update('\x00');
      } else {
        throw new Error(`${label}_unsafe`);
      }
    }
  };
  visit(base);
  return hash.digest('hex');
}

function validCursorManifest(root) {
  try {
    const manifest = JSON.parse(readFileSync(join(root, '.cursor-plugin', 'plugin.json'), 'utf8'));
    return manifest?.name === 'pulse' && typeof manifest.version === 'string' && manifest.version.length > 0 &&
      manifest.hooks === './cursor-hooks/hooks.json' &&
      manifest.mcpServers?.['pulse-product']?.command === 'node' &&
      JSON.stringify(manifest.mcpServers['pulse-product'].args) ===
        JSON.stringify(['${CURSOR_PLUGIN_ROOT}/mcp/cursor-server.mjs']) &&
      existsSync(join(root, 'hooks', 'cursor-hook.mjs')) &&
      existsSync(join(root, 'mcp', 'cursor-server.mjs'));
  } catch {
    return false;
  }
}

export function inspectCursorPlugin({ cursorHome = cursorHomePath(), expectedDigest } = {}) {
  const path = cursorPluginPath(cursorHome);
  if (!existsSync(path)) return { ready: false, reason: 'cursor_plugin_missing', path };
  try {
    const digest = exactTreeDigest(path, 'cursor_plugin');
    if (!validCursorManifest(path)) return { ready: false, reason: 'cursor_plugin_manifest_invalid', path, digest };
    if (expectedDigest !== undefined && digest !== expectedDigest) {
      return { ready: false, reason: 'cursor_plugin_digest_mismatch', path, digest };
    }
    return { ready: true, reason: 'cursor_plugin_exact', path, digest };
  } catch (error) {
    return { ready: false, reason: 'cursor_plugin_unsafe', path, detail: error.message };
  }
}

export function installCursorPlugin(sourceRoot, {
  cursorHome = cursorHomePath(),
  expectedDigest,
} = {}) {
  const source = resolve(sourceRoot);
  const sourceDigest = exactTreeDigest(source, 'cursor_plugin_source');
  if (expectedDigest !== undefined && sourceDigest !== expectedDigest) {
    throw new Error('cursor_plugin_source_digest_mismatch');
  }
  if (!validCursorManifest(source)) throw new Error('cursor_plugin_source_manifest_invalid');

  const destination = cursorPluginPath(cursorHome);
  const current = inspectCursorPlugin({ cursorHome, expectedDigest: sourceDigest });
  if (current.ready) return { ...current, reused: true };

  const parent = join(resolve(cursorHome), 'plugins', 'local');
  const next = join(parent, `.pulse-next-${process.pid}-${Date.now()}`);
  const previous = join(parent, `.pulse-previous-${process.pid}-${Date.now()}`);
  mkdirSync(parent, { recursive: true, mode: 0o700 });
  let movedPrevious = false;
  try {
    cpSync(source, next, { recursive: true, dereference: false });
    if (exactTreeDigest(next, 'cursor_plugin_staged') !== sourceDigest) {
      throw new Error('cursor_plugin_staged_digest_mismatch');
    }
    if (existsSync(destination)) {
      renameSync(destination, previous);
      movedPrevious = true;
    }
    try {
      renameSync(next, destination);
      const installed = inspectCursorPlugin({ cursorHome, expectedDigest: sourceDigest });
      if (!installed.ready) throw new Error(installed.reason);
      rmSync(previous, { recursive: true, force: true });
      return { ...installed, reused: false };
    } catch (error) {
      rmSync(destination, { recursive: true, force: true });
      if (movedPrevious && existsSync(previous)) renameSync(previous, destination);
      throw error;
    }
  } finally {
    rmSync(next, { recursive: true, force: true });
    if (!movedPrevious || existsSync(destination)) rmSync(previous, { recursive: true, force: true });
  }
}

export function removeCursorPlugin({ cursorHome = cursorHomePath() } = {}) {
  const path = cursorPluginPath(cursorHome);
  if (!existsSync(path)) return { removed: false };
  const current = inspectCursorPlugin({ cursorHome });
  if (current.reason === 'cursor_plugin_unsafe') throw new Error('cursor_plugin_unsafe');
  rmSync(path, { recursive: true, force: false });
  if (existsSync(path)) throw new Error('cursor_plugin_remove_failed');
  return { removed: true };
}

export function inspectCursorNativeIntegration({
  cursorHome = cursorHomePath(), pluginRoot, expectedDigest,
} = {}) {
  if (!pluginRoot) return { ready: false, reason: 'cursor_runtime_missing' };
  const source = resolve(pluginRoot);
  let digest;
  try {
    digest = exactTreeDigest(source, 'cursor_runtime_source');
    if (!validCursorManifest(source)) {
      return { ready: false, reason: 'cursor_runtime_manifest_invalid', plugin_root: source, digest };
    }
    if (expectedDigest !== undefined && digest !== expectedDigest) {
      return { ready: false, reason: 'cursor_runtime_digest_mismatch', plugin_root: source, digest };
    }
    const paths = cursorConfigPaths(cursorHome);
    const hooks = readJSONFile(paths.hooks, { version: 1, hooks: {} });
    const mcp = readJSONFile(paths.mcp, { mcpServers: {} });
    for (const event of PULSE_CURSOR_HOOKS) {
      const matches = Array.isArray(hooks.hooks?.[event])
        ? hooks.hooks[event].filter((entry) => entry?.command === pulseHookCommand(source, event)) : [];
      if (matches.length !== 1) {
        return { ready: false, reason: 'cursor_native_hooks_missing', plugin_root: source, digest };
      }
    }
    const pulseHooks = Object.values(hooks.hooks ?? {}).flatMap((entries) => Array.isArray(entries) ? entries : [])
      .filter((entry) => isPulseHook(entry));
    if (pulseHooks.length !== PULSE_CURSOR_HOOKS.length) {
      return { ready: false, reason: 'cursor_native_hooks_duplicate', plugin_root: source, digest };
    }
    const server = mcp.mcpServers?.['pulse-product'];
    if (server?.command !== 'node' || JSON.stringify(server.args) !==
        JSON.stringify([join(source, 'mcp', 'cursor-server.mjs')])) {
      return { ready: false, reason: 'cursor_native_mcp_missing', plugin_root: source, digest };
    }
    if (Object.hasOwn(mcp.mcpServers ?? {}, 'pulse_local')) {
      return { ready: false, reason: 'cursor_legacy_mcp_present', plugin_root: source, digest };
    }
    if (existsSync(cursorPluginPath(cursorHome))) {
      return { ready: false, reason: 'cursor_local_plugin_duplicate', plugin_root: source, digest };
    }
    return { ready: true, reason: 'cursor_native_exact', plugin_root: source, digest };
  } catch (error) {
    return { ready: false, reason: 'cursor_native_config_invalid', plugin_root: source, digest, detail: error.message };
  }
}

export function installCursorNativeIntegration(pluginRoot, {
  cursorHome = cursorHomePath(), expectedDigest,
} = {}) {
  const source = resolve(pluginRoot);
  const sourceDigest = exactTreeDigest(source, 'cursor_runtime_source');
  if (expectedDigest !== undefined && sourceDigest !== expectedDigest) {
    throw new Error('cursor_runtime_source_digest_mismatch');
  }
  if (!validCursorManifest(source)) throw new Error('cursor_runtime_source_manifest_invalid');
  const current = inspectCursorNativeIntegration({ cursorHome, pluginRoot: source, expectedDigest: sourceDigest });
  if (current.ready) return { ...current, reused: true };

  const root = resolve(cursorHome);
  const paths = cursorConfigPaths(root);
  mkdirSync(root, { recursive: true, mode: 0o700 });
  const state = {
    hooksExisted: existsSync(paths.hooks),
    hooks: existsSync(paths.hooks) ? readFileSync(paths.hooks) : Buffer.alloc(0),
    mcpExisted: existsSync(paths.mcp),
    mcp: existsSync(paths.mcp) ? readFileSync(paths.mcp) : Buffer.alloc(0),
    pluginExisted: existsSync(cursorPluginPath(root)),
  };
  const stamp = new Date().toISOString().replaceAll(/[:.]/g, '-');
  const backup = join(root, 'pulse-backups', `${stamp}-${process.pid}`);
  mkdirSync(backup, { recursive: true, mode: 0o700 });
  writeFileSync(join(backup, 'state.json'), `${JSON.stringify({
    hooks_existed: state.hooksExisted,
    mcp_existed: state.mcpExisted,
    plugin_existed: state.pluginExisted,
  })}\n`, { mode: 0o600 });
  if (state.hooksExisted) writeFileSync(join(backup, 'hooks.json'), state.hooks, { mode: 0o600 });
  if (state.mcpExisted) writeFileSync(join(backup, 'mcp.json'), state.mcp, { mode: 0o600 });
  if (state.pluginExisted) cpSync(cursorPluginPath(root), join(backup, 'local-plugin'), { recursive: true, dereference: false });

  try {
    const hooks = readJSONFile(paths.hooks, { version: 1, hooks: {} });
    const mcp = readJSONFile(paths.mcp, { mcpServers: {} });
    atomicWriteJSON(paths.hooks, mergedCursorHooks(hooks, source));
    atomicWriteJSON(paths.mcp, mergedCursorMCP(mcp, source));
    if (state.pluginExisted) rmSync(cursorPluginPath(root), { recursive: true, force: false });
    const installed = inspectCursorNativeIntegration({
      cursorHome: root, pluginRoot: source, expectedDigest: sourceDigest,
    });
    if (!installed.ready) throw new Error(installed.reason);
    return { ...installed, reused: false, backup };
  } catch (error) {
    restoreFile(paths.hooks, state.hooksExisted, state.hooks);
    restoreFile(paths.mcp, state.mcpExisted, state.mcp);
    if (state.pluginExisted && !existsSync(cursorPluginPath(root))) {
      cpSync(join(backup, 'local-plugin'), cursorPluginPath(root), { recursive: true, dereference: false });
    }
    throw error;
  }
}

export function restoreCursorNativeIntegrationBackup(backup, {
  cursorHome = cursorHomePath(),
} = {}) {
  const root = resolve(cursorHome);
  const backupRoot = resolve(backup);
  const allowedRoot = join(root, 'pulse-backups');
  if (backupRoot === allowedRoot || !backupRoot.startsWith(`${allowedRoot}/`)) {
    throw new Error('cursor_backup_invalid');
  }
  const state = JSON.parse(readFileSync(join(backupRoot, 'state.json'), 'utf8'));
  const paths = cursorConfigPaths(root);
  restoreFile(paths.hooks, state.hooks_existed === true,
    state.hooks_existed ? readFileSync(join(backupRoot, 'hooks.json')) : Buffer.alloc(0));
  restoreFile(paths.mcp, state.mcp_existed === true,
    state.mcp_existed ? readFileSync(join(backupRoot, 'mcp.json')) : Buffer.alloc(0));
  rmSync(cursorPluginPath(root), { recursive: true, force: true });
  if (state.plugin_existed === true) {
    mkdirSync(join(root, 'plugins', 'local'), { recursive: true, mode: 0o700 });
    cpSync(join(backupRoot, 'local-plugin'), cursorPluginPath(root), { recursive: true, dereference: false });
  }
  return { restored: true };
}

export function removeCursorNativeIntegration({ cursorHome = cursorHomePath() } = {}) {
  const root = resolve(cursorHome);
  const paths = cursorConfigPaths(root);
  let removed = false;
  if (existsSync(paths.hooks)) {
    const hooks = readJSONFile(paths.hooks, { version: 1, hooks: {} });
    for (const [event, entries] of Object.entries(hooks.hooks ?? {})) {
      if (!Array.isArray(entries)) continue;
      const filtered = entries.filter((entry) => !isPulseHook(entry));
      if (filtered.length !== entries.length) removed = true;
      hooks.hooks[event] = filtered;
    }
    atomicWriteJSON(paths.hooks, hooks);
  }
  if (existsSync(paths.mcp)) {
    const mcp = readJSONFile(paths.mcp, { mcpServers: {} });
    for (const name of ['pulse-product', 'pulse_local']) {
      if (Object.hasOwn(mcp.mcpServers ?? {}, name)) {
        delete mcp.mcpServers[name];
        removed = true;
      }
    }
    atomicWriteJSON(paths.mcp, mcp);
  }
  const legacy = removeCursorPlugin({ cursorHome: root });
  return { removed: removed || legacy.removed };
}
