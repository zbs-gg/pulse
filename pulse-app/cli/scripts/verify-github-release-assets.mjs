#!/usr/bin/env node
import { createHash } from 'node:crypto';
import { createReadStream, readFileSync, statSync } from 'node:fs';
import { basename, join } from 'node:path';
import { isGitHubReleaseAssetURL } from '../src/github-release-download.js';
const root = process.argv[2];
if (!root) throw new Error('release_asset_directory_required');
const manifest = JSON.parse(readFileSync(new URL('../release/personal-preview-manifest.json', import.meta.url)));
const artifacts = [...Object.values(manifest.payload.common_artifacts),
  ...Object.values(manifest.payload.targets).flatMap(target => Object.values(target.artifacts))];
for (const artifact of artifacts) {
  if (!isGitHubReleaseAssetURL(artifact.url)) throw new Error('release_asset_origin_invalid');
  const path = join(root, basename(new URL(artifact.url).pathname));
  if (statSync(path).size !== artifact.bytes) throw new Error('release_asset_size_mismatch');
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  if (hash.digest('hex') !== artifact.sha256) throw new Error('release_asset_digest_mismatch');
}
process.stdout.write(JSON.stringify({verified: artifacts.length, hosting: 'github-releases'}) + '\n');
