#!/usr/bin/env node

import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

import { loadNativeUniversalMatrix } from './native-universal-matrix.mjs';

const SHA256 = /^[a-f0-9]{64}$/;
const COMMIT = /^[a-f0-9]{40}$/;
const AUTHORITIES = Object.freeze(['fixture', 'production_candidate', 'public_registry']);

function argument(name, argv = process.argv) {
  const index = argv.indexOf(name);
  return index >= 0 ? argv[index + 1] : undefined;
}

function jsonFiles(root) {
  const files = [];
  const visit = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) visit(path);
      else if (entry.isFile() && entry.name.endsWith('.json')) files.push(path);
    }
  };
  visit(root);
  return files.sort();
}

export function validateNativeEvidenceSet(directory, {
  authority,
  matrix = loadNativeUniversalMatrix(),
} = {}) {
  const root = resolve(directory);
  assert.equal(isAbsolute(root), true);
  if (authority !== undefined) assert.equal(AUTHORITIES.includes(authority), true);
  const files = jsonFiles(root);
  const expectedPairs = matrix.harnesses.flatMap((harness) =>
    harness.supported_targets.map((targetID) => `${harness.host}\0${targetID}`));
  assert.equal(files.length, expectedPairs.length, `expected ${expectedPairs.length} native evidence files`);
  const seen = new Set();
  let packageSHA256;
  let sourceCommit;
  let artifactSetDigest;
  let snapshotDigest;
  for (const file of files) {
    const evidence = JSON.parse(readFileSync(file, 'utf8'));
    assert.equal(evidence.schema, 'pulse.native_host_target_evidence.v2');
    assert.equal(AUTHORITIES.includes(evidence.authority), true);
    if (authority !== undefined) assert.equal(evidence.authority, authority);
    assert.equal(evidence.content_free, true);
    assert.equal(typeof evidence.support_claim, 'boolean');
    assert.equal(evidence.support_claim === true, evidence.authority === 'public_registry');
    assert.match(evidence.source_commit, COMMIT);
    sourceCommit ??= evidence.source_commit;
    assert.equal(evidence.source_commit, sourceCommit);
    const harness = matrix.harnesses.find((value) => value.host === evidence.host);
    const target = matrix.targets.find((value) => value.target_id === evidence.target_id);
    assert.equal(Boolean(harness), true);
    assert.equal(Boolean(target), true);
    assert.equal(harness.supported_targets.includes(target.target_id), true);
    assert.equal(evidence.host_version, harness.version);
    assert.deepEqual(evidence.target, target);
    assert.equal(evidence.harness.vendor, harness.vendor);
    assert.equal(evidence.harness.distribution, harness.distribution);
    assert.equal(evidence.harness.identity, harness.identity);
    assert.equal(evidence.harness.vendor_source, harness.vendor_source);
    assert.equal(evidence.harness.download_url, harness.downloads[target.target_id]);
    assert.equal(['vendor_executable', 'fixture_driver'].includes(evidence.harness.executable_kind), true);
    assert.match(evidence.harness.executable_sha256, SHA256);
    const pair = `${evidence.host}\0${evidence.target_id}`;
    assert.equal(seen.has(pair), false, `duplicate native evidence for ${evidence.host}/${evidence.target_id}`);
    seen.add(pair);
    assert.match(evidence.package?.sha256, SHA256);
    assert.equal(Number.isSafeInteger(evidence.package?.bytes) && evidence.package.bytes > 0, true);
    packageSHA256 ??= evidence.package.sha256;
    assert.equal(evidence.package.sha256, packageSHA256);
    assert.deepEqual(evidence.privacy_defaults, {
      raw_transcripts: false, old_chat_import: false, backend_llm: false,
    });
    assert.equal(evidence.first_value?.degraded_lifecycle, false);
    if (pair === 'codex\0win32-arm64') {
      assert.equal(evidence.first_value?.stability?.consecutive_runs, 5);
      assert.equal(evidence.first_value.stability.runs_ms?.length, 5);
      assert.equal(evidence.first_value.stability.runs_ms.every((value) =>
        Number.isSafeInteger(value) && value >= 0 && value <= 60_000), true);
      assert.equal(Number.isSafeInteger(evidence.first_value.stability.median_ms), true);
      assert.equal(evidence.first_value.stability.median_ms <= 55_000, true);
    }
    if (evidence.authority === 'fixture') {
      assert.equal(evidence.release?.artifact_set_digest, null);
      assert.equal(evidence.release?.snapshot_digest, null);
    } else {
      assert.match(evidence.release?.artifact_set_digest, SHA256);
      assert.match(evidence.release?.snapshot_digest, SHA256);
      artifactSetDigest ??= evidence.release.artifact_set_digest;
      snapshotDigest ??= evidence.release.snapshot_digest;
      assert.equal(evidence.release.artifact_set_digest, artifactSetDigest);
      assert.equal(evidence.release.snapshot_digest, snapshotDigest);
      assert.equal(evidence.harness.executable_kind, 'vendor_executable');
      assert.match(evidence.harness.session_executable_sha256, SHA256);
      for (const milestone of [
        'install', 'vendor_session', 'lifecycle', 'memory_home', 'fresh_recall', 'repair', 'disconnect',
      ]) assert.equal(evidence.milestones?.[milestone], true, `${pair}:${milestone}`);
      if (evidence.target_id === 'darwin-arm64') {
        for (const milestone of [
          'deduplicated', 'fail_open', 'memory_survived_restart',
          'no_automatic_continuation', 'stop_and_goal_control_available',
        ]) assert.equal(evidence.milestones?.[milestone], true, `${pair}:${milestone}`);
      }
      assert.equal(evidence.first_value?.measured, true);
      assert.equal(Number.isSafeInteger(evidence.first_value?.milliseconds), true);
      assert.equal(evidence.first_value.milliseconds <= 60_000, true);
      assert.equal(Object.keys(evidence.first_value?.stages_ms ?? {}).length > 0, true);
    }
  }
  assert.deepEqual([...seen].sort(), expectedPairs.sort());
  return Object.freeze({
    schema: 'pulse.native_evidence_set_validation.v1',
    authority: authority ?? 'mixed',
    count: files.length,
    artifact_set_digest: artifactSetDigest ?? null,
    package_sha256: packageSHA256,
    snapshot_digest: snapshotDigest ?? null,
    source_commit: sourceCommit,
  });
}

function main() {
  const directory = argument('--directory');
  const authority = argument('--authority');
  if (!directory || (authority !== undefined && !AUTHORITIES.includes(authority))) {
    throw new Error('use --directory <path> [--authority fixture|production_candidate|public_registry]');
  }
  process.stdout.write(`${JSON.stringify(validateNativeEvidenceSet(directory, { authority }))}\n`);
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) main();
