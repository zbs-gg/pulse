#!/usr/bin/env node

import { existsSync, lstatSync, readFileSync, readdirSync } from 'node:fs';
import { basename, dirname, extname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const PUBLIC_MCP_SCRIPT_NAMES = ['build', 'start', 'dev', 'prepack', 'prepublishOnly'];
const ALLOWED_PUBLIC_KEY = 'release/pulse-release-root.pem';
const FORBIDDEN_EXTENSIONS = new Set([
  '.db', '.env', '.jsonl', '.key', '.log', '.sqlite', '.sqlite3', '.zip',
]);
const FORBIDDEN_PATH_SEGMENTS = /(?:^|\/)(?:\.pulse|docs|memories|plans|sessions)(?:\/|$)/i;
const FORBIDDEN_PRODUCT_PATHS = /(?:^|\/)(?:pulse-team(?:-server|-relay)?|team-remote|git-team-memory|cloudrun|cloud-sql|secret-manager)(?:[./_-]|\/|$)/i;
const FORBIDDEN_PRODUCT_CONTENT = [
  /(?:^|\s)pulse\s+team\s+(?:create|join|login|owner|policy|publish|pull|push|status|sync)\b/im,
  /\bPULSE_TEAM_[A-Z0-9_]+\b/,
  /\bpulse-team-(?:server|relay)\b/i,
  /\bteam-remote-client\b/i,
  /\bgit[_-]team[_-]memory\b/i,
  /\/v1\/(?:policy|admin\/policy|push|pull)(?:\b|\/)/i,
  /https:\/\/[^\s"']+\.run\.app\b/i,
  /\bCloud SQL\b/i,
  /\bSecret Manager\b/i,
];

function packageRelativePath(root, path) {
  return relative(root, path).split(sep).join('/');
}

export function publicMcpPackageManifest(sourcePackageJSON) {
  const scripts = {};
  for (const name of PUBLIC_MCP_SCRIPT_NAMES) {
    const command = sourcePackageJSON?.scripts?.[name];
    if (typeof command !== 'string' || command.trim() === '') {
      throw new Error(`source MCP package is missing required public script: ${name}`);
    }
    scripts[name] = command;
  }
  return {
    ...sourcePackageJSON,
    files: ['dist', 'src', 'README.md', 'README_DEV_PREVIEW.md', 'LICENSE'],
    scripts,
    private: true,
  };
}

function assertSafePath(relativePath, directory) {
  const lower = relativePath.toLowerCase();
  if (basename(lower) === 'agents.md' || basename(lower) === 'claude.md' ||
      /(?:private-report|provider-output|consolidation-report-sidecar)/i.test(basename(lower)) ||
      FORBIDDEN_PATH_SEGMENTS.test(lower) || FORBIDDEN_PRODUCT_PATHS.test(lower)) {
    throw new Error(`forbidden package path: ${relativePath}`);
  }
  if (!directory && FORBIDDEN_EXTENSIONS.has(extname(lower)) && relativePath !== ALLOWED_PUBLIC_KEY) {
    throw new Error(`forbidden package path: ${relativePath}`);
  }
}

function assertNoMachinePath(text, relativePath) {
  for (const match of text.matchAll(/\/Users\/([A-Za-z0-9._-]+)\//g)) {
    if (!['example', 'private', 'pulse', 'runner', 'test', 'tester'].includes(match[1].toLowerCase())) {
      throw new Error(`personal filesystem path in package file: ${relativePath}`);
    }
  }
  if (/[A-Za-z]:\\Users\\(?!example\\|test\\|tester\\)[^\\\s]+\\/i.test(text)) {
    throw new Error(`personal filesystem path in package file: ${relativePath}`);
  }
}

function assertNoSecrets(text, relativePath) {
  if (/-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/.test(text)) {
    throw new Error(`private key in package file: ${relativePath}`);
  }
  if (/\b(?:sk|rk|pk)_[A-Za-z0-9_-]{20,}\b|\bsk-[A-Za-z0-9_-]{20,}\b/.test(text) ||
      /\bghp_[A-Za-z0-9]{20,}\b|\bxoxb-[A-Za-z0-9-]{10,}\b|\bAKIA[A-Z0-9]{16}\b/i.test(text)) {
    throw new Error(`secret token in package file: ${relativePath}`);
  }
  for (const match of text.matchAll(/\b[A-Z0-9._%+-]+@([A-Z0-9.-]+\.[A-Z]{2,})\b/gi)) {
    if (!['example.invalid', 'example.test'].includes(match[1].toLowerCase())) {
      throw new Error(`email address in package file: ${relativePath}`);
    }
  }
  const quotedCredential = /\b(?:api[_-]?key|password|private[_-]?key|token)\s*[:=]\s*["'](?!<redacted>|your-example-key)[A-Za-z0-9._-]{12,}["']/i;
  const unquotedCredential = /\b(?:api[_-]?key|password|token)\s*=\s*[A-Za-z0-9_-]{16,}\b/i;
  const bearerCredential = /\bauthorization\s*:\s*Bearer\s+(?!\$\{token\})[A-Za-z0-9._-]{12,}/i;
  if (quotedCredential.test(text) || unquotedCredential.test(text) || bearerCredential.test(text)) {
    throw new Error(`credential or authorization token in package file: ${relativePath}`);
  }
}

function assertNoTeamProduct(text, relativePath) {
  if (relativePath === 'scripts/public-package-audit.mjs') return;
  // The database guard deliberately recognizes old unpublished Team table
  // names so Personal can refuse the file without mutating it.
  const compatibilityGuard = /(?:^|\/)internal\/store\/(?:store|schema)\.go$/.test(relativePath);
  for (const pattern of FORBIDDEN_PRODUCT_CONTENT) {
    if (pattern.test(text) && !(compatibilityGuard && /git[_-]team[_-]memory/i.test(pattern.source))) {
      throw new Error(`Team or cloud product content in package file: ${relativePath}`);
    }
  }
}

function assertPackageManifest(root, relativePath, text) {
  if (basename(relativePath) !== 'package.json') return;
  const manifest = JSON.parse(text);
  for (const command of Object.values(manifest.scripts ?? {})) {
    if (typeof command !== 'string') continue;
    for (const match of command.matchAll(/(?:^|\s)node\s+(scripts\/[A-Za-z0-9._/-]+)/g)) {
      if (!existsSync(join(dirname(join(root, relativePath)), match[1]))) {
        throw new Error(`advertised package script is missing: ${relativePath} -> ${match[1]}`);
      }
    }
  }
  if (relativePath === 'package.json') {
    if (manifest.name !== '@zbs-gg/pulse' || manifest.version !== '0.7.2' || manifest.private === true) {
      throw new Error('public package identity must be @zbs-gg/pulse@0.7.2');
    }
    if (Object.keys(manifest.bin ?? {}).some((name) => /team/i.test(name))) {
      throw new Error('public package exposes a Team command');
    }
  }
}

export function auditPublicPackageRoot(packageRoot) {
  const root = resolve(packageRoot);
  const stack = [root];
  let files = 0;
  let bytes = 0;
  while (stack.length > 0) {
    const directory = stack.pop();
    for (const name of readdirSync(directory).sort()) {
      const path = join(directory, name);
      const relativePath = packageRelativePath(root, path);
      const info = lstatSync(path);
      if (info.isSymbolicLink()) throw new Error(`forbidden package symlink: ${relativePath}`);
      assertSafePath(relativePath, info.isDirectory());
      if (info.isDirectory()) {
        stack.push(path);
        continue;
      }
      if (!info.isFile()) throw new Error(`forbidden package entry: ${relativePath}`);
      const content = readFileSync(path);
      const text = content.toString('utf8');
      assertNoSecrets(text, relativePath);
      assertNoMachinePath(text, relativePath);
      assertNoTeamProduct(text, relativePath);
      assertPackageManifest(root, relativePath, text);
      if (relativePath === ALLOWED_PUBLIC_KEY &&
          (!text.startsWith('-----BEGIN PUBLIC KEY-----\n') || text.includes('PRIVATE KEY'))) {
        throw new Error('release root must contain one public verification key');
      }
      files += 1;
      bytes += content.length;
    }
  }
  return { files, bytes, personal_only: true, content_free: true };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const packageRoot = process.argv[2];
  if (!packageRoot) throw new Error('usage: public-package-audit.mjs <unpacked-package-root>');
  process.stdout.write(`${JSON.stringify(auditPublicPackageRoot(packageRoot))}\n`);
}
