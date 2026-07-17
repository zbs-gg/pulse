import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { generateKeyPairSync, sign } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, realpathSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';

import { bindingRegistryAnchor, canonicalJSONStringify } from './workspace-binding.js';
import { readUnassignedInbox, unassignedInboxPath } from './unassigned-inbox.js';

const cliPath = resolve(import.meta.dirname, 'cli.js');

function git(cwd, ...args) {
  const result = spawnSync('/usr/bin/git', args, { cwd, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
}

function trustFixture(root) {
  const trust = join(root, 'trust');
  mkdirSync(trust, { recursive: true, mode: 0o700 });
  const registryPath = join(trust, 'workspace-bindings.json');
  const publicKeyPath = join(trust, 'workspace-bindings.pub.pem');
  const anchorPath = join(trust, 'workspace-bindings.anchor.json');
  const payload = { schema: 'pulse.workspace-binding-registry.v1', epoch: 1, bindings: [] };
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const signature = sign(null, Buffer.from(canonicalJSONStringify(payload)), privateKey).toString('base64');
  const registryBytes = Buffer.from(`${JSON.stringify({ algorithm: 'ed25519', payload, signature })}\n`);
  writeFileSync(registryPath, registryBytes, { mode: 0o600 });
  writeFileSync(publicKeyPath, publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  writeFileSync(anchorPath, `${canonicalJSONStringify(bindingRegistryAnchor(registryBytes, payload.epoch))}\n`, { mode: 0o600 });
  for (const path of [registryPath, publicKeyPath, anchorPath]) chmodSync(path, 0o600);
  return { registryPath, publicKeyPath, anchorPath };
}

function writeMcpFixture(root) {
  const path = join(root, 'mcp-entrypoint.mjs');
  writeFileSync(path, `
export async function runMcpEntrypoint() {
  let input = '';
  for await (const chunk of process.stdin) input += chunk;
  const runtime = await import(process.env.PULSE_HOST_RUNTIME_MODULE);
  for (const line of input.trim().split(/\\r?\\n/)) {
    const message = JSON.parse(line);
    if (message.method === 'initialize') {
      process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:message.id,result:{serverInfo:{name:'pulse-mcp-fixture',version:'1'},protocolVersion:'2024-11-05',capabilities:{}}})+'\\n');
    } else if (message.method === 'tools/list') {
      process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:message.id,result:{tools:[{name:'pulse_remember'}],unassigned:process.env.PULSE_PRODUCT_UNASSIGNED,cwd:process.cwd(),binding:process.env.PULSE_BINDING_DIGEST||''}})+'\\n');
    } else if (message.method === 'tools/call') {
      const receipt = runtime.stageUnassignedProductCandidate(process.env.PULSE_HOST_ADAPTER, message.params.arguments, 'mcp_fixture_request');
      process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:message.id,result:{content:[{type:'text',text:JSON.stringify(receipt)}]}})+'\\n');
    }
  }
}
`, { mode: 0o600 });
  return path;
}

for (const [command, host, gitWorkspace, expectedReason] of [
  ['claude-mcp', 'claude-code', true, 'binding_missing'],
  ['cursor-mcp', 'cursor', true, 'binding_missing'],
  ['codex-mcp', 'codex', true, 'binding_missing'],
  ['codex-mcp', 'codex', false, 'workspace_not_git'],
]) {
test(`product ${host} bootstrap keeps ${expectedReason} in the private non-retrievable Inbox`, () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-unassigned-product-mcp.'));
  const home = join(root, 'home');
  const workspace = join(root, 'workspace');
  mkdirSync(home, { mode: 0o700 });
  mkdirSync(workspace, { mode: 0o700 });
  if (gitWorkspace) {
    git(workspace, 'init', '-q');
    git(workspace, 'config', 'user.email', 'pulse-tests@example.test');
    git(workspace, 'config', 'user.name', 'Pulse Tests');
    git(workspace, 'commit', '--allow-empty', '-q', '-m', 'fixture');
  }
  const trust = trustFixture(root);
  const entrypoint = writeMcpFixture(root);
  const capsule = {
    schema: 'pulse.memory_capsule.v1',
    source: { host, conversation_scope: 'current_turn', timestamp: '2026-07-17T10:00:00Z' },
    items: [{
      kind: 'decision', redacted_summary: 'Keep this in Inbox until the exact project is chosen.', confidence: 1,
      evidence_hint: 'current_turn', privacy_tier: 'normal', retention: 'project',
    }],
    raw_input_included: false,
  };
  const input = [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'pulse_remember', arguments: capsule } },
  ].map((message) => JSON.stringify(message)).join('\n') + '\n';
  const result = spawnSync(process.execPath, [cliPath, command], {
    cwd: workspace,
    env: {
      ...process.env, HOME: home, PULSE_TRUST_MODE: 'test', PULSE_MCP_ENTRYPOINT: entrypoint,
      PULSE_BINDING_REGISTRY_PATH: trust.registryPath,
      PULSE_BINDING_PUBLIC_KEY_PATH: trust.publicKeyPath,
      PULSE_BINDING_ANCHOR_PATH: trust.anchorPath,
    },
    input, encoding: 'utf8', timeout: 15_000,
  });
  assert.equal(result.status, 0, result.stderr);
  const messages = result.stdout.trim().split(/\r?\n/).map((line) => JSON.parse(line));
  const tools = messages.find((message) => message.id === 2).result;
  assert.equal(tools.unassigned, expectedReason);
  assert.equal(tools.binding, '');
  assert.equal(tools.cwd, realpathSync(workspace));
  const receipt = JSON.parse(messages.find((message) => message.id === 3).result.content[0].text);
  assert.equal(receipt.destination, 'unassigned_inbox');

  const inbox = readUnassignedInbox(unassignedInboxPath(home));
  assert.equal(inbox.items.length, 1);
  assert.equal(inbox.items[0].host, host);
  assert.equal(inbox.items[0].candidate.capsule.items[0].redacted_summary,
    'Keep this in Inbox until the exact project is chosen.');
});
}
