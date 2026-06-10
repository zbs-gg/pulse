import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const packageJSON = JSON.parse(
  readFileSync(new URL('../package.json', import.meta.url), 'utf8'),
);

test('package exposes Claude connector smoke as an npm bin', () => {
  assert.equal(packageJSON.bin['pulse-mcp'], 'dist/index.js');
  assert.equal(
    packageJSON.bin['pulse-mcp-claude-smoke'],
    'scripts/claude-connector-smoke.mjs',
  );
  assert.ok(packageJSON.files.includes('scripts'));
  assert.ok(packageJSON.files.includes('docs'));
  assert.ok(packageJSON.files.includes('README_DEV_PREVIEW.md'));
});
