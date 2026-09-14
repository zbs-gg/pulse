import { createHash, randomBytes } from 'node:crypto';
import {
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';

export const OPENCODE_PLUGIN_ENTRY = './pulse/pulse.js';
export const OPENCODE_FUN_FACT_MODES = Object.freeze(['off', 'small-model']);
const MAX_CONFIG_BYTES = 1024 * 1024;

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

export function opencodeConfigDirectory(home = homedir()) {
  return join(resolve(home), '.config', 'opencode');
}

export function opencodeConfigPaths(home = homedir()) {
  const root = opencodeConfigDirectory(home);
  return {
    root,
    json: join(root, 'opencode.json'),
    jsonc: join(root, 'opencode.jsonc'),
    loader: join(root, 'pulse', 'pulse.js'),
    pluginRoot: join(root, 'pulse'),
  };
}

function selectedConfigPath(home) {
  const paths = opencodeConfigPaths(home);
  const json = existsSync(paths.json);
  const jsonc = existsSync(paths.jsonc);
  if (json && jsonc) fail('opencode_config_conflict');
  return jsonc ? paths.jsonc : paths.json;
}

function readBoundedFile(path) {
  const info = lstatSync(path);
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size > MAX_CONFIG_BYTES) {
    fail('opencode_config_unsafe');
  }
  return readFileSync(path, 'utf8');
}

function stripJSONC(source) {
  let output = '';
  let state = 'code';
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    const next = source[index + 1];
    if (state === 'string') {
      output += char;
      if (char === '\\') {
        if (next !== undefined) output += source[++index];
      } else if (char === '"') state = 'code';
      continue;
    }
    if (state === 'line') {
      if (char === '\n' || char === '\r') {
        output += char;
        state = 'code';
      } else output += ' ';
      continue;
    }
    if (state === 'block') {
      if (char === '*' && next === '/') {
        output += '  ';
        index += 1;
        state = 'code';
      } else output += char === '\n' || char === '\r' ? char : ' ';
      continue;
    }
    if (char === '"') {
      output += char;
      state = 'string';
    } else if (char === '/' && next === '/') {
      output += '  ';
      index += 1;
      state = 'line';
    } else if (char === '/' && next === '*') {
      output += '  ';
      index += 1;
      state = 'block';
    } else output += char;
  }
  if (state === 'string' || state === 'block') fail('opencode_config_invalid');
  return output.replace(/,([\s\r\n]*[}\]])/g, '$1');
}

export function parseOpenCodeConfig(source) {
  let value;
  try { value = JSON.parse(stripJSONC(source)); } catch { fail('opencode_config_invalid'); }
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail('opencode_config_invalid');
  if (value.plugin !== undefined && !Array.isArray(value.plugin)) fail('opencode_plugin_config_invalid');
  return value;
}

function codeTokens(source) {
  const tokens = [];
  let state = 'code';
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    const next = source[index + 1];
    if (state === 'string') {
      if (char === '\\') index += 1;
      else if (char === '"') state = 'code';
      continue;
    }
    if (state === 'line') {
      if (char === '\n' || char === '\r') state = 'code';
      continue;
    }
    if (state === 'block') {
      if (char === '*' && next === '/') {
        index += 1;
        state = 'code';
      }
      continue;
    }
    if (char === '"') {
      tokens.push({ char, index });
      state = 'string';
    }
    else if (char === '/' && next === '/') {
      index += 1;
      state = 'line';
    } else if (char === '/' && next === '*') {
      index += 1;
      state = 'block';
    } else if (!/\s/.test(char)) tokens.push({ char, index });
  }
  if (state === 'string' || state === 'block') fail('opencode_config_invalid');
  return tokens;
}

function stringEnd(source, start) {
  for (let index = start + 1; index < source.length; index += 1) {
    if (source[index] === '\\') index += 1;
    else if (source[index] === '"') return index + 1;
  }
  fail('opencode_config_invalid');
}

function topLevelPluginSpan(source) {
  const tokens = codeTokens(source);
  let depth = 0;
  for (let position = 0; position < tokens.length; position += 1) {
    const token = tokens[position];
    if (token.char === '{' || token.char === '[') {
      depth += 1;
      continue;
    }
    if (token.char === '}' || token.char === ']') {
      depth -= 1;
      continue;
    }
    if (depth !== 1 || token.char !== '"') continue;
    const end = stringEnd(source, token.index);
    let key;
    try { key = JSON.parse(source.slice(token.index, end)); } catch { fail('opencode_config_invalid'); }
    if (key !== 'plugin') continue;
    const colon = tokens.find((candidate) => candidate.index >= end);
    if (!colon || colon.char !== ':') fail('opencode_config_invalid');
    const open = tokens.find((candidate) => candidate.index > colon.index);
    if (!open || open.char !== '[') fail('opencode_plugin_config_invalid');
    let arrayDepth = 0;
    for (const candidate of tokens) {
      if (candidate.index < open.index) continue;
      if (candidate.char === '[') arrayDepth += 1;
      else if (candidate.char === ']') {
        arrayDepth -= 1;
        if (arrayDepth === 0) return { start: open.index, end: candidate.index };
      }
    }
    fail('opencode_config_invalid');
  }
  return undefined;
}

function rootClosingBrace(source) {
  const tokens = codeTokens(source);
  let depth = 0;
  for (const token of tokens) {
    if (token.char === '{') depth += 1;
    else if (token.char === '}') {
      depth -= 1;
      if (depth === 0) return token.index;
    }
  }
  fail('opencode_config_invalid');
}

function lineIndent(source, index) {
  const start = source.lastIndexOf('\n', index - 1) + 1;
  return source.slice(start, index).match(/^\s*/)?.[0] ?? '';
}

function lastCodeChar(source, start, end) {
  const tokens = codeTokens(source.slice(start, end));
  return tokens.at(-1)?.char;
}

function addPluginEntry(source) {
  const config = parseOpenCodeConfig(source);
  if (config.plugin?.includes(OPENCODE_PLUGIN_ENTRY)) return source;
  const span = topLevelPluginSpan(source);
  if (span) {
    const interior = source.slice(span.start + 1, span.end);
    const multiline = interior.includes('\n');
    const last = lastCodeChar(source, span.start + 1, span.end);
    if (last === undefined) {
      return `${source.slice(0, span.start + 1)}${JSON.stringify(OPENCODE_PLUGIN_ENTRY)}${source.slice(span.end)}`;
    }
    if (!multiline) {
      return `${source.slice(0, span.end)}, ${JSON.stringify(OPENCODE_PLUGIN_ENTRY)}${source.slice(span.end)}`;
    }
    const closingIndent = lineIndent(source, span.end);
    const itemIndent = `${closingIndent}  `;
    const comma = last === ',' ? '' : ',';
    return `${source.slice(0, span.end)}${comma}\n${itemIndent}${JSON.stringify(OPENCODE_PLUGIN_ENTRY)}${source.slice(span.end)}`;
  }
  const close = rootClosingBrace(source);
  const rootInteriorLast = lastCodeChar(source, 0, close);
  const closingIndent = lineIndent(source, close);
  const itemIndent = `${closingIndent}  `;
  const comma = rootInteriorLast === '{' || rootInteriorLast === ',' ? '' : ',';
  return `${source.slice(0, close)}${comma}\n${itemIndent}"plugin": [${JSON.stringify(OPENCODE_PLUGIN_ENTRY)}]\n${closingIndent}${source.slice(close)}`;
}

function removePluginEntry(source) {
  const config = parseOpenCodeConfig(source);
  if (!config.plugin?.includes(OPENCODE_PLUGIN_ENTRY)) return source;
  const next = config.plugin.filter((entry) => entry !== OPENCODE_PLUGIN_ENTRY);
  const span = topLevelPluginSpan(source);
  if (!span) fail('opencode_plugin_config_invalid');
  const indent = lineIndent(source, span.start);
  const rendered = next.length === 0
    ? '[]'
    : `[\n${indent}  ${next.map((entry) => JSON.stringify(entry)).join(`,\n${indent}  `)}\n${indent}]`;
  return `${source.slice(0, span.start)}${rendered}${source.slice(span.end + 1)}`;
}

function atomicWrite(path, body) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${randomBytes(8).toString('hex')}.new`;
  try {
    writeFileSync(temporary, body, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

export function opencodeOptionsPath({
  productHome = join(homedir(), '.pulse'), workspaceDigest,
} = {}) {
  if (typeof workspaceDigest !== 'string' || !/^[a-f0-9]{64}$/.test(workspaceDigest)) {
    fail('opencode_options_workspace_invalid');
  }
  return join(resolve(productHome), 'product-host-options', workspaceDigest, 'opencode.json');
}

export function writeOpenCodeOptions({
  productHome = join(homedir(), '.pulse'), workspaceDigest, funFacts = 'off',
} = {}) {
  if (!OPENCODE_FUN_FACT_MODES.includes(funFacts)) fail('opencode_fun_facts_mode_invalid');
  const path = opencodeOptionsPath({ productHome, workspaceDigest });
  atomicWrite(path, `${JSON.stringify({
    schema: 'pulse.opencode_options.v1',
    workspace_digest: workspaceDigest,
    fun_facts: funFacts,
  })}\n`);
  return { path, fun_facts: funFacts };
}

export function readOpenCodeOptions({
  productHome = join(homedir(), '.pulse'), workspaceDigest,
} = {}) {
  const path = opencodeOptionsPath({ productHome, workspaceDigest });
  if (!existsSync(path)) return { path, fun_facts: 'off', configured: false };
  let value;
  try { value = JSON.parse(readBoundedFile(path)); } catch { fail('opencode_options_invalid'); }
  if (value?.schema !== 'pulse.opencode_options.v1' || value.workspace_digest !== workspaceDigest ||
      !OPENCODE_FUN_FACT_MODES.includes(value.fun_facts) ||
      Object.keys(value).sort().join('\0') !== ['fun_facts', 'schema', 'workspace_digest'].sort().join('\0')) {
    fail('opencode_options_invalid');
  }
  return { path, fun_facts: value.fun_facts, configured: true };
}

function treeDigest(root) {
  const base = resolve(root);
  const hash = createHash('sha256');
  let files = 0;
  const visit = (directory, prefix = '') => {
    const rootInfo = lstatSync(directory);
    if (!rootInfo.isDirectory() || rootInfo.isSymbolicLink()) fail('opencode_plugin_tree_unsafe');
    for (const name of readdirSync(directory).sort()) {
      const path = join(directory, name);
      const relative = prefix ? `${prefix}/${name}` : name;
      const info = lstatSync(path);
      if (info.isSymbolicLink()) fail('opencode_plugin_tree_unsafe');
      if (info.isDirectory()) visit(path, relative);
      else if (info.isFile() && info.nlink === 1) {
        const bytes = readFileSync(path);
        hash.update(relative);
        hash.update('\0');
        hash.update(bytes);
        hash.update('\0');
        files += 1;
      } else fail('opencode_plugin_tree_unsafe');
    }
  };
  visit(base);
  if (files < 1) fail('opencode_plugin_tree_unsafe');
  return hash.digest('hex');
}

function initialConfig() {
  return `${JSON.stringify({
    $schema: 'https://opencode.ai/config.json',
    plugin: [OPENCODE_PLUGIN_ENTRY],
  }, null, 2)}\n`;
}

export function previewOpenCodeIntegration({ home = homedir() } = {}) {
  const paths = opencodeConfigPaths(home);
  const configPath = selectedConfigPath(home);
  const exists = existsSync(configPath);
  const before = exists ? readBoundedFile(configPath) : '';
  const parsed = exists ? parseOpenCodeConfig(before) : {};
  const after = exists ? addPluginEntry(before) : initialConfig();
  return {
    config_path: configPath,
    loader_path: paths.loader,
    plugin_root: paths.pluginRoot,
    config_exists: exists,
    config_changed: before !== after,
    before_plugin: Array.isArray(parsed.plugin) ? parsed.plugin : [],
    after_plugin: parseOpenCodeConfig(after).plugin,
    before,
    after,
  };
}

export function inspectOpenCodeIntegration({ home = homedir(), expectedDigest } = {}) {
  let preview;
  try { preview = previewOpenCodeIntegration({ home }); } catch (error) {
    return { ready: false, reason: error.code ?? 'opencode_config_invalid' };
  }
  if (preview.config_changed) {
    return { ready: false, reason: 'opencode_plugin_registration_missing', ...preview };
  }
  if (!existsSync(preview.loader_path)) {
    return { ready: false, reason: 'opencode_loader_missing', ...preview };
  }
  try {
    const digest = treeDigest(preview.plugin_root);
    if (expectedDigest !== undefined && digest !== expectedDigest) {
      return { ready: false, reason: 'opencode_plugin_digest_mismatch', digest, ...preview };
    }
    return { ready: true, reason: 'opencode_plugin_exact', digest, ...preview };
  } catch (error) {
    return { ready: false, reason: error.code ?? 'opencode_plugin_unsafe', ...preview };
  }
}

export function installOpenCodeIntegration(sourceRoot, {
  home = homedir(), expectedDigest,
} = {}) {
  const source = resolve(sourceRoot);
  const sourceDigest = treeDigest(source);
  if (expectedDigest !== undefined && sourceDigest !== expectedDigest) fail('opencode_plugin_source_digest_mismatch');
  if (!existsSync(join(source, 'pulse.js'))) fail('opencode_loader_source_missing');
  const preview = previewOpenCodeIntegration({ home });
  const current = inspectOpenCodeIntegration({ home, expectedDigest: sourceDigest });
  if (current.ready) return { ...current, reused: true };

  const paths = opencodeConfigPaths(home);
  mkdirSync(paths.root, { recursive: true, mode: 0o700 });
  const next = join(paths.root, `.pulse-next-${process.pid}-${Date.now()}`);
  const previous = join(paths.root, `.pulse-previous-${process.pid}-${Date.now()}`);
  let movedPrevious = false;
  let configWritten = false;
  try {
    cpSync(source, next, { recursive: true, dereference: false });
    if (treeDigest(next) !== sourceDigest) fail('opencode_plugin_staged_digest_mismatch');
    if (existsSync(paths.pluginRoot)) {
      renameSync(paths.pluginRoot, previous);
      movedPrevious = true;
    }
    renameSync(next, paths.pluginRoot);
    if (preview.config_changed) {
      atomicWrite(preview.config_path, preview.after);
      configWritten = true;
    }
    const installed = inspectOpenCodeIntegration({ home, expectedDigest: sourceDigest });
    if (!installed.ready) fail(installed.reason ?? 'opencode_activation_incomplete');
    rmSync(previous, { recursive: true, force: true });
    return { ...installed, reused: false };
  } catch (error) {
    if (configWritten) {
      if (preview.config_exists) atomicWrite(preview.config_path, preview.before);
      else rmSync(preview.config_path, { force: true });
    }
    rmSync(paths.pluginRoot, { recursive: true, force: true });
    if (movedPrevious && existsSync(previous)) renameSync(previous, paths.pluginRoot);
    throw error;
  } finally {
    rmSync(next, { recursive: true, force: true });
    if (!movedPrevious || existsSync(paths.pluginRoot)) rmSync(previous, { recursive: true, force: true });
  }
}

export function removeOpenCodeIntegration({ home = homedir() } = {}) {
  const paths = opencodeConfigPaths(home);
  const configPath = selectedConfigPath(home);
  const hadConfig = existsSync(configPath);
  const before = hadConfig ? readBoundedFile(configPath) : '';
  const after = hadConfig ? removePluginEntry(before) : before;
  const backup = join(paths.root, `.pulse-remove-${process.pid}-${Date.now()}`);
  let moved = false;
  try {
    if (existsSync(paths.pluginRoot)) {
      renameSync(paths.pluginRoot, backup);
      moved = true;
    }
    if (after !== before) atomicWrite(configPath, after);
    rmSync(backup, { recursive: true, force: true });
    return { removed: moved || after !== before, config_changed: after !== before };
  } catch (error) {
    if (after !== before) atomicWrite(configPath, before);
    if (moved && existsSync(backup)) renameSync(backup, paths.pluginRoot);
    throw error;
  }
}

export const __opencodeInstallTest = Object.freeze({ addPluginEntry, removePluginEntry, stripJSONC, treeDigest });
