#!/usr/bin/env node
/**
 * Published-tarball NEGATIVE smoke for the Safe Mode standalone store.
 *
 * WHY: a happy-path smoke alone cannot catch a dangerous payload that is
 * wrongly accepted. This script is the
 * regression guard on the SHIPPED ARTIFACT: it builds the package, imports the
 * BUILT StandaloneStore from dist/ (not src/), and fires a battery of
 * dangerous / out-of-contract payloads at remember() and graphDelta(). Every
 * one of them MUST be rejected (throw) or — for "should not persist" cases —
 * leave no trace in the on-disk store.json.
 *
 * Exit 0  => every dangerous payload was rejected / not persisted. Safe.
 * Exit 1  => at least one dangerous payload was accepted or persisted. RED.
 *
 * Keep in sync with mcp/src/validation.ts and the Go daemon validators. If a
 * validation check is removed or weakened, this smoke must go RED.
 */
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const mcpRoot = join(here, '..');
const distStore = join(mcpRoot, 'dist', 'standalone.js');

// 1) Build the package so we test the SHIPPED artifact, not src/.
console.log('[negative-smoke] building package (npm run build)...');
execFileSync('npm', ['run', 'build'], { cwd: mcpRoot, stdio: 'inherit' });

// 2) Import the BUILT StandaloneStore.
const { StandaloneStore } = await import(pathToFileURL(distStore).href);
if (typeof StandaloneStore !== 'function') {
  console.error('[negative-smoke] FATAL: dist/standalone.js does not export StandaloneStore');
  process.exit(1);
}

// --- Valid baselines (deep-cloned + mutated per case so a single bad field is
// the ONLY thing wrong; everything else is contract-valid). ---
function baseCapsule() {
  return {
    schema: 'pulse.memory_capsule.v1',
    source: {
      host: 'claude-code',
      conversation_scope: 'current_turn',
      timestamp: '2026-06-10T10:00:00Z',
    },
    items: [
      {
        kind: 'decision',
        redacted_summary: 'Shipped the standalone lite engine for zero-config installs.',
        confidence: 0.9,
        evidence_hint: 'current_turn',
        privacy_tier: 'normal',
        retention: 'project',
        tags: ['standalone-smoke'],
      },
    ],
    raw_input_included: false,
  };
}

function baseDelta() {
  return {
    schema: 'pulse.semantic_delta.v1',
    source: {
      host: 'claude-code',
      conversation_scope: 'current_turn',
      timestamp: '2026-06-10T10:00:00Z',
      thread_id: 'negative-smoke',
    },
    nodes: [
      {
        client_id: 'project:pulse',
        kind: 'project',
        canonical_name: 'Pulse',
        summary: 'Local-first state-aware memory engine.',
        privacy_tier: 'normal',
      },
    ],
    raw_input_included: false,
  };
}

const clone = (v) => JSON.parse(JSON.stringify(v));

// A fresh store per case keeps the on-disk persistence assertions isolated.
function freshStore() {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-neg-smoke-'));
  return { store: new StandaloneStore(dir), dir };
}

/**
 * A dangerous case is SAFE only if the call throws. If it returns, the payload
 * was accepted — RED. We also re-read store.json to confirm nothing leaked to
 * disk even if a future bug both throws AND persists (defense in depth).
 */
function expectRejected(name, build, op) {
  const { store, dir } = freshStore();
  let accepted = false;
  let acceptedDetail = '';
  try {
    const payload = build();
    const result = op === 'remember' ? store.remember(payload) : store.graphDelta(payload);
    accepted = true;
    acceptedDetail = JSON.stringify(result);
  } catch (err) {
    // expected: rejected by the content contract.
  }
  let persisted = false;
  try {
    const raw = readFileSync(store.path(), 'utf8');
    // If the store file exists with any items/graph content, something leaked.
    const parsed = JSON.parse(raw);
    const itemCount = Array.isArray(parsed.items) ? parsed.items.length : 0;
    const g = parsed.graph ?? {};
    const graphCount =
      (Array.isArray(g.nodes) ? g.nodes.length : 0) +
      (Array.isArray(g.edges) ? g.edges.length : 0) +
      (Array.isArray(g.facts) ? g.facts.length : 0) +
      (Array.isArray(g.events) ? g.events.length : 0);
    const cpCount = Array.isArray(parsed.checkpoints) ? parsed.checkpoints.length : 0;
    persisted = itemCount + graphCount + cpCount > 0;
  } catch {
    // store.json may not exist (loadUnlocked creates an empty store on read;
    // here we never read, so absence == nothing persisted). Fine.
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  return { name, accepted, acceptedDetail, persisted };
}

const cases = [];

// --- remember() dangerous payloads ---
cases.push(
  expectRejected(
    'secret token in redacted_summary',
    () => {
      const c = clone(baseCapsule());
      c.items[0].redacted_summary = 'Use token=sk-live-ABCDEF1234567890 to authenticate the agent.';
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'absolute /Users path in redacted_summary',
    () => {
      const c = clone(baseCapsule());
      c.items[0].redacted_summary = 'Config lives at /Users/victim/.pulse/secret.key on the host.';
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'transcript-like content in redacted_summary',
    () => {
      const c = clone(baseCapsule());
      c.items[0].redacted_summary =
        'User: hi there\nAssistant: hello\nUser: do x\nAssistant: ok\nUser: and y\nAssistant: done';
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'bad host enum',
    () => {
      const c = clone(baseCapsule());
      c.source.host = 'evil-host-not-in-allowlist';
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'bad kind enum',
    () => {
      const c = clone(baseCapsule());
      c.items[0].kind = 'arbitrary_kind';
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'bad privacy_tier enum',
    () => {
      const c = clone(baseCapsule());
      c.items[0].privacy_tier = 'top-secret';
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'bad retention enum',
    () => {
      const c = clone(baseCapsule());
      c.items[0].retention = 'forever';
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'confidence 99 (out of 0..1)',
    () => {
      const c = clone(baseCapsule());
      c.items[0].confidence = 99;
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'more than 20 items',
    () => {
      const c = clone(baseCapsule());
      const one = c.items[0];
      c.items = Array.from({ length: 21 }, (_, i) => ({
        ...clone(one),
        redacted_summary: `${one.redacted_summary} #${i}`,
      }));
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'secret marker inside a tag',
    () => {
      const c = clone(baseCapsule());
      c.items[0].tags = ['safe', 'ghp_0123456789abcdef0123456789abcdef0123'];
      return c;
    },
    'remember',
  ),
);
cases.push(
  expectRejected(
    'raw_input_included true (raw capture flag)',
    () => {
      const c = clone(baseCapsule());
      c.raw_input_included = true;
      return c;
    },
    'remember',
  ),
);

// --- graphDelta() dangerous payloads ---
cases.push(
  expectRejected(
    'secret in a graph node summary',
    () => {
      const d = clone(baseDelta());
      d.nodes[0].summary = 'Deploy key sk-prod-DEADBEEFCAFE0987 is stored in the node summary.';
      return d;
    },
    'graphDelta',
  ),
);
cases.push(
  expectRejected(
    'absolute path in node canonical_name',
    () => {
      const d = clone(baseDelta());
      d.nodes[0].canonical_name = '/Users/victim/projects/secret';
      return d;
    },
    'graphDelta',
  ),
);
cases.push(
  expectRejected(
    'more than 30 nodes',
    () => {
      const d = clone(baseDelta());
      const one = d.nodes[0];
      d.nodes = Array.from({ length: 31 }, (_, i) => ({
        ...clone(one),
        client_id: `project:pulse-${i}`,
        canonical_name: `Pulse ${i}`,
      }));
      return d;
    },
    'graphDelta',
  ),
);

// Arbitrary unknown node field must NOT be persisted. validateDelta whitelists
// fields, so even when the delta is otherwise valid the unknown field is
// dropped. This case asserts the field never reaches disk.
{
  const { store, dir } = freshStore();
  let leaked = false;
  let threw = false;
  try {
    const d = clone(baseDelta());
    d.nodes[0].malicious_field = '/Users/victim/.ssh/id_rsa contents here';
    d.nodes[0].exfil = 'token=sk-leak-me';
    store.graphDelta(d);
    const parsed = JSON.parse(readFileSync(store.path(), 'utf8'));
    const blob = JSON.stringify(parsed);
    leaked = blob.includes('malicious_field') || blob.includes('exfil') || blob.includes('id_rsa') || blob.includes('sk-leak-me');
  } catch {
    threw = true;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  cases.push({
    name: 'arbitrary unknown node field not persisted',
    // For this case "accepted" is fine (valid delta); the failure is leakage.
    accepted: false,
    acceptedDetail: '',
    persisted: leaked,
    note: threw ? '(threw — also acceptable)' : '',
  });
}

// --- Report ---
let red = false;
console.log('\n[negative-smoke] results:');
for (const c of cases) {
  const bad = c.accepted || c.persisted;
  if (bad) red = true;
  const status = bad ? 'ACCEPTED/PERSISTED (BAD)' : 'rejected (ok)';
  const extra = c.accepted && c.acceptedDetail ? ` <- returned ${c.acceptedDetail}` : '';
  const persistedNote = c.persisted ? ' <- LEAKED TO DISK' : '';
  console.log(`  [${bad ? 'X' : ' '}] ${c.name}: ${status}${extra}${persistedNote}${c.note ? ' ' + c.note : ''}`);
}

if (red) {
  console.error(
    '\n[negative-smoke] FAILED: a dangerous payload was accepted or persisted by the BUILT standalone store. The shipped Safe Mode store would store secrets/paths/transcripts/out-of-contract content. This is a trust-contract regression.',
  );
  process.exit(1);
}

console.log(`\n[negative-smoke] PASS: all ${cases.length} dangerous payloads were rejected / not persisted by the built artifact.`);
process.exit(0);
