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
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';

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
	if (typeof output !== 'string') return { installed: false, enabled: false, path: undefined };
	for (const line of output.split(/\r?\n/)) {
		const match = line.match(/^pulse@zbs-gg\s{2,}(installed, enabled|installed, disabled)\s{2,}(\S+)\s{2,}(.+?)\s*$/);
		if (match) return { installed: true, enabled: match[1] === 'installed, enabled', version: match[2], path: match[3] };
	}
	return { installed: false, enabled: false, path: undefined };
}

function runtimeRoot(dataDir) {
  return join(dataDir, 'runtime', 'codex');
}

function codexProductWorkspaceDigest(canonicalPath) {
  return createHash('sha256')
    .update('pulse-codex-product-locator-v1\x00')
    .update(canonicalPath)
    .digest('hex');
}

export function readCodexProductLocator({ codexHome, binding }) {
	if (!binding?.workspace?.canonical_path) throw new Error('Codex product locator binding is invalid');
	const path = join(resolve(codexHome), 'pulse', 'product-locators.json');
	const info = lstatSync(path);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
		(info.mode & 0o077) !== 0 || info.size > 1024 * 1024) {
		throw new Error('Codex product locator is unsafe');
	}
	const locator = JSON.parse(readFileSync(path, 'utf8'));
	const workspaceDigest = codexProductWorkspaceDigest(binding.workspace.canonical_path);
	if (locator?.schema !== 'pulse.codex_product_locators.v1' ||
		!locator.entries || typeof locator.entries !== 'object' || Array.isArray(locator.entries) ||
		Object.keys(locator).some((name) => !['schema', 'entries'].includes(name))) {
		throw new Error('Codex product locator is invalid');
	}
	const entry = locator.entries[workspaceDigest];
	const allowed = ['data_dir', 'registry_path', 'public_key_path', 'trust_mode', 'workspace_digest'];
	if (!entry) throw new Error('Codex product locator is missing for this workspace');
	if (entry.workspace_digest !== workspaceDigest ||
		Object.keys(entry).length !== allowed.length || Object.keys(entry).some((name) => !allowed.includes(name)) ||
		!['production', 'test'].includes(entry.trust_mode) ||
		![entry.data_dir, entry.registry_path, entry.public_key_path]
			.every((value) => typeof value === 'string' && isAbsolute(value))) {
		throw new Error('Codex product locator is invalid for this workspace');
	}
	return { path, entry };
}

export function writeCodexProductLocator({
	codexHome, binding, dataDir, registryPath, publicKeyPath, trustMode = 'production',
}) {
	if (!binding?.workspace?.canonical_path || !['production', 'test'].includes(trustMode)) {
    throw new Error('Codex product locator input is invalid');
  }
  const directory = join(resolve(codexHome), 'pulse');
  const path = join(directory, 'product-locators.json');
  let current = { schema: 'pulse.codex_product_locators.v1', entries: {} };
  if (existsSync(path)) {
    const info = lstatSync(path);
    const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
    if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0) {
      throw new Error('Codex product locator is unsafe');
    }
    current = JSON.parse(readFileSync(path, 'utf8'));
    if (current?.schema !== 'pulse.codex_product_locators.v1' ||
        !current.entries || typeof current.entries !== 'object' || Array.isArray(current.entries)) {
      throw new Error('Codex product locator is invalid');
    }
  }
  const workspaceDigest = codexProductWorkspaceDigest(binding.workspace.canonical_path);
  current.entries[workspaceDigest] = {
    workspace_digest: workspaceDigest,
    data_dir: resolve(dataDir),
		registry_path: resolve(registryPath),
		public_key_path: resolve(publicKeyPath),
		trust_mode: trustMode,
  };
  atomicWriteJSON(path, current);
  return path;
}

export function removeCodexProductLocator({ codexHome, binding }) {
	const { path } = readCodexProductLocator({ codexHome, binding });
	const locator = JSON.parse(readFileSync(path, 'utf8'));
	const workspaceDigest = codexProductWorkspaceDigest(binding.workspace.canonical_path);
	delete locator.entries[workspaceDigest];
	const remaining = Object.keys(locator.entries).length;
	if (remaining === 0) {
		rmSync(path, { force: true });
	} else {
		atomicWriteJSON(path, locator);
	}
	return { path, remaining };
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

function committedRuntimeDigest(dataDir) {
	const path = join(dataDir, 'runtime', 'product-daemon.json');
	if (!existsSync(path)) {
		throw new Error('Codex runtime activation journal is missing; refusing ambiguous recovery');
	}
	const info = lstatSync(path);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
		(info.mode & 0o077) !== 0 || info.size > 8192) {
		throw new Error('Codex runtime activation journal is unsafe; refusing ambiguous recovery');
	}
	const activation = JSON.parse(readFileSync(path, 'utf8'));
	const allowed = ['activated_at', 'daemon_digest', 'daemon_path', 'runtime_path', 'runtime_tree_digest', 'schema'];
	const expectedRuntimePath = join(runtimeRoot(dataDir), 'current', 'src', 'cli.js');
	if (activation?.schema !== 'pulse.product_activation.v2' ||
		Object.keys(activation).length !== allowed.length ||
		Object.keys(activation).some((name) => !allowed.includes(name)) ||
		resolve(activation.runtime_path ?? '') !== resolve(expectedRuntimePath) ||
		!isAbsolute(activation.daemon_path ?? '') ||
		![activation.runtime_tree_digest, activation.daemon_digest]
			.every((value) => /^[a-f0-9]{64}$/.test(value ?? '')) ||
		typeof activation.activated_at !== 'string' || Number.isNaN(Date.parse(activation.activated_at))) {
		throw new Error('Codex runtime activation journal is invalid; refusing ambiguous recovery');
	}
	return activation.runtime_tree_digest;
}

function recoverInterruptedRuntimeInstall(dataDir) {
	const root = runtimeRoot(dataDir);
	const current = join(root, 'current');
	const previous = join(root, 'previous');
	if (!existsSync(previous)) return { action: 'none' };

	const committedDigest = committedRuntimeDigest(dataDir);
	const currentRuntime = inspectRuntimeRoot(current);
	const previousRuntime = inspectRuntimeRoot(previous);
	if (currentRuntime.ok && currentRuntime.digest === committedDigest) {
		// Activation publication is the commit point. A crash before finalize only
		// left an obsolete rollback tree behind; never replace the committed tree.
		rmSync(previous, { recursive: true, force: true });
		return { action: 'finalized_current', runtime: currentRuntime };
	}
	if (previousRuntime.ok && previousRuntime.digest === committedDigest) {
		// The process died before publishing the staged current tree. Restore the
		// exact tree still named by the durable activation receipt.
		const abandoned = join(root, `abandoned-${process.pid}-${Date.now()}`);
		if (existsSync(current)) renameSync(current, abandoned);
		try {
			renameSync(previous, current);
		} catch (error) {
			if (existsSync(abandoned) && !existsSync(current)) renameSync(abandoned, current);
			throw error;
		}
		rmSync(abandoned, { recursive: true, force: true });
		return { action: 'rolled_back_previous', runtime: previousRuntime };
	}
	throw new Error('Codex runtime activation journal matches neither current nor previous; refusing ambiguous recovery');
}

export function installCodexRuntime(packageRoot, dataDir, options = {}) {
  const root = runtimeRoot(dataDir);
  const current = join(root, 'current');
  const previous = join(root, 'previous');
  const next = join(root, `next-${process.pid}-${Date.now()}`);
  mkdirSync(root, { recursive: true, mode: 0o700 });
	// `previous` is an activation journal entry. If the installer process died
	// before finalize/rollback, the durable product activation decides which
	// tree committed. Ambiguous state is preserved and rejected, never guessed.
	recoverInterruptedRuntimeInstall(dataDir);
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
    if (options.keepPrevious !== true) rmSync(previous, { recursive: true, force: true });
    return inspectCodexRuntime(dataDir);
  } finally {
    rmSync(next, { recursive: true, force: true });
  }
}

export function finalizeCodexRuntimeInstall(dataDir) {
  rmSync(join(runtimeRoot(dataDir), 'previous'), { recursive: true, force: true });
}

export function rollbackCodexRuntimeInstall(dataDir) {
  const root = runtimeRoot(dataDir);
  const current = join(root, 'current');
  const previous = join(root, 'previous');
  rmSync(current, { recursive: true, force: true });
  if (existsSync(previous)) {
    renameSync(previous, current);
    return inspectCodexRuntime(dataDir);
  }
	return {
		ok: true,
		restored: 'absent',
		detail: 'the first staged runtime was removed; no prior runtime existed',
	};
}

function inspectRuntimeRoot(root) {
	const manifestPath = join(root, RUNTIME_MANIFEST);
  if (!existsSync(manifestPath)) return { ok: false, detail: 'trusted Codex runtime is not installed' };
  try {
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    if (manifest?.schema !== 'pulse.codex_runtime.v1' ||
        !/^[a-f0-9]{64}$/.test(manifest.tree_digest ?? '') ||
        manifest.entrypoint !== 'src/cli.js') {
      throw new Error('runtime manifest is invalid');
    }
		const actual = runtimeTreeDigest(root);
    if (actual !== manifest.tree_digest) throw new Error('runtime integrity digest mismatch');
    return {
      ok: true,
      detail: `local immutable runtime ${manifest.package_version}`,
			path: join(root, manifest.entrypoint),
      digest: manifest.tree_digest,
      manifest,
    };
  } catch (error) {
    return { ok: false, detail: error.message };
  }
}

export function inspectCodexRuntime(dataDir) {
	return inspectRuntimeRoot(join(runtimeRoot(dataDir), 'current'));
}

export function inspectCodexRuntimeAt(runtimePath) {
	if (typeof runtimePath !== 'string' || !isAbsolute(runtimePath) ||
		basename(runtimePath) !== 'cli.js' || basename(dirname(runtimePath)) !== 'src') {
		return { ok: false, detail: 'trusted Codex runtime path is invalid' };
	}
	const inspected = inspectRuntimeRoot(dirname(dirname(resolve(runtimePath))));
	if (inspected.ok && inspected.path !== resolve(runtimePath)) {
		return { ok: false, detail: 'trusted Codex runtime entrypoint is invalid' };
	}
	return inspected;
}
