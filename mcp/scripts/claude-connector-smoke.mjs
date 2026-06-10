#!/usr/bin/env node
import { createHash, randomBytes } from 'node:crypto';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

const DEFAULT_REDIRECT_URI = 'https://claude.ai/api/mcp/auth_callback';
const DEFAULT_THREAD = 'pulse-live-connector-smoke';

function parseArgs(argv) {
  const options = {
    base: '',
    thread: DEFAULT_THREAD,
    redirectUri: DEFAULT_REDIRECT_URI,
    json: false,
  };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--json') {
      options.json = true;
      continue;
    }
    if (arg === '--base') {
      options.base = argv[++index] ?? '';
      continue;
    }
    if (arg === '--thread') {
      options.thread = argv[++index] ?? '';
      continue;
    }
    if (arg === '--redirect-uri') {
      options.redirectUri = argv[++index] ?? '';
      continue;
    }
    if (arg === '--help' || arg === '-h') {
      printHelp();
      process.exit(0);
    }
    throw new Error(`Unknown argument: ${arg}`);
  }
  if (!options.base) {
    throw new Error('--base is required');
  }
  if (!options.thread) {
    throw new Error('--thread must not be empty');
  }
  if (!options.redirectUri) {
    throw new Error('--redirect-uri must not be empty');
  }
  options.base = normalizeBase(options.base);
  return options;
}

function printHelp() {
  process.stdout.write(`Pulse Claude connector smoke

Usage:
  node scripts/claude-connector-smoke.mjs --base https://your-tunnel.example --thread pulse-live-connector-smoke

Options:
  --base <url>          Public connector origin, or the /mcp URL.
  --thread <id>         Thread id to checkpoint and resume. Defaults to ${DEFAULT_THREAD}.
  --redirect-uri <url>  OAuth callback URI. Defaults to Claude's MCP callback.
  --json                Print a machine-readable result.
`);
}

function normalizeBase(value) {
  const url = new URL(value);
  url.search = '';
  url.hash = '';
  url.pathname = url.pathname.replace(/\/mcp\/?$/, '');
  if (url.pathname === '/') {
    url.pathname = '';
  }
  return url.toString().replace(/\/$/, '');
}

function makeURL(base, path) {
  return new URL(path, `${base}/`);
}

function pkceChallenge(verifier) {
  return createHash('sha256').update(verifier).digest('base64url');
}

async function fetchJSON(url, init = {}, expectedStatuses = [200]) {
  const response = await fetch(url, init);
  if (!expectedStatuses.includes(response.status)) {
    const text = await response.text();
    throw new Error(`HTTP ${response.status} from ${url}: ${text.slice(0, 500)}`);
  }
  return response.json();
}

async function devOAuthToken(base, redirectUri) {
  await fetchJSON(makeURL(base, '/.well-known/oauth-protected-resource/mcp'));
  await fetchJSON(makeURL(base, '/.well-known/oauth-authorization-server'));

  const registration = await fetchJSON(
    makeURL(base, '/register'),
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        client_name: 'Pulse Claude connector smoke',
        redirect_uris: [redirectUri],
      }),
    },
    [200, 201],
  );
  const clientId = registration.client_id;
  if (typeof clientId !== 'string' || clientId === '') {
    throw new Error('OAuth registration did not return client_id');
  }

  const verifier = randomBytes(48).toString('base64url');
  const state = randomBytes(16).toString('base64url');
  const authorizeURL = makeURL(base, '/authorize');
  authorizeURL.searchParams.set('response_type', 'code');
  authorizeURL.searchParams.set('client_id', clientId);
  authorizeURL.searchParams.set('redirect_uri', redirectUri);
  authorizeURL.searchParams.set('code_challenge', pkceChallenge(verifier));
  authorizeURL.searchParams.set('code_challenge_method', 'S256');
  authorizeURL.searchParams.set('scope', 'pulse:read pulse:write');
  authorizeURL.searchParams.set('state', state);

  const authorization = await fetch(authorizeURL, { redirect: 'manual' });
  if (authorization.status !== 302) {
    const text = await authorization.text();
    throw new Error(`OAuth authorize returned HTTP ${authorization.status}: ${text.slice(0, 500)}`);
  }
  const callback = new URL(authorization.headers.get('Location') ?? '');
  if (callback.searchParams.get('state') !== state) {
    throw new Error('OAuth state mismatch');
  }
  const code = callback.searchParams.get('code');
  if (!code) {
    throw new Error('OAuth authorize did not return code');
  }

  const token = await fetchJSON(makeURL(base, '/token'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      grant_type: 'authorization_code',
      client_id: clientId,
      redirect_uri: redirectUri,
      code,
      code_verifier: verifier,
    }),
  });
  if (typeof token.access_token !== 'string' || token.access_token === '') {
    throw new Error('OAuth token response did not include access_token');
  }
  return token.access_token;
}

function parseToolText(result, toolName) {
  const text = result?.content?.find((part) => part?.type === 'text')?.text;
  if (typeof text !== 'string') {
    throw new Error(`${toolName} did not return text content`);
  }
  return JSON.parse(text);
}

async function runSmoke(options) {
  const accessToken = await devOAuthToken(options.base, options.redirectUri);
  const client = new Client({
    name: 'pulse-claude-connector-smoke',
    version: '0.0.0',
  });
  const transport = new StreamableHTTPClientTransport(makeURL(options.base, '/mcp'), {
    requestInit: {
      headers: {
        Authorization: `Bearer ${accessToken}`,
      },
    },
  });

  await client.connect(transport);
  try {
    const tools = await client.listTools();
    const toolNames = tools.tools.map((tool) => tool.name);
    for (const requiredTool of ['pulse_status', 'pulse_graph_delta', 'pulse_resume']) {
      if (!toolNames.includes(requiredTool)) {
        throw new Error(`MCP tool missing: ${requiredTool}`);
      }
    }

    const status = parseToolText(
      await client.callTool({ name: 'pulse_status', arguments: {} }),
      'pulse_status',
    );
    const smokeSummary = `Claude connector smoke saved graph continuity for ${options.thread}.`;
    const graph = parseToolText(
      await client.callTool({
        name: 'pulse_graph_delta',
        arguments: {
          schema: 'pulse.semantic_delta.v1',
          source: {
            host: 'claude',
            conversation_scope: 'current_turn',
            timestamp: new Date().toISOString(),
            thread_id: options.thread,
          },
          continuity: {
            summary: smokeSummary,
            decisions: ['Pulse custom connector can send host-extracted graph deltas.'],
            open_loops: ['Paste the same public connector URL into Claude custom connector UI.'],
            do_not_repeat: ['Do not send raw chat transcripts through Pulse MCP.'],
            emotional_anchors: ['Pulse keeps the thread while the host subscription does extraction.'],
            state_signals: ['backend LLM should remain disabled in the default smoke path.'],
          },
          raw_input_included: false,
        },
      }),
      'pulse_graph_delta',
    );
    const resume = parseToolText(
      await client.callTool({
        name: 'pulse_resume',
        arguments: {
          thread_id: options.thread,
          host: 'claude',
          token_budget: 1200,
        },
      }),
      'pulse_resume',
    );
    const resumeMarkdown = String(resume.resume_markdown ?? '');
    return {
      ok: true,
      base: options.base,
      tools: toolNames.length,
      hasGraphDelta: toolNames.includes('pulse_graph_delta'),
      backendLLMEnabled: status.backend_llm_enabled,
      storagePath: status.storage_path,
      checkpointSaved: graph.checkpoint_saved === true,
      resumeHasSmoke: resumeMarkdown.includes(options.thread) || resumeMarkdown.includes(smokeSummary),
      resumeTokens: resume.token_estimate ?? null,
    };
  } finally {
    await client.close();
  }
}

try {
  const options = parseArgs(process.argv.slice(2));
  const result = await runSmoke(options);
  if (options.json) {
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
  } else {
    process.stdout.write(`[pulse] Claude connector smoke OK

Base: ${result.base}
Tools: ${result.tools}
Graph delta: ${result.hasGraphDelta ? 'available' : 'missing'}
Checkpoint saved: ${result.checkpointSaved ? 'yes' : 'no'}
Resume contains smoke thread: ${result.resumeHasSmoke ? 'yes' : 'no'}
Backend LLM enabled: ${result.backendLLMEnabled ? 'yes' : 'no'}
Storage path exposed to MCP: ${result.storagePath}
Resume tokens: ${result.resumeTokens}
`);
  }
} catch (error) {
  const message = error instanceof Error ? error.message : String(error);
  process.stderr.write(`[pulse] Claude connector smoke failed: ${message}\n`);
  process.exit(1);
}
