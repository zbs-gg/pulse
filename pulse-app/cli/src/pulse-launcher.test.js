import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { installPulseLauncher, pulseLauncherPath } from './pulse-launcher.js';

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'pulse-launcher-'));
  const home = join(root, 'home');
  const dataDir = join(home, '.pulse');
  const entry = join(dataDir, 'runtime', 'codex', 'current', 'src', 'cli.js');
  mkdirSync(join(dataDir, 'runtime', 'codex', 'current', 'src'), { recursive: true, mode: 0o700 });
  writeFileSync(entry, '#!/usr/bin/env node\n', { mode: 0o700 });
  chmodSync(entry, 0o700);
  return { home, dataDir };
}

test('installer creates one executable Pulse launcher for the installed runtime', () => {
  const { home, dataDir } = fixture();
  const result = installPulseLauncher({ dataDir, home, nodeExecutable: '/usr/bin/node' });
  assert.equal(result.path, pulseLauncherPath(home));
  assert.match(readFileSync(result.path, 'utf8'), /runtime\/codex\/current\/src\/cli\.js/);
  assert.equal(result.changed, true);
});

test('installer preserves a different existing launcher before replacement', () => {
  const { home, dataDir } = fixture();
  const path = pulseLauncherPath(home);
  mkdirSync(join(home, '.local', 'bin'), { recursive: true, mode: 0o700 });
  writeFileSync(path, '#!/bin/sh\necho old\n', { mode: 0o700 });
  const result = installPulseLauncher({ dataDir, home, nodeExecutable: '/usr/bin/node' });
  assert.equal(existsSync(result.backup_path), true);
  assert.equal(readFileSync(result.backup_path, 'utf8'), '#!/bin/sh\necho old\n');
  assert.doesNotMatch(readFileSync(path, 'utf8'), /echo old/);
});

