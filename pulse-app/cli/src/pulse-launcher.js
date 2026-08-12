import { createHash } from 'node:crypto';
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function ensurePlainDirectory(path) {
  if (existsSync(path)) {
    const info = lstatSync(path);
    if (info.isSymbolicLink() || !info.isDirectory()) fail('pulse_launcher_directory_unsafe');
    return;
  }
  mkdirSync(path, { recursive: true, mode: 0o700 });
  if (lstatSync(path).isSymbolicLink()) fail('pulse_launcher_directory_unsafe');
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", `'"'"'`)}'`;
}

export function pulseLauncherPath(home = homedir()) {
  return join(resolve(home), '.local', 'bin', 'pulse');
}

export function pulseLauncherScript({ dataDir, nodeExecutable = process.execPath } = {}) {
  const entrypoint = join(resolve(dataDir ?? ''), 'runtime', 'codex', 'current', 'src', 'cli.js');
  return `#!/bin/sh\nexec ${shellQuote(nodeExecutable)} ${shellQuote(entrypoint)} "$@"\n`;
}

export function installPulseLauncher({
  dataDir, home = homedir(), nodeExecutable = process.execPath,
} = {}) {
  if (process.platform === 'win32') return { installed: false, reason: 'unsupported_platform' };
  const root = resolve(dataDir ?? '');
  const entrypoint = join(root, 'runtime', 'codex', 'current', 'src', 'cli.js');
  if (!existsSync(entrypoint)) fail('pulse_launcher_runtime_missing');
  const entryInfo = lstatSync(entrypoint);
  if (entryInfo.isSymbolicLink() || !entryInfo.isFile() || statSync(entrypoint).size < 1) {
    fail('pulse_launcher_runtime_unsafe');
  }

  const local = join(resolve(home), '.local');
  const bin = join(local, 'bin');
  ensurePlainDirectory(local);
  ensurePlainDirectory(bin);
  const path = join(bin, 'pulse');
  const script = pulseLauncherScript({ dataDir: root, nodeExecutable });
  let backupPath = '';
  if (existsSync(path)) {
    const current = lstatSync(path);
    if (current.isSymbolicLink() || !current.isFile()) fail('pulse_launcher_existing_unsafe');
    const bytes = readFileSync(path);
    if (bytes.toString('utf8') === script) {
      chmodSync(path, 0o700);
      return { installed: true, path, backup_path: '', changed: false };
    }
    const digest = createHash('sha256').update(bytes).digest('hex');
    const backupDir = join(root, 'backups', 'launcher');
    ensurePlainDirectory(join(root, 'backups'));
    ensurePlainDirectory(backupDir);
    backupPath = join(backupDir, `pulse-${digest}`);
    if (!existsSync(backupPath)) writeFileSync(backupPath, bytes, { mode: 0o600, flag: 'wx' });
  }

  const temporary = `${path}.new-${process.pid}`;
  writeFileSync(temporary, script, { mode: 0o700, flag: 'wx' });
  try {
    renameSync(temporary, path);
    chmodSync(path, 0o700);
  } catch (error) {
    rmSync(temporary, { force: true });
    throw error;
  }
  return { installed: true, path, backup_path: backupPath, changed: true };
}
