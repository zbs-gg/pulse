#!/usr/bin/env node

import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const SHA256 = /^[a-f0-9]{64}$/;
const VERSION = /^\d+\.\d+\.\d+$/;
const scriptRoot = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(scriptRoot, '..', '..', '..');

function fail(code, detail = '') {
  throw new Error(detail ? `${code}: ${detail}` : code);
}

function readJSON(path) {
  try { return JSON.parse(readFileSync(path, 'utf8')); }
  catch { fail('preview_publication_json_invalid', path); }
}

function requireText(path, pattern, code) {
  let text;
  try { text = readFileSync(path, 'utf8'); }
  catch { fail(code, path); }
  if (!pattern.test(text)) fail(code, path);
  return text;
}

function exactReleaseURL(url, version, suffix) {
  return url === `https://pulse-personal-releases-zbs.storage.googleapis.com/pulse/${version}/${suffix}`;
}

export function verifyPreviewPublication(root = repositoryRoot) {
  const publicationPath = join(root, 'docs', 'release', 'PREVIEW_PUBLICATION.json');
  const publication = readJSON(publicationPath);
  const packageJSON = readJSON(join(root, 'pulse-app', 'cli', 'package.json'));
  const packageLock = readJSON(join(root, 'pulse-app', 'cli', 'package-lock.json'));
  const artifactSet = readJSON(join(root, 'pulse-app', 'cli', 'release', 'personal-preview-manifest.json'));
  const snapshot = readJSON(join(root, 'pulse-app', 'cli', 'release', 'personal-release-snapshot.json'));
  const version = publication.version;
  const epoch = publication.release_epoch;

  if (publication.schema !== 'pulse.preview_publication.v1' || publication.package !== '@zbs-gg/pulse' ||
      !VERSION.test(version ?? '') || !Number.isSafeInteger(epoch) || epoch < 1 ||
      publication.npm_tag !== 'preview') fail('preview_publication_identity_invalid');
  for (const value of [
    publication.archive?.sha256, publication.archive?.tree_sha256,
    publication.artifact_set?.sha256, publication.snapshot?.sha256,
  ]) if (!SHA256.test(value ?? '')) fail('preview_publication_digest_invalid');
  if (!exactReleaseURL(publication.archive?.url, version, `npm/epoch-${epoch}-final/zbs-gg-pulse-${version}.tgz`) ||
      !exactReleaseURL(publication.artifact_set?.url, version, `epoch-${epoch}/catalog/artifact-set.json`) ||
      !exactReleaseURL(publication.snapshot?.url, version, 'catalog/snapshot.json')) {
    fail('preview_publication_url_invalid');
  }
  if (packageJSON.name !== publication.package || packageJSON.version !== version ||
      packageLock.version !== version || packageLock.packages?.['']?.version !== version) {
    fail('preview_publication_package_version_mismatch');
  }
  const release = artifactSet.payload?.release;
  if (artifactSet.schema !== 'pulse.personal_release_artifact_set.v1' ||
      release?.package !== publication.package || release?.version !== version || release?.epoch !== epoch ||
      snapshot.schema !== 'pulse.release_snapshot_envelope.v1' ||
      snapshot.payload?.package !== publication.package || snapshot.payload?.version !== version ||
      snapshot.payload?.release_epoch !== epoch ||
      snapshot.payload?.artifact_set?.url !== publication.artifact_set.url ||
      snapshot.payload?.artifact_set?.sha256 !== publication.artifact_set.sha256) {
    fail('preview_publication_signed_release_mismatch');
  }
  const pluginManifests = [
    'plugins/pulse/.claude-plugin/plugin.json',
    'plugins/pulse/.codex-plugin/plugin.json',
    'plugins/pulse/.cursor-plugin/plugin.json',
  ];
  for (const relativePath of pluginManifests) {
    if (readJSON(join(root, relativePath)).version !== version) {
      fail('preview_publication_plugin_version_mismatch', relativePath);
    }
  }
  const escaped = version.replaceAll('.', '\\.');
  for (const relativePath of [
    'README.md', 'llms.txt', 'pulse-app/PRODUCT.md', 'docs/INSTALL_WITH_AGENT.md',
    'docs/PERSONAL_PULSE_ONBOARDING.md', 'docs/SECURITY_INSTALL_CHECKLIST.md',
  ]) requireText(join(root, relativePath), new RegExp(escaped), 'preview_publication_documentation_stale');
  requireText(join(root, 'CHANGELOG.md'), new RegExp(`^## ${escaped}(?:\\s|$)`, 'm'), 'preview_publication_changelog_missing');

  const github = publication.github;
  if (github?.tag !== `v${version}` || github?.title !== `Pulse Personal ${version} preview` ||
      github?.prerelease !== true || typeof github?.notes !== 'string' ||
      !github.notes.startsWith('docs/releases/') || !github.notes.endsWith('.md')) {
    fail('preview_publication_github_release_invalid');
  }
  requireText(join(root, github.notes), new RegExp(`Pulse Personal ${escaped} preview`), 'preview_publication_release_notes_missing');

  return Object.freeze({
    archive_sha256: publication.archive.sha256,
    archive_tree_sha256: publication.archive.tree_sha256,
    archive_url: publication.archive.url,
    artifact_set_sha256: publication.artifact_set.sha256,
    artifact_set_url: publication.artifact_set.url,
    github_notes: publication.github.notes,
    github_tag: publication.github.tag,
    github_title: publication.github.title,
    package: publication.package,
    release_epoch: epoch,
    schema: 'pulse.preview_publication_verification.v1',
    snapshot_sha256: publication.snapshot.sha256,
    snapshot_url: publication.snapshot.url,
    version,
  });
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.stdout.write(`${JSON.stringify(verifyPreviewPublication())}\n`);
}
