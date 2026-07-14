import assert from 'node:assert/strict';
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash, generateKeyPairSync } from 'node:crypto';
import { afterEach, test } from 'node:test';

import { exportJWK, SignJWT, type JWK } from 'jose';

import type { VerifiedOAuthIdentity } from './oauth-resource.js';
import {
  ENROLLMENT_REGISTRY_SCHEMA,
  InstallationEnrollmentRegistry,
  InstallationProofError,
  InstallationProofVerifier,
  SenderConstrainedOAuthVerifier,
  requireSenderConstrainedHeaders,
} from './sender-constrained-auth.js';

const ISSUER = 'https://auth.example.com';
const RESOURCE = 'https://pulse.example.com/mcp';
const CLIENT_ID = 'pulse-native-nik';
const SUBJECT = 'auth0|synthetic-nik';
const ACCESS_TOKEN = 'access.header.signature';
const NOW = 1_783_987_200;

const temporaryDirectories: string[] = [];
afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

type EnrollmentEntry = {
  enrollment_id: string;
  generation: number;
  client_id: string;
  subject: string;
  status: 'active' | 'revoked';
  public_jwk: JWK;
};

function temporaryRegistry(entries: EnrollmentEntry[], mode = 0o600) {
  const directory = mkdtempSync(join(tmpdir(), 'pulse-sender-proof-'));
  temporaryDirectories.push(directory);
  const file = join(directory, 'enrollments.json');
  const write = (nextEntries: EnrollmentEntry[], extra: Record<string, unknown> = {}) => {
    writeFileSync(file, JSON.stringify({
      schema: ENROLLMENT_REGISTRY_SCHEMA,
      issuer: ISSUER,
      enrollments: nextEntries,
      ...extra,
    }), { mode });
    chmodSync(file, mode);
  };
  write(entries);
  return { file, write };
}

function identity(overrides: Partial<VerifiedOAuthIdentity> = {}): VerifiedOAuthIdentity {
  return {
    issuer: ISSUER,
    subject: SUBJECT,
    clientId: CLIENT_ID,
    capabilities: ['pulse:connect', 'pulse:read'],
    ...overrides,
  };
}

async function installationKey() {
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const publicJWK = await exportJWK(publicKey);
  return { privateKey, publicJWK };
}

let proofSequence = 0;
async function proof(
  key: Awaited<ReturnType<typeof installationKey>>,
  overrides: Record<string, unknown> = {},
) {
  proofSequence++;
  return new SignJWT({
    htu: RESOURCE,
    htm: 'POST',
    iat: NOW,
    jti: `proof-jti-${proofSequence}`,
    ath: createHash('sha256').update(ACCESS_TOKEN).digest('base64url'),
    enrollment_id: 'enrollment_nik_mac_1',
    enrollment_generation: 1,
    client_id: CLIENT_ID,
    sub: SUBJECT,
    ...overrides,
  }).setProtectedHeader({ typ: 'dpop+jwt', alg: 'ES256', jwk: key.publicJWK })
    .sign(key.privateKey);
}

function entry(key: Pick<Awaited<ReturnType<typeof installationKey>>, 'publicJWK'>, overrides: Partial<EnrollmentEntry> = {}): EnrollmentEntry {
  return {
    enrollment_id: 'enrollment_nik_mac_1',
    generation: 1,
    client_id: CLIENT_ID,
    subject: SUBJECT,
    status: 'active',
    public_jwk: key.publicJWK,
    ...overrides,
  };
}

function verifier(file: string, now = NOW) {
  const registry = new InstallationEnrollmentRegistry({ file, issuer: ISSUER });
  return new InstallationProofVerifier({ registry, now: () => now });
}

function verifyInput(dpop: string, overrides: Record<string, unknown> = {}) {
  const header = JSON.parse(Buffer.from(dpop.split('.')[0] ?? '', 'base64url').toString('utf8')) as { jwk: JWK };
  const thumbprint = createHash('sha256').update(JSON.stringify({
    crv: header.jwk.crv, kty: header.jwk.kty, x: header.jwk.x, y: header.jwk.y,
  })).digest('base64url');
  return {
    authorization: `DPoP ${ACCESS_TOKEN}`,
    dpop,
    enrollmentID: 'enrollment_nik_mac_1',
    method: 'POST',
    targetURL: RESOURCE,
    identity: identity({ confirmationKeyThumbprint: thumbprint }),
    ...overrides,
  };
}

test('accepts the already-exported CLI installation proof contract and fences its JTI once', async () => {
  const cli = await import('../../pulse-app/cli/src/remote-auth.js') as {
    MemoryCredentialStore: new () => {
      createSigningKey(ref: string, record: unknown): void;
      set(ref: string, value: string): void;
    };
    createInstallationKeyRecord(input: { keyID: string }): {
      keyID: string; keyThumbprint: string; publicJWK: JWK;
    };
    persistRemoteCredential(
      store: unknown,
      ref: string,
      value: { tokenSet: unknown; key: unknown; enrollment: unknown; authority: unknown },
    ): unknown;
    buildSenderConstrainedRemoteHeaders(
      url: string,
      input: { method: string; credentialStore: unknown; credentialRef: string; now: number },
    ): Record<string, string>;
  };
  const store = new cli.MemoryCredentialStore();
  const key = cli.createInstallationKeyRecord({ keyID: 'installation-key-1' });
  const enrollment = {
    id: 'enrollment_nik_mac_1', generation: 1, clientID: CLIENT_ID, subject: SUBJECT,
    status: 'active', revokedAt: null, keyThumbprint: key.keyThumbprint,
  };
  cli.persistRemoteCredential(store, 'credential', {
    tokenSet: {
      tokens: {
        accessToken: ACCESS_TOKEN,
        refreshToken: 'synthetic-refresh-token',
        idToken: 'synthetic.id.token',
      },
      oauth: {
        issuer: ISSUER, audience: RESOURCE, clientID: CLIENT_ID, subject: SUBJECT,
        scope: ['pulse:connect', 'pulse:read'],
        accessExpiresAt: NOW + 300, tokenKeyThumbprint: key.keyThumbprint,
      },
      metadata: {},
    },
    key,
    enrollment,
    authority: {
      tokenEndpoint: `${ISSUER}/oauth/token`,
      jwksURI: `${ISSUER}/.well-known/jwks.json`,
    },
  });
  const headers = cli.buildSenderConstrainedRemoteHeaders(RESOURCE, {
    method: 'POST', credentialStore: store, credentialRef: 'credential', now: NOW,
  });
  const registry = temporaryRegistry([entry({ publicJWK: key.publicJWK })]);
  const installationVerifier = verifier(registry.file);

  assert.deepEqual(installationVerifier.verify(verifyInput(headers.DPoP)), {
    enrollmentID: enrollment.id,
    generation: 1,
    keyThumbprint: key.keyThumbprint,
  });
  assert.throws(
    () => installationVerifier.verify(verifyInput(headers.DPoP)),
    (error: unknown) => error instanceof InstallationProofError && error.code === 'proof_replayed',
  );
});

test('binds target, method, access token, enrollment generation, OAuth identity, and P-256 thumbprint', async () => {
  const key = await installationKey();
  const otherKey = await installationKey();
  const registry = temporaryRegistry([entry(key)]);
  const cases: Array<[string, () => Promise<string>, Record<string, unknown>, string]> = [
    ['target', () => proof(key, { htu: 'https://pulse.example.com/other' }), {}, 'proof_target_mismatch'],
    ['method', () => proof(key, { htm: 'GET' }), {}, 'proof_method_mismatch'],
    ['token', () => proof(key), { authorization: 'DPoP stolen.header.signature' }, 'proof_token_mismatch'],
    ['header enrollment', () => proof(key), { enrollmentID: 'enrollment_other' }, 'wrong_enrollment'],
    ['generation', () => proof(key, { enrollment_generation: 2 }), {}, 'wrong_enrollment'],
    ['client', () => proof(key, { client_id: 'other-client' }), {}, 'client_mismatch'],
    ['subject', () => proof(key, { sub: 'other-subject' }), {}, 'subject_mismatch'],
    ['issuer', () => proof(key), { identity: identity({ issuer: 'https://other.example.com' }) }, 'issuer_mismatch'],
    ['key', () => proof(otherKey), {}, 'installation_key_mismatch'],
    ['clock', () => proof(key, { iat: NOW - 31 }), {}, 'proof_clock_invalid'],
  ];
  for (const [name, createProof, input, code] of cases) {
    const candidate = await createProof();
    assert.throws(
      () => verifier(registry.file).verify(verifyInput(candidate, input)),
      (error: unknown) => error instanceof InstallationProofError && error.code === code,
      name,
    );
  }
});

test('reloads the operator registry on every request so rotation and revocation fail closed', async () => {
  const firstKey = await installationKey();
  const secondKey = await installationKey();
  const registryFile = temporaryRegistry([entry(firstKey)]);
  const installationVerifier = verifier(registryFile.file);
  installationVerifier.verify(verifyInput(await proof(firstKey)));

  registryFile.write([
    entry(firstKey, { status: 'revoked' }),
    entry(secondKey, {
      enrollment_id: 'enrollment_nik_mac_2',
      generation: 2,
    }),
  ]);
  const staleProof = await proof(firstKey);
  assert.throws(
    () => installationVerifier.verify(verifyInput(staleProof)),
    (error: unknown) => error instanceof InstallationProofError && error.code === 'enrollment_revoked',
  );
  const currentProof = await proof(secondKey, {
    enrollment_id: 'enrollment_nik_mac_2', enrollment_generation: 2,
  });
  assert.doesNotThrow(() => installationVerifier.verify(verifyInput(currentProof, {
    enrollmentID: 'enrollment_nik_mac_2',
  })));

  registryFile.write([
    entry(firstKey, { status: 'revoked' }),
    entry(secondKey, {
      enrollment_id: 'enrollment_nik_mac_2', generation: 2, status: 'revoked',
    }),
  ]);
  const revokedProof = await proof(secondKey, {
    enrollment_id: 'enrollment_nik_mac_2', enrollment_generation: 2,
  });
  assert.throws(
    () => installationVerifier.verify(verifyInput(revokedProof, {
      enrollmentID: 'enrollment_nik_mac_2',
    })),
    (error: unknown) => error instanceof InstallationProofError && error.code === 'enrollment_revoked',
  );
});

test('requires exactly one Authorization, DPoP, and X-Pulse-Enrollment header', () => {
  const headers = {
    authorization: 'DPoP access.header.signature',
    dpop: 'proof.header.signature',
    'x-pulse-enrollment': 'enrollment_nik_mac_1',
  };
  const rawHeaders = [
    'Authorization', headers.authorization,
    'DPoP', headers.dpop,
    'X-Pulse-Enrollment', headers['x-pulse-enrollment'],
  ];
  assert.deepEqual(requireSenderConstrainedHeaders({ rawHeaders, headers }), {
    authorization: headers.authorization,
    dpop: headers.dpop,
    enrollmentID: headers['x-pulse-enrollment'],
  });
  for (const name of ['authorization', 'dpop', 'x-pulse-enrollment']) {
    assert.throws(
      () => requireSenderConstrainedHeaders({
        rawHeaders: rawHeaders.filter((_, index) => {
          const headerIndex = index % 2 === 0 ? index : index - 1;
          return rawHeaders[headerIndex]?.toLowerCase() !== name;
        }),
        headers,
      }),
      (error: unknown) => error instanceof InstallationProofError && error.code === 'sender_headers_invalid',
      `missing ${name}`,
    );
    assert.throws(
      () => requireSenderConstrainedHeaders({
        rawHeaders: [...rawHeaders, name, 'duplicate-value'], headers,
      }),
      (error: unknown) => error instanceof InstallationProofError && error.code === 'sender_headers_invalid',
      `duplicate ${name}`,
    );
  }
});

test('keeps OAuth verification intact and completes proof verification before the caller can forward', async () => {
  const order: string[] = [];
  const oauthVerifier = {
    async verifyAuthorization(authorization: unknown, required: readonly string[]) {
      assert.equal(authorization, `Bearer ${ACCESS_TOKEN}`);
      order.push(`oauth:${required.join(',')}`);
      return identity();
    },
  };
  const proofVerifier = {
    verify() {
      order.push('proof');
      return { enrollmentID: 'enrollment_nik_mac_1', generation: 1, keyThumbprint: 'thumbprint' };
    },
  };
  const authenticator = new SenderConstrainedOAuthVerifier({ oauthVerifier, proofVerifier });
  const result = await authenticator.verifyAuthorization({
    authorization: `DPoP ${ACCESS_TOKEN}`,
    dpop: 'proof.header.signature',
    enrollmentID: 'enrollment_nik_mac_1',
    method: 'POST',
    targetURL: RESOURCE,
  }, ['pulse:connect']);
  order.push('forward');
  assert.equal(result.subject, SUBJECT);
  assert.deepEqual(order, ['oauth:pulse:connect', 'proof', 'forward']);

  let proofCalled = false;
  const rejectingAuthenticator = new SenderConstrainedOAuthVerifier({
    oauthVerifier: {
      async verifyAuthorization() {
        throw new Error('oauth rejected');
      },
    },
    proofVerifier: {
      verify() {
        proofCalled = true;
        return { enrollmentID: 'unused', generation: 1, keyThumbprint: 'unused' };
      },
    },
  });
  await assert.rejects(
    rejectingAuthenticator.verifyAuthorization({
      authorization: `DPoP ${ACCESS_TOKEN}`,
      dpop: 'proof.header.signature',
      enrollmentID: 'enrollment_nik_mac_1',
      method: 'POST',
      targetURL: RESOURCE,
    }, ['pulse:connect']),
    /oauth rejected/,
  );
  assert.equal(proofCalled, false);
});

test('rejects missing, unsafe, and malformed registry state without exposing file or credential content', async () => {
  const key = await installationKey();
  const missingDirectory = mkdtempSync(join(tmpdir(), 'pulse-sender-proof-missing-'));
  temporaryDirectories.push(missingDirectory);
  const missingRegistry = new InstallationEnrollmentRegistry({
    file: join(missingDirectory, 'missing.json'), issuer: ISSUER,
  });
  assert.throws(
    () => missingRegistry.assertReady(),
    (error: unknown) => error instanceof InstallationProofError && error.code === 'registry_unavailable',
  );

  const unsafe = temporaryRegistry([entry(key)], 0o644);
  const unsafeRegistry = new InstallationEnrollmentRegistry({ file: unsafe.file, issuer: ISSUER });
  assert.throws(
    () => unsafeRegistry.assertReady(),
    (error: unknown) => error instanceof InstallationProofError && error.code === 'registry_unavailable',
  );

  const malformed = temporaryRegistry([entry(key)]);
  malformed.write([entry(key)], { secret_access_token_sentinel: true });
  let rendered = '';
  try {
    verifier(malformed.file).verify(verifyInput(await proof(key)));
  } catch (error) {
    rendered = error instanceof Error ? `${error.message}\n${error.stack}` : String(error);
  }
  assert.match(rendered, /Sender-constrained credential rejected/);
  assert.doesNotMatch(rendered, /secret-access-token-sentinel|secret_access_token_sentinel|enrollments\.json/);

  const safe = temporaryRegistry([entry(key)]);
  rendered = '';
  try {
    verifier(safe.file).verify(verifyInput(await proof(key), {
      authorization: 'DPoP secret-access-token-sentinel.header.signature',
    }));
  } catch (error) {
    rendered = error instanceof Error ? `${error.message}\n${error.stack}` : String(error);
  }
  assert.match(rendered, /Sender-constrained credential rejected/);
  assert.doesNotMatch(rendered, /secret-access-token-sentinel|eyJ|proof-jti/);
});

test('exports the exact closed registry schema and a pure provisioning validator', async () => {
  const module = await import('./sender-constrained-auth.js') as Record<string, unknown>;
  const schema = module.INSTALLATION_ENROLLMENT_REGISTRY_JSON_SCHEMA as {
    $id?: string;
    additionalProperties?: boolean;
    properties?: { enrollments?: { maxItems?: number } };
  } | undefined;
  assert.equal(schema?.$id, 'https://zbs.gg/schemas/pulse.team.installation_enrollment_registry.v1.json');
  assert.equal(schema?.additionalProperties, false);
  assert.equal(schema?.properties?.enrollments?.maxItems, 1024);
  assert.equal(typeof module.validateInstallationEnrollmentRegistry, 'function');
  const packageModule = await import('./index.js') as Record<string, unknown>;
  assert.equal(
    packageModule.validateInstallationEnrollmentRegistry,
    module.validateInstallationEnrollmentRegistry,
  );

  const key = await installationKey();
  const candidate = {
    schema: ENROLLMENT_REGISTRY_SCHEMA,
    issuer: ISSUER,
    enrollments: [entry(key)],
  };
  const validate = module.validateInstallationEnrollmentRegistry as (
    value: unknown,
    options: { issuer: string },
  ) => typeof candidate;
  const validated = validate(candidate, { issuer: ISSUER });
  assert.deepEqual(validated, candidate);
  assert.equal(Object.isFrozen(validated), true);
  assert.equal(Object.isFrozen(validated.enrollments), true);
  assert.equal(Object.isFrozen(validated.enrollments[0]?.public_jwk), true);
  assert.throws(
    () => validate({ ...candidate, operator_approved: true }, { issuer: ISSUER }),
    (error: unknown) => error instanceof InstallationProofError && error.code === 'registry_invalid',
  );
});
