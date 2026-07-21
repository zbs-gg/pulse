#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const cliDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const appDir = resolve(cliDir, '..');

function run(command, args, cwd) {
  const result = spawnSync(command, args, { cwd, stdio: 'inherit', env: process.env });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}

run(process.execPath, ['--test', 'src/git-team-memory.test.js'], cliDir);
run('go', [
  'test', './internal/store', './internal/server', './internal/retrieve', './cmd/pulse',
  '-run', 'GitTeamMemory', '-count=1',
], appDir);

process.stdout.write(`${JSON.stringify({
  schema: 'pulse.git_team_memory_e2e.v1',
  authority: 'synthetic-local-test',
  production_install_proof: false,
  committed_only: true,
  second_checkout_retrieval: true,
  existing_state_aware_engine: true,
})}\n`);
