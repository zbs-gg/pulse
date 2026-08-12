import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('./cli.js', import.meta.url), 'utf8');

test('product connect paths recover the binding journal before resolving authority', () => {
  assert.match(source, /async function connectCodexActivation\([^)]*\) \{[\s\S]*?await recoverBindingAuthority\(\);\s+const resolved = resolveCodexMcpRuntime/);
  assert.match(source, /async function connectClaudeCodeActivation\(remoteControl\) \{[\s\S]*?await recoverBindingAuthority\(\);\s+const resolved = resolveCodexMcpRuntime/);
});

test('product doctor and auto-viewer recover the binding journal before resolving authority', () => {
  assert.match(source, /async function codexDoctorReport\([^)]*\) \{[\s\S]*?try \{\s+await recoverBindingAuthority\(\);\s+binding = resolveCodexMcpRuntime/);
  assert.match(source, /async function claudeProductDoctorReport\(\) \{[\s\S]*?try \{\s+await recoverBindingAuthority\(\);\s+binding = resolveCodexMcpRuntime/);
  assert.match(source, /async function runViewer\(rest\) \{[\s\S]*?if \(explicitDataDir === undefined && explicitBaseURL === undefined\) \{\s+try \{\s+await recoverBindingAuthority\(\);\s+product = resolveCodexMcpRuntime/);
});

test('connect and supervisor commands resolve the same product binding authority they recovered', () => {
  assert.match(source, /async function connectInstalledPersonalHost\([^)]*\) \{[\s\S]*?await recoverBindingAuthority\(\);\s+resolveProductWorkspaceBinding/);
  assert.match(source, /if \(command === 'supervisor'\) \{[\s\S]*?await recoverBindingAuthority\(\);\s+const binding = resolveProductWorkspaceBinding/);
  assert.match(source, /if \(subcommand === 'start'\) \{\s+await ensureActivatedVaultRuntime\(\{ binding, runtime \}\);\s+result = inspectVaultRuntime\(runtime\);/);
});

test('Claude connect grants project access before enabling one native plugin path', () => {
  const start = source.indexOf('async function connectClaudeCodeActivation(remoteControl) {');
  const end = source.indexOf('\nasync function ', start + 1);
  const connect = source.slice(start, end === -1 ? undefined : end);
  const access = connect.indexOf('writeProductHostAccess({');
  const plugin = connect.indexOf('activateClaudePlugin(');
  const legacyRemoval = connect.indexOf('removeLegacyClaudeProductRegistration(');
  assert.notEqual(access, -1);
  assert.notEqual(plugin, -1);
  assert.notEqual(legacyRemoval, -1);
  assert.ok(access < plugin, 'Claude plugin started before workspace access existed');
  assert.ok(plugin < legacyRemoval, 'legacy MCP was removed before the native plugin was verified');
  assert.doesNotMatch(connect, /installClaudeCode\(/);
});

test('Codex doctor reports delivery from the content-free recall activity receipt', () => {
  assert.match(source, /function codexMemoryDeliveryObserved\(runtime, binding\)/);
  assert.match(source, /activity\.hosts\?\.codex/);
  assert.match(source, /ready: codexMemoryDeliveryObserved\(runtime, binding\)/);
});

test('installed product MCP pins the verified vault before activation checks', () => {
  assert.match(source, /process\.env\.PULSE_DATA_DIR = DATA_DIR;[\s\S]*?await ensureActivatedVaultRuntime\(resolved\);[\s\S]*?process\.env\.PULSE_DATA_DIR = resolved\.runtime\.data_dir;/);
});
