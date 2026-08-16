import assert from 'node:assert/strict';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { verifyPreviewPublication } from './verify-preview-publication.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');

const fixturePaths = [
  'docs/release/PREVIEW_PUBLICATION.json', 'pulse-app/cli/package.json', 'pulse-app/cli/package-lock.json',
  'pulse-app/cli/release/personal-preview-manifest.json', 'pulse-app/cli/release/personal-release-snapshot.json',
  'plugins/pulse/.claude-plugin/plugin.json', 'plugins/pulse/.codex-plugin/plugin.json',
  'plugins/pulse/.cursor-plugin/plugin.json', 'README.md', 'llms.txt', 'pulse-app/PRODUCT.md',
  'docs/INSTALL_WITH_AGENT.md', 'docs/PERSONAL_PULSE_ONBOARDING.md',
  'docs/SECURITY_INSTALL_CHECKLIST.md', 'CHANGELOG.md', 'docs/releases/v0.8.1.md',
];

function publicationFixture() {
  const fixture = mkdtempSync(join(tmpdir(), 'pulse-preview-publication-'));
  for (const relativePath of fixturePaths) {
    const destination = join(fixture, relativePath);
    mkdirSync(dirname(destination), { recursive: true });
    cpSync(join(root, relativePath), destination);
  }
  const version = JSON.parse(readFileSync(join(fixture, 'docs/release/PREVIEW_PUBLICATION.json'), 'utf8')).version;
  for (const relativePath of [
    'pulse-app/cli/package.json', 'pulse-app/cli/package-lock.json',
    'plugins/pulse/.claude-plugin/plugin.json', 'plugins/pulse/.codex-plugin/plugin.json',
    'plugins/pulse/.cursor-plugin/plugin.json',
  ]) {
    const path = join(fixture, relativePath);
    const value = JSON.parse(readFileSync(path, 'utf8'));
    value.version = version;
    if (value.packages?.['']) value.packages[''].version = version;
    writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`);
  }
  return fixture;
}

test('preview publication binds package, signed release, docs, npm bytes, and GitHub notes', () => {
  const fixture = publicationFixture();
  try {
    const result = verifyPreviewPublication(fixture);
    assert.equal(result.schema, 'pulse.preview_publication_verification.v1');
    assert.equal(result.package, '@zbs-gg/pulse');
    assert.equal(result.version, '0.8.1');
    assert.equal(result.release_epoch, 35);
    assert.match(result.archive_sha256, /^[a-f0-9]{64}$/);
    assert.equal(result.github_tag, 'v0.8.1');
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

test('preview publication refuses stale product documentation', () => {
  const fixture = publicationFixture();
  try {
    writeFileSync(join(fixture, 'README.md'), readFileSync(join(fixture, 'README.md'), 'utf8').replaceAll('0.8.1', '0.8.0'));
    assert.throws(() => verifyPreviewPublication(fixture), /preview_publication_documentation_stale/);
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});
