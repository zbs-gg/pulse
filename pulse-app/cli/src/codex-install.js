import {
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { createHash } from 'node:crypto';
import { homedir } from 'node:os';
import { basename, dirname, join, resolve } from 'node:path';

import { migrateLegacyPulseHookConfig } from './host-adapter.js';

const RUNTIME_MANIFEST = 'runtime-manifest.json';

export function codexHomePath(env = process.env) {
  return resolve(env.CODEX_HOME || join(homedir(), '.codex'));
}

export function legacyCodexHookPaths({ cwd = process.cwd(), codexHome = codexHomePath() } = {}) {
  return [...new Set([
    join(codexHome, 'hooks.json'),
    resolve(cwd, '.codex', 'hooks.json'),
  ])];
}

function atomicWriteJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  try {
    writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

export function inspectLegacyPulseHookFiles(options = {}) {
  const files = [];
  let total = 0;
  for (const path of legacyCodexHookPaths(options)) {
    if (!existsSync(path)) continue;
    let raw;
    try {
      raw = JSON.parse(readFileSync(path, 'utf8'));
    } catch {
      throw new Error(`invalid Codex hook JSON: ${path}`);
    }
    const migrated = migrateLegacyPulseHookConfig(raw);
    total += migrated.removed;
    files.push({ path, removed: migrated.removed, config: migrated.config });
  }
  return { removed: total, files };
}

export function migrateLegacyPulseHookFiles(options = {}) {
  const inspection = inspectLegacyPulseHookFiles(options);
  for (const file of inspection.files) {
    if (file.removed > 0) atomicWriteJSON(file.path, file.config);
  }
  return inspection;
}

function hasPulseProductTable(text) {
  const name = '(?:pulse-product|"pulse-product"|\'pulse-product\')';
  return new RegExp(`^\\s*\\[\\s*mcp_servers\\s*\\.\\s*${name}\\s*\\]\\s*$`, 'm').test(text) ||
    new RegExp(`^\\s*mcp_servers\\s*\\.\\s*${name}\\s*=`, 'm').test(text);
}

export function pulseProductMcpShadowFiles({ cwd = process.cwd(), codexHome = codexHomePath() } = {}) {
  const paths = [...new Set([
    join(codexHome, 'config.toml'),
    resolve(cwd, '.codex', 'config.toml'),
  ])];
  return paths.filter((path) => existsSync(path) && hasPulseProductTable(readFileSync(path, 'utf8')));
}

export function parsePulsePluginList(output) {
  if (typeof output !== 'string') return { enabled: false, path: undefined };
  for (const line of output.split(/\r?\n/)) {
    const match = line.match(/^pulse@zbs-gg\s{2,}(installed, enabled|installed, disabled)\s{2,}(\S+)\s{2,}(.+?)\s*$/);
    if (match) return { enabled: match[1] === 'installed, enabled', version: match[2], path: match[3] };
  }
  return { enabled: false, path: undefined };
}

function runtimeRoot(dataDir) {
  return join(dataDir, 'runtime', 'codex');
}

function runtimeTreeDigest(root) {
  const hash = createHash('sha256');
  const visit = (directory, prefix = '') => {
    for (const name of readdirSync(directory).sort()) {
      if (prefix === '' && name === RUNTIME_MANIFEST) continue;
      const path = join(directory, name);
      const relative = prefix ? `${prefix}/${name}` : name;
      const info = lstatSync(path);
      if (info.isSymbolicLink()) throw new Error(`Codex runtime contains a symlink: ${relative}`);
      if (info.isDirectory()) {
        visit(path, relative);
      } else if (info.isFile()) {
        hash.update(relative);
        hash.update('\x00');
        hash.update(readFileSync(path));
        hash.update('\x00');
      } else {
        throw new Error(`Codex runtime contains an unsupported entry: ${relative}`);
      }
    }
  };
  visit(root);
  return hash.digest('hex');
}

function includeRuntimePath(sourceRoot, sourcePath) {
  const relative = sourcePath.slice(sourceRoot.length).replace(/^\/+/, '');
  if (relative === '') return true;
  const top = relative.split('/')[0];
  if (!new Set(['src', 'vendor', 'node_modules', 'package.json', 'LICENSE']).has(top)) return false;
  if (top === 'vendor' && relative !== 'vendor' && !relative.startsWith('vendor/pulse-mcp-dist')) return false;
  return !relative.endsWith('.test.js') && !relative.endsWith('.map') && !relative.endsWith('.d.ts');
}

export function installCodexRuntime(packageRoot, dataDir, options = {}) {
  const root = runtimeRoot(dataDir);
  const current = join(root, 'current');
  const previous = join(root, 'previous');
  const next = join(root, `next-${process.pid}-${Date.now()}`);
  mkdirSync(root, { recursive: true, mode: 0o700 });
  try {
    cpSync(packageRoot, next, {
      recursive: true,
      dereference: true,
      filter: (sourcePath) => includeRuntimePath(packageRoot, sourcePath),
    });
    if (!existsSync(join(next, 'node_modules'))) {
      const hoisted = resolve(packageRoot, '..', '..');
      if (basename(hoisted) !== 'node_modules' || !existsSync(hoisted)) {
        throw new Error('Codex runtime dependencies are unavailable');
      }
      mkdirSync(join(next, 'node_modules'), { recursive: true, mode: 0o700 });
      for (const name of readdirSync(hoisted)) {
        if (name === '.bin' || name.startsWith('@')) continue;
        cpSync(join(hoisted, name), join(next, 'node_modules', name), {
          recursive: true,
          dereference: true,
        });
      }
      for (const scope of readdirSync(hoisted).filter((name) => name.startsWith('@') && name !== '@zbs-gg')) {
        cpSync(join(hoisted, scope), join(next, 'node_modules', scope), {
          recursive: true,
          dereference: true,
        });
      }
    }
    const entrypoint = join(next, 'src', 'cli.js');
    if (!existsSync(entrypoint) || !existsSync(join(next, 'vendor', 'pulse-mcp-dist', 'index.js'))) {
      throw new Error('Codex runtime package is incomplete');
    }
    const digest = runtimeTreeDigest(next);
    const packageJSON = JSON.parse(readFileSync(join(next, 'package.json'), 'utf8'));
    atomicWriteJSON(join(next, RUNTIME_MANIFEST), {
      schema: 'pulse.codex_runtime.v1',
      package_version: packageJSON.version,
      tree_digest: digest,
      installed_at: (options.now ?? new Date()).toISOString(),
      entrypoint: 'src/cli.js',
    });
    rmSync(previous, { recursive: true, force: true });
    if (existsSync(current)) renameSync(current, previous);
    try {
      renameSync(next, current);
    } catch (error) {
      if (existsSync(previous) && !existsSync(current)) renameSync(previous, current);
      throw error;
    }
    rmSync(previous, { recursive: true, force: true });
    return inspectCodexRuntime(dataDir);
  } finally {
    rmSync(next, { recursive: true, force: true });
  }
}

export function inspectCodexRuntime(dataDir) {
  const current = join(runtimeRoot(dataDir), 'current');
  const manifestPath = join(current, RUNTIME_MANIFEST);
  if (!existsSync(manifestPath)) return { ok: false, detail: 'trusted Codex runtime is not installed' };
  try {
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    if (manifest?.schema !== 'pulse.codex_runtime.v1' ||
        !/^[a-f0-9]{64}$/.test(manifest.tree_digest ?? '') ||
        manifest.entrypoint !== 'src/cli.js') {
      throw new Error('runtime manifest is invalid');
    }
    const actual = runtimeTreeDigest(current);
    if (actual !== manifest.tree_digest) throw new Error('runtime integrity digest mismatch');
    return {
      ok: true,
      detail: `local immutable runtime ${manifest.package_version}`,
      path: join(current, manifest.entrypoint),
      digest: manifest.tree_digest,
      manifest,
    };
  } catch (error) {
    return { ok: false, detail: error.message };
  }
}
