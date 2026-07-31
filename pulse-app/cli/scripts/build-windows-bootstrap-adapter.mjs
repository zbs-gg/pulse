#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptPath = fileURLToPath(import.meta.url);
const cliRoot = resolve(dirname(scriptPath), '..');
const defaultAppRoot = resolve(cliRoot, '..');
const defaultOutputRoot = join(cliRoot, 'runtime', 'windows-bootstrap');
const architectureMap = Object.freeze({ arm64: 'arm64', x64: 'amd64' });

function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}

export function buildWindowsBootstrapAdapters({
  appRoot = defaultAppRoot,
  outputRoot = defaultOutputRoot,
  architectures = ['arm64', 'x64'],
} = {}) {
  if (!Array.isArray(architectures) || architectures.length < 1 ||
      architectures.some((architecture) => !architectureMap[architecture])) {
    throw new Error('windows_bootstrap_architecture_invalid');
  }
  const exactArchitectures = [...new Set(architectures)].sort();
  rmSync(outputRoot, { recursive: true, force: true });
  mkdirSync(outputRoot, { recursive: true, mode: 0o700 });
  const adapters = {};
  for (const architecture of exactArchitectures) {
    const target = `win32-${architecture}`;
    const relativePath = `${target}/pulse-platform-adapter.exe`;
    const binary = join(outputRoot, relativePath);
    mkdirSync(dirname(binary), { recursive: true, mode: 0o700 });
    execFileSync('go', [
      'build', '-buildvcs=false', '-trimpath', '-ldflags=-s -w -buildid=',
      '-o', binary, './cmd/pulse-platform-adapter',
    ], {
      cwd: appRoot,
      env: { ...process.env, CGO_ENABLED: '0', GOARCH: architectureMap[architecture], GOOS: 'windows' },
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout: 120_000,
    });
    chmodSync(binary, 0o600);
    const bytes = readFileSync(binary);
    adapters[target] = {
      bytes: statSync(binary).size,
      path: relativePath,
      sha256: createHash('sha256').update(bytes).digest('hex'),
      target,
    };
  }
  const catalog = {
    adapters,
    protocol: 1,
    schema: 'pulse.windows_bootstrap_adapter_catalog.v1',
  };
  writeFileSync(join(outputRoot, 'catalog.json'), `${canonical(catalog)}\n`, { mode: 0o600 });
  return catalog;
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  const catalog = buildWindowsBootstrapAdapters();
  process.stdout.write(`${canonical(catalog)}\n`);
}
