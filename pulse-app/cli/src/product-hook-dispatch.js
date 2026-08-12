import { runClaudeHookCLI } from './claude-hooks.js';
import { runCodexHookCLI } from './codex-hooks.js';

const MAX_HOOK_INPUT = 1 << 20;

async function readHookInput(stream) {
  const chunks = [];
  let size = 0;
  for await (const chunk of stream) {
    size += chunk.length;
    if (size > MAX_HOOK_INPUT) throw new Error('product_hook_input_too_large');
    chunks.push(chunk);
  }
  if (chunks.length === 0) throw new Error('product_hook_input_empty');
  const input = JSON.parse(Buffer.concat(chunks).toString('utf8'));
  if (!input || typeof input !== 'object' || Array.isArray(input)) {
    throw new Error('product_hook_input_invalid');
  }
  return input;
}

export function productHookHost(input) {
  const codex = typeof input?.turn_id === 'string' && input.turn_id.length > 0;
  const claude = typeof input?.prompt_id === 'string' && input.prompt_id.length > 0;
  if (codex === claude) throw new Error('product_hook_host_ambiguous');
  return codex ? 'codex' : 'claude-code';
}

export async function runProductHookCLI(eventName, dependencies = {}) {
  const input = dependencies.input ?? await readHookInput(dependencies.inputStream ?? process.stdin);
  const host = productHookHost(input);
  if (host === 'codex') {
    return (dependencies.runCodex ?? runCodexHookCLI)(eventName, { ...dependencies, input });
  }
  return (dependencies.runClaude ?? runClaudeHookCLI)(eventName, { ...dependencies, input });
}

