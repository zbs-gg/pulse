import { spawnSync } from 'node:child_process';
import { createHash, randomBytes as cryptoRandomBytes } from 'node:crypto';
import {
  chmodSync, closeSync, constants, fstatSync, fsyncSync, lstatSync, mkdirSync, openSync,
  readFileSync, realpathSync, renameSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { homedir, release as osReleaseDefault } from 'node:os';
import path, { dirname, win32 } from 'node:path';

const SHA256 = /^[a-f0-9]{64}$/;
const STARTUP_NONCE = /^[a-f0-9]{64}$/;
const LOCK_SCHEMA = 'pulse.platform_lock.v1';
const LOCK_WAIT = new Int32Array(new SharedArrayBuffer(4));
const PORT_PROBE = String.raw`
const net = require('node:net');
const port = Number(process.argv[1]);
const server = net.createServer();
const timer = setTimeout(() => process.exit(3), 2500);
server.once('error', (error) => {
  clearTimeout(timer);
  process.exit(error && error.code === 'EADDRINUSE' ? 2 : 3);
});
server.listen({ host: '127.0.0.1', port, exclusive: true }, () => {
  clearTimeout(timer);
  server.close(() => process.exit(0));
});
`;

export class PlatformServicesError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = 'PlatformServicesError';
    this.code = code;
  }
}

function fail(code, message) {
  throw new PlatformServicesError(code, message);
}

function exactObject(value, keys) {
  return value && !Array.isArray(value) && typeof value === 'object' &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
}

function nativeOperation(nativeAdapter, name) {
  const operation = nativeAdapter?.[name];
  if (typeof operation !== 'function') {
    fail('platform_native_adapter_unavailable', `native ${name} adapter is unavailable`);
  }
  return operation.bind(nativeAdapter);
}

function canonicalVersion(value) {
  const match = String(value ?? '').match(/^(\d+)\.(\d+)(?:\.(\d+))?/);
  if (!match) fail('platform_os_version_invalid', 'desktop OS version is invalid');
  return `${match[1]}.${match[2]}.${match[3] ?? '0'}`;
}

function windowsCandidates(home, env, pathAPI) {
  const localAppData = env.LOCALAPPDATA || pathAPI.join(home, 'AppData', 'Local');
  const appData = env.APPDATA || pathAPI.join(home, 'AppData', 'Roaming');
  const programFiles = env.ProgramFiles || 'C:\\Program Files';
  return {
    claude: [
      pathAPI.join(home, '.local', 'bin', 'claude.exe'),
      pathAPI.join(home, '.local', 'bin', 'claude.cmd'),
      pathAPI.join(appData, 'npm', 'claude.cmd'),
    ],
    codex: [
      pathAPI.join(home, '.local', 'bin', 'codex.exe'),
      pathAPI.join(home, '.local', 'bin', 'codex.cmd'),
      pathAPI.join(appData, 'npm', 'codex.cmd'),
    ],
    cursor: [
      pathAPI.join(localAppData, 'Programs', 'cursor', 'Cursor.exe'),
      pathAPI.join(programFiles, 'Cursor', 'Cursor.exe'),
    ],
    git: [
      pathAPI.join(programFiles, 'Git', 'cmd', 'git.exe'),
      pathAPI.join(localAppData, 'Programs', 'Git', 'cmd', 'git.exe'),
    ],
  };
}

function posixCandidates(platform, home, pathAPI) {
  const common = {
    claude: [pathAPI.join(home, '.local', 'bin', 'claude'), '/usr/local/bin/claude', '/usr/bin/claude'],
    codex: [pathAPI.join(home, '.local', 'bin', 'codex'), '/usr/local/bin/codex', '/usr/bin/codex'],
    git: platform === 'darwin'
      ? ['/opt/homebrew/bin/git', '/usr/local/bin/git', '/usr/bin/git']
      : ['/usr/local/bin/git', '/usr/bin/git'],
  };
  return {
    ...common,
    cursor: platform === 'darwin'
      ? ['/Applications/Cursor.app', pathAPI.join(home, 'Applications', 'Cursor.app')]
      : [pathAPI.join(home, '.local', 'bin', 'cursor'), '/usr/local/bin/cursor', '/usr/bin/cursor', '/opt/Cursor/cursor'],
  };
}

export function createPlatformServices({
  platform = process.platform,
  architecture = process.arch,
  home = homedir(),
  env = process.env,
  spawn = spawnSync,
  osRelease = osReleaseDefault,
  nodeExecutable = process.execPath,
  nativeAdapter,
  randomBytes = cryptoRandomBytes,
  inspectExecutable: inspectExecutableOverride,
} = {}) {
  if (!['darwin', 'linux', 'win32'].includes(platform) || !['arm64', 'x64'].includes(architecture) ||
      typeof home !== 'string' || typeof spawn !== 'function' || typeof osRelease !== 'function' ||
      typeof nodeExecutable !== 'string' || typeof randomBytes !== 'function') {
    fail('platform_services_configuration_invalid');
  }
  const pathAPI = platform === 'win32' ? win32 : path;

  function hostCandidates(selectedHome = home) {
    if (typeof selectedHome !== 'string' || !pathAPI.isAbsolute(pathAPI.resolve(selectedHome))) {
      fail('platform_home_invalid');
    }
    const candidates = platform === 'win32'
      ? windowsCandidates(pathAPI.resolve(selectedHome), env, pathAPI)
      : posixCandidates(platform, pathAPI.resolve(selectedHome), pathAPI);
    return Object.freeze(Object.fromEntries(Object.entries(candidates)
      .map(([kind, values]) => [kind, Object.freeze([...new Set(values.map((value) => pathAPI.resolve(value)))])])));
  }

  function inspectExecutable(executablePath) {
    if (typeof inspectExecutableOverride === 'function') return inspectExecutableOverride(executablePath);
    if (typeof executablePath !== 'string' || !pathAPI.isAbsolute(executablePath) || pathAPI.resolve(executablePath) !== executablePath) {
      fail('platform_executable_path_invalid');
    }
    if (platform === 'win32') {
      const proof = nativeOperation(nativeAdapter, 'inspectExecutable')(executablePath);
      if (!exactObject(proof, [
        'canonical_path', 'executable', 'owner_only', 'regular_file', 'reparse_point', 'sha256',
      ]) || typeof proof.canonical_path !== 'string' || !pathAPI.isAbsolute(proof.canonical_path) ||
          proof.executable !== true || proof.regular_file !== true || proof.owner_only !== true ||
          proof.reparse_point !== false || !SHA256.test(proof.sha256 ?? '')) {
        fail('platform_executable_unsafe');
      }
      return Object.freeze(proof);
    }
    try {
      const canonicalPath = realpathSync(executablePath);
      const info = statSync(canonicalPath);
      if (!info.isFile() || (info.mode & 0o111) === 0 || (info.mode & 0o022) !== 0) return null;
      return Object.freeze({
        canonical_path: canonicalPath,
        executable: true,
        owner_only: (info.mode & 0o077) === 0,
        regular_file: true,
        reparse_point: false,
        sha256: createHash('sha256').update(readFileSync(canonicalPath)).digest('hex'),
      });
    } catch {
      return null;
    }
  }

  function inspectApplication(applicationPath) {
    if (typeof applicationPath !== 'string' || !pathAPI.isAbsolute(applicationPath) || pathAPI.resolve(applicationPath) !== applicationPath) {
      fail('platform_application_path_invalid');
    }
    if (platform === 'win32') return inspectExecutable(applicationPath);
    try {
      const canonicalPath = realpathSync(applicationPath);
      const info = statSync(canonicalPath);
      if (platform === 'darwin' ? !info.isDirectory() : (!info.isFile() || (info.mode & 0o111) === 0)) return null;
      return Object.freeze({ canonical_path: canonicalPath, application: true });
    } catch {
      return null;
    }
  }

  function assertPrivateState(statePath, { kind } = {}) {
    if (!['file', 'directory'].includes(kind) || typeof statePath !== 'string' || !pathAPI.isAbsolute(statePath)) {
      fail('platform_private_state_invalid');
    }
    if (platform === 'win32') {
      const proof = nativeOperation(nativeAdapter, 'inspectPrivateState')(statePath, { kind });
      if (!exactObject(proof, ['canonical_path', 'kind', 'owner_only', 'reparse_point']) ||
          proof.kind !== kind || proof.owner_only !== true || proof.reparse_point !== false ||
          typeof proof.canonical_path !== 'string' || !pathAPI.isAbsolute(proof.canonical_path)) {
        fail('platform_private_state_unsafe');
      }
      return Object.freeze(proof);
    }
    const info = lstatSync(statePath);
    const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
    if (info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0 ||
        (kind === 'file' ? !info.isFile() || info.nlink !== 1 : !info.isDirectory())) {
      fail('platform_private_state_unsafe');
    }
    return Object.freeze({
      canonical_path: realpathSync(statePath), kind, owner_only: true, reparse_point: false,
    });
  }

  function validatePrivatePath(statePath) {
    if (typeof statePath !== 'string' || !pathAPI.isAbsolute(statePath) || pathAPI.resolve(statePath) !== statePath) {
      fail('platform_private_state_invalid');
    }
  }

  function validateByteBounds({ minBytes = 0, maxBytes = 1 << 20 } = {}) {
    if (!Number.isSafeInteger(minBytes) || minBytes < 0 || !Number.isSafeInteger(maxBytes) ||
        maxBytes < minBytes || maxBytes > 64 * 1024 * 1024) {
      fail('platform_private_state_invalid');
    }
    return { minBytes, maxBytes };
  }

  function syncDirectory(directoryPath) {
    const descriptor = openSync(directoryPath, 'r');
    try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
  }

  function ensurePrivateDirectory(directoryPath) {
    validatePrivatePath(directoryPath);
    if (platform === 'win32') {
      nativeOperation(nativeAdapter, 'ensurePrivateDirectory')(directoryPath);
      return assertPrivateState(directoryPath, { kind: 'directory' });
    }
    try { mkdirSync(directoryPath, { recursive: true, mode: 0o700 }); } catch {
      fail('platform_private_state_unsafe');
    }
    try { return assertPrivateState(directoryPath, { kind: 'directory' }); } catch (error) {
      if (error instanceof PlatformServicesError) throw error;
      fail('platform_private_state_unsafe');
    }
  }

  function readPrivateFile(filePath, {
    missing = false, encoding = 'utf8', minBytes = 0, maxBytes = 1 << 20,
  } = {}) {
    validatePrivatePath(filePath);
    const bounds = validateByteBounds({ minBytes, maxBytes });
    if (typeof missing !== 'boolean' || (encoding !== null && encoding !== 'utf8')) fail('platform_private_state_invalid');
    if (platform === 'win32') {
      let value;
      try {
        assertPrivateState(filePath, { kind: 'file' });
        value = nativeOperation(nativeAdapter, 'readPrivateFile')(filePath, { encoding, ...bounds });
      } catch (error) {
        if (missing && error?.code === 'ENOENT') return null;
        throw error;
      }
      if (!(typeof value === 'string' || Buffer.isBuffer(value))) fail('platform_private_state_unsafe');
      const size = Buffer.byteLength(value);
      if (size < bounds.minBytes || size > bounds.maxBytes) fail('platform_private_state_unsafe');
      assertPrivateState(filePath, { kind: 'file' });
      return value;
    }
    let descriptor;
    try {
      descriptor = openSync(filePath, constants.O_RDONLY | constants.O_NOFOLLOW);
      const opened = fstatSync(descriptor);
      const linked = lstatSync(filePath);
      const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : opened.uid;
      if (!opened.isFile() || opened.nlink !== 1 || opened.uid !== currentUID || (opened.mode & 0o077) !== 0 ||
          linked.isSymbolicLink() || linked.dev !== opened.dev || linked.ino !== opened.ino) {
        fail('platform_private_state_unsafe');
      }
      const bytes = readFileSync(descriptor);
      if (bytes.length < bounds.minBytes || bytes.length > bounds.maxBytes) fail('platform_private_state_unsafe');
      const after = lstatSync(filePath);
      if (after.dev !== opened.dev || after.ino !== opened.ino) fail('platform_private_state_unsafe');
      return encoding === null ? bytes : bytes.toString(encoding);
    } catch (error) {
      if (missing && error?.code === 'ENOENT') return null;
      if (error instanceof PlatformServicesError) throw error;
      fail('platform_private_state_unsafe');
    } finally {
      if (descriptor !== undefined) closeSync(descriptor);
    }
  }

  function atomicWritePrivateFile(filePath, data, {
    ensureParent = true, maxBytes = 1 << 20,
  } = {}) {
    validatePrivatePath(filePath);
    const { maxBytes: boundedMax } = validateByteBounds({ minBytes: 0, maxBytes });
    if (typeof ensureParent !== 'boolean' || !(typeof data === 'string' || Buffer.isBuffer(data)) ||
        Buffer.byteLength(data) > boundedMax) fail('platform_private_state_invalid');
    if (platform === 'win32') {
      nativeOperation(nativeAdapter, 'atomicWritePrivateFile')(filePath, data, { ensureParent, maxBytes: boundedMax });
      return assertPrivateState(filePath, { kind: 'file' });
    }
    const parent = dirname(filePath);
    if (ensureParent) ensurePrivateDirectory(parent);
    else {
      try { assertPrivateState(parent, { kind: 'directory' }); } catch (error) {
        if (error instanceof PlatformServicesError) throw error;
        fail('platform_private_state_unsafe');
      }
    }
    const temporary = `${filePath}.new-${process.pid}-${randomBytes(16).toString('hex')}`;
    let descriptor;
    try {
      descriptor = openSync(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
      writeFileSync(descriptor, data);
      fsyncSync(descriptor);
      closeSync(descriptor);
      descriptor = undefined;
      chmodSync(temporary, 0o600);
      renameSync(temporary, filePath);
      syncDirectory(parent);
      return assertPrivateState(filePath, { kind: 'file' });
    } catch (error) {
      if (error instanceof PlatformServicesError) throw error;
      fail('platform_private_state_write_failed');
    } finally {
      if (descriptor !== undefined) closeSync(descriptor);
      rmSync(temporary, { force: true });
    }
  }

  function removePrivateFile(filePath, { missing = true } = {}) {
    validatePrivatePath(filePath);
    if (typeof missing !== 'boolean') fail('platform_private_state_invalid');
    if (platform === 'win32') {
      const result = nativeOperation(nativeAdapter, 'removePrivateFile')(filePath, { missing });
      if (typeof result !== 'boolean') fail('platform_private_state_unsafe');
      return result;
    }
    const quarantine = `${filePath}.remove-${randomBytes(16).toString('hex')}`;
    try {
      const before = lstatSync(filePath);
      assertPrivateState(filePath, { kind: 'file' });
      renameSync(filePath, quarantine);
      const moved = lstatSync(quarantine);
      if (moved.dev !== before.dev || moved.ino !== before.ino) fail('platform_private_state_unsafe');
      assertPrivateState(quarantine, { kind: 'file' });
      rmSync(quarantine);
      syncDirectory(dirname(filePath));
      return true;
    } catch (error) {
      if (missing && error?.code === 'ENOENT') return false;
      if (error instanceof PlatformServicesError) throw error;
      fail('platform_private_state_unsafe');
    }
  }

  function lockRecord() {
    const processProof = inspectProcess(process.pid);
    if (!processProof.running || typeof processProof.identity_token !== 'string') fail('platform_lock_identity_unavailable');
    return {
      schema: LOCK_SCHEMA,
      created_at: new Date().toISOString(),
      pid: process.pid,
      process_identity: processProof.identity_token,
      token: randomBytes(32).toString('hex'),
    };
  }

  function readLock(lockPath) {
    const bytes = readPrivateFile(lockPath, { minBytes: 1, maxBytes: 2048 });
    let record;
    try { record = JSON.parse(bytes); } catch { fail('platform_lock_unsafe'); }
    if (!exactObject(record, ['created_at', 'pid', 'process_identity', 'schema', 'token']) ||
        record.schema !== LOCK_SCHEMA || !Number.isSafeInteger(record.pid) || record.pid <= 1 ||
        typeof record.process_identity !== 'string' || record.process_identity.length < 1 ||
        !SHA256.test(record.token ?? '') || Number.isNaN(Date.parse(record.created_at)) ||
        bytes !== `${JSON.stringify(record)}\n`) fail('platform_lock_unsafe');
    return record;
  }

  function acquirePrivateLock(lockPath, { staleAfterMs = 0, timeoutMs = 0 } = {}) {
    validatePrivatePath(lockPath);
    if (!Number.isSafeInteger(staleAfterMs) || staleAfterMs < 0 || !Number.isSafeInteger(timeoutMs) ||
        timeoutMs < 0 || timeoutMs > 60_000) fail('platform_lock_invalid');
    if (platform === 'win32') {
      const release = nativeOperation(nativeAdapter, 'acquirePrivateLock')(lockPath, { staleAfterMs, timeoutMs });
      if (typeof release !== 'function') fail('platform_lock_unsafe');
      return release;
    }
    ensurePrivateDirectory(dirname(lockPath));
    const owner = lockRecord();
    const deadline = Date.now() + timeoutMs;
    while (true) {
      try {
        const descriptor = openSync(lockPath, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
        try { writeFileSync(descriptor, `${JSON.stringify(owner)}\n`); fsyncSync(descriptor); } finally { closeSync(descriptor); }
        syncDirectory(dirname(lockPath));
        break;
      } catch (error) {
        if (error?.code !== 'EEXIST') fail('platform_lock_failed');
        let existing;
        try { existing = readLock(lockPath); } catch (inspectionError) {
          if (inspectionError?.code === 'ENOENT') continue;
          if (inspectionError instanceof PlatformServicesError) throw inspectionError;
          fail('platform_lock_unsafe');
        }
        const processProof = inspectProcess(existing.pid);
        const liveOwner = processProof.running && processProof.identity_token === existing.process_identity;
        const oldEnough = Date.now() - Date.parse(existing.created_at) >= staleAfterMs;
        if (!liveOwner && oldEnough) {
          const quarantine = `${lockPath}.stale-${randomBytes(16).toString('hex')}`;
          try {
            renameSync(lockPath, quarantine);
            const moved = readLock(quarantine);
            if (moved.token !== existing.token) fail('platform_lock_occupied');
            removePrivateFile(quarantine, { missing: false });
            continue;
          } catch (recoveryError) {
            if (recoveryError instanceof PlatformServicesError && recoveryError.code === 'platform_lock_unsafe') throw recoveryError;
          }
        }
        if (Date.now() >= deadline) fail('platform_lock_occupied');
        Atomics.wait(LOCK_WAIT, 0, 0, 10);
      }
    }
    let released = false;
    return () => {
      if (released) return;
      let current;
      try { current = readLock(lockPath); } catch (error) {
        if (error instanceof PlatformServicesError) throw error;
        fail('platform_unlock_failed');
      }
      if (current.token !== owner.token || current.pid !== owner.pid || current.process_identity !== owner.process_identity) {
        fail('platform_unlock_not_owner');
      }
      try { removePrivateFile(lockPath, { missing: false }); } catch (error) {
        if (error instanceof PlatformServicesError) throw error;
        fail('platform_unlock_failed');
      }
      released = true;
    };
  }

  function probePort(port) {
    if (!Number.isInteger(port) || port < 1 || port > 65535) fail('platform_port_invalid');
    let result;
    try {
      result = spawn(nodeExecutable, ['-e', PORT_PROBE, String(port)], {
        encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 3000,
        env: { HOME: env.HOME ?? '', LANG: env.LANG ?? '', LC_ALL: env.LC_ALL ?? '' },
      });
    } catch {
      return 'unknown';
    }
    if (result?.status === 0) return 'free';
    if (result?.status === 2) return 'occupied';
    return 'unknown';
  }

  function desktopOSVersion() {
    if (platform !== 'darwin') return canonicalVersion(osRelease());
    let result;
    try {
      result = spawn('/usr/bin/sw_vers', ['-productVersion'], {
        encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 3000,
        env: { HOME: env.HOME ?? '', LANG: env.LANG ?? '', LC_ALL: env.LC_ALL ?? '' },
      });
    } catch {
      fail('platform_os_version_invalid');
    }
    if (result?.status !== 0) fail('platform_os_version_invalid');
    return canonicalVersion(String(result.stdout ?? '').trim());
  }

  function isPathInside(candidate, parent) {
    if (![candidate, parent].every((value) => typeof value === 'string' && pathAPI.isAbsolute(value))) return false;
    let relativePath = pathAPI.relative(pathAPI.resolve(parent), pathAPI.resolve(candidate));
    if (platform === 'win32') relativePath = relativePath.toLowerCase();
    return relativePath === '' || (!relativePath.startsWith(`..${pathAPI.sep}`) && relativePath !== '..' && !pathAPI.isAbsolute(relativePath));
  }

  function isAbsolutePath(value) {
    return typeof value === 'string' && pathAPI.isAbsolute(value) && pathAPI.resolve(value) === value;
  }

  function resolvePath(value) {
    if (typeof value !== 'string') fail('platform_path_invalid');
    return pathAPI.resolve(value);
  }

  function runGit(cwd, args) {
    if (typeof cwd !== 'string' || !pathAPI.isAbsolute(cwd) || !Array.isArray(args) || args.length > 32 ||
        args.some((argument) => typeof argument !== 'string' || argument.length > 4096 || argument.includes('\0'))) {
      fail('platform_git_request_invalid');
    }
    const git = hostCandidates().git.map((candidate) => {
      try { return inspectExecutable(candidate); } catch (error) {
        if (error instanceof PlatformServicesError && error.code === 'platform_native_adapter_unavailable') throw error;
        return null;
      }
    }).find(Boolean);
    if (!git) fail('platform_git_unavailable');
    let result;
    try {
      result = spawn(git.canonical_path, ['-C', cwd, ...args], {
        encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5000,
        env: { HOME: env.HOME ?? '', LANG: env.LANG ?? '', LC_ALL: env.LC_ALL ?? '' },
      });
    } catch {
      fail('platform_git_failed');
    }
    if (result?.status !== 0) fail('platform_git_failed');
    return String(result.stdout ?? '').trim();
  }

  function inspectProcess(pid) {
    if (!Number.isSafeInteger(pid) || pid <= 1) fail('platform_process_invalid');
    if (platform === 'win32') {
      const proof = nativeOperation(nativeAdapter, 'inspectProcess')(pid);
      if (!exactObject(proof, ['command', 'identity_token', 'pid', 'running']) || proof.pid !== pid ||
          typeof proof.running !== 'boolean' || (proof.running &&
            (typeof proof.command !== 'string' || typeof proof.identity_token !== 'string' || proof.identity_token.length < 1))) {
        fail('platform_process_proof_invalid');
      }
      return Object.freeze(proof);
    }
    if (platform === 'linux') {
      try {
        const stat = readFileSync(`/proc/${pid}/stat`, 'utf8');
        const closing = stat.lastIndexOf(')');
        const fields = closing >= 0 ? stat.slice(closing + 1).trim().split(/\s+/) : [];
        const identityToken = fields[19];
        const command = readFileSync(`/proc/${pid}/cmdline`, 'utf8').replaceAll('\0', ' ').trim();
        if (!/^\d+$/.test(identityToken ?? '') || command.length === 0) {
          return Object.freeze({ pid, running: false, command: '', identity_token: null });
        }
        return Object.freeze({ pid, running: true, command, identity_token: identityToken });
      } catch {
        return Object.freeze({ pid, running: false, command: '', identity_token: null });
      }
    }
    const processEnv = { HOME: env.HOME ?? '', LANG: env.LANG ?? '', LC_ALL: env.LC_ALL ?? '' };
    const commandResult = spawn('/bin/ps', ['-ww', '-p', String(pid), '-o', 'command='], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 3000, env: processEnv,
    });
    const startedResult = spawn('/bin/ps', ['-ww', '-p', String(pid), '-o', 'lstart='], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 3000, env: processEnv,
    });
    const command = commandResult?.status === 0 ? String(commandResult.stdout ?? '').trim() : '';
    const started = startedResult?.status === 0 ? String(startedResult.stdout ?? '').trim() : '';
    return Object.freeze({
      pid,
      running: command.length > 0 && started.length > 0,
      command,
      identity_token: started.length > 0 ? createHash('sha256').update(started).digest('hex') : null,
    });
  }

  function terminateProcess(pid, { force = false } = {}) {
    if (!Number.isSafeInteger(pid) || pid <= 1 || typeof force !== 'boolean') fail('platform_process_invalid');
    if (platform === 'win32') return nativeOperation(nativeAdapter, 'terminateProcess')(pid, { force });
    try {
      process.kill(pid, force ? 'SIGKILL' : 'SIGTERM');
      return true;
    } catch (error) {
      if (error?.code === 'ESRCH') return false;
      fail('platform_process_termination_failed');
    }
  }

  function createStartupNonce() {
    const value = randomBytes(32).toString('hex');
    if (!STARTUP_NONCE.test(value)) fail('platform_startup_nonce_invalid');
    return value;
  }

  return Object.freeze({
    schema: 'pulse.platform_services.v1',
    platform,
    architecture,
    path_delimiter: pathAPI.delimiter,
    path_separator: pathAPI.sep,
    acquirePrivateLock,
    assertPrivateState,
    atomicWritePrivateFile,
    createStartupNonce,
    desktopOSVersion,
    ensurePrivateDirectory,
    hostCandidates,
    inspectApplication,
    inspectExecutable,
    inspectProcess,
    isAbsolutePath,
    isPathInside,
    probePort,
    readPrivateFile,
    removePrivateFile,
    resolvePath,
    runGit,
    terminateProcess,
  });
}

export const defaultPlatformServices = createPlatformServices();
