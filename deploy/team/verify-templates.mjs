#!/usr/bin/env node

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const directory = dirname(fileURLToPath(import.meta.url));
const root = resolve(directory, '../..');

function read(name) {
  return readFileSync(join(directory, name), 'utf8');
}

function requireAll(text, patterns, label) {
  for (const pattern of patterns) assert.match(text, pattern, `${label} missing ${pattern}`);
}

function rejectAll(text, patterns, label) {
  for (const pattern of patterns) assert.doesNotMatch(text, pattern, `${label} contains forbidden ${pattern}`);
}

assert.equal(process.cwd() === root || readFileSync(join(root, 'Makefile'), 'utf8').length > 0, true);

const daemon = read('pulse-team-daemon.service');
const gateway = read('pulse-team-gateway.service');
const caddy = read('Caddyfile.example');
const environment = read('env.example');

for (const [name, unit, account] of [
  ['daemon unit', daemon, 'pulse-team-daemon'],
  ['gateway unit', gateway, 'pulse-team-gateway'],
]) {
  requireAll(unit, [
    new RegExp(`^User=${account}$`, 'm'),
    new RegExp(`^Group=${account}$`, 'm'),
    /^UMask=0077$/m,
    /^NoNewPrivileges=yes$/m,
    /^ProtectSystem=strict$/m,
    /^ConditionPathExists=\/etc\/pulse-team\/gates\/enable-synthetic-runtime$/m,
    /^ConditionPathExists=!\/etc\/pulse-team\/gates\/allow-external$/m,
    /^ConditionPathExists=!\/etc\/pulse-team\/gates\/allow-team-read$/m,
    /^ConditionPathExists=!\/etc\/pulse-team\/gates\/allow-publication$/m,
    /^ConditionPathExists=!\/etc\/pulse-team\/gates\/allow-real-content$/m,
  ], name);
  rejectAll(unit, [
    /^User=root$/m,
    /PULSE_API_KEY/,
    /PULSE_REMOTE_BEARER=\S+/,
    /0\.0\.0\.0/,
  ], name);
}

requireAll(daemon, [
  /^ExecStart=\/opt\/pulse-team\/current\/pulse .* -addr 127\.0\.0\.1:18789$/m,
  /^LoadCredential=principal-verify-keyring\.json:/m,
  /^InaccessiblePaths=-\/var\/lib\/pulse-team-gateway$/m,
]);
requireAll(gateway, [
  /^ExecStart=\/usr\/bin\/node .* --host 127\.0\.0\.1 --port 8787$/m,
  /^LoadCredential=secret\.key:/m,
  /^LoadCredential=principal-signing-key\.pem:/m,
  /^InaccessiblePaths=-\/var\/lib\/pulse-team-daemon$/m,
]);

requireAll(caddy, [
  /^\s*admin 127\.0\.0\.1:2019$/m,
  /^\s*bind 127\.0\.0\.1$/m,
  /^\s*tls internal$/m,
  /^\s*reverse_proxy 127\.0\.0\.1:8787/m,
  /EXTERNAL INGRESS IS INTENTIONALLY ABSENT/,
]);
rejectAll(caddy, [/0\.0\.0\.0/, /tls \{\$|tls [^i]/], 'Caddy template');

requireAll(environment, [
  /^PULSE_TEAM_PUBLICATION_SYNTHETIC_ONLY=1$/m,
  /^PULSE_TEAM_REMOTE_ACTIVATED=0$/m,
  /^PULSE_REMOTE_OAUTH_DEV=0$/m,
  /^PULSE_REMOTE_AUTH_PROXY_MODE=0$/m,
  /^PULSE_REMOTE_TRUST_AUTH_HEADER=0$/m,
  /^PULSE_REMOTE_ALLOW_UNAUTHENTICATED=0$/m,
  /^PULSE_ALLOW_AUTHLESS_PUBLIC=0$/m,
  /^PULSE_REMOTE_BEARER=$/m,
  /^PULSE_TEAM_SHARED_PROJECT_ID=project_SYNTHETIC_PLACEHOLDER$/m,
]);
rejectAll(environment, [
  /^PULSE_API_KEY=.+$/m,
  /-----BEGIN (?:PRIVATE KEY|OPENSSH PRIVATE KEY)-----/,
  /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/,
], 'environment template');

console.log('[pulse] Team deployment templates: static verification passed (portable; no live systemd/Caddy validation performed).');
