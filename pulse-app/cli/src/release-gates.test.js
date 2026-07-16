import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');

test('release verification includes packed Personal clean-room, interruption, physical attestation, real MLX, Team race, and portable deployment gates', () => {
  const makefile = readFileSync(join(root, 'Makefile'), 'utf8');
  assert.match(makefile, /verify:[\s\S]*test:personal-clean-room[\s\S]*test:personal-interruption[\s\S]*test:codex-team-packaging-contract/);
  assert.match(makefile, /^personal-preview-attestation:.*\n\tcd \$\(CLI_DIR\) && \$\(NPM\) run --silent attest:personal-preview/m);
  assert.match(makefile, /^team-race-release:.*\n\tcd \$\(APP_DIR\) && \$\(GO\) test -race -count=1 -timeout 20m /m);
  assert.match(makefile, /^team-deploy-static-verify:/m);
  assert.match(makefile, /^personal-real-mlx-release:.*\n\tcd \$\(CLI_DIR\) && \$\(NPM\) run --silent test:codex-product:real-mlx/m);
  assert.match(makefile, /^release-verify:.*personal-real-mlx-release.*personal-preview-attestation.*team-race-release.*team-deploy-static-verify/m);

  const packageJSON = JSON.parse(readFileSync(join(root, 'pulse-app', 'cli', 'package.json'), 'utf8'));
  assert.equal(
    packageJSON.scripts?.['test:personal-clean-room'],
    'node scripts/personal-preview-clean-room.mjs',
  );
  assert.equal(
    packageJSON.scripts?.['test:personal-interruption'],
    'node scripts/personal-preview-interruption-e2e.mjs',
  );
  assert.equal(
    packageJSON.scripts?.['attest:personal-preview'],
    'node scripts/personal-preview-release-attestation.mjs',
  );
  assert.equal(
    packageJSON.scripts?.['test:codex-product:real-mlx'],
    'PULSE_CODEX_E2E_REQUIRE_REAL_MLX=1 node scripts/codex-product-e2e.mjs',
  );
	const productE2E = readFileSync(join(root, 'pulse-app', 'cli', 'scripts', 'codex-product-e2e.mjs'), 'utf8');
	assert.match(productE2E, /PULSE_CODEX_E2E_REAL_RELEASE_MANIFEST/);
	assert.match(productE2E, /PULSE_CODEX_E2E_REAL_RELEASE_ROOT/);
	assert.match(productE2E, /verifyReleaseManifestEnvelope/);
	assert.match(productE2E, /materializeVerifiedDmg/);
	assert.match(productE2E, /quality_claimed !== true/);
	assert.doesNotMatch(productE2E, /materializeAdHocDmgForE2E|testOnlyMaterializer:\s*true/);
	for (const script of [
		'personal-preview-clean-room.mjs',
		'personal-preview-interruption-e2e.mjs',
		'personal-preview-release-attestation.mjs',
	]) {
		assert.notEqual(statSync(join(root, 'pulse-app', 'cli', 'scripts', script)).mode & 0o111, 0,
			`${script} must be executable`);
	}
	const attestation = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'personal-preview-release-attestation.mjs'), 'utf8',
	);
	assert.match(attestation, /production_install_proof/);
	assert.match(attestation, /content_free/);
	assert.match(attestation, /synthetic_authority_forbidden/);
	assert.doesNotMatch(attestation, /PULSE_RELEASE_TEST_MODE\s*=\s*['\"]1/);
	for (const relative of [
		'README.md', 'AGENTS.md', 'docs/INSTALL_WITH_AGENT.md',
		'docs/PERSONAL_PULSE_ONBOARDING.md', 'pulse-app/cli/README.md',
	]) {
		const document = readFileSync(join(root, relative), 'utf8');
		assert.match(document, /npx @zbs-gg\/pulse@preview install/,
			`${relative} must lead with the one-command Personal install`);
		assert.match(document, /Codex/);
		assert.match(document, /Memory Home/);
	}
	const onboarding = readFileSync(join(root, 'docs', 'PERSONAL_PULSE_ONBOARDING.md'), 'utf8');
	assert.match(onboarding, /does not require\s+Go, Python, Make, Docker, or a\s+model API key/i);
	assert.match(onboarding, /collecting|estimated|measured/i);
	assert.match(onboarding, /pulse repair/);
	assert.match(onboarding, /pulse disconnect codex/);
	const releaseFixture = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'product-release-fixture.mjs'), 'utf8',
	);
	const realRuntimeBranch = releaseFixture.match(
		/if \(realInputs && kind === 'embedder-runtime'\) \{([\s\S]*?)\} else if/,
	)?.[1] ?? '';
	assert.match(realRuntimeBranch, /linkSync\(realInputs\.runtimePath/);
	assert.doesNotMatch(realRuntimeBranch, /materializers\[kind\]/,
		'real MLX runtime must flow through the packed production DMG materializer');

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
