import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');

test('release verification includes Team packaging, explicit race, and portable deployment gates', () => {
  const makefile = readFileSync(join(root, 'Makefile'), 'utf8');
  assert.match(makefile, /verify:[\s\S]*test:codex-team-packaging-contract/);
  assert.match(makefile, /^team-race-release:.*\n\tcd \$\(APP_DIR\) && \$\(GO\) test -race -count=1 -timeout 20m /m);
  assert.match(makefile, /^team-deploy-static-verify:/m);
  assert.match(makefile, /^release-verify:.*team-race-release.*team-deploy-static-verify/m);

  const verifier = join(root, 'deploy', 'team', 'verify-templates.mjs');
  assert.notEqual(statSync(verifier).mode & 0o111, 0, 'deployment verifier must be executable');
  const result = spawnSync(verifier, [], { cwd: root, encoding: 'utf8' });
  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stdout, /Team deployment templates: static verification passed/);
});

test('npm publication runs the repository release gate before preparing package artifacts', () => {
  const packageJSON = JSON.parse(readFileSync(join(root, 'pulse-app', 'cli', 'package.json'), 'utf8'));
  const prepublish = packageJSON.scripts?.prepublishOnly;
  assert.equal(
    prepublish,
    'cd ../.. && make release-verify && cd pulse-app/cli && node scripts/prepare-preview-vendor.mjs',
  );
  assert.ok(prepublish.indexOf('make release-verify') < prepublish.indexOf('prepare-preview-vendor.mjs'));

  const pilot = readFileSync(join(root, 'docs', 'TEAM_REMOTE_PILOT.md'), 'utf8');
  assert.match(
    pilot,
    /`npm publish` runs `make release-verify` from the repository root before package artifacts are prepared/,
  );
});
