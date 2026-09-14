import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFile, realpath, stat } from 'node:fs/promises';
import { homedir } from 'node:os';
import { isAbsolute, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import { scopeReadScript, scopedContext } from './scope-guard.mjs';

const exec = promisify(execFile);
const source = 'Current BB project and personal memory';
const mode = 'project_memory';

async function privateFile(path, maxBytes) {
  const info = await stat(path);
  if (!info.isFile() || info.size > maxBytes || (info.mode & 0o077) !== 0 ||
      (typeof process.getuid === 'function' && info.uid !== process.getuid())) {
    throw new Error('private_file_unavailable');
  }
  return readFile(path, 'utf8');
}

export async function callPulse(input, callerSignal, lifecycleSignal) {
  const start = Date.now();
  const result = (value) => ({ source, mode, elapsedMs: Date.now() - start, ...value });
  const signals = [AbortSignal.timeout(10000), callerSignal, lifecycleSignal].filter(Boolean);
  const signal = AbortSignal.any(signals);
  try {
    signal.throwIfAborted();
    if (!isAbsolute(input.workspace) || !isAbsolute(input.nodePath) ||
        !['status', 'recall', 'remember', 'moment'].includes(input.action) ||
        (input.action === 'recall' && (!input.query?.trim() || input.query.length > 1000))) {
      return result({ status: 'unavailable', reason: 'invalid_request' });
    }
    const root = join(homedir(), '.pulse');
    const runtime = join(root, 'runtime/codex/current/src');
    const cli = join(runtime, 'cli.js');
    let workspace = await realpath(input.workspace);
    try {
      const mappings = JSON.parse(await privateFile(join(root, 'bb/workspaces.json'), 1024 * 1024));
      const mapping = mappings.find(row => row.projectId === input.projectId);
      if (mapping) {
        const info = await stat(workspace);
        if (mapping.path !== workspace || mapping.identity !== `${info.dev}:${info.ino}` ||
            mapping.canonical_path !== join(root, 'bb/projects', input.projectId)) throw new Error('project_mapping_mismatch');
        workspace = mapping.canonical_path;
      }
    } catch (error) { if (error.code !== 'ENOENT') throw error; }
    const { stdout } = await exec(input.nodePath,
      [cli, 'binding', 'resolve', '--json', '--cwd', workspace],
      { signal, timeout: 5000, maxBuffer: 32768, env: { ...process.env, PULSE_DATA_DIR: root } });
    const binding = JSON.parse(stdout);
    if (binding.mode !== 'personal' || binding.fallback !== false ||
        binding.workspace?.canonical_path !== await realpath(workspace) ||
        !/^[a-f0-9]{64}$/.test(binding.binding_digest ?? '') ||
        !Number.isSafeInteger(binding.resolver_epoch)) {
      throw new Error('binding_unavailable');
    }
    const base = new URL(binding.personal.base_url);
    if (base.protocol !== 'http:' || base.hostname !== '127.0.0.1' ||
        base.username || base.password || base.pathname !== '/' || base.search || base.hash) {
      throw new Error('loopback_required');
    }
    const key = (await privateFile(join(root, 'secret.key'), 4096)).trim();
    const headers = {
      'X-Pulse-Key': key,
      'X-Pulse-Product-Workspace': Buffer.from(binding.workspace.canonical_path).toString('base64url'),
      'X-Pulse-Product-Binding': binding.binding_digest,
      'X-Pulse-Product-Repository': binding.workspace.repository_id,
      'X-Pulse-Product-Resolver-Epoch': String(binding.resolver_epoch),
      'Content-Type': 'application/json',
    };
    const request = async (_resolved, path, options = {}) => {
      if (!['/memory/status', '/context/query', '/memory/moments'].includes(path) && !/^\/memory\/moments\/moment:[a-f0-9]{64}\?cursor=\d+&status=(true|false)$/.test(path)) throw new Error('route_not_allowed');
      const res = await fetch(new URL(path, base), {
        method: options.body ? 'POST' : 'GET', headers,
        body: options.body ? JSON.stringify(options.body) : undefined,
        // AI-SURPRISE: installed semantic recall takes ~4.5s on this vault;
        // the compositor's 2.5s budget rejects a healthy local response.
        // Recall keeps its independent deadline; writes use one exact replay.
        signal: AbortSignal.any([signal, AbortSignal.timeout(path === '/context/query' ? 6000 : options.timeoutMs ?? 4000)]),
        redirect: 'error',
      });
      if (!res.ok) {
        const detail = res.status === 400 && path === '/memory/moments' ? await res.text() : '';
        await res.body?.cancel();
        const reason = detail.includes('nothing accepted') ? 'memory_validation_nothing_accepted' :
          res.status === 409 ? 'turn_already_finalized' : 'pulse_request_unavailable';
        throw Object.assign(new Error(reason), { status: res.status });
      }
      const chunks = [];
      let size = 0;
      for await (const chunk of res.body) {
        size += chunk.length;
        if (size > 1024 * 1024) throw new Error('pulse_response_too_large');
        chunks.push(Buffer.from(chunk));
      }
      const data = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      if (path !== '/context/query') return data;
      // AI-SURPRISE: installed Pulse 0.8.3 accepts the binding but returns
      // foreign project events. Fence output before any text reaches BB.
      const dataDir = join(root, 'vaults/personal', binding.personal.store_id);
      if (!/^store_personal_[a-zA-Z0-9_]+$/.test(binding.personal.store_id) ||
          binding.personal.data_dir !== dataDir || await realpath(dataDir) !== dataDir ||
          !Array.isArray(data.events) || data.events.length > 48) throw new Error('scope_metadata_unavailable');
      const ids = data.events.map(event => event.id);
      const { stdout } = await exec(input.nodePath,
        ['--input-type=module', '-e', scopeReadScript, join(dataDir, 'pulse.db'), binding.workspace.repository_id, JSON.stringify(ids)],
        { signal, timeout: 1500, maxBuffer: 16384 });
      return scopedContext(data, JSON.parse(stdout));
    };
    const health = await request(null, '/memory/status');
    // AI-SURPRISE: a listening Pulse daemon may lack semantic retrieval; its
    // status alone is not recall proof. Never enable a configured paid backend.
    if (health.backend_llm_enabled !== false || (['status','recall'].includes(input.action) && health.full_retrieval !== true)) {
      return result({ status: 'unavailable', reason: 'local_semantic_retrieval_not_ready', fullRetrieval: false });
    }
    if (input.action === 'status') {
      const activation = JSON.parse(await privateFile(join(root, 'runtime/product-daemon.json'), 8192));
      return result({ status: 'ok', fullRetrieval: true,
        releaseVersion: activation.release_version, releaseEpoch: activation.release_epoch });
    }
    if (input.action === 'moment') {
      const q=JSON.parse(input.query);
      if (!/^moment:[a-f0-9]{64}$/.test(q.moment_id) || !Number.isSafeInteger(q.cursor??0) || (q.cursor??0)<0) throw new Error('invalid_request');
      return result({status:'ok',moment:await request(null,`/memory/moments/${q.moment_id}?cursor=${q.cursor??0}&status=${q.status===true}`)});
    }
    if (input.action === 'remember') {
      if (!input.threadId || !input.turnId || !input.timestamp || !Number.isFinite(Date.parse(input.timestamp))) throw new Error('invalid_request');
      const { memoryMomentBody, compactMomentResult, writeMemoryMoment } = await import(pathToFileURL(join(runtime,'memory-moment.js')).href);
      const identity=createHash('sha256').update(`${input.threadId}:${input.turnId}`).digest('hex');
      const body=memoryMomentBody(input.memory,{host:'pulse-cli',session_id:`bb:${input.threadId}`,turn_id:`bb:${input.turnId}`,source_event_key:`bb:${identity}`,idempotency_key:`bb:${identity}`,binding_digest:binding.binding_digest,policy_epoch:0,resolver_epoch:binding.resolver_epoch},new Date(input.timestamp));
      const receipt=await writeMemoryMoment((path,options)=>request(null,path,options),body,signal);
      const compact=compactMomentResult(receipt,body.candidates.length);
      const receipts=(receipt.receipts??[]).map(r=>({status:r.status,id:r.receipt_id,...(r.object_id?{objectId:r.object_id}:{}),...(r.reason_code?{reason:r.reason_code}:{})}));
      return result({status:compact.status,momentId:compact.moment_id,accepted:compact.accepted,stored:compact.stored,ledgerId:receipt.ledger_id,receipts});
    }
    // Reuse the installed, versioned relevance filter instead of inventing a
    // second ranking policy. Missing/incompatible runtime fails closed.
    const { composePromptMemoryContext } = await import(pathToFileURL(join(runtime, 'product-compositor.js')).href);
    const context = await composePromptMemoryContext({}, input.query.trim(), { request });
    if (Buffer.byteLength(context, 'utf8') > 2400) throw new Error('context_too_large');
    return result({ status: context ? 'ok' : 'empty', context });
  } catch (error) {
    // Error strings from child processes or HTTP bodies can contain private
    // paths/data. Return a stable diagnosis; never echo or log those strings.
    return result(classifyPulseError(error, signal.aborted));
  }
}

// Expose only fixed error codes. A pre-admission rejection permits correction;
// transport uncertainty must never imply that an accepted write was discarded.
export function classifyPulseError(error, aborted = false) {
  const validation = new Set([
    'memory_items_required_nothing_accepted', 'memory_item_invalid_nothing_accepted',
    'unsafe_memory_nothing_accepted', 'memory_emotions_required_nothing_accepted',
    'memory_emotion_invalid_nothing_accepted', 'memory_emotions_unexpected_nothing_accepted',
    'memory_validation_nothing_accepted',
  ]);
  if (validation.has(error?.message)) return {status:'rejected',reason:error.message};
  return {status:'unavailable',reason:aborted || ['TimeoutError','AbortError'].includes(error?.name)
    ? 'cancelled_or_timeout' : error?.message==='turn_already_finalized'
      ? 'turn_already_finalized' : 'binding_required_or_pulse_unavailable'};
}
