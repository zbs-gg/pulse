import assert from 'node:assert/strict';
import test from 'node:test';
import { isGitHubReleaseAssetRedirect } from './github-release-download.js';
const source = 'https://github.com/zbs-gg/pulse/releases/download/v0.8.3/common-model.tar.gz';
const cdn = 'https://release-assets.githubusercontent.com/github-production-release-asset/123/abc-def?se=expires&sig=signature';
test('only public Pulse GitHub release assets may redirect to the GitHub asset CDN', () => {
  assert.equal(isGitHubReleaseAssetRedirect(source, cdn), true);
  for (const bad of [source.replace('zbs-gg/pulse', 'other/repo'), source+'?token=x', source.replace('https:', 'http:'), source.replace('/releases/download/', '/raw/')]) {
    assert.equal(isGitHubReleaseAssetRedirect(bad, cdn), false);
  }
  for (const bad of [cdn.replace('https:', 'http:'), cdn.replace('.com/', '.com.evil.test/'), cdn.replace('https://', 'https://user:pass@'), cdn+'#hash', cdn.replace('/github-production-release-asset/', '/other/'), 'https://127.0.0.1/payload']) {
    assert.equal(isGitHubReleaseAssetRedirect(source, bad), false);
  }
});
