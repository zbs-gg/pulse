import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createServer, request as httpRequest } from 'node:http';
import { test } from 'node:test';

import { canonicalAirlockEnvelope } from './airlock-contracts.js';
import {
  AIRLOCK_HUMAN_PRESENCE_ACR,
  AIRLOCK_HUMAN_PRESENCE_SCHEMA,
  AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
} from './oauth-resource.js';
import {
  TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH,
  TEAM_PUBLICATION_AIRLOCK_PATH,
  TeamPublicationBrowserGateway,
  type AirlockDaemonProxyRequest,
} from './airlock-browser-gateway.js';

const NOW = 1_789_000_000;
const PUBLIC_ORIGIN = 'https://pulse.example.com';
const CLIENT_ID = 'owner-browser-client';

interface TestResponse {
  status: number;
  headers: Record<string, string | string[] | undefined>;
  body: Buffer;
}

function request(
  port: number,
  path: string,
  options: { method?: string; headers?: Record<string, string>; body?: string } = {},
): Promise<TestResponse> {
  return new Promise((resolve, reject) => {
    const req = httpRequest({
      hostname: '127.0.0.1',
      port,
      path,
      method: options.method ?? 'GET',
      headers: { Host: 'pulse.example.com', ...options.headers },
    }, (res) => {
      const chunks: Buffer[] = [];
      res.on('data', (chunk: Buffer) => chunks.push(chunk));
      res.once('end', () => resolve({
        status: res.statusCode ?? 0,
        headers: res.headers,
        body: Buffer.concat(chunks),
      }));
    });
    req.once('error', reject);
    req.end(options.body);
  });
}

function cookieValue(headers: TestResponse['headers'], name: string): string {
  const values = Array.isArray(headers['set-cookie'])
    ? headers['set-cookie']
    : headers['set-cookie'] ? [headers['set-cookie']] : [];
  const cookie = values.find((value) => value.startsWith(`${name}=`));
  assert.ok(cookie, `missing ${name}`);
  return cookie.slice(name.length + 1).split(';', 1)[0]!;
}

function envelope() {
  return canonicalAirlockEnvelope(JSON.stringify({
    schema: 'pulse.team.airlock_envelope.v1',
    action: 'team.commons.publish',
    deployment_id: 'deployment_synthetic',
    store_id: 'store_test',
    team_id: 'team_test',
    target_kind: 'commons',
    target_id: 'team_test',
    publication_key: 'publication_public_01',
    policy_epoch: 1,
    writer_principal_id: 'principal_publisher',
    client_key: 'a'.repeat(64),
    writer_id: 'writer_primary',
    source_timestamp: '2026-07-14T12:00:00.000Z',
    content: 'Use one exact approved rule.',
    metadata: { kind: 'decision', tags: ['synthetic'] },
  }));
}

async function fixture() {
  const random = Array.from({ length: 2048 }, (_, index) =>
    createHash('sha256').update(`airlock-test-random-${index}`).digest('base64url'));
  const verifyCalls: Array<Record<string, unknown>> = [];
  const signCalls: Array<Record<string, unknown>> = [];
  const proxyCalls: AirlockDaemonProxyRequest[] = [];
  const tokenCalls: Array<{ url: string; init?: RequestInit }> = [];
  const gateway = new TeamPublicationBrowserGateway({
    publicBaseURL: PUBLIC_ORIGIN,
    authIssuer: 'https://auth.example.com',
    authorizationEndpoint: 'https://auth.example.com/authorize',
    tokenEndpoint: 'https://auth.example.com/oauth/token',
    clientId: CLIENT_ID,
    daemonBaseURL: 'http://127.0.0.1:18789',
    now: () => NOW,
    randomToken: () => {
      const value = random.shift();
      assert.ok(value);
      return value;
    },
    fetch: async (input, init) => {
      tokenCalls.push({ url: input.toString(), init });
      return Response.json({
        token_type: 'Bearer',
        access_token: `${'a'.repeat(32)}.${'b'.repeat(32)}.${'c'.repeat(32)}`,
        id_token: `${'d'.repeat(32)}.${'e'.repeat(32)}.${'f'.repeat(32)}`,
      });
    },
    verifier: {
      async verifyBrowserAuthorization(input) {
        verifyCalls.push({ ...input });
        return {
          issuer: 'https://auth.example.com',
          subject: 'owner-subject',
          clientId: CLIENT_ID,
          capabilities: ['pulse:owner'],
          authTime: NOW,
          authenticationContext: AIRLOCK_HUMAN_PRESENCE_ACR,
          authenticationMethods: ['pwd', 'mfa'],
          humanPresence: {
            schema: AIRLOCK_HUMAN_PRESENCE_SCHEMA,
            factor: AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
            verifiedAt: NOW,
          },
          tokenExpiresAt: NOW + 300,
        };
      },
    },
    signer: {
      async signOwnerStepUp(input) {
        signCalls.push({ ...input, body: Buffer.from(input.body) });
        return `${'s'.repeat(40)}.${'t'.repeat(40)}.${'u'.repeat(40)}`;
      },
    },
    proxy: async (input) => {
      proxyCalls.push({ ...input, body: input.body ? Buffer.from(input.body) : undefined });
      if (input.method === 'GET') {
        return {
          status: 200,
          headers: {
            'content-type': 'text/html; charset=utf-8',
            'set-cookie': `__Host-pulse-airlock-csrf=${Buffer.alloc(32, 99).toString('base64url')}; Path=/; Secure; HttpOnly; SameSite=Strict`,
          },
          body: Buffer.from('<form>exact disclosure</form>'),
        };
      }
      return {
        status: 201,
        headers: { 'content-type': 'text/html; charset=utf-8' },
        body: Buffer.from('<p>published</p>'),
      };
    },
  });
  const server = createServer((req, res) => {
    void gateway.handle(req, res, new URL(req.url ?? '/', 'http://127.0.0.1'));
  });
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => resolve());
  });
  const address = server.address();
  assert.ok(address && typeof address === 'object');
  return {
    port: address.port,
    verifyCalls,
    signCalls,
    proxyCalls,
    tokenCalls,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

async function authenticate(f: Awaited<ReturnType<typeof fixture>>) {
  const start = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH);
  assert.equal(start.status, 303);
  const authorization = new URL(String(start.headers.location));
  const flow = cookieValue(start.headers, '__Host-pulse-airlock-flow');
  assert.equal(authorization.origin + authorization.pathname, 'https://auth.example.com/authorize');
  assert.equal(authorization.searchParams.get('response_type'), 'code');
  assert.equal(authorization.searchParams.get('client_id'), CLIENT_ID);
  assert.equal(authorization.searchParams.get('redirect_uri'), `${PUBLIC_ORIGIN}${TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH}`);
  assert.equal(authorization.searchParams.get('scope'), 'openid pulse:owner');
  assert.equal(authorization.searchParams.get('audience'), `${PUBLIC_ORIGIN}/mcp`);
  assert.equal(authorization.searchParams.get('code_challenge_method'), 'S256');
  assert.equal(authorization.searchParams.get('prompt'), 'login');
  assert.equal(authorization.searchParams.get('max_age'), '0');
  assert.equal(authorization.searchParams.get('acr_values'), AIRLOCK_HUMAN_PRESENCE_ACR);
  assert.match(String(start.headers['set-cookie']), /Secure; HttpOnly; SameSite=Lax/);

  const callback = await request(
    f.port,
    `${TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH}?code=${'code'.repeat(8)}&state=${authorization.searchParams.get('state')}`,
    { headers: { Cookie: `__Host-pulse-airlock-flow=${flow}` } },
  );
  assert.equal(callback.status, 303);
  assert.equal(callback.headers.location, `${PUBLIC_ORIGIN}${TEAM_PUBLICATION_AIRLOCK_PATH}`);
  const session = cookieValue(callback.headers, '__Host-pulse-airlock-session');
  const cookies = Array.isArray(callback.headers['set-cookie'])
    ? callback.headers['set-cookie']
    : [String(callback.headers['set-cookie'])];
  assert.ok(cookies.some((value) => /__Host-pulse-airlock-session=.*Max-Age=300; Secure; HttpOnly; SameSite=Strict/.test(value)));
  assert.equal(f.verifyCalls.length, 1);
  assert.equal(f.verifyCalls[0]!.nonce, authorization.searchParams.get('nonce'));
  assert.equal(f.verifyCalls[0]!.authorizationStartedAt, NOW);
  assert.equal(f.verifyCalls[0]!.accessToken, `${'a'.repeat(32)}.${'b'.repeat(32)}.${'c'.repeat(32)}`);
  assert.equal(f.tokenCalls.length, 1);
  const exchange = new URLSearchParams(String(f.tokenCalls[0]!.init?.body));
  assert.equal(exchange.get('grant_type'), 'authorization_code');
  assert.equal(exchange.get('code'), 'code'.repeat(8));
  assert.equal(exchange.get('redirect_uri'), `${PUBLIC_ORIGIN}${TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH}`);
  assert.equal(exchange.get('client_id'), CLIENT_ID);
  assert.match(exchange.get('code_verifier') ?? '', /^[A-Za-z0-9_-]{43}$/);
  return { session, flow, authorization };
}

test('Owner browser uses one-use OIDC PKCE state and never receives token or assertion material', async () => {
  const f = await fixture();
  try {
    const auth = await authenticate(f);
    const replay = await request(
      f.port,
      `${TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH}?code=${'code'.repeat(8)}&state=${auth.authorization.searchParams.get('state')}`,
      { headers: { Cookie: `__Host-pulse-airlock-flow=${auth.flow}` } },
    );
    assert.equal(replay.status, 401);
    assert.equal(f.tokenCalls.length, 1);
    assert.doesNotMatch(replay.body.toString(), /codecode|owner-subject|access_token|id_token/i);
  } finally {
    await f.close();
  }
});

test('authenticated preview proxies only to the exact Go Airlock and forwards only its CSRF cookie', async () => {
  const f = await fixture();
  try {
    const { session } = await authenticate(f);
    const preview = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
      headers: { Cookie: `__Host-pulse-airlock-session=${session}; unrelated=private` },
    });
    assert.equal(preview.status, 200);
    assert.equal(preview.body.toString(), '<form>exact disclosure</form>');
    assert.equal(f.proxyCalls.length, 1);
    assert.deepEqual(f.proxyCalls[0], {
      method: 'GET',
      path: TEAM_PUBLICATION_AIRLOCK_PATH,
      headers: { Host: 'pulse.example.com' },
      body: undefined,
    });
    assert.match(String(preview.headers['set-cookie']), /^__Host-pulse-airlock-csrf=/);
    assert.doesNotMatch(String(preview.headers['set-cookie']), /pulse-airlock-session/);
  } finally {
    await f.close();
  }
});

test('exact form is signed server-side, assertion is header-only, and the session is single-use', async () => {
  const f = await fixture();
  try {
    const { session } = await authenticate(f);
    const csrf = Buffer.alloc(32, 99).toString('base64url');
    const canonical = envelope();
    const form = new URLSearchParams({
      csrf_token: csrf,
      decision: 'approve',
      canonical_envelope: Buffer.from(canonical.bytes).toString('base64url'),
      envelope_digest: canonical.envelopeDigest,
      store_id: canonical.value.store_id,
      team_id: canonical.value.team_id,
      publisher_principal_id: canonical.value.writer_principal_id,
      request_id: 'airlock-request-0001',
    }).toString();
    const headers = {
      Cookie: `__Host-pulse-airlock-session=${session}; __Host-pulse-airlock-csrf=${csrf}`,
      Origin: PUBLIC_ORIGIN,
      'Sec-Fetch-Site': 'same-origin',
      'Content-Type': 'application/x-www-form-urlencoded',
      'Content-Length': String(Buffer.byteLength(form)),
    };
    const approved = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
      method: 'POST', headers, body: form,
    });
    assert.equal(approved.status, 201);
    assert.equal(f.signCalls.length, 1);
    assert.deepEqual(f.signCalls[0], {
      requestId: 'airlock-request-0001',
      path: TEAM_PUBLICATION_AIRLOCK_PATH,
      action: 'team.commons.publish',
      body: Buffer.from(canonical.bytes),
      storeId: 'store_test',
      teamId: 'team_test',
      oauthIssuer: 'https://auth.example.com',
      oauthSubject: 'owner-subject',
      oauthClientId: CLIENT_ID,
      authTime: NOW,
    });
    assert.equal(f.proxyCalls.length, 1);
    assert.equal(Buffer.from(f.proxyCalls[0]!.body!).toString(), form);
    assert.equal(Object.hasOwn(Object.fromEntries(new URLSearchParams(form)), 'step_up_assertion'), false);
    assert.match(f.proxyCalls[0]!.headers['X-Pulse-Owner-Step-Up'] ?? '', /^s{40}\./);
    assert.doesNotMatch(approved.body.toString(), /s{40}|owner-subject|step_up/i);

    const replay = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
      method: 'POST', headers, body: form,
    });
    assert.equal(replay.status, 401);
    assert.equal(f.signCalls.length, 1);
    assert.equal(f.proxyCalls.length, 1);
  } finally {
    await f.close();
  }
});

test('changed disclosure binding and cross-origin mutation are rejected before signing or proxying', async () => {
  const f = await fixture();
  try {
    const { session } = await authenticate(f);
    const csrf = Buffer.alloc(32, 99).toString('base64url');
    const canonical = envelope();
    const form = new URLSearchParams({
      csrf_token: csrf,
      decision: 'approve',
      canonical_envelope: Buffer.from(canonical.bytes).toString('base64url'),
      envelope_digest: canonical.envelopeDigest,
      store_id: 'store_changed',
      team_id: canonical.value.team_id,
      publisher_principal_id: canonical.value.writer_principal_id,
      request_id: 'airlock-request-0002',
    }).toString();
    const rejected = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
      method: 'POST',
      headers: {
        Cookie: `__Host-pulse-airlock-session=${session}; __Host-pulse-airlock-csrf=${csrf}`,
        Origin: 'https://attacker.example',
        'Content-Type': 'application/x-www-form-urlencoded',
        'Content-Length': String(Buffer.byteLength(form)),
      },
      body: form,
    });
    assert.equal(rejected.status, 403);
    assert.equal(f.signCalls.length, 0);
    assert.equal(f.proxyCalls.length, 0);
    assert.doesNotMatch(rejected.body.toString(), /store_changed|owner-subject|canonical_envelope/);
  } finally {
    await f.close();
  }
});

test('unauthenticated flow flood cannot globally block a new Owner authorization', async () => {
  const f = await fixture();
  try {
    let evicted: { flow: string; state: string } | undefined;
    for (let source = 0; source < 32; source++) {
      for (let attempt = 0; attempt < 8; attempt++) {
        const started = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
          headers: { 'User-Agent': `synthetic-attacker-${source}` },
        });
        assert.equal(started.status, 303);
        if (!evicted) {
          evicted = {
            flow: cookieValue(started.headers, '__Host-pulse-airlock-flow'),
            state: new URL(String(started.headers.location)).searchParams.get('state')!,
          };
        }
      }
    }
    const owner = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
      headers: { 'User-Agent': 'owner-browser' },
    });
    assert.equal(owner.status, 303);
    assert.ok(evicted);
    const oldCallback = await request(
      f.port,
      `${TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH}?code=${'code'.repeat(8)}&state=${evicted.state}`,
      { headers: { Cookie: `__Host-pulse-airlock-flow=${evicted.flow}` } },
    );
    assert.equal(oldCallback.status, 401, 'oldest flood flow must be fairly evicted');
  } finally {
    await f.close();
  }
});

test('flow-start rate limit is enforced before allocating more authorization state', async () => {
  const f = await fixture();
  try {
    for (let attempt = 0; attempt < 32; attempt++) {
      const started = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
        headers: { 'User-Agent': 'single-flood-source' },
      });
      assert.equal(started.status, 303);
    }
    const limited = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
      headers: { 'User-Agent': 'single-flood-source' },
    });
    assert.equal(limited.status, 429);
    assert.equal(limited.headers['retry-after'], '60');

    const owner = await request(f.port, TEAM_PUBLICATION_AIRLOCK_PATH, {
      headers: { 'User-Agent': 'owner-browser-after-rate-limit' },
    });
    assert.equal(owner.status, 303);
  } finally {
    await f.close();
  }
});
