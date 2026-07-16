import { createHash } from 'node:crypto';
import {
  cpSync, existsSync, lstatSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';

export function cursorHomePath(env = process.env) {
  return resolve(env.CURSOR_HOME || join(homedir(), '.cursor'));
}

function cursorPluginPath(cursorHome) {
  return join(resolve(cursorHome), 'plugins', 'local', 'pulse');
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
