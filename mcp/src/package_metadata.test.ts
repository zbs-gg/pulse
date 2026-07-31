import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const packageJSON = JSON.parse(
  readFileSync(new URL('../package.json', import.meta.url), 'utf8'),
);

test('internal package exposes only the local MCP executable', () => {
	assert.equal(packageJSON.bin['pulse-mcp'], 'dist/index.js');
	assert.equal(packageJSON.bin['pulse-mcp-claude-smoke'], undefined);
	assert.equal(packageJSON.private, true);
  assert.ok(packageJSON.files.includes('scripts'));
  assert.ok(packageJSON.files.includes('docs'));
  assert.ok(packageJSON.files.includes('README_DEV_PREVIEW.md'));
});
