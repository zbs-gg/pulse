import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import {
  chmodSync, linkSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  PersonalPrincipalError,
  ensurePersonalPrincipal,
  personalPrincipalPath,
  readPersonalPrincipal,
} from './personal-principal.js';

function expectCode(action, code) {
  assert.throws(action, (error) => error instanceof PersonalPrincipalError && error.code === code);
}

test('principal creation requires explicit consent and leaves no state when denied', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-principal-denied.'));
  try {
    expectCode(() => ensurePersonalPrincipal({ home, consentGranted: false }), 'principal_consent_required');
    assert.equal(readPersonalPrincipal({ home }), null);
  } finally { rmSync(home, { recursive: true, force: true }); }
});

test('principal is created once as canonical private durable identity and then reused', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-principal-create.'));
  try {
    const first = ensurePersonalPrincipal({ home, consentGranted: true, randomBytes: () => Buffer.alloc(16, 0xab) });
    assert.equal(first.principal_id, `principal_${'ab'.repeat(16)}`);
    assert.equal(readFileSync(personalPrincipalPath(home), 'utf8'), `${JSON.stringify({ principal_id: first.principal_id, schema: 'pulse.personal_principal.v1' })}\n`);
    assert.equal(statSync(personalPrincipalPath(home)).mode & 0o777, 0o600);
    assert.equal(statSync(join(home, '.pulse', 'identity')).mode & 0o777, 0o700);
    const second = ensurePersonalPrincipal({ home, consentGranted: true, randomBytes: () => { throw new Error('must not regenerate'); } });
    assert.deepEqual(second, first);
  } finally { rmSync(home, { recursive: true, force: true }); }
});

test('principal rejects corrupt, non-private, symlink, and hard-linked files', () => {
  for (const kind of ['corrupt', 'permissions', 'symlink', 'hardlink']) {
    const home = mkdtempSync(join(tmpdir(), `pulse-principal-${kind}.`));
    try {
      const directory = join(home, '.pulse', 'identity');
      mkdirSync(directory, { recursive: true, mode: 0o700 });
      chmodSync(join(home, '.pulse'), 0o700);
      const path = personalPrincipalPath(home);
      if (kind === 'symlink') {
        const target = join(home, 'target');
        writeFileSync(target, '{}\n', { mode: 0o600 });
        symlinkSync(target, path);
      } else {
        writeFileSync(path, kind === 'corrupt' ? '{}\n' : '{"principal_id":"principal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","schema":"pulse.personal_principal.v1"}\n', { mode: 0o600 });
        if (kind === 'permissions') chmodSync(path, 0o644);
        if (kind === 'hardlink') linkSync(path, join(home, 'principal-copy'));
      }
      expectCode(() => readPersonalPrincipal({ home }), kind === 'corrupt' ? 'principal_invalid' : 'principal_file_unsafe');
    } finally { rmSync(home, { recursive: true, force: true }); }
  }
});

test('concurrent creators converge on one valid principal', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-principal-race.'));
  const moduleURL = new URL('./personal-principal.js', import.meta.url).href;
  const source = `import { ensurePersonalPrincipal } from ${JSON.stringify(moduleURL)}; process.stdout.write(ensurePersonalPrincipal({home:process.argv[1],consentGranted:true}).principal_id);`;
  const run = () => new Promise((resolve, reject) => {
    const child = execFile(process.execPath, ['--input-type=module', '-e', source, home]);
    let stdout = '';
    let stderr = '';
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.once('error', reject);
    child.once('close', (code) => code === 0 ? resolve(stdout) : reject(new Error(stderr || `child exited ${code}`)));
  });
  try {
    const ids = await Promise.all(Array.from({ length: 6 }, run));
    assert.equal(new Set(ids).size, 1);
    assert.match(ids[0], /^principal_[a-f0-9]{32}$/);
    assert.equal(readPersonalPrincipal({ home }).principal_id, ids[0]);
    assert.deepEqual(readdirSync(join(home, '.pulse', 'identity')), ['personal-principal.json']);
  } finally { rmSync(home, { recursive: true, force: true }); }
});
