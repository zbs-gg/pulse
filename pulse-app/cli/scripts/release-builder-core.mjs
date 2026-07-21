import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createGzip } from 'node:zlib';
import {
  chmodSync, closeSync, constants, createReadStream, createWriteStream, fstatSync, lstatSync, mkdirSync,
  openSync, readFileSync, readSync, readdirSync, writeFileSync,
} from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';
import { pipeline } from 'node:stream/promises';
import { pack } from 'tar-stream';

const TARGETS = Object.freeze({
  'darwin-arm64': Object.freeze({ architecture: 'arm64', daemon_name: 'pulse', goarch: 'arm64', goos: 'darwin', libc: null, platform: 'darwin', target_id: 'darwin-arm64' }),
  'darwin-x64': Object.freeze({ architecture: 'x64', daemon_name: 'pulse', goarch: 'amd64', goos: 'darwin', libc: null, platform: 'darwin', target_id: 'darwin-x64' }),
  'win32-arm64': Object.freeze({ architecture: 'arm64', daemon_name: 'pulse.exe', goarch: 'arm64', goos: 'windows', libc: null, platform: 'win32', target_id: 'win32-arm64' }),
  'win32-x64': Object.freeze({ architecture: 'x64', daemon_name: 'pulse.exe', goarch: 'amd64', goos: 'windows', libc: null, platform: 'win32', target_id: 'win32-x64' }),
  'linux-arm64-gnu': Object.freeze({ architecture: 'arm64', daemon_name: 'pulse', goarch: 'arm64', goos: 'linux', libc: 'gnu', platform: 'linux', target_id: 'linux-arm64-gnu' }),
  'linux-x64-gnu': Object.freeze({ architecture: 'x64', daemon_name: 'pulse', goarch: 'amd64', goos: 'linux', libc: 'gnu', platform: 'linux', target_id: 'linux-x64-gnu' }),
});

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function absoluteDirectory(path, code) {
  if (typeof path !== 'string' || !isAbsolute(path) || resolve(path) !== path) fail(code);
  return path;
}

function defaultRun(command, args, options) {
  execFileSync(command, args, {
    cwd: options.cwd,
    env: { ...process.env, ...options.env },
    stdio: 'inherit',
    timeout: 30 * 60 * 1000,
  });
}

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number' && Number.isSafeInteger(value) && !Object.is(value, -0)) return String(value);
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  fail('release_canonical_value_invalid');
}

function digestFile(path, maximum = 64 * 1024 * 1024 * 1024, minimum = 1) {
  let descriptor;
  try {
    descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    const before = fstatSync(descriptor);
    if (!before.isFile() || before.nlink !== 1 || before.size < minimum || before.size > maximum) {
      fail('release_tree_file_invalid');
    }
    const hash = createHash('sha256');
    const buffer = Buffer.allocUnsafe(1024 * 1024);
    let bytes = 0;
    for (;;) {
      const count = readSync(descriptor, buffer, 0, buffer.length, null);
      if (count === 0) break;
      bytes += count;
      hash.update(buffer.subarray(0, count));
    }
    const after = fstatSync(descriptor);
    if (bytes !== before.size || after.size !== before.size || after.mtimeMs !== before.mtimeMs) {
      fail('release_tree_file_changed');
    }
    return Object.freeze({ bytes, sha256: hash.digest('hex') });
  } finally {
    if (descriptor !== undefined) closeSync(descriptor);
  }
}

function walkArtifact(root, directory, files) {
  for (const name of readdirSync(directory).sort()) {
    if (name === 'pulse-artifact-tree.json') continue;
    if (!name || name === '.DS_Store' || name.includes('\0') || name.includes('/')) fail('release_tree_entry_invalid');
    const path = join(directory, name);
    const stat = lstatSync(path);
    if (stat.isSymbolicLink() || (!stat.isDirectory() && !stat.isFile()) || (stat.isFile() && stat.nlink !== 1)) {
      fail('release_tree_entry_unsafe');
    }
    if (stat.isDirectory()) {
      chmodSync(path, 0o700);
      walkArtifact(root, path, files);
      continue;
    }
    const relativePath = path.slice(root.length + 1).split('\\').join('/');
    const executable = (stat.mode & 0o111) !== 0;
    const mode = executable ? 0o700 : 0o600;
    chmodSync(path, mode);
    const digest = digestFile(path, 4 * 1024 * 1024 * 1024, 0);
    files.push(Object.freeze({
      bytes: digest.bytes,
      executable,
      mode,
      path: relativePath,
      sha256: digest.sha256,
    }));
  }
}

export function prepareNormalizedArtifact(root) {
  absoluteDirectory(root, 'release_artifact_root_invalid');
  const stat = lstatSync(root);
  if (!stat.isDirectory() || stat.isSymbolicLink()) fail('release_artifact_root_invalid');
  chmodSync(root, 0o700);
  const files = [];
  walkArtifact(root, root, files);
  if (files.length < 1 || files.length > 8191) fail('release_tree_file_count_invalid');
  const tree = Object.freeze({ files: Object.freeze(files), schema: 'pulse.artifact_tree.v1' });
  const treeJSON = `${canonical(tree)}\n`;
  const manifestPath = join(root, 'pulse-artifact-tree.json');
  writeFileSync(manifestPath, treeJSON, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
  return Object.freeze({
    files: tree.files.length,
    tree,
    tree_digest: createHash('sha256').update(canonical(tree)).digest('hex'),
  });
}

export async function packNormalizedArtifact(root, outputPath) {
  absoluteDirectory(root, 'release_artifact_root_invalid');
  if (!isAbsolute(outputPath) || resolve(outputPath) !== outputPath || !outputPath.endsWith('.tar.gz')) {
    fail('release_carrier_path_invalid');
  }
  const treePath = join(root, 'pulse-artifact-tree.json');
  const tree = JSON.parse(readFileSync(treePath, 'utf8'));
  const entries = [
    ...tree.files.map((file) => ({ ...file, source: join(root, file.path) })),
    {
      bytes: lstatSync(treePath).size, executable: false,
      mode: 0o600, path: 'pulse-artifact-tree.json', source: treePath,
    },
  ].sort((left, right) => left.path < right.path ? -1 : left.path > right.path ? 1 : 0);
  mkdirSync(resolve(outputPath, '..'), { recursive: true, mode: 0o700 });
  const archive = pack();
  const writing = pipeline(
    archive,
    createGzip({ level: 9, mtime: 0 }),
    createWriteStream(outputPath, { flags: 'wx', mode: 0o600 }),
  );
  for (const entry of entries) {
    const output = archive.entry({
      gid: 0, gname: '', mode: entry.mode, mtime: new Date(0), name: entry.path,
      size: entry.bytes, type: 'file', uid: 0, uname: '',
    });
    await pipeline(createReadStream(entry.source), output);
    const packed = digestFile(entry.source, 4 * 1024 * 1024 * 1024, 0);
    if (packed.bytes !== entry.bytes || (entry.sha256 !== undefined && packed.sha256 !== entry.sha256)) {
      fail('release_tree_file_changed');
    }
  }
  archive.finalize();
  await writing;
  const carrier = digestFile(outputPath);
  return Object.freeze({
    bytes: carrier.bytes,
    format: 'tar.gz',
    sha256: carrier.sha256,
  });
}

export function releaseTargetDefinition(targetID) {
  const target = TARGETS[targetID];
  if (!target) fail('release_target_invalid');
  return { ...target };
}

export function releaseTargetIDs() {
  return Object.freeze(Object.keys(TARGETS));
}

export function buildDaemonTarget({ appRoot, outputRoot, run = defaultRun, targetID } = {}) {
  absoluteDirectory(appRoot, 'release_app_root_invalid');
  absoluteDirectory(outputRoot, 'release_output_root_invalid');
  const target = releaseTargetDefinition(targetID);
  const binary = join(outputRoot, 'bin', target.daemon_name);
  mkdirSync(join(outputRoot, 'bin'), { recursive: true, mode: 0o700 });
  run('go', [
    'build', '-trimpath', '-ldflags=-s -w -buildid=', '-o', binary, './cmd/pulse',
  ], {
    cwd: appRoot,
    env: { CGO_ENABLED: '0', GOARCH: target.goarch, GOOS: target.goos },
  });
  const stat = lstatSync(binary);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1) fail('release_daemon_build_missing');
  chmodSync(binary, 0o700);
  const digest = digestFile(binary);
  const evidence = Object.freeze({
    architecture: target.architecture,
    cgo_enabled: false,
    executable_relative_path: `bin/${target.daemon_name}`,
    goarch: target.goarch,
    goos: target.goos,
    production_ready: false,
    schema: 'pulse.target_daemon_build.v2',
    sha256: digest.sha256,
    target_id: target.target_id,
  });
  writeFileSync(join(outputRoot, 'pulse-target-build.json'), `${JSON.stringify(evidence)}\n`, {
    encoding: 'utf8', flag: 'wx', mode: 0o600,
  });
  return evidence;
}
