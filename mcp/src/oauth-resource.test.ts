import assert from 'node:assert/strict';
import { generateKeyPairSync } from 'node:crypto';
import { test } from 'node:test';

import { exportJWK, SignJWT } from 'jose';

import {
  AIRLOCK_HUMAN_PRESENCE_ACR,
  AIRLOCK_HUMAN_PRESENCE_CLAIM,
  AIRLOCK_HUMAN_PRESENCE_SCHEMA,
  AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
  OAuthResourceError,
  OAuthResourceVerifier,
  protectedResourceMetadata,
} from './oauth-resource.js';

const ISSUER = 'https://auth.example.com';
const RESOURCE = 'https://pulse.example.com/mcp';
const NOW = 1_789_000_000;

async function fixture(issuer = ISSUER, resolvedAddresses: readonly string[] = ['93.184.216.34']) {
  const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const jwk = await exportJWK(publicKey);
  Object.assign(jwk, { kid: 'issuer-key-1', alg: 'RS256', use: 'sig' });
  let jwksAvailable = true;
  let jwks = { keys: [jwk] };
  const calls: string[] = [];
  const fetcher: typeof fetch = async (input, init) => {
    const url = input.toString();
    calls.push(url);
    assert.equal(init?.redirect, 'error');
    const issuerURL = new URL(issuer);
    const metadataURL = new URL(
      `/.well-known/oauth-authorization-server${issuerURL.pathname === '/' ? '' : issuerURL.pathname}`,
      issuerURL.origin,
    ).toString();
    if (url === metadataURL) {
      return Response.json({
        issuer,
        jwks_uri: new URL('jwks', issuer).toString(),
        code_challenge_methods_supported: ['S256'],
      });
    }
    if (url === new URL('jwks', issuer).toString() && jwksAvailable) {
      return Response.json(jwks);
    }
    throw new Error('synthetic issuer outage');
  };
  const verifier = new OAuthResourceVerifier({
    issuer,
    resource: RESOURCE,
    fetch: fetcher,
    resolveHost: async () => resolvedAddresses,
    now: () => NOW,
    cacheTtlSeconds: 60,
    staleIfErrorSeconds: 120,
  });
  const token = async (overrides: Record<string, unknown> = {}, header: Record<string, unknown> = {}) => {
    const claims: Record<string, unknown> = {
      iss: issuer,
      sub: 'human-subject-1',
      aud: RESOURCE,
      exp: NOW + 300,
      iat: NOW,
      jti: 'token-jti-1',
      client_id: 'agent-client-a',
      scope: 'pulse:connect pulse:read',
      ...overrides,
    };
    return new SignJWT(claims)
      .setProtectedHeader({ alg: 'RS256', kid: 'issuer-key-1', typ: 'at+jwt', ...header })
      .sign(privateKey);
  };
  const idToken = async (overrides: Record<string, unknown> = {}, header: Record<string, unknown> = {}) => {
    const claims: Record<string, unknown> = {
      iss: issuer,
      sub: 'human-subject-1',
      aud: 'owner-browser-client',
      exp: NOW + 3600,
      iat: NOW,
      auth_time: NOW - 30,
      nonce: 'n'.repeat(43),
      acr: AIRLOCK_HUMAN_PRESENCE_ACR,
      amr: ['pwd', 'mfa'],
      [AIRLOCK_HUMAN_PRESENCE_CLAIM]: {
        schema: AIRLOCK_HUMAN_PRESENCE_SCHEMA,
        factor: AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
        verified_at: NOW - 30,
      },
      ...overrides,
    };
    return new SignJWT(claims)
      .setProtectedHeader({ alg: 'RS256', kid: 'issuer-key-1', typ: 'JWT', ...header })
      .sign(privateKey);
  };
  return {
    calls,
    setJwksAvailable(value: boolean) { jwksAvailable = value; },
    setJwks(value: { keys: typeof jwks.keys }) { jwks = value; },
    idToken,
    token,
    verifier,
  };
}

test('preserves a canonical issuer trailing slash across metadata and JWT identity', async () => {
  const issuer = 'https://auth.example.com/tenant/';
  const f = await fixture(issuer);
  const identity = await f.verifier.verifyAuthorization(
    `Bearer ${await f.token()}`,
    ['pulse:connect'],
  );
  assert.equal(identity.issuer, issuer);
  assert.equal(f.calls[0], 'https://auth.example.com/.well-known/oauth-authorization-server/tenant/');
  assert.deepEqual(protectedResourceMetadata(RESOURCE, issuer).authorization_servers, [issuer]);
});

test('applies one absolute OAuth fetch deadline to DNS resolution', async () => {
  const f = await fixture();
  const verifier = new OAuthResourceVerifier({
    issuer: ISSUER,
    resource: RESOURCE,
    fetchTimeoutMs: 100,
    resolveHost: () => new Promise<readonly string[]>(() => undefined),
  });
  const started = Date.now();
  await assert.rejects(
    verifier.verifyAuthorization(`Bearer ${await f.token()}`, ['pulse:connect']),
    (error: unknown) => error instanceof OAuthResourceError && error.reasonCode === 'invalid_credential',
  );
  assert.ok(Date.now() - started < 750, 'DNS resolution exceeded the absolute fetch deadline');
});

test('never starts an OAuth request after DNS consumes the absolute deadline', async () => {
  const f = await fixture();
  let fetchCalls = 0;
  const verifier = new OAuthResourceVerifier({
    issuer: ISSUER,
    resource: RESOURCE,
    fetchTimeoutMs: 100,
    resolveHost: async () => {
      await new Promise((resolve) => setTimeout(resolve, 150));
      return ['93.184.216.34'];
    },
    fetch: async () => {
      fetchCalls++;
      return Response.json({});
    },
  });
  await assert.rejects(
    verifier.verifyAuthorization(`Bearer ${await f.token()}`, ['pulse:connect']),
    OAuthResourceError,
  );
  await new Promise((resolve) => setTimeout(resolve, 100));
  assert.equal(fetchCalls, 0);
});

test('publishes exact protected-resource metadata and validates a strict access token', async () => {
  assert.deepEqual(protectedResourceMetadata(RESOURCE, ISSUER), {
    resource: RESOURCE,
    authorization_servers: [ISSUER],
    bearer_methods_supported: ['header'],
    scopes_supported: [
      'pulse:connect',
      'pulse:status',
      'pulse:read',
      'pulse:write',
      'pulse:audit',
      'pulse:delete',
      'pulse:owner',
    ],
  });

  const f = await fixture();
  const identity = await f.verifier.verifyAuthorization(
    `Bearer ${await f.token()}`,
    ['pulse:connect', 'pulse:read'],
  );
  assert.deepEqual(identity, {
    issuer: ISSUER,
    subject: 'human-subject-1',
    clientId: 'agent-client-a',
    capabilities: ['pulse:connect', 'pulse:read'],
  });
  assert.deepEqual(f.calls, [
    `${ISSUER}/.well-known/oauth-authorization-server`,
    `${ISSUER}/jwks`,
  ]);
});

test('preserves externally verified auth_time only for recent Owner enforcement', async () => {
  const f = await fixture();
  const identity = await f.verifier.verifyAuthorization(
    `Bearer ${await f.token({
      scope: 'pulse:connect pulse:owner',
      auth_time: NOW - 60,
      client_id: 'owner-browser-client',
    })}`,
    ['pulse:owner'],
  );
  assert.deepEqual(identity, {
    issuer: ISSUER,
    subject: 'human-subject-1',
    clientId: 'owner-browser-client',
    capabilities: ['pulse:connect', 'pulse:owner'],
    authTime: NOW - 60,
  });
  await assert.rejects(
    f.verifier.verifyAuthorization(
      `Bearer ${await f.token({ scope: 'pulse:connect pulse:owner', auth_time: 'recent' })}`,
      ['pulse:owner'],
    ),
    OAuthResourceError,
  );
});

test('preserves only an exact P-256 DPoP confirmation thumbprint from the access token', async () => {
  const f = await fixture();
  const thumbprint = 'a'.repeat(43);
  const identity = await f.verifier.verifyAuthorization(
    `Bearer ${await f.token({ cnf: { jkt: thumbprint } })}`,
    ['pulse:connect'],
  );
  assert.equal(identity.confirmationKeyThumbprint, thumbprint);
  for (const cnf of [
    { jkt: 'short' },
    { jkt: thumbprint, other: true },
    { x5t: thumbprint },
  ]) {
    await assert.rejects(
      f.verifier.verifyAuthorization(`Bearer ${await f.token({ cnf })}`, ['pulse:connect']),
      OAuthResourceError,
    );
  }
});

test('browser authorization requires current-flow platform WebAuthn, one subject/client, and a nonce-bound ID token', async () => {
  const f = await fixture();
  const accessToken = await f.token({
    scope: 'pulse:owner',
    client_id: 'owner-browser-client',
    auth_time: NOW - 30,
  });
  const idToken = await f.idToken();
  assert.deepEqual(await f.verifier.verifyBrowserAuthorization({
    accessToken,
    idToken,
    clientId: 'owner-browser-client',
    nonce: 'n'.repeat(43),
    requiredCapabilities: ['pulse:owner'],
    authorizationStartedAt: NOW - 40,
  }), {
    issuer: ISSUER,
    subject: 'human-subject-1',
    clientId: 'owner-browser-client',
    capabilities: ['pulse:owner'],
    authTime: NOW - 30,
    authenticationContext: AIRLOCK_HUMAN_PRESENCE_ACR,
    authenticationMethods: ['pwd', 'mfa'],
    humanPresence: {
      schema: AIRLOCK_HUMAN_PRESENCE_SCHEMA,
      factor: AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
      verifiedAt: NOW - 30,
    },
    tokenExpiresAt: NOW + 3600,
  });

  await assert.rejects(f.verifier.verifyBrowserAuthorization({
    accessToken,
    idToken,
    clientId: 'owner-browser-client',
    nonce: 'x'.repeat(43),
    requiredCapabilities: ['pulse:owner'],
    authorizationStartedAt: NOW - 40,
  }), OAuthResourceError);
  await assert.rejects(f.verifier.verifyBrowserAuthorization({
    accessToken: await f.token({
      scope: 'pulse:owner', client_id: 'other-browser-client', auth_time: NOW - 30,
    }),
    idToken,
    clientId: 'owner-browser-client',
    nonce: 'n'.repeat(43),
    requiredCapabilities: ['pulse:owner'],
    authorizationStartedAt: NOW - 40,
  }), OAuthResourceError);
  await assert.rejects(f.verifier.verifyBrowserAuthorization({
    accessToken,
    idToken: await f.idToken({ azp: 'other-browser-client' }),
    clientId: 'owner-browser-client',
    nonce: 'n'.repeat(43),
    requiredCapabilities: ['pulse:owner'],
    authorizationStartedAt: NOW - 40,
  }), OAuthResourceError);
  await assert.rejects(f.verifier.verifyBrowserAuthorization({
    accessToken,
    idToken: await f.idToken({ sub: 'other-subject' }),
    clientId: 'owner-browser-client',
    nonce: 'n'.repeat(43),
    requiredCapabilities: ['pulse:owner'],
    authorizationStartedAt: NOW - 40,
  }), OAuthResourceError);
  await assert.rejects(f.verifier.verifyBrowserAuthorization({
    accessToken,
    idToken: await f.idToken({ auth_time: NOW - 301 }),
    clientId: 'owner-browser-client',
    nonce: 'n'.repeat(43),
    requiredCapabilities: ['pulse:owner'],
    authorizationStartedAt: NOW - 40,
  }), OAuthResourceError);
});

test('browser authorization rejects silent SSO, ordinary MFA, wrong factors, stale evidence, and ambiguous claims', async (t) => {
  const cases: Array<[string, Record<string, unknown>]> = [
    ['silent but otherwise recent SSO predating this flow', { auth_time: NOW - 60 }],
    ['fresh ordinary SSO without MFA', { amr: ['pwd'], [AIRLOCK_HUMAN_PRESENCE_CLAIM]: undefined }],
    ['generic MFA without the platform factor claim', { [AIRLOCK_HUMAN_PRESENCE_CLAIM]: undefined }],
    ['OTP is not platform WebAuthn', {
      [AIRLOCK_HUMAN_PRESENCE_CLAIM]: {
        schema: AIRLOCK_HUMAN_PRESENCE_SCHEMA, factor: 'otp', verified_at: NOW,
      },
    }],
    ['email is not platform WebAuthn', {
      [AIRLOCK_HUMAN_PRESENCE_CLAIM]: {
        schema: AIRLOCK_HUMAN_PRESENCE_SCHEMA, factor: 'email', verified_at: NOW,
      },
    }],
    ['stale platform WebAuthn evidence', {
      [AIRLOCK_HUMAN_PRESENCE_CLAIM]: {
        schema: AIRLOCK_HUMAN_PRESENCE_SCHEMA,
        factor: AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
        verified_at: NOW - 60,
      },
    }],
    ['wrong ACR', { acr: 'https://attacker.example/acr/mfa' }],
    ['duplicated authentication method', { amr: ['pwd', 'mfa', 'mfa'] }],
    ['duplicated ACR copy', { acr: [AIRLOCK_HUMAN_PRESENCE_ACR, AIRLOCK_HUMAN_PRESENCE_ACR] }],
    ['tampered factor claim with an extra field', {
      [AIRLOCK_HUMAN_PRESENCE_CLAIM]: {
        schema: AIRLOCK_HUMAN_PRESENCE_SCHEMA,
        factor: AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
        verified_at: NOW,
        copied_from: 'older-session',
      },
    }],
  ];
  for (const [name, overrides] of cases) {
    await t.test(name, async () => {
      const f = await fixture();
      const authTime = typeof overrides.auth_time === 'number' ? overrides.auth_time : NOW;
      const accessToken = await f.token({
        scope: 'pulse:owner', client_id: 'owner-browser-client', auth_time: authTime,
      });
      await assert.rejects(f.verifier.verifyBrowserAuthorization({
        accessToken,
        idToken: await f.idToken({ auth_time: NOW, ...overrides }),
        clientId: 'owner-browser-client',
        nonce: 'n'.repeat(43),
        requiredCapabilities: ['pulse:owner'],
        authorizationStartedAt: NOW,
      }), OAuthResourceError);
    });
  }
});

test('rejects malformed RFC9068-style claims, headers, lifetime, and exact audience violations', async (t) => {
  const cases: Array<[string, Record<string, unknown>, Record<string, unknown>]> = [
    ['missing subject', { sub: undefined }, {}],
    ['wrong issuer', { iss: 'https://attacker.example' }, {}],
    ['audience arrays are not exact', { aud: [RESOURCE, 'https://other.example'] }, {}],
    ['expired', { exp: NOW - 31 }, {}],
    ['not yet valid', { nbf: NOW + 31 }, {}],
    ['future issued-at', { iat: NOW + 31 }, {}],
    ['lifetime over 900 seconds', { exp: NOW + 901 }, {}],
    ['missing token id', { jti: undefined }, {}],
    ['missing client id', { client_id: undefined }, {}],
    ['missing scope', { scope: undefined }, {}],
    ['wrong typ', {}, { typ: 'JWT' }],
    ['unapproved algorithm', {}, { alg: 'RS512' }],
  ];
  for (const [name, claims, header] of cases) {
    await t.test(name, async () => {
      const f = await fixture();
      await assert.rejects(
        f.verifier.verifyAuthorization(`Bearer ${await f.token(claims, header)}`, ['pulse:connect']),
        OAuthResourceError,
      );
    });
  }
});

test('accepts application/at+jwt but rejects non-canonical Authorization syntax', async () => {
  const f = await fixture();
  const token = await f.token({}, { typ: 'application/at+jwt' });
  await f.verifier.verifyAuthorization(`Bearer ${token}`, ['pulse:connect']);
  for (const header of [`Basic ${token}`, `Bearer ${token} extra`, `Bearer  ${token}`, [`Bearer ${token}`]]) {
    await assert.rejects(
      f.verifier.verifyAuthorization(header, ['pulse:connect']),
      OAuthResourceError,
    );
  }
});

test('returns a minimal insufficient_scope result without exposing token claims', async () => {
  const f = await fixture();
  const bearer = await f.token();
  await assert.rejects(
    f.verifier.verifyAuthorization(`Bearer ${bearer}`, ['pulse:write']),
    (error: unknown) => {
      assert.ok(error instanceof OAuthResourceError);
      assert.equal(error.status, 403);
      assert.equal(error.code, 'insufficient_scope');
      assert.equal(error.reasonCode, 'insufficient_scope');
      assert.deepEqual(error.requiredCapabilities, ['pulse:write']);
      assert.equal(error.message.includes(bearer), false);
      assert.equal(error.message.includes('human-subject-1'), false);
      return true;
    },
  );
});

test('classifies denials with fixed content-free security reason codes', async () => {
  const f = await fixture();
  for (const [authorization, reasonCode] of [
    [undefined, 'missing_credential'],
    ['Basic synthetic', 'malformed_credential'],
    [`Bearer ${await f.token({ exp: NOW - 31 })}`, 'expired_credential'],
    [`Bearer ${await f.token({ iss: 'https://wrong.example' })}`, 'issuer_mismatch'],
  ] as const) {
    await assert.rejects(
      f.verifier.verifyAuthorization(authorization, ['pulse:connect']),
      (error: unknown) => error instanceof OAuthResourceError && error.reasonCode === reasonCode,
    );
  }
});

test('identity strings reject surrounding whitespace/control bytes while preserving Unicode', async () => {
  for (const claims of [
    { sub: ' human-subject ' },
    { sub: 'human\u0000subject' },
    { client_id: 'client\nname' },
  ]) {
    const f = await fixture();
    await assert.rejects(
      f.verifier.verifyAuthorization(`Bearer ${await f.token(claims)}`, ['pulse:connect']),
      OAuthResourceError,
    );
  }
  const f = await fixture();
  const identity = await f.verifier.verifyAuthorization(
    `Bearer ${await f.token({ sub: 'пользователь-一', client_id: 'клиент-二' })}`,
    ['pulse:connect'],
  );
  assert.equal(identity.subject, 'пользователь-一');
  assert.equal(identity.clientId, 'клиент-二');
});

test('uses a bounded trusted cache but unknown kid during outage fails closed', async () => {
  const f = await fixture();
  await f.verifier.verifyAuthorization(`Bearer ${await f.token()}`, ['pulse:connect']);
  f.setJwksAvailable(false);
  await f.verifier.verifyAuthorization(`Bearer ${await f.token()}`, ['pulse:connect']);

  const unknown = await f.token({}, { kid: 'unknown-key' });
  await assert.rejects(
    f.verifier.verifyAuthorization(`Bearer ${unknown}`, ['pulse:connect']),
    OAuthResourceError,
  );
});

test('coalesces unknown-kid refreshes and negatively caches the missing kid', async () => {
  const f = await fixture();
  const unknown = await f.token({}, { kid: 'missing-key' });
  await Promise.all(Array.from({ length: 8 }, () => assert.rejects(
    f.verifier.verifyAuthorization(`Bearer ${unknown}`, ['pulse:connect']),
    OAuthResourceError,
  )));
  assert.equal(f.calls.filter((url) => url === `${ISSUER}/.well-known/oauth-authorization-server`).length, 1);
  assert.equal(f.calls.filter((url) => url === `${ISSUER}/jwks`).length, 2);
  await assert.rejects(
    f.verifier.verifyAuthorization(`Bearer ${unknown}`, ['pulse:connect']),
    OAuthResourceError,
  );
  assert.equal(f.calls.filter((url) => url === `${ISSUER}/jwks`).length, 2);
});

test('requires JWKS on the pinned issuer origin and rejects IPv4-mapped private DNS answers', async () => {
  const f = await fixture();
  let calls = 0;
  const crossOrigin = new OAuthResourceVerifier({
    issuer: ISSUER, resource: RESOURCE, now: () => NOW,
    resolveHost: async () => ['93.184.216.34'],
    fetch: async () => {
      calls++;
      return Response.json({
        issuer: ISSUER, jwks_uri: 'https://keys.example.com/jwks', code_challenge_methods_supported: ['S256'],
      });
    },
  });
  await assert.rejects(
    crossOrigin.verifyAuthorization(`Bearer ${await f.token()}`, ['pulse:connect']),
    OAuthResourceError,
  );
  assert.equal(calls, 1, 'cross-origin JWKS must be rejected before its fetch');

  for (const address of [
    '::ffff:127.0.0.1', '::ffff:7f00:1',
    '::127.0.0.1', '0:0:0:0:0:0:7f00:1',
    '::ffff:0:127.0.0.1', '0:0:0:0:ffff:0:7f00:1',
    'fec0::1', 'fec0:0:0:0:0:0:0:1',
    '64:ff9b:1::7f00:1', '0064:ff9b:0001:0000:0000:0000:7f00:0001',
    '100::1', '0100:0000:0000:0000:0000:0000:0000:0001',
  ]) {
    calls = 0;
    const verifier = new OAuthResourceVerifier({
      issuer: ISSUER, resource: RESOURCE, now: () => NOW,
      resolveHost: async () => [address],
      fetch: async () => {
        calls++;
        return Response.json({
          issuer: ISSUER, jwks_uri: `${ISSUER}/jwks`, code_challenge_methods_supported: ['S256'],
        });
      },
    });
    await assert.rejects(
      verifier.verifyAuthorization(`Bearer ${await f.token()}`, ['pulse:connect']),
      OAuthResourceError,
    );
    assert.equal(calls, 0, `${address} must be rejected before network fetch`);
  }

  const globalAAAA = await fixture(ISSUER, ['2606:2800:220:1:248:1893:25c8:1946']);
  const globalIdentity = await globalAAAA.verifier.verifyAuthorization(
    `Bearer ${await globalAAAA.token()}`,
    ['pulse:connect'],
  );
  assert.equal(globalIdentity.issuer, ISSUER);
});

test('rejects SSRF-sensitive JWKS endpoints before fetching them', async () => {
  const f = await fixture();
  const verifier = new OAuthResourceVerifier({
    issuer: ISSUER,
    resource: RESOURCE,
    now: () => NOW,
    resolveHost: async () => ['127.0.0.1'],
    fetch: async () => Response.json({
      issuer: ISSUER,
      jwks_uri: 'https://127.0.0.1/jwks',
      code_challenge_methods_supported: ['S256'],
    }),
  });
  await assert.rejects(
    verifier.verifyAuthorization(`Bearer ${await f.token()}`, ['pulse:connect']),
    OAuthResourceError,
  );
});
