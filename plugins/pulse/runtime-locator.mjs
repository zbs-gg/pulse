import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, readdirSync, realpathSync } from 'node:fs';
import { homedir } from 'node:os';
import { isAbsolute, join, resolve } from 'node:path';

function workspaceDigest(canonicalPath) {
  return createHash('sha256')
    .update('pulse-codex-product-locator-v1\x00')
    .update(canonicalPath)
    .digest('hex');
}

function canonicalWorkspace(cwd) {
  const result = spawnSync('/usr/bin/git', ['-C', cwd, 'rev-parse', '--show-toplevel'], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (result.status !== 0) throw new Error('Pulse product locator requires a Git workspace');
  return realpathSync(resolve(result.stdout.trim()));
}

function requirePrivateLocator(path) {
  const info = lstatSync(path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0 ||
      info.size > 1024 * 1024) {
    throw new Error('Pulse product locator is unsafe');
  }
}

function requireOwnerControlledRuntimeEntry(path, relative, expectedKind) {
	const info = lstatSync(path);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (info.isSymbolicLink()) {
		throw new Error(`Pulse trusted runtime contains a symlink: ${relative}`);
	}
	if (info.uid !== currentUID || (info.mode & 0o022) !== 0 ||
		(expectedKind === 'directory' ? !info.isDirectory() : !info.isFile())) {
		throw new Error(`Pulse trusted runtime entry is unsafe: ${relative}`);
	}
	return info;
}

// Keep this byte-for-byte compatible with pulse-app/cli/src/codex-install.js.
// The plugin owns this verification because code inside the installed runtime
// cannot establish its own integrity after Node has already executed it.
function runtimeTreeDigest(root) {
	requireOwnerControlledRuntimeEntry(root, '.', 'directory');
	const hash = createHash('sha256');
	const visit = (directory, prefix = '') => {
		for (const name of readdirSync(directory).sort()) {
			const path = join(directory, name);
			const relative = prefix ? `${prefix}/${name}` : name;
			const info = lstatSync(path);
			if (info.isSymbolicLink()) {
				throw new Error(`Pulse trusted runtime contains a symlink: ${relative}`);
			}
			const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
			if (info.uid !== currentUID || (info.mode & 0o022) !== 0) {
				throw new Error(`Pulse trusted runtime entry is unsafe: ${relative}`);
			}
			if (info.isDirectory()) {
				visit(path, relative);
			} else if (info.isFile()) {
				if (prefix === '' && name === 'runtime-manifest.json') continue;
				hash.update(relative);
				hash.update('\x00');
				hash.update(readFileSync(path));
				hash.update('\x00');
			} else {
				throw new Error(`Pulse trusted runtime contains an unsupported entry: ${relative}`);
			}
		}
	};
	visit(root);
	return hash.digest('hex');
}

export function resolveProductEnvironment({ cwd = process.cwd(), env = process.env } = {}) {
  const codexHome = resolve(env.CODEX_HOME || join(homedir(), '.codex'));
  const path = join(codexHome, 'pulse', 'product-locators.json');
  requirePrivateLocator(path);
  const locator = JSON.parse(readFileSync(path, 'utf8'));
  const canonical = canonicalWorkspace(cwd);
  const key = workspaceDigest(canonical);
  const entry = locator?.schema === 'pulse.codex_product_locators.v1' ? locator.entries?.[key] : undefined;
	const allowed = ['anchor_path', 'data_dir', 'registry_path', 'public_key_path', 'trust_mode', 'workspace_digest'];
  if (!entry || entry.workspace_digest !== key ||
			Object.keys(entry).length !== allowed.length || Object.keys(entry).some((name) => !allowed.includes(name)) ||
      !['production', 'test'].includes(entry.trust_mode) ||
			![entry.data_dir, entry.registry_path, entry.public_key_path, entry.anchor_path]
        .every((value) => typeof value === 'string' && isAbsolute(value))) {
    throw new Error('Pulse product locator is missing or invalid for this workspace; run `pulse connect codex` again.');
  }
	if (entry.trust_mode === 'test' && env.PULSE_TRUST_MODE !== 'test') {
		throw new Error('Pulse synthetic test locator requires an explicitly test-mode host process; production trust is not active.');
	}
	if (entry.trust_mode === 'production' && env.PULSE_TRUST_MODE === 'test') {
		throw new Error('Pulse host trust mode does not match the production product locator.');
	}
	const activationPath = join(entry.data_dir, 'runtime', 'product-daemon.json');
	requirePrivateLocator(activationPath);
	const activation = JSON.parse(readFileSync(activationPath, 'utf8'));
	const activationKeys = ['activated_at', 'daemon_digest', 'daemon_path', 'runtime_path', 'runtime_tree_digest', 'schema'];
	if (activation?.schema !== 'pulse.product_activation.v2' ||
		Object.keys(activation).length !== activationKeys.length ||
		Object.keys(activation).some((name) => !activationKeys.includes(name)) ||
		![activation.daemon_path, activation.runtime_path].every((value) => typeof value === 'string' && isAbsolute(value)) ||
		![activation.daemon_digest, activation.runtime_tree_digest].every((value) => /^[a-f0-9]{64}$/.test(value ?? '')) ||
		typeof activation.activated_at !== 'string' || Number.isNaN(Date.parse(activation.activated_at))) {
		throw new Error('Pulse product activation is missing or invalid; reconnect this workspace.');
	}
	const expectedRuntimePath = join(entry.data_dir, 'runtime', 'codex', 'current', 'src', 'cli.js');
	if (resolve(activation.runtime_path) !== resolve(expectedRuntimePath)) {
		throw new Error('Pulse product activation runtime does not match the shared runtime root.');
	}
	const runtimeRoot = resolve(activation.runtime_path, '..', '..');
	const actualRuntimeDigest = runtimeTreeDigest(runtimeRoot);
	const runtimeManifest = JSON.parse(readFileSync(join(runtimeRoot, 'runtime-manifest.json'), 'utf8'));
	if (runtimeManifest?.schema !== 'pulse.codex_runtime.v1' ||
		runtimeManifest.entrypoint !== 'src/cli.js' ||
		runtimeManifest.tree_digest !== activation.runtime_tree_digest ||
		actualRuntimeDigest !== activation.runtime_tree_digest) {
		throw new Error('Pulse product runtime and activation are out of sync; retry after activation completes.');
	}
	const daemonPath = realpathSync(resolve(activation.daemon_path));
	const daemonInfo = lstatSync(daemonPath);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : daemonInfo.uid;
	if (!daemonInfo.isFile() || daemonInfo.isSymbolicLink() || daemonInfo.uid !== currentUID ||
		(daemonInfo.mode & 0o077) !== 0 || (daemonInfo.mode & 0o111) === 0 ||
		createHash('sha256').update(readFileSync(daemonPath)).digest('hex') !== activation.daemon_digest) {
		throw new Error('Pulse product daemon activation failed integrity validation.');
	}
	const productEnvironment = {
    PULSE_DATA_DIR: entry.data_dir,
		PULSE_RUNTIME_PATH: activation.runtime_path,
		PULSE_RUNTIME_DIGEST: activation.runtime_tree_digest,
  };
  if (entry.trust_mode === 'test') {
    productEnvironment.PULSE_TRUST_MODE = 'test';
    productEnvironment.PULSE_BINDING_REGISTRY_PATH = entry.registry_path;
    productEnvironment.PULSE_BINDING_PUBLIC_KEY_PATH = entry.public_key_path;
    productEnvironment.PULSE_BINDING_ANCHOR_PATH = entry.anchor_path;
  }
  return productEnvironment;
}
