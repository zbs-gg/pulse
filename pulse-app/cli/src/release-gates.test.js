import assert from 'node:assert/strict';
import {
  mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  auditPublicPackageRoot,
  publicMcpPackageManifest,
} from '../scripts/public-package-audit.mjs';

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

test('published package excludes repository archives and carries every advertised script', () => {
  const cliRoot = join(root, 'pulse-app', 'cli');
  const packageJSON = JSON.parse(readFileSync(join(cliRoot, 'package.json'), 'utf8'));
  const mcpPackageJSON = JSON.parse(readFileSync(join(root, 'mcp', 'package.json'), 'utf8'));
  assert.equal(packageJSON.author, 'ZBS GG');
  assert.equal(mcpPackageJSON.author, 'ZBS GG');
  assert.ok(packageJSON.files.includes('scripts'));

  const advertisedScriptPaths = [...new Set(Object.values(packageJSON.scripts)
    .flatMap((command) => [...command.matchAll(/(?:^|\s)node\s+(scripts\/[A-Za-z0-9._/-]+)/g)])
    .map((match) => match[1]))];
  assert.ok(advertisedScriptPaths.length > 0);
  assert.deepEqual(
    advertisedScriptPaths.filter((relative) => !statSync(join(cliRoot, relative)).isFile()),
    [],
  );

  const packager = readFileSync(join(cliRoot, 'scripts', 'prepare-preview-vendor.mjs'), 'utf8');
  assert.doesNotMatch(packager, /copyTree\(join\(pulseRoot, 'docs'/);
  assert.doesNotMatch(packager, /copyTree\(join\(mcpRoot, 'docs'/);
  assert.doesNotMatch(packager, /['"]AGENTS\.md['"]/);
});

test('public package audit rejects repository archives, personal paths, emails, and private keys', () => {
  const packageRoot = mkdtempSync(join(tmpdir(), 'pulse-public-package-audit.'));
  try {
    mkdirSync(join(packageRoot, 'src'), { recursive: true });
    mkdirSync(join(packageRoot, 'release'), { recursive: true });
    writeFileSync(join(packageRoot, 'package.json'), '{"name":"@zbs-gg/pulse","version":"0.7.2"}\n');
    writeFileSync(join(packageRoot, 'src', 'cli.js'), 'export const ready = true;\n');
    writeFileSync(join(packageRoot, 'release', 'pulse-release-root.pem'), [
      '-----BEGIN PUBLIC KEY-----',
      'MCowBQYDK2VwAyEA7QzyDphiWdg2Qljgf2fqJSxrzroxU5NJL2wplLDW9RI=',
      '-----END PUBLIC KEY-----',
      '',
    ].join('\n'));
    assert.doesNotThrow(() => auditPublicPackageRoot(packageRoot));

    mkdirSync(join(packageRoot, 'docs'), { recursive: true });
    writeFileSync(join(packageRoot, 'docs', 'internal.md'), 'internal plan\n');
    assert.throws(() => auditPublicPackageRoot(packageRoot), /forbidden package path/);
    rmSync(join(packageRoot, 'docs'), { recursive: true, force: true });

    writeFileSync(join(packageRoot, 'src', 'cli.js'), 'const leaked = "/Users/real-person/private";\n');
    assert.throws(() => auditPublicPackageRoot(packageRoot), /personal filesystem path/);
    writeFileSync(join(packageRoot, 'src', 'cli.js'), 'const leaked = "person@private.invalid";\n');
    assert.throws(() => auditPublicPackageRoot(packageRoot), /email address/);
    writeFileSync(join(packageRoot, 'src', 'cli.js'), '-----BEGIN PRIVATE KEY-----\n');
    assert.throws(() => auditPublicPackageRoot(packageRoot), /private key/);

    writeFileSync(join(packageRoot, 'src', 'cli.js'), 'export const ready = true;\n');
    for (const path of [
      'src/private-report.txt',
      'src/provider-output.txt',
      'src/consolidation-report-sidecar.txt',
    ]) {
      writeFileSync(join(packageRoot, path), 'benign-looking internal output\n');
      assert.throws(() => auditPublicPackageRoot(packageRoot), /forbidden package path/);
      rmSync(join(packageRoot, path), { force: true });
    }

    const syntheticTokenShapes = [
      ['ghp_', '1234567890abcdefghijABCDEFGHIJ'].join(''),
      ['xoxb', '1234567890', 'abcdefghijklmnop'].join('-'),
      ['AKIA', '1234567890ABCDEF'].join(''),
    ];
    for (const secret of [
      'token=z9Kq3hTm8Nx4Wp7Rv2Bc',
      'api_key: "q8Lm4Rs7Tv2Wx9Yp"',
      'password="n7Vp4Kx9Rm2Qs8Tz"',
      'private-key: "m6Rt3Wq8Xp5Kv9Nz"',
      'Authorization: Bearer AbCdEf1234567890',
      ...syntheticTokenShapes,
    ]) {
      writeFileSync(join(packageRoot, 'src', 'cli.js'), `${secret}\n`);
      assert.throws(() => auditPublicPackageRoot(packageRoot), /credential|authorization|token|access key/);
    }

    writeFileSync(join(packageRoot, 'src', 'cli.js'), [
      'const token = "<redacted>";',
      'const api_key = "your-example-key";',
      'const authorization = `Bearer ${token}`;',
      '',
    ].join('\n'));
    assert.doesNotThrow(() => auditPublicPackageRoot(packageRoot));
  } finally {
    rmSync(packageRoot, { recursive: true, force: true });
  }
});

test('vendored MCP manifest advertises only scripts present in the public package', () => {
  const packageRoot = mkdtempSync(join(tmpdir(), 'pulse-public-mcp-manifest.'));
  try {
    const nestedRoot = join(packageRoot, 'vendor', 'pulse-preview-source', 'mcp');
    mkdirSync(join(nestedRoot, 'src'), { recursive: true });
    writeFileSync(join(packageRoot, 'package.json'), '{"name":"@zbs-gg/pulse","version":"0.7.2"}\n');
    const sourcePackageJSON = JSON.parse(readFileSync(join(root, 'mcp', 'package.json'), 'utf8'));
    const publicPackageJSON = publicMcpPackageManifest(sourcePackageJSON);
    writeFileSync(join(nestedRoot, 'package.json'), `${JSON.stringify(publicPackageJSON, null, 2)}\n`);

    assert.deepEqual(Object.keys(publicPackageJSON.scripts), [
      'build',
      'start',
      'dev',
      'prepack',
      'prepublishOnly',
    ]);
    assert.doesNotThrow(() => auditPublicPackageRoot(packageRoot));

    publicPackageJSON.scripts['smoke:missing'] = 'node scripts/missing-smoke.mjs';
    writeFileSync(join(nestedRoot, 'package.json'), `${JSON.stringify(publicPackageJSON, null, 2)}\n`);
    assert.throws(() => auditPublicPackageRoot(packageRoot), /advertised package script is missing/);
  } finally {
    rmSync(packageRoot, { recursive: true, force: true });
  }
});

test('release verification includes the scanned archive and isolated native install', () => {
  const makefile = readFileSync(join(root, 'Makefile'), 'utf8');
	assert.match(makefile, /verify:[\s\S]*test:personal-clean-room[\s\S]*test:personal-interruption[\s\S]*test:personal-multiharness[\s\S]*test:personal-consolidation-report[\s\S]*test:codex-product/);
  assert.match(makefile, /^personal-package-verify:.*\n\tcd \$\(CLI_DIR\) && \$\(NPM\) run --silent verify:personal-package/m);
  assert.match(makefile, /^personal-native-packed-e2e:.*\n\tcd \$\(CLI_DIR\) && \$\(NPM\) run --silent test:personal-native-packed/m);
  assert.match(makefile, /^release-verify: verify personal-package-verify personal-native-packed-e2e/m);

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
    packageJSON.scripts?.['test:personal-consolidation-report'],
    'node scripts/personal-consolidation-report-e2e.mjs',
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
  assert.equal(
    packageJSON.scripts?.['verify:personal-package'],
    'node scripts/verify-personal-package.mjs',
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
	assert.match(cli, /codex activation detail/);
	assert.match(cli, /core activation stage/);
	assert.match(cli, /daemon_start_started/);
	assert.match(cli, /transaction_complete/);
	assert.match(cli, /runtime_provision_started/);
	assert.match(cli, /managed_runtime_resolution_started/);
	assert.match(cli, /<windows-path>/);
	assert.match(cli, /defaultPlatformServices\.inspectExecutable\(resolve\(managedRuntime\.daemon\.path\)\)/);
	assert.match(cli, /executableProof\.sha256 !== managedRuntime\.daemon\.digest/);
	const nativePacked = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'personal-native-packed-e2e.mjs'), 'utf8',
	);
	assert.match(nativePacked, /process\.platform === 'win32' \? 5 \* 60_000 : 15 \* 60_000/);
	assert.match(nativePacked, /firstValueMs <= 60_000/);
	assert.doesNotMatch(nativePacked, /firstValueLimitMs/);
	assert.match(nativePacked, /taskkill\.exe/);
	assert.match(nativePacked, /\['\/PID', String\(child\.pid\), '\/T', '\/F'\]/);
	assert.match(nativePacked, /await packedPulse\(tarball, \['init', 'codex', '--yes', '--json'\]/);
	assert.match(nativePacked, /PULSE_PERSONAL_PACKED_TARBALL/);
	const universalTarget = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'native-universal-target.mjs'), 'utf8',
	);
	assert.match(universalTarget, /productReceipt\.first_value_ms <= 60_000/);
	assert.doesNotMatch(universalTarget, /firstValueLimitMs/);
	assert.match(universalTarget, /process\.env\.PULSE_CURSOR_VERSION/);
	assert.doesNotMatch(universalTarget, /cursorExecutable, \['--version'\]/);
	assert.match(universalTarget, /pulse\.personal_consolidation_report_fixture\.v1/);
	assert.match(universalTarget, /consolidationReceipt\?\.package_sha256, productReceipt\.packed_tarball_sha256/);
	assert.match(universalTarget, /sources_byte_preserved/);
	assert.match(universalTarget, /mutation_authority_exercised: false/);
	const universalWorkflow = readFileSync(join(root, '.github', 'workflows', 'verify.yml'), 'utf8');
	assert.equal((universalWorkflow.match(/PULSE_CURSOR_VERSION=/g) ?? []).length, 3);
	assert.match(universalWorkflow, /name: Packed npm input/);
	assert.match(universalWorkflow, /needs: \[contract, package\]/);
	assert.match(universalWorkflow, /timeout-minutes: \$\{\{ matrix\.job_timeout_minutes \}\}/);
	assert.match(universalWorkflow, /PULSE_PERSONAL_PACKED_TARBALL/);
	const runtimeInstaller = readFileSync(
		join(root, 'pulse-app', 'cli', 'src', 'personal-runtime-installer.js'), 'utf8',
	);
	assert.match(runtimeInstaller, /runtime provision stage/);
	assert.match(runtimeInstaller, /preflight_release_started/);
	assert.match(runtimeInstaller, /active_set_inspection_started/);
	assert.match(runtimeInstaller, /activation_\$\{kind\}_started/);
	const supervisor = readFileSync(
		join(root, 'pulse-app', 'cli', 'src', 'local-supervisor.js'), 'utf8',
	);
	assert.match(supervisor, /managed runtime stage/);
	assert.match(supervisor, /daemon_file_started/);
	assert.match(supervisor, /daemon_identity_complete/);
	for (const script of [
		'personal-preview-clean-room.mjs',
		'personal-preview-interruption-e2e.mjs',
		'personal-preview-multiharness-e2e.mjs',
		'personal-consolidation-report-e2e.mjs',
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
		'README.md', 'docs/INSTALL_WITH_AGENT.md',
		'docs/PERSONAL_PULSE_ONBOARDING.md', 'pulse-app/cli/README.md',
	]) {
		const document = readFileSync(join(root, relative), 'utf8');
		assert.match(document, /npx (?:-y )?@zbs-gg\/pulse@(?:preview|0\.7\.2) init codex/,
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
	assert.match(onboarding, /pulse consolidate report/);
	const releaseFixture = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'product-release-fixture.mjs'), 'utf8',
	);
	const realRuntimeBranch = releaseFixture.match(
		/if \(realInputs && kind === 'embedder-runtime'\) \{([\s\S]*?)\} else if/,
	)?.[1] ?? '';
	assert.match(realRuntimeBranch, /linkSync\(realInputs\.runtimePath/);
	assert.doesNotMatch(realRuntimeBranch, /materializers\[kind\]/,
		'real MLX runtime must flow through the packed production DMG materializer');

});

test('npm publication runs the repository release gate before preparing package artifacts', () => {
  const packageJSON = JSON.parse(readFileSync(join(root, 'pulse-app', 'cli', 'package.json'), 'utf8'));
  const prepublish = packageJSON.scripts?.prepublishOnly;
  assert.equal(
    prepublish,
    'cd ../.. && make release-verify && cd pulse-app/cli && node scripts/prepare-preview-vendor.mjs',
  );
  assert.ok(prepublish.indexOf('make release-verify') < prepublish.indexOf('prepare-preview-vendor.mjs'));

  const workflow = readFileSync(join(root, '.github', 'workflows', 'publish-npm.yml'), 'utf8');
  assert.match(workflow, /runs-on: macos-15/);
  assert.match(workflow, /EXPECTED_VERSION: '0\.7\.2'/);
  assert.match(workflow, /default: false/);
  assert.match(workflow, /pulse-personal-releases-zbs\.storage\.googleapis\.com\/pulse\/0\.7\.2\/epoch-9\/catalog\/artifact-set\.json/);
  assert.match(workflow, /pulse-personal-releases-zbs\.storage\.googleapis\.com\/pulse\/0\.7\.2\/catalog\/snapshot\.json/);
  assert.match(workflow, /test "\$\(uname -m\)" = arm64/);
  assert.match(workflow, /init codex --only codex --yes --json/);
  assert.match(workflow, /r\.outcome!=="ready"/);
  assert.match(workflow, /github\.ref == 'refs\/heads\/main' && inputs\.publish/);

});

test('npm preview auto-stages an exact successful seal through OIDC and still requires human 2FA approval', () => {
  const workflow = readFileSync(join(root, '.github', 'workflows', 'stage-npm-preview.yml'), 'utf8');
  assert.match(workflow, /^\s*workflow_dispatch:/m);
  assert.match(workflow, /^\s*workflow_run:/m);
  assert.match(workflow, /^\s*- Seal production candidate$/m);
  assert.match(workflow, /^\s*id-token: write$/m);
  assert.match(workflow, /^\s*environment: npm-preview$/m);
  assert.match(workflow, /test "\$GITHUB_REF" = refs\/heads\/main/);
  assert.match(workflow, /test "\$STAGE_CONFIRMATION" = 'stage @zbs-gg\/pulse preview'/);
  assert.match(workflow, /github\.event\.workflow_run\.conclusion == 'success'/);
  assert.match(workflow, /github\.event\.workflow_run\.head_branch == 'main'/);
  assert.match(workflow, /CANDIDATE_RUN_ID: \$\{\{ github\.event\.workflow_run\.id \|\| inputs\.production_candidate_run_id \}\}/);
  assert.match(workflow, /sealed_sha256="\$\(jq -r \.sha256 candidate\/candidate\.json\)"/);
  assert.match(workflow, /test -z "\$\{NODE_AUTH_TOKEN:-\}"/);
  assert.match(workflow, /test -z "\$\{NPM_TOKEN:-\}"/);
  assert.match(workflow, /npm@11\.18\.0/);
  assert.match(workflow, /pulse-npm-production-candidate/);
  assert.match(workflow, /\.github\/workflows\/seal-production-candidate\.yml/);
  assert.match(workflow, /\.github\/workflows\/verify\.yml/);
  assert.match(workflow, /verify-npm-stage-candidate\.mjs/);
  assert.match(workflow, /npm stage publish "\$GITHUB_WORKSPACE\/candidate\/\$tarball"/);
  assert.match(workflow, /--tag preview/);
  assert.doesNotMatch(workflow, /npm publish|npm stage approve|NODE_AUTH_TOKEN:\s*\$\{\{|NPM_TOKEN:\s*\$\{\{/);
  assert.ok(workflow.indexOf('verify-npm-stage-candidate.mjs') < workflow.indexOf('npm stage publish'));

  const release = readFileSync(join(root, 'docs', 'release', 'NPM_STAGED_PREVIEW.md'), 'utf8');
  assert.match(release, /npm stage publish/);
  assert.match(release, /automatically starts/);
  assert.match(release, /2FA/);
  assert.match(release, /production_ready:false/);
  assert.match(release, /Long-lived npm publication\s+tokens are forbidden/);
});

test('production release is split into immutable inputs, origin publication, and 18 real vendor sessions', () => {
  const inputsWorkflow = readFileSync(join(root, '.github', 'workflows', 'production-candidate.yml'), 'utf8');
  assert.match(inputsWorkflow, /^name: Production candidate$/m);
  assert.match(inputsWorkflow, /^\s*workflow_dispatch:/m);
  assert.match(inputsWorkflow, /test "\$GITHUB_REF" = refs\/heads\/main/);
  assert.match(inputsWorkflow, /test "\$RELEASE_CONFIRMATION" = 'build universal production candidate'/);
  assert.match(inputsWorkflow, /\.github\/workflows\/verify\.yml/);
  for (const environment of [
    'production-linux', 'production-apple', 'production-windows',
    'production-model', 'production-catalog', 'production-candidate',
  ]) assert.match(inputsWorkflow, new RegExp(`environment: ${environment}`));
  for (const platform of ['darwin', 'linux', 'win32']) {
    assert.match(inputsWorkflow, new RegExp(`--github-platform ${platform}`));
  }
  for (const targetID of [
    'darwin-arm64', 'darwin-x64', 'linux-arm64-gnu',
    'linux-x64-gnu', 'win32-arm64', 'win32-x64',
  ]) assert.match(inputsWorkflow, new RegExp(`pulse-production-target-${targetID}`));
  assert.match(inputsWorkflow, /PULSE_RELEASE_SUBMISSION_AUTHORIZATION: target-build-approved/);
  assert.match(inputsWorkflow, /npm run build:personal-catalog/);
  assert.match(inputsWorkflow, /npm run build:npm-production-inputs/);
  assert.match(inputsWorkflow, /pulse-production-release-catalog/);
  assert.match(inputsWorkflow, /pulse-npm-production-inputs/);
  assert.doesNotMatch(inputsWorkflow, /npm publish|npm stage publish|npm stage approve|continue-on-error/);
  assert.ok(inputsWorkflow.indexOf('npm run build:personal-catalog') <
    inputsWorkflow.indexOf('npm run build:npm-production-inputs'));

  const originWorkflow = readFileSync(join(root, '.github', 'workflows', 'publish-production-origin.yml'), 'utf8');
  assert.match(originWorkflow, /^name: Production origin$/m);
  assert.match(originWorkflow, /^\s*environment: production-origin$/m);
  assert.match(originWorkflow, /pulse\.npm_production_inputs\.v1/);
  assert.match(originWorkflow, /publish-r2-release\.mjs/);
  assert.doesNotMatch(originWorkflow, /npm publish|npm stage publish|npm stage approve|continue-on-error/);

  const sealWorkflow = readFileSync(join(root, '.github', 'workflows', 'seal-production-candidate.yml'), 'utf8');
  const vendorAction = readFileSync(
    join(root, '.github', 'actions', 'setup-pulse-vendor-harness', 'action.yml'), 'utf8',
  );
  assert.match(sealWorkflow, /^name: Seal production candidate$/m);
  assert.match(sealWorkflow, /^\s*environment: production-harness-e2e$/m);
  assert.match(sealWorkflow, /--authority production_candidate/);
  assert.match(sealWorkflow, /test:native-vendor-session/);
  assert.match(sealWorkflow, /validate-native-evidence-set\.mjs/);
  assert.match(sealWorkflow, /npm run build:npm-production-candidate/);
  assert.match(sealWorkflow, /pulse-npm-production-candidate/);
  assert.doesNotMatch(sealWorkflow, /personal-native-packed-e2e|authority fixture|npm publish|npm stage publish|continue-on-error/);
  assert.match(vendorAction, /sudo chmod 4755 "\$sandbox"/);
  assert.match(vendorAction, /root:root 4755/);
  assert.match(vendorAction, /cursor-installer\.exe/);

  const multiharness = readFileSync(
    join(root, 'pulse-app', 'cli', 'scripts', 'personal-preview-multiharness-e2e.mjs'), 'utf8',
  );
  assert.match(multiharness, /process\.platform === 'linux'/);
  assert.match(multiharness, /\[process\.execPath, npmArgs\]/);
  assert.match(multiharness, /process\.platform === 'win32' \? 'git\.exe'/);
  assert.match(multiharness, /workspace_id: 'workspace_multiharness_fixture'/);

  const packager = readFileSync(
    join(root, 'pulse-app', 'cli', 'scripts', 'prepare-preview-vendor.mjs'), 'utf8',
  );
  assert.match(packager, /process\.platform === 'win32'/);
  assert.match(packager, /flag: 'wx'/);
  assert.match(packager, /PULSE_PREVIEW_VENDOR_LOCKED: '1'/);

  const packageJSON = JSON.parse(readFileSync(join(root, 'pulse-app', 'cli', 'package.json'), 'utf8'));
  assert.equal(
    packageJSON.scripts?.['build:npm-production-inputs'],
    'node scripts/build-npm-production-inputs.mjs',
  );
  assert.equal(
    packageJSON.scripts?.['build:npm-production-candidate'],
    'node scripts/build-npm-production-candidate.mjs',
  );
  const builder = readFileSync(
    join(root, 'pulse-app', 'cli', 'scripts', 'build-npm-production-candidate.mjs'), 'utf8',
  );
  assert.match(builder, /DESKTOP_TARGET_IDS/);
  assert.match(builder, /pulse\.native_host_target_evidence\.v2|validateNativeEvidenceSet/);
  assert.match(builder, /production_candidate/);
  assert.match(builder, /production: true/);
  assert.match(builder, /support_claim: false/);
});

test('scheduled snapshot refresh is inactive before Gold and fail-closed after activation', () => {
  const workflow = readFileSync(
    join(root, '.github', 'workflows', 'refresh-production-snapshot.yml'), 'utf8',
  );
  assert.match(workflow, /releases\/tags\/v0\.7\.1/);
  assert.match(workflow, /git\/ref\/tags\/v0\.7\.1/);
  assert.match(workflow, /test "\$status" = 200/);
  assert.match(workflow, /test "\$status" = 404 && test "\$tag_status" = 404/);
  assert.match(workflow, /test "\$GITHUB_EVENT_NAME" = schedule/);
  assert.match(workflow, /echo 'gold_active=false' >> "\$GITHUB_OUTPUT"/);
  assert.match(workflow, /Gold activation check failed: release status[\s\S]*?exit 1/);
  assert.match(workflow, /if: steps\.activation\.outputs\.gold_active == 'true'/);
  assert.match(
    workflow,
    /refresh_required: \$\{\{ steps\.activation\.outputs\.gold_active == 'true' && steps\.freshness\.outputs\.refresh_required == 'true' \}\}/,
  );
  assert.match(workflow, /if: needs\.inspect\.outputs\.refresh_required == 'true'/);
});

test('public soak requires four timed 18-pair registry runs and Gold remains human-promoted', () => {
  const soak = readFileSync(join(root, '.github', 'workflows', 'public-registry-soak.yml'), 'utf8');
  assert.match(soak, /^name: Public registry soak$/m);
  assert.match(soak, /options: \['0', '24', '48', '72'\]/);
  assert.match(soak, /dist-tags\.preview/);
  assert.match(soak, /npm pack @zbs-gg\/pulse@0\.7\.1/);
  assert.match(soak, /https:\/\/releases\.zbs\.gg\/pulse\/0\.7\.1\/catalog\/snapshot\.json/);
  assert.match(soak, /--max-redirs 0/);
  assert.match(soak, /--range 0-1023/);
  assert.match(soak, /npm audit --omit=dev --audit-level=high/);
  assert.match(soak, /--authority public_registry/);
  assert.match(soak, /test:native-vendor-session/);
  assert.match(soak, /build:public-soak-receipt/);
  assert.match(soak, /environment: production-harness-e2e/);
  assert.doesNotMatch(soak, /continue-on-error|npm publish|npm stage publish|npm dist-tag/);

  const authorize = readFileSync(join(root, '.github', 'workflows', 'authorize-gold-promotion.yml'), 'utf8');
  assert.match(authorize, /^name: Authorize Gold promotion$/m);
  assert.match(authorize, /for checkpoint in 0 24 48 72/);
  assert.match(authorize, /--name "pulse-public-soak-\$checkpoint"/);
  assert.match(authorize, /environment: npm-gold/);
  assert.match(authorize, /environment: production-catalog/);
  assert.match(authorize, /build:gold-promotion-receipt/);
  assert.match(authorize, /publication_performed/);
  assert.doesNotMatch(authorize, /npm publish|npm stage publish|npm dist-tag|gh release create|git tag/);

  const verify = readFileSync(join(root, '.github', 'workflows', 'verify-gold-publication.yml'), 'utf8');
  assert.match(verify, /^name: Verify Gold publication$/m);
  assert.match(verify, /dist-tags\.preview/);
  assert.match(verify, /dist-tags\.latest/);
  assert.match(verify, /cmp "\$\{preview\[0\]\}" "\$\{latest\[0\]\}"/);
  assert.match(verify, /v0\.7\.1\^\{commit\}/);
  assert.match(verify, /generate:native-support-ledger/);
  assert.doesNotMatch(verify, /npm publish|npm stage publish|npm dist-tag|gh release create|git tag/);

  const ledger = readFileSync(join(root, 'docs', 'release', 'NATIVE_SUPPORT_LEDGER.md'), 'utf8');
  assert.match(ledger, /Codex \| `0\.145\.0`/);
  assert.match(ledger, /Claude Code \| `2\.1\.220`/);
  assert.match(ledger, /Cursor Desktop \| `3\.13`/);
  assert.match(ledger, /Rows must never be promoted by hand/);
  assert.match(ledger, /All Gold columns are intentionally pending/);
});

test('optional macOS presence carrier stays exact-tree while npm packaging accepts a signed target subset', () => {
	const builder = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'build-presence-helper.mjs'), 'utf8',
	);
	assert.match(builder, /buildPulseArtifactTree\(carrierRoot\)/);
	assert.match(builder, /pulse-artifact-tree\.json/);
	assert.match(builder, /join\(carrierRoot, 'bin'\)/);
	assert.match(builder, /--identifier', EXPECTED_IDENTIFIER/);
	assert.match(builder, /join\(mountPoint, 'bin', EXPECTED_IDENTIFIER\)/);
	assert.match(builder, /'--check-notarization'/);
	assert.doesNotMatch(builder, /spctl'[^]*'-t', 'exec'/);

	const packager = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'prepare-preview-vendor.mjs'), 'utf8',
	);
	assert.match(packager, /'--check-notarization'/);
	assert.match(packager, /targetIDs\.length < 1/);
	assert.match(packager, /DESKTOP_TARGET_IDS\.includes/);
	assert.match(packager, /verified\.target_id !== targetID/);
	assert.match(packager, /artifact\.format !== 'tar\.gz'/);
	assert.match(packager, /verifyHelperProtocol\(nativeHelper\)/);
	assert.doesNotMatch(packager, /nativeHelperCarrier|hdiutil|stapler|helperArtifact\.format !== 'dmg'/);
	assert.doesNotMatch(packager, /spctl'[^]*'-t', 'exec'/);
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
	assert.match(builder, /'--check-notarization'/);
	assert.match(builder, /production_ready: false/);

	const fixture = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'target-release-fixture.mjs'), 'utf8',
	);
	for (const artifact of ['daemon', 'embedder-runtime', 'model', 'plugin-runtime', 'presence-helper']) {
		assert.match(fixture, new RegExp(`['\"]${artifact}['\"]`));
	}
	assert.match(fixture, /allowFixtureVerification: true/);
	assert.match(fixture, /production_ready: false/);

	const catalogBuilder = readFileSync(
		join(root, 'pulse-app', 'cli', 'scripts', 'build-personal-catalog.mjs'), 'utf8',
	);
	assert.match(catalogBuilder, /DESKTOP_TARGET_IDS/);
	assert.match(catalogBuilder, /targetIDs\.length < 1/);
	assert.match(catalogBuilder, /target_count: targetIDs\.length/);
	assert.match(catalogBuilder, /artifact_count: 2 \+ targetIDs\.length \* TARGET_ARTIFACT_KINDS\.length/);
	assert.doesNotMatch(catalogBuilder, /target\.target\?\.target_id !== 'darwin-arm64'/);

	const releaseReadme = readFileSync(
		join(root, 'pulse-app', 'cli', 'release', 'README.md'), 'utf8',
	);
	assert.match(releaseReadme, /--target darwin-arm64=/);
	assert.match(releaseReadme, /Mac Apple Silicon can be released first/i);
});
