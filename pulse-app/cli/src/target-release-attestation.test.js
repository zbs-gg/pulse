import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  TargetReleaseAttestationError,
  attestSelectedTarget,
} from './target-release-attestation.js';

function executableArtifact(root, { identifier = null, platform, publisher = null } = {}) {
  const path = join(root, platform === 'win32' ? 'pulse.exe' : 'pulse');
  const contents = Buffer.from('fixture');
  writeFileSync(path, contents, { mode: 0o700 });
  const file = {
    bytes: contents.byteLength, executable: true, mode: 0o700,
    path: platform === 'win32' ? 'pulse.exe' : 'pulse',
    sha256: createHash('sha256').update(contents).digest('hex'),
  };
  const tree = { files: [file], schema: 'pulse.artifact_tree.v1' };
  return {
    descriptor: {
      executable: true,
      signing: platform === 'darwin'
        ? { identifier, scheme: 'apple-developer-id', team_id: '44N4NZ86S5' }
        : { identifier: null, scheme: platform === 'win32' ? 'windows-authenticode' : 'release-manifest', team_id: null },
      tree_digest: createHash('sha256').update(JSON.stringify(tree)).digest('hex'),
    },
    root,
    tree,
  };
}

function dataArtifact(root, name) {
  const contents = Buffer.from(`${name}-fixture`);
  const path = join(root, name);
  writeFileSync(path, contents, { mode: 0o600 });
  const file = {
    bytes: contents.byteLength, executable: false, mode: 0o600, path: name,
    sha256: createHash('sha256').update(contents).digest('hex'),
  };
  const tree = { files: [file], schema: 'pulse.artifact_tree.v1' };
  return {
    descriptor: {
      executable: false, signing: { identifier: null, scheme: 'release-manifest', team_id: null },
      tree_digest: createHash('sha256').update(JSON.stringify(tree)).digest('hex'),
    },
    root,
    tree,
  };
}

function emptyDataArtifact(root, name) {
  const contents = Buffer.alloc(0);
  const path = join(root, name);
  writeFileSync(path, contents, { mode: 0o600 });
  const file = {
    bytes: 0, executable: false, mode: 0o600, path: name,
    sha256: createHash('sha256').update(contents).digest('hex'),
  };
  const tree = { files: [file], schema: 'pulse.artifact_tree.v1' };
  return {
    descriptor: {
      executable: false, signing: { identifier: null, scheme: 'release-manifest', team_id: null },
      tree_digest: createHash('sha256').update(JSON.stringify(tree)).digest('hex'),
    },
    root,
    tree,
  };
}

function completeArtifacts(root, platform) {
  const values = {};
  for (const kind of ['daemon', 'embedder-runtime', 'model', 'plugin-runtime']) {
    const artifactRoot = join(root, kind);
    mkdirSync(artifactRoot);
    values[kind] = kind === 'daemon' || kind === 'embedder-runtime'
      ? executableArtifact(artifactRoot, { identifier: `gg.zbs.pulse.${kind}`, platform })
      : dataArtifact(artifactRoot, `${kind}.bin`);
  }
  return values;
}

function expectCode(fn, code) {
  assert.throws(fn, (error) => error instanceof TargetReleaseAttestationError && error.code === code);
}

test('fixture evidence is explicit and can never satisfy production policy', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-attest-fixture.'));
  try {
    const target = {
      platform: 'linux', verification_profile: { fixture_id: 'pr-linux-x64', kind: 'fixture', production: false },
    };
    const receipt = attestSelectedTarget({ artifacts: {}, mode: 'fixture', platform: 'linux', target });
    assert.equal(receipt.production, false);
    assert.equal(receipt.policy, 'fixture');
    expectCode(() => attestSelectedTarget({ artifacts: {}, mode: 'production', platform: 'linux', target }),
      'release_fixture_cannot_attest_production');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Apple policy verifies every inner executable with exact Team ID, identifier, and notarized other-code evidence', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-attest-apple.'));
  try {
    const artifacts = completeArtifacts(root, 'darwin');
    const calls = [];
    const receipt = attestSelectedTarget({
      artifacts,
      catalogVerified: true,
      manifestDigest: 'a'.repeat(64),
      mode: 'production',
      platform: 'darwin',
      run(command, args) {
        calls.push([command, args]);
        if (command === '/usr/bin/codesign' && args[0] === '-d') {
          const identifier = String(args.at(-1)).includes('embedder-runtime')
            ? 'gg.zbs.pulse.embedder-runtime'
            : 'gg.zbs.pulse.daemon';
          return { status: 0, stderr: `Identifier=${identifier}\nTeamIdentifier=44N4NZ86S5\nAuthority=Developer ID Application: ZBS GG Inc. (44N4NZ86S5)\n` };
        }
        return { status: 0, stderr: '' };
      },
      target: {
        platform: 'darwin',
        verification_profile: { gatekeeper: true, kind: 'apple', notarized: true, stapled: false, team_id: '44N4NZ86S5' },
      },
    });
    assert.equal(receipt.production, true);
    assert.equal(receipt.executables_verified, 2);
    assert.equal(calls.some(([command, args]) =>
      command === '/usr/bin/codesign' && args.includes('--check-notarization')), true);
    expectCode(() => attestSelectedTarget({
      artifacts, catalogVerified: true, manifestDigest: 'a'.repeat(64), mode: 'production', platform: 'darwin',
      run: () => ({ status: 0, stderr: '' }),
      target: { platform: 'darwin', verification_profile: {
        gatekeeper: true, kind: 'apple', notarized: true, stapled: true, team_id: '44N4NZ86S5',
      } },
    }), 'apple_stapling_claim_invalid');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Windows policy refuses a valid signature without timestamp evidence or exact publisher', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-attest-windows.'));
  try {
    const artifacts = completeArtifacts(root, 'win32');
    const target = {
      platform: 'win32',
      verification_profile: {
        kind: 'windows', publisher: 'CN=ZBS GG Inc.', timestamp_url: 'https://timestamp.digicert.com', timestamped: true,
      },
    };
    const receipt = attestSelectedTarget({
      artifacts, catalogVerified: true, manifestDigest: 'a'.repeat(64), mode: 'production', platform: 'win32', target,
      run: (_command, _args, options) => ({
        status: 0,
        stdout: JSON.stringify({
          signature_type: 'Authenticode', signer_subject: 'CN=ZBS GG Inc.', status: 'Valid',
          timestamper_subject: 'CN=DigiCert Timestamp',
        }),
        verified_path: options.env.PULSE_ATTEST_PATH,
      }),
    });
    assert.equal(receipt.production, true);
    assert.equal(receipt.executables_verified, 2);
    expectCode(() => attestSelectedTarget({
      artifacts, mode: 'production', platform: 'win32', target,
      catalogVerified: true, manifestDigest: 'a'.repeat(64),
      run: () => ({ status: 0, stdout: JSON.stringify({ signature_type: 'Authenticode', signer_subject: 'CN=ZBS GG Inc.', status: 'Valid', timestamper_subject: null }) }),
    }), 'windows_authenticode_timestamp_missing');
    expectCode(() => attestSelectedTarget({
      artifacts, mode: 'production', platform: 'win32', target,
      catalogVerified: true, manifestDigest: 'a'.repeat(64),
      run: () => ({ status: 0, stdout: JSON.stringify({ signature_type: 'Authenticode', signer_subject: 'CN=Other Corp', status: 'Valid', timestamper_subject: 'CN=DigiCert Timestamp' }) }),
    }), 'windows_authenticode_publisher_mismatch');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Linux production policy is signed catalog plus exact tree and never shells out', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-attest-linux.'));
  try {
    const artifacts = completeArtifacts(root, 'linux');
    const receipt = attestSelectedTarget({
      artifacts, catalogVerified: true, manifestDigest: 'a'.repeat(64), mode: 'production', platform: 'linux',
      run: () => { throw new Error('must not execute'); },
      target: { platform: 'linux', verification_profile: { kind: 'linux', policy: 'signed-catalog-tree-v1' } },
    });
    assert.equal(receipt.policy, 'signed-catalog-tree-v1');
    expectCode(() => attestSelectedTarget({
      artifacts, catalogVerified: false, manifestDigest: 'a'.repeat(64), mode: 'production', platform: 'linux',
      target: { platform: 'linux', verification_profile: { kind: 'linux', policy: 'signed-catalog-tree-v1' } },
    }), 'linux_signed_catalog_required');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('signed data trees allow canonical zero-byte package files', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-attest-empty-data.'));
  try {
    const artifacts = completeArtifacts(root, 'linux');
    const pluginRoot = join(root, 'plugin-runtime');
    artifacts['plugin-runtime'] = emptyDataArtifact(pluginRoot, 'empty-module.js');
    const receipt = attestSelectedTarget({
      artifacts,
      catalogVerified: true,
      manifestDigest: 'a'.repeat(64),
      mode: 'production',
      platform: 'linux',
      run: () => { throw new Error('must not execute'); },
      target: {
        platform: 'linux',
        verification_profile: { kind: 'linux', policy: 'signed-catalog-tree-v1' },
      },
    });
    assert.equal(receipt.production, true);
    assert.equal(receipt.executables_verified, 2);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
