import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { createHookWorkerRuntimeResolver } from './product-hook-worker.js';

test('hook worker reuses one verified runtime only while authority witnesses stay exact', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-hook-worker-resolver.'));
  try {
    const workspace = join(root, 'workspace');
    const witness = join(root, 'authority.json');
    mkdirSync(workspace);
    const canonicalWorkspace = realpathSync(workspace);
    writeFileSync(witness, '{"epoch":1}\n');
    let resolutions = 0;
    let time = 1_000;
    const resolved = {
      binding: { workspace: { canonical_path: canonicalWorkspace } },
      runtime: { data_dir: join(root, 'data') },
    };
    const cache = createHookWorkerRuntimeResolver({
      host: 'codex',
      now: () => time,
      resolveRuntime: () => { resolutions += 1; return resolved; },
      ttlMs: 500,
      witnessPaths: () => [witness],
    });
    const input = { cwd: canonicalWorkspace, session_id: 'session-one' };
    cache.begin('SessionStart', input);
    assert.equal(cache.resolve(input), resolved);
    cache.begin('UserPromptSubmit', input);
    assert.equal(cache.resolve(input), resolved);
    assert.equal(resolutions, 1);

    writeFileSync(witness, '{"epoch":2}\n');
    cache.begin('PreToolUse', input);
    assert.equal(cache.resolve(input), resolved);
    assert.equal(resolutions, 2, 'authority drift must force a fresh trusted resolution');

    time += 501;
    cache.begin('Stop', input);
    assert.equal(cache.resolve(input), resolved);
    assert.equal(resolutions, 3, 'the bounded lease must expire even without file drift');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('every SessionStart refreshes the worker runtime even for the same session id', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-hook-worker-session.'));
  try {
    const workspace = join(root, 'workspace');
    const witness = join(root, 'authority.json');
    mkdirSync(workspace);
    const canonicalWorkspace = realpathSync(workspace);
    writeFileSync(witness, '{}\n');
    let resolutions = 0;
    const cache = createHookWorkerRuntimeResolver({
      host: 'claude-code',
      resolveRuntime: () => ({
        binding: { workspace: { canonical_path: canonicalWorkspace }, generation: ++resolutions },
        runtime: {},
      }),
      witnessPaths: () => [witness],
    });
    const input = { cwd: canonicalWorkspace, session_id: 'same-session' };
    cache.begin('SessionStart', input);
    assert.equal(cache.resolve(input).binding.generation, 1);
    cache.begin('SessionStart', input);
    assert.equal(cache.resolve(input).binding.generation, 2);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Cursor keeps one worker session across startup session_id and lifecycle conversation_id', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-hook-worker-cursor.'));
  try {
    const workspace = join(root, 'workspace');
    const witness = join(root, 'authority.json');
    mkdirSync(workspace);
    const canonicalWorkspace = realpathSync(workspace);
    writeFileSync(witness, '{}\n');
    let resolutions = 0;
    const cache = createHookWorkerRuntimeResolver({
      host: 'cursor',
      resolveRuntime: () => {
        resolutions += 1;
        return { binding: { workspace: { canonical_path: canonicalWorkspace } }, runtime: {} };
      },
      witnessPaths: () => [witness],
    });
    const startup = { cwd: canonicalWorkspace, session_id: 'cursor-conversation' };
    cache.begin('sessionStart', startup);
    cache.resolve(startup);
    const prompt = { conversation_id: 'cursor-conversation', cwd: canonicalWorkspace };
    cache.begin('beforeSubmitPrompt', prompt);
    cache.resolve(prompt);
    assert.equal(resolutions, 1);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
