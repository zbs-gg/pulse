import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('./cli.js', import.meta.url), 'utf8');

test('product connect paths recover the binding journal before resolving authority', () => {
  assert.match(source, /async function connectCodexActivation\(\) \{[\s\S]*?await recoverBindingAuthority\(\);\s+const resolved = resolveCodexMcpRuntime/);
  assert.match(source, /async function connectClaudeCodeActivation\(remoteControl\) \{[\s\S]*?await recoverBindingAuthority\(\);\s+const resolved = resolveCodexMcpRuntime/);
});

test('product doctor and auto-viewer recover the binding journal before resolving authority', () => {
  assert.match(source, /async function codexDoctorReport\(\) \{[\s\S]*?try \{\s+await recoverBindingAuthority\(\);\s+binding = resolveCodexMcpRuntime/);
  assert.match(source, /async function claudeProductDoctorReport\(\) \{[\s\S]*?try \{\s+await recoverBindingAuthority\(\);\s+binding = resolveCodexMcpRuntime/);
  assert.match(source, /async function runViewer\(rest\) \{[\s\S]*?if \(explicitDataDir === undefined && explicitBaseURL === undefined\) \{\s+try \{\s+await recoverBindingAuthority\(\);\s+product = resolveCodexMcpRuntime/);
});
