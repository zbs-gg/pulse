import { createHash } from 'node:crypto';

const CONTROL = /[\u0000-\u001f\u007f]/;
const CANONICAL_KEY = /^[A-Za-z_][A-Za-z0-9_.:-]{0,63}$/;

export interface CanonicalEnvelopeResult {
  bytes: string;
  digest: string;
}

export function canonicalizeEnvelopeJSON(
  raw: string,
  allowedTopLevel: readonly string[],
): CanonicalEnvelopeResult {
  rejectDuplicateObjectKeys(raw);
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (error) {
    throw new Error(`canonical_invalid_json:${String(error)}`);
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('canonical_root_must_be_object');
  }
  const allowed = new Set(allowedTopLevel.map((key) => key.normalize('NFC')));
  for (const key of Object.keys(parsed)) {
    if (!allowed.has(key.normalize('NFC'))) throw new Error(`canonical_unknown_field:${key}`);
  }
  const bytes = encodeCanonical(parsed);
  const digest = `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
  return { bytes, digest };
}

function encodeCanonical(value: unknown): string {
  if (value === null) return 'null';
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) throw new Error('canonical_number_not_safe_integer');
    return String(value);
  }
  if (typeof value === 'string') {
    const normalized = value.normalize('NFC');
    if (CONTROL.test(normalized)) throw new Error('canonical_control_character');
    return JSON.stringify(normalized);
  }
  if (Array.isArray(value)) return `[${value.map(encodeCanonical).join(',')}]`;
  if (typeof value === 'object') {
    const normalized = new Map<string, unknown>();
    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      const normalizedKey = key.normalize('NFC');
      if (CONTROL.test(normalizedKey)) throw new Error('canonical_control_character');
      if (!CANONICAL_KEY.test(normalizedKey)) throw new Error(`canonical_key_invalid:${normalizedKey}`);
      if (normalized.has(normalizedKey)) throw new Error(`canonical_duplicate_key:${normalizedKey}`);
      normalized.set(normalizedKey, child);
    }
    const entries = [...normalized.entries()].sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0);
    return `{${entries.map(([key, child]) => `${JSON.stringify(key)}:${encodeCanonical(child)}`).join(',')}}`;
  }
  throw new Error('canonical_value_invalid');
}

function rejectDuplicateObjectKeys(raw: string): void {
  const stack: Array<{ type: 'object'; keys: Set<string> } | { type: 'array' }> = [];
  for (let index = 0; index < raw.length; index += 1) {
    const char = raw[index];
    if (/\s/.test(char)) continue;
    if (char === '{') {
      stack.push({ type: 'object', keys: new Set() });
      continue;
    }
    if (char === '[') {
      stack.push({ type: 'array' });
      continue;
    }
    if (char === '}' || char === ']') {
      stack.pop();
      continue;
    }
    if (char !== '"') continue;
    const start = index;
    index += 1;
    let escaped = false;
    for (; index < raw.length; index += 1) {
      const current = raw[index];
      if (escaped) {
        escaped = false;
      } else if (current === '\\') {
        escaped = true;
      } else if (current === '"') {
        break;
      }
    }
    if (index >= raw.length) throw new Error('canonical_invalid_json');
    let cursor = index + 1;
    while (cursor < raw.length && /\s/.test(raw[cursor])) cursor += 1;
    const container = stack.at(-1);
    if (raw[cursor] === ':' && container?.type === 'object') {
      let key: string;
      try {
        key = JSON.parse(raw.slice(start, index + 1)).normalize('NFC');
      } catch (error) {
        throw new Error(`canonical_invalid_json:${String(error)}`);
      }
      if (!CANONICAL_KEY.test(key)) throw new Error(`canonical_key_invalid:${key}`);
      if (container.keys.has(key)) throw new Error(`canonical_duplicate_key:${key}`);
      container.keys.add(key);
    }
  }
}
