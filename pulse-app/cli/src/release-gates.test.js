import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');

test('published package includes every production CLI module', () => {
  const cliRoot = join(root, 'pulse-app', 'cli');
  const packageJSON = JSON.parse(readFileSync(join(cliRoot, 'package.json'), 'utf8'));
  const productionModules = readdirSync(join(cliRoot, 'src'))
    .filter((name) => name.endsWith('.js') && !name.endsWith('.test.js'))
    .map((name) => `src/${name}`)
    .sort();
  assert.deepEqual(productionModules.filter((path) => !packageJSON.files.includes(path)), []);
});

test('release verification includes packed Personal clean-room, interruption, physical attestation, real MLX, Team race, and portable deployment gates', () => {
  const makefile = readFileSync(join(root, 'Makefile'), 'utf8');
  assert.match(makefile, /verify:[\s\S]*test:personal-clean-room[\s\S]*test:personal-interruption[\s\S]*test:personal-multiharness[\s\S]*test:codex-team-packaging-contract/);
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
    packageJSON.scripts?.['test:personal-multiharness'],
    'node scripts/personal-preview-multiharness-e2e.mjs',
  );
  assert.equal(
    packageJSON.scripts?.['attest:personal-preview'],
    'node scripts/personal-preview-release-attestation.mjs',
  );
  assert.ok(packageJSON.files?.includes('src/release-attestation.js'));
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
	assert.match(productE2E, /PULSE_PERSONAL_PACKED_TARBALL/);
	assert.match(productE2E, /packed_tarball_sha256/);
	assert.match(productE2E, /package_version/);
	const cli = readFileSync(join(root, 'pulse-app', 'cli', 'src', 'cli.js'), 'utf8');
	assert.match(cli, /PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION/);
	assert.match(cli, /core activation detail/);
	assert.match(cli, /core activation stage/);
	assert.match(cli, /daemon_start_started/);
	assert.match(cli, /transaction_complete/);
	assert.match(cli, /<windows-path>/);
	assert.match(cli, /defaultPlatformServices\.inspectExecutable\(resolve\(managedRuntime\.daemon\.path\)\)/);
	assert.match(cli, /executableProof\.sha256 !== managedRuntime\.daemon\.digest/);
	const nativePacked = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'personal-native-packed-e2e.mjs'), 'utf8',
	);
	assert.match(nativePacked, /process\.platform === 'win32' \? 180_000 : 15 \* 60_000/);
	assert.match(nativePacked, /taskkill\.exe/);
	assert.match(nativePacked, /\['\/PID', String\(child\.pid\), '\/T', '\/F'\]/);
	assert.match(nativePacked, /await packedPulse\(tarball, \['install', '--json'\]/);
	for (const script of [
		'personal-preview-clean-room.mjs',
		'personal-preview-interruption-e2e.mjs',
		'personal-preview-multiharness-e2e.mjs',
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
	assert.match(attestation, /PULSE_PERSONAL_ATTEST_TARBALL/);
	assert.match(attestation, /PULSE_PERSONAL_ATTEST_HOST/);
	assert.match(attestation, /PULSE_PERSONAL_ATTEST_BETA_WORKSPACE/);
	assert.match(attestation, /packed_tarball_sha256/);
	assert.match(attestation, /exact_tarball_install_executed/);
	assert.match(attestation, /runPackedPulse\(tarball, \['doctor', host, '--json'\]/);
	assert.match(attestation, /runPackedPulse\(tarball, \['home', '--host', host\]/);
	assert.match(attestation, /pulse\.personal_preview_release_attestation\.v2/);
	assert.doesNotMatch(attestation, /native_hook_trusted:\s*host\s*===\s*'codex'\s*\?\s*true\s*:\s*null/);
	const attestationHelper = readFileSync(
		join(root, 'pulse-app', 'cli', 'src', 'release-attestation.js'), 'utf8',
	);
	assert.match(attestationHelper, /`--package=\$\{tarballPath\}`/);
	assert.match(attestation, /public_preview_version/);
	assert.match(attestation, /public_preview_matches_artifact/);
	assert.match(attestation, /unassigned_assignment_receipt/);
	assert.match(attestation, /assigned_object_recalled_in_alpha/);
	assert.match(attestation, /assigned_object_absent_from_beta/);
	assert.match(attestation, /beta_context_trace_empty/);
	assert.match(attestation, /\/context\/query/);
	assert.doesNotMatch(attestation, /PULSE_RELEASE_TEST_MODE\s*=\s*['\"]1/);
	const multiharness = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'personal-preview-multiharness-e2e.mjs'), 'utf8',
	);
	assert.match(multiharness, /personal_preview_multiharness_orchestration\.v1/);
	assert.match(multiharness, /PULSE_PERSONAL_PACKED_TARBALL/);
	assert.match(multiharness, /packed_tarball_sha256/);
	assert.match(multiharness, /production_install_proof:\s*false/);
	assert.match(multiharness, /daemon_backed_cross_host_recall:\s*false/);
	assert.doesNotMatch(multiharness, /behavioral_cross_host_object_id|state\.memories/);
	for (const relative of [
		'README.md', 'AGENTS.md', 'docs/INSTALL_WITH_AGENT.md',
		'docs/PERSONAL_PULSE_ONBOARDING.md', 'pulse-app/cli/README.md',
	]) {
		const document = readFileSync(join(root, relative), 'utf8');
		assert.match(document, /npx (?:-y )?@zbs-gg\/pulse@preview install/,
			`${relative} must lead with the one-command Personal install`);
		assert.match(document, /Codex/);
		assert.match(document, /Memory Home/);
		assert.doesNotMatch(document, /pulse (?:doctor|disconnect) <(?:installed-host|host)>/,
			`${relative} must publish executable host commands, not angle-bracket placeholders`);
	}
	const onboarding = readFileSync(join(root, 'docs', 'PERSONAL_PULSE_ONBOARDING.md'), 'utf8');
	assert.match(onboarding, /does not require\s+Go, Python, Make, Docker, or a\s+model API key/i);
	assert.match(onboarding, /collecting|estimated|measured/i);
	assert.match(onboarding, /pulse repair/);
	assert.match(onboarding, /pulse disconnect claude-code/);
	assert.match(onboarding, /pulse disconnect cursor/);
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

test('npm preview release is manual, OIDC-only, staged, and still requires human 2FA approval', () => {
  const workflow = readFileSync(join(root, '.github', 'workflows', 'stage-npm-preview.yml'), 'utf8');
  assert.match(workflow, /^\s*workflow_dispatch:/m);
  assert.match(workflow, /^\s*id-token: write$/m);
  assert.match(workflow, /^\s*environment: npm-preview$/m);
  assert.match(workflow, /test "\$GITHUB_REF" = refs\/heads\/main/);
  assert.match(workflow, /test "\$STAGE_CONFIRMATION" = 'stage @zbs-gg\/pulse preview'/);
  assert.match(workflow, /test -z "\$\{NODE_AUTH_TOKEN:-\}"/);
  assert.match(workflow, /test -z "\$\{NPM_TOKEN:-\}"/);
  assert.match(workflow, /npm@11\.18\.0/);
  assert.match(workflow, /pulse-npm-production-candidate/);
  assert.match(workflow, /\.github\/workflows\/production-candidate\.yml/);
  assert.match(workflow, /\.github\/workflows\/verify\.yml/);
  assert.match(workflow, /verify-npm-stage-candidate\.mjs/);
  assert.match(workflow, /npm stage publish "\$GITHUB_WORKSPACE\/candidate\/\$tarball"/);
  assert.match(workflow, /--tag preview/);
  assert.doesNotMatch(workflow, /npm publish|npm stage approve|NODE_AUTH_TOKEN:\s*\$\{\{|NPM_TOKEN:\s*\$\{\{/);
  assert.ok(workflow.indexOf('verify-npm-stage-candidate.mjs') < workflow.indexOf('npm stage publish'));

  const release = readFileSync(join(root, 'docs', 'release', 'NPM_STAGED_PREVIEW.md'), 'utf8');
  assert.match(release, /npm stage publish/);
  assert.match(release, /2FA/);
  assert.match(release, /production:false/);
  assert.match(release, /Long-lived publication tokens are forbidden/);
});

test('production presence carrier uses the same exact-tree contract as the installer', () => {
	const builder = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'build-presence-helper.mjs'), 'utf8',
	);
	assert.match(builder, /buildPulseArtifactTree\(carrierRoot\)/);
	assert.match(builder, /pulse-artifact-tree\.json/);
	assert.match(builder, /join\(carrierRoot, 'bin'\)/);
	assert.match(builder, /--identifier', EXPECTED_IDENTIFIER/);
	assert.match(builder, /join\(mountPoint, 'bin', EXPECTED_IDENTIFIER\)/);

	const packager = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'prepare-preview-vendor.mjs'), 'utf8',
	);
	assert.match(packager, /join\(mountPoint, 'bin', expectedHelperIdentifier\)/);
});

test('target release builder emits native artifacts and production stays explicitly authorized', () => {
	const packageJSON = JSON.parse(readFileSync(join(root, 'pulse-app', 'cli', 'package.json'), 'utf8'));
	assert.equal(packageJSON.scripts?.['build:personal-release'], 'node scripts/build-personal-release.mjs');
	const builder = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'build-personal-release.mjs'), 'utf8',
	);
	for (const artifact of ['daemon', 'embedder-runtime']) {
		assert.match(builder, new RegExp(`['\"]${artifact}['\"]`));
	}
	assert.match(builder, /PULSE_RELEASE_SUBMISSION_AUTHORIZATION/);
	assert.match(builder, /target-build-approved/);
	assert.ok(builder.indexOf('requireProductionAuthority') < builder.lastIndexOf('buildDaemonTarget'));
	assert.match(builder, /notarytool', 'submit'/);
	assert.match(builder, /production_ready: false/);

	const fixture = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'target-release-fixture.mjs'), 'utf8',
	);
	for (const artifact of ['daemon', 'embedder-runtime', 'model', 'plugin-runtime', 'presence-helper']) {
		assert.match(fixture, new RegExp(`['\"]${artifact}['\"]`));
	}
	assert.match(fixture, /allowFixtureVerification: true/);
	assert.match(fixture, /production_ready: false/);
});
