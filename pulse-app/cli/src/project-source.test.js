import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { ensureBoundPortableProjectID, readBoundProjectSourceWindow } from './project-source.js';

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'pulse-project-source.'));
  mkdirSync(join(root, 'notes'), { recursive: true });
  return {
    root,
    resolved: {
      binding: {
        binding_digest: 'a'.repeat(64),
        resolver_epoch: 7,
        workspace: {
          canonical_path: root,
          repository_id: 'repository_source_fixture',
        },
      },
      runtime: { data_dir: join(root, '.pulse-test') },
    },
  };
}

test('second checkout adopts the committed portable project id and rejects later drift', () => {
  const { resolved } = fixture();
  const committed = `project_${'b'.repeat(32)}`;
  assert.equal(ensureBoundPortableProjectID(resolved, { adoptPortableProjectID: committed }), committed);
  assert.equal(ensureBoundPortableProjectID(resolved), committed);
  assert.throws(
    () => ensureBoundPortableProjectID(resolved, { adoptPortableProjectID: `project_${'c'.repeat(32)}` }),
    /project_identity_mismatch/,
  );
});

test('bound source window returns stable metadata and withholds unsafe lines', () => {
  const { root, resolved } = fixture();
  const secret = 'sk-abcdefghijklmnopqrstuvwxyz123456';
  writeFileSync(join(root, 'notes', 'call.md'), [
    '# Call',
    'Use short project briefs before writing.',
    `Authorization: Bearer ${secret}`,
    'Read /Users/private/customer.md first.',
    'User: this raw transcript line must not pass.',
    'The approved conclusion remains visible.',
  ].join('\n'));

  const first = readBoundProjectSourceWindow(resolved, {
    locator: 'notes/call.md', cursor: 0, max_bytes: 120,
  });
  assert.equal(first.schema, 'pulse.project_source.window.v1');
  assert.equal(first.locator, 'notes/call.md');
  assert.equal(first.repository_id, 'repository_source_fixture');
  assert.match(first.version_digest, /^[a-f0-9]{64}$/);
  assert.equal(first.status, 'more');
  assert.ok(first.next_cursor > 0);
  assert.doesNotMatch(JSON.stringify(first), new RegExp(secret));

  const windows = [first];
  while (windows.at(-1).status === 'more') {
    windows.push(readBoundProjectSourceWindow(resolved, {
      locator: 'notes/call.md', cursor: windows.at(-1).next_cursor, max_bytes: 120,
      expected_version_digest: first.version_digest,
    }));
  }
  const rendered = windows.map((item) => item.content).join('\n');
  assert.match(rendered, /approved conclusion/);
  assert.doesNotMatch(rendered, /Authorization|\/Users\/private|User:|abcdefghijklmnopqrstuvwxyz/);
  assert.ok(windows.flatMap((item) => item.withheld).some((item) => item.reason === 'credential'));
  assert.ok(windows.flatMap((item) => item.withheld).some((item) => item.reason === 'path'));
  assert.ok(windows.flatMap((item) => item.withheld).some((item) => item.reason === 'transcript'));
});

test('bound source window rejects path escape, links, binary data, and stale versions', () => {
  const { root, resolved } = fixture();
  writeFileSync(join(root, 'notes', 'safe.txt'), 'Safe project source.\n');
  writeFileSync(join(root, 'notes', 'binary.txt'), Buffer.from([0x66, 0x6f, 0x00, 0x6f]));
  symlinkSync(join(root, 'notes', 'safe.txt'), join(root, 'notes', 'linked.txt'));

  for (const locator of ['/tmp/source.txt', '../source.txt', '.git/config', 'notes/linked.txt']) {
    assert.throws(
      () => readBoundProjectSourceWindow(resolved, { locator, cursor: 0, max_bytes: 128 }),
      /project_source_(?:locator|file)_unsafe/,
      locator,
    );
  }
  assert.throws(
    () => readBoundProjectSourceWindow(resolved, { locator: 'notes/binary.txt', cursor: 0, max_bytes: 128 }),
    /project_source_binary_unsupported/,
  );
  assert.throws(
    () => readBoundProjectSourceWindow(resolved, {
      locator: 'notes/safe.txt', cursor: 0, max_bytes: 128,
      expected_version_digest: 'f'.repeat(64),
    }),
    /project_source_version_changed/,
  );
});

test('bound source window rejects unknown input fields and invalid bounds', () => {
  const { root, resolved } = fixture();
  writeFileSync(join(root, 'notes', 'safe.md'), 'Safe project source.\n');
  for (const input of [
    { locator: 'notes/safe.md', cursor: 0, max_bytes: 128, authority: 'spoofed' },
    { locator: 'notes/safe.md', cursor: -1, max_bytes: 128 },
    { locator: 'notes/safe.md', cursor: 0, max_bytes: 1 },
    { locator: 'notes/safe.md', cursor: 0, max_bytes: 1_000_000 },
  ]) {
    assert.throws(() => readBoundProjectSourceWindow(resolved, input), /project_source_window_invalid/);
  }
});
