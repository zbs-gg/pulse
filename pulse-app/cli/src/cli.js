#!/usr/bin/env node
import { spawnSync, spawn } from 'node:child_process';
import { createHash, createPrivateKey, randomBytes, sign as cryptoSign } from 'node:crypto';
import { createServer, request as httpRequest } from 'node:http';
import {
  appendFileSync,
  chmodSync,
	cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  realpathSync,
  mkdtempSync,
  openSync,
  renameSync,
  rmSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import {
  buildSenderConstrainedRemoteHeaders,
  createOSCredentialStore,
} from './remote-auth.js';
import {
  buildPulseRequestHeaders,
  isLoopbackPulseBase,
  requireLoopbackPulseIPC,
} from './remote-auth-network.js';
import { readTeamAuthProfile, runTeamLogin } from './team-login.js';
import { inspectTeamInstallation, setTeamStatusExitCode } from './team-status.js';
import {
	createTeamOwnerRemotePost,
	buildTeamOwnerStepUp,
	runTeamOwnerOperation,
	TeamOwnerError,
} from './team-owner-client.js';
import { readTeamOwnerAuthProfile, runTeamOwnerLogin, runTeamOwnerStepUp } from './team-owner-login.js';
import { createWorkspaceBinding, recoverWorkspaceBindingTransaction } from './binding-admin.js';
import {
  inspectPresenceTrust,
  installPresenceTrust,
  INSTALL_CONFIRMATION as TRUST_INSTALL_CONFIRMATION,
} from './trust-helper.js';
import { BindingError, canonicalizeWorkspace, defaultBindingPaths, resolveWorkspaceBinding } from './workspace-binding.js';
import { captureEnabledForHost, captureStatePaths, writeCaptureStateFiles } from './capture-state.js';
import { assertSupportedNodeVersion } from './release-manifest.js';
import { acquireCLIInvocation, consumeCLIResponse } from './cli-idempotency.js';
import { executePersonalInstallCommand } from './personal-install-command.js';
import {
  buildPersonalInstallPlan,
  detectCodexCLI,
  formatPersonalInstallPlan,
} from './install-plan.js';
import { detectClaudeCodeCLI, detectCursorInstallation, SUPPORTED_HOST_IDS } from './supported-hosts.js';
import { selectHomeDoctorReport } from './home-doctor.js';
import {
  activateDetectedPersonalHosts,
  inspectDetectedPersonalHosts,
} from './personal-host-adapters.js';
import {
  normalizePersonalInstallHostStatus,
  PersonalInstallError,
  writePersonalInstallReceipt,
} from './personal-install.js';
import {
  projectPersonalLiveReadiness,
  projectSupportedHostLiveReadiness,
} from './personal-live-readiness.js';
import {
  PERSONAL_PROTECTED_ACTIONS,
  portablePersonalAuthorityProfile,
} from './personal-authority-profile.js';
import {
  PersonalPrincipalError,
  ensurePersonalPrincipal,
  readPersonalPrincipal,
} from './personal-principal.js';
import {
  codexWorkspaceDigest,
	inspectCodexNativeHookTrust,
	projectCodexLifecycleAttestation,
  resolveCodexMcpRuntime,
  runCodexHookCLI,
} from './codex-hooks.js';
import { claudeHookContractDigest, claudeWorkspaceDigest, runClaudeHookCLI } from './claude-hooks.js';
import { activateClaudePlugin, disableClaudePlugin, parseClaudePluginList } from './claude-plugin-install.js';
import { inspectCursorLifecycleReadiness, runCursorHookCLI } from './cursor-hooks.js';
import { cursorHomePath, inspectCursorPlugin, installCursorPlugin, removeCursorPlugin } from './cursor-install.js';
import {
	codexMarketplaceDoctorCheck,
  codexHomePath,
	inspectCodexMarketplaceSnapshot,
  inspectCodexRuntime,
	inspectCodexRuntimeAt,
	inspectCodexPluginCompatibility,
  inspectLegacyPulseHookFiles,
  installCodexRuntime,
	materializeCodexMarketplaceSnapshot,
  finalizeCodexRuntimeInstall,
	migrateLegacyPulseHookFiles,
	parseCodexMarketplaceList,
	parsePulsePluginList,
	pulseProductMcpShadowFiles,
	productHostAccessPath,
	readCodexProductLocator,
	readProductHostAccess,
	readProductLocator,
	removeCodexProductLocator,
	removeProductHostAccess,
	removeProductLocator,
	resolveSignedCodexProductEdge,
  rollbackCodexRuntimeInstall,
	writeCodexProductLocator,
	writeProductHostAccess,
	writeProductLocator,
} from './codex-install.js';
import { codexHookExecutionDigest, validateHookReadiness } from './host-adapter.js';
import {
	acquireVaultActivationLock,
	boundPulseRequest,
	ensureActivatedVaultRuntime,
	inspectProductWorkspaceBinding,
	readProductActivation,
	readProductActivationBundle,
} from './codex-runtime.js';
import {
  SupervisorError,
	activateManagedEmbedderConfig,
	assertVaultRuntimeHealthy,
	inspectVaultRuntime,
	resolveManagedRuntime,
	startVaultRuntime,
	stopVaultRuntimeAndWait,
  vaultRuntimeFromBinding,
} from './local-supervisor.js';
import {
	commitPersonalRuntimeRelease,
  inspectPersonalRelease,
	inspectPersonalRuntime,
	packagedPersonalRuntimeOptions,
	provisionPersonalRuntime,
} from './personal-runtime-installer.js';
import { nativePackedFixtureAttestation } from './native-packed-fixture.js';

const DEFAULT_BASE_URL = process.env.PULSE_BASE_URL || 'http://127.0.0.1:18789';
// `||` on purpose: an empty PULSE_DATA_DIR must not become a relative path
// (same rule as mcp/src/index.ts and standalone.ts).
const DATA_DIR = resolve(process.env.PULSE_DATA_DIR || join(homedir(), '.pulse'));
const SECRET_PATH = join(DATA_DIR, 'secret.key');
const MODE_PATH = join(DATA_DIR, 'mode');
const CLI_PATH = fileURLToPath(import.meta.url);
const CLI_PACKAGE_ROOT = resolve(dirname(CLI_PATH), '..');
const PREVIEW_VERSION = '0.7.0';
const IMPORT_PREVIEW_FLOW = 'pulse.import_preview.v2';
const LEGACY_IMPORT_PREVIEW_FLOW = 'pulse.import_preview.v1';
const PUBLIC_REPO_URL = process.env.PULSE_REPO_URL ?? 'https://github.com/zbs-gg/pulse';
const MAX_MIGRATION_FILE_BYTES = positiveEnvInt('PULSE_MIGRATION_MAX_FILE_BYTES', 300 * 1024 * 1024);
const MAX_MIGRATION_FILES = positiveEnvInt('PULSE_MIGRATION_MAX_FILES', 3000);
const FIRST_PROOF_MEMORY =
	'Pulse keeps the thread: structured memories, never raw transcripts, deletion stays human-controlled.';
const FIRST_PROOF_REMEMBER_PROMPT = `Remember this in Pulse: ${FIRST_PROOF_MEMORY}`;
const FIRST_PROOF_RECALL_PROMPT = 'What did we decide about how Pulse stores memory?';

const args = process.argv.slice(2);
const command = args[0] ?? '--help';

function usage() {
  console.log(`Pulse host-extracted memory

Usage:
  pulse --why
  pulse mcp [--http --port <port>]
  pulse codex-mcp
  pulse codex-hook SessionStart|UserPromptSubmit|PreToolUse|PostToolUse|PreCompact|PostCompact|SubagentStart|SubagentStop|Stop
  pulse claude-mcp
  pulse claude-hook SessionStart|UserPromptSubmit|PreToolUse|PostToolUse|PreCompact|PostCompact|SubagentStart|SubagentStop|Stop
  pulse cursor-mcp
  pulse cursor-hook sessionStart|beforeSubmitPrompt|preToolUse|postToolUse|preCompact|stop
  pulse connect cursor
  pulse install-plan [--json]
  pulse install [--json]
  pulse repair [--json]
  pulse install-plan claude-code [--json]   legacy Claude Code preview plan
  pulse init claude-code
  pulse init claude-code --dry-run
  pulse init claude-code --yes
  pulse demo [--clean]
  pulse doctor
  pulse doctor codex
  pulse doctor claude-code
  pulse doctor cursor
  pulse doctor --json
  pulse trust status [--json]
  pulse trust install --confirm "install pulse presence helper"
  pulse binding resolve [--cwd <path>] [--json]
  pulse binding create-personal --principal-id <id> [--cwd <path>] [--port <port>] --confirm "bind pulse personal workspace"
  pulse binding create-team --principal-id <id> --team-id <id> --commons-store-id <id> --commons-project-id <project_id> --commons-resource <https-url/mcp> [--cwd <path>] [--port <port>] --confirm "bind pulse team workspace"
  pulse supervisor start|status|stop [--cwd <path>] [--json]
  pulse team login --profile <root-owned-json> [--out <json>] [--no-open]
  pulse team status [--json]
  pulse team owner login --profile <root-owned-json> [--out <json>] [--no-open]
  pulse team owner member create --profile <root-owned-json> --issuer <https-url> --subject <id> --role owner|member|reviewer [--json] [--no-open]
  pulse team owner member revoke --profile <root-owned-json> --principal-id <id> [--json] [--no-open]
  pulse team owner binding create --profile <root-owned-json> --issuer <https-url> --subject <id> --client-id <id> [--json] [--no-open]
  pulse team owner binding revoke --profile <root-owned-json> --binding-id <id> [--json] [--no-open]
  pulse team owner project create --profile <root-owned-json> --name <name> [--json] [--no-open]
  pulse team owner project grant --profile <root-owned-json> --project-id <id> --principal-id <id> --access read|write|admin [--json] [--no-open]
  pulse team owner project revoke-grant --profile <root-owned-json> --grant-id <id> [--json] [--no-open]
  pulse connect claude-code [--remote-control]
  pulse connect codex
  pulse connect chatgpt|claude-chat --base <https-origin-or-mcp-url> [--open]
  pulse connect-smoke --base <https-origin> [--thread <id>] [--json]
  pulse disconnect claude-code
  pulse disconnect codex
  pulse disconnect cursor
  pulse stop
  pulse remove claude-code
  pulse daemon --go-bin <path> [-- <extra pulse server args>]
  pulse hook session-start|user-prompt-submit|post-tool-use|stop
  pulse migrate start [--dir <dir>] [--people-graph <path>] [--open] [--watch] [--downloads <dir>] [--timeout-ms <ms>] [--interval-ms <ms>]
  pulse migrate guide chatgpt|claude|codex|claude-code [--open]
  pulse migrate concierge [--html <file>] [--brief <file>] [--open]
  pulse migrate request chatgpt|claude|codex|claude-code [--downloads <dir>] [--timeout-ms <ms>] [--interval-ms <ms>] [--html <file>] [--out <file>] [--open]
  pulse migrate preview-latest chatgpt|claude [--downloads <dir>] [--html <file>] [--out <file>] [--open]
  pulse migrate wait-latest chatgpt|claude [--downloads <dir>] [--timeout-ms <ms>] [--interval-ms <ms>] [--html <file>] [--out <file>] [--open]
  pulse migrate preview <export-folder-or-json-or-zip> [--json] [--html <file>] [--out <file>] [--open]
  pulse migrate preview-people-graph <graph-dir-or-people-index> [--json] [--html <file>] [--out <file>] [--open]
  pulse migrate commit <preview-json-file> --confirm "import pulse graph" [--privacy private|sensitive|normal] [--open]
  pulse home [--host claude-code|codex|cursor] [--base <url>] [--data-dir <path>]
  pulse viewer [--base <url>] [--data-dir <path>] [--thread-id <id>] [--open] [--print-url]   legacy inspection surface
  pulse status
  pulse export
  pulse import --file <path>
  pulse delete --id <pulse:id>
  pulse wipe --confirm "wipe pulse memory"
  pulse consolidate [--threshold 0.90] [--scope session|project|long_term] [--apply]

Environment:
  PULSE_BASE_URL   ${DEFAULT_BASE_URL}
  PULSE_DATA_DIR   ${DATA_DIR}
  PULSE_GO_BIN     explicit Pulse Go server binary for daemon command
`);
}

function positiveEnvInt(name, fallback) {
  const value = Number.parseInt(process.env[name] ?? '', 10);
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function installPlan(host = 'claude-code') {
  return {
    product: 'Pulse MCP Preview',
    version: PREVIEW_VERSION,
    repo_url: PUBLIC_REPO_URL,
    target_host: host,
    mode: 'developer_preview',
    will_install: [
      'local Pulse daemon',
      'Pulse MCP server',
      'Claude Code MCP config',
      'Claude Code lifecycle hooks',
      'local viewer',
      'private first memory proof',
    ],
    will_write: [
      '~/.pulse',
      'project .claude/settings.local.json',
      'Claude Code MCP config',
    ],
    will_not_do: [
      'import old chats',
      'store raw transcripts by default',
      'call backend OpenAI/Anthropic/Cohere model APIs by default',
      'print secrets',
      'claim production readiness',
    ],
    requires: [
      'Node 20+',
      'npm',
      'Go',
      'Claude Code CLI',
      'internet for npm install',
    ],
    rollback: [
      'pulse wipe --confirm "wipe pulse memory"',
      'pulse disconnect claude-code',
      'pulse stop',
    ],
  };
}

function printInstallPlan(host = 'claude-code', { json = false, dryRun = false } = {}) {
  const plan = installPlan(host);
  if (json) {
    console.log(JSON.stringify(plan, null, 2));
    return;
  }
  console.log(`Pulse install plan

Product: ${plan.product} v${plan.version}
Target host: ${harnessDisplayName(host)}
Mode: ${plan.mode}
Repository: ${plan.repo_url}

Will install:
${plan.will_install.map((item) => `- ${item}`).join('\n')}

Will write:
${plan.will_write.map((item) => `- ${item}`).join('\n')}

Will not:
${plan.will_not_do.map((item) => `- ${item}`).join('\n')}

Requires:
${plan.requires.map((item) => `- ${item}`).join('\n')}

Rollback:
${plan.rollback.map((item) => `- ${item}`).join('\n')}

${dryRun ? 'Dry run only. Nothing was written.\n' : ''}Run with --yes to install after the agent explains this plan and you confirm.`);
}

const PERSONAL_INSTALL_TEST_OVERRIDE_NAMES = Object.freeze([
  'PULSE_BINDING_REGISTRY_PATH',
  'PULSE_BINDING_PUBLIC_KEY_PATH',
  'PULSE_BINDING_ANCHOR_PATH',
  'PULSE_CODEX_MARKETPLACE_SOURCE',
  'PULSE_RELEASE_MANIFEST_PATH',
  'PULSE_RELEASE_TEST_ROOT_PATH',
  'PULSE_RELEASE_TEST_ASSET_ROOT',
  'PULSE_RELEASE_TEST_MATERIALIZER_SPEC',
]);

function personalInstallUsesSyntheticOverrides() {
  return process.env.PULSE_TRUST_MODE === 'test' || process.env.PULSE_RELEASE_TEST_MODE === '1' ||
    PERSONAL_INSTALL_TEST_OVERRIDE_NAMES.some((name) => Boolean(process.env[name]));
}

function currentNativePackedFixtureAttestation(workspace, release) {
  if (!workspace || !release) return null;
  return nativePackedFixtureAttestation({
    cwd: workspace.canonical_path,
    dataDir: DATA_DIR,
    env: process.env,
    home: homedir(),
    plan: {
      schema: 'pulse.personal_install_plan.v2',
      contract_version: 2,
      detected: { workspace: { canonical_path: workspace.canonical_path } },
      release: {
        catalog_schema: release.catalog_schema,
        verification_profile: release.verification_profile,
      },
    },
  });
}

function currentPersonalInstallPlan() {
  let releaseInspection;
  let releaseReasonCode;
  try {
    releaseInspection = inspectPersonalRelease(packagedPersonalRuntimeOptions(DATA_DIR));
  } catch (error) {
    releaseReasonCode = typeof error?.code === 'string' ? error.code : 'release_manifest_unavailable';
  }
  let workspace;
  let workspaceError;
  try { workspace = canonicalizeWorkspace(process.cwd()); } catch (error) { workspaceError = error; }
  return buildPersonalInstallPlan({
    cwd: process.cwd(),
    home: homedir(),
    codexHome: codexHomePath(),
    dataDir: DATA_DIR,
    release: releaseInspection?.release,
    releaseReasonCode,
    currentState: inspectPersonalPreflightState(workspace, releaseInspection?.release),
    detectWorkspace: () => {
      if (workspaceError) throw workspaceError;
      return workspace;
    },
  });
}

function privateStateFileStatus(path) {
  if (!existsSync(path)) return 'missing';
  try {
    const info = lstatSync(path);
    const uid = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
    return info.isFile() && !info.isSymbolicLink() && info.nlink === 1 && info.uid === uid && (info.mode & 0o077) === 0
      ? 'present_unverified'
      : 'invalid';
  } catch {
    return 'invalid';
  }
}

function writeProductLocators(options) {
	writeProductLocator({ ...options, productHome: join(homedir(), '.pulse') });
	return writeCodexProductLocator(options);
}

function writeSharedProductLocator(options) {
	return writeProductLocator({ ...options, productHome: join(homedir(), '.pulse') });
}

function validInstallReceiptHostStatus(status) {
  if (status === undefined) return false;
  try {
    normalizePersonalInstallHostStatus(status);
    return true;
  } catch {
    return false;
  }
}

function personalInstallReceiptStatus(workspace) {
  const journalPath = join(DATA_DIR, 'runtime', 'install-journal.json');
  const journalStatus = privateStateFileStatus(journalPath);
  let resumableJournal = false;
  if (journalStatus === 'present_unverified') {
    try {
      const journal = JSON.parse(readFileSync(journalPath, 'utf8'));
      resumableJournal = journal?.schema === 'pulse.personal_install_journal.v1' &&
        ['planned', 'downloading', 'artifacts_staged', 'activating', 'activated'].includes(journal.phase) &&
        typeof journal.manifest_digest === 'string' && /^[a-f0-9]{64}$/.test(journal.manifest_digest);
      if (!resumableJournal) return 'invalid';
    } catch {
      return 'invalid';
    }
  } else if (journalStatus === 'invalid') {
    return 'invalid';
  }
  if (!workspace) return resumableJournal ? 'resumable' : 'missing';
  const path = join(DATA_DIR, 'receipts', 'install', `${workspace.workspace_id}.json`);
  const fileStatus = privateStateFileStatus(path);
  if (fileStatus === 'missing') return resumableJournal ? 'resumable' : 'missing';
  if (fileStatus !== 'present_unverified') return fileStatus;
  try {
    const receipt = JSON.parse(readFileSync(path, 'utf8'));
    if (!['pulse.personal_install_receipt.v1', 'pulse.personal_install_receipt.v2'].includes(receipt?.schema) ||
        receipt.workspace_id !== workspace.workspace_id || receipt.repository_id !== workspace.repository_id ||
        !['ready', 'warming', 'action_required', 'partial', 'blocked'].includes(receipt.outcome) ||
        (receipt.schema === 'pulse.personal_install_receipt.v2' && !validInstallReceiptHostStatus(receipt.host_status))) {
      return 'invalid';
    }
    if (receipt.outcome === 'ready') return 'ready';
    if (['warming', 'action_required', 'partial'].includes(receipt.outcome)) return 'resumable';
    return 'blocked';
  } catch {
    return 'invalid';
  }
}

function inspectPersonalPreflightState(workspace, release) {
  let principal;
  let principalStatus = 'missing';
  try {
    principal = readPersonalPrincipal();
    if (principal) principalStatus = 'ready';
  } catch {
    principalStatus = 'invalid';
  }
  let presence = 'not_installed';
  if (personalInstallUsesSyntheticOverrides()) {
    presence = currentNativePackedFixtureAttestation(workspace, release)
      ? 'not_installed'
      : 'synthetic_test_authority';
  } else {
    try {
      presence = inspectPresenceTrust({ probePublicKey: false, probeCapabilities: false }).status;
    } catch {
      presence = 'invalid';
    }
  }
  let bindingStatus = 'missing';
  let binding;
  if (principal && workspace) {
    const inspected = existingPersonalBinding(principal, { detected: { workspace } });
    bindingStatus = inspected.status;
    binding = inspected.binding;
  } else {
    const paths = personalBindingPaths();
    if (paths.registryPath && existsSync(paths.registryPath)) bindingStatus = 'present_unverified';
  }
  let vaultStatus = binding ? inspectVaultRuntime(vaultRuntimeFromBinding(binding)).status : 'missing';
  if (!/^[a-z0-9_]{1,64}$/.test(vaultStatus)) vaultStatus = 'unknown';
  let daemonStatus = 'missing';
  try {
    readProductActivation(DATA_DIR);
    daemonStatus = 'activated';
  } catch {
    if (existsSync(join(DATA_DIR, 'runtime', 'product-daemon.json'))) daemonStatus = 'invalid';
  }
  const pluginStatus = privateStateFileStatus(join(homedir(), '.pulse', 'product-locators.json'));
  const activationSetStatus = privateStateFileStatus(join(DATA_DIR, 'artifacts', 'active-release.json'));
  const hookTrustStatus = privateStateFileStatus(join(DATA_DIR, 'codex-hook-readiness.json'));
  return {
    binding: bindingStatus,
    daemon: daemonStatus,
    embedder: activationSetStatus,
    hook_trust: hookTrustStatus,
    install_receipt: personalInstallReceiptStatus(workspace),
    plugin: pluginStatus,
    presence,
    principal: principalStatus,
    runtime: activationSetStatus,
    vault: vaultStatus,
  };
}

function printPersonalInstallPlan(plan, { json = false } = {}) {
  if (json) console.log(JSON.stringify(plan, null, 2));
  else console.log(formatPersonalInstallPlan(plan));
}

function personalBindingPaths() {
  if (process.env.PULSE_TRUST_MODE === 'test') {
    return {
      registryPath: process.env.PULSE_BINDING_REGISTRY_PATH,
      publicKeyPath: process.env.PULSE_BINDING_PUBLIC_KEY_PATH,
      anchorPath: process.env.PULSE_BINDING_ANCHOR_PATH,
      rootPublicKey: false,
      rootAnchor: false,
    };
  }
  return { ...defaultBindingPaths(), rootPublicKey: true, rootAnchor: true };
}

function existingPersonalBinding(principal, plan) {
  const paths = personalBindingPaths();
  const present = [paths.registryPath, paths.anchorPath].map((path) => Boolean(path && existsSync(path)));
  if (present.every((value) => !value)) return { ready: false, status: 'missing' };
  if (present.some((value) => !value) || !paths.publicKeyPath || !existsSync(paths.publicKeyPath)) {
    return { ready: false, status: 'repair_required', reason_code: 'binding_repair_required' };
  }
  try {
    const binding = resolveWorkspaceBinding(process.env.PULSE_TRUST_MODE === 'test' ? {
      cwd: plan.detected.workspace.canonical_path,
      registryPath: paths.registryPath,
      publicKeyPath: paths.publicKeyPath,
      anchorPath: paths.anchorPath,
      rootAnchor: false,
    } : { cwd: plan.detected.workspace.canonical_path });
    if (binding.mode !== 'personal' || binding.principal_ref !== principal.principal_id) {
      return { ready: false, status: 'conflict', reason_code: 'binding_conflict' };
    }
    return { ready: true, status: 'ready', binding };
  } catch (error) {
    if (error instanceof BindingError && error.code === 'binding_missing') {
      return { ready: false, status: 'missing' };
    }
    return { ready: false, status: 'repair_required', reason_code: 'binding_repair_required' };
  }
}

async function createPersonalBindingForInstall(principal, plan) {
  const paths = personalBindingPaths();
  try {
    const fixture = nativePackedFixtureAttestation({
      cwd: plan.detected.workspace.canonical_path,
      dataDir: DATA_DIR,
      env: process.env,
      home: homedir(),
      plan,
    });
    const fixtureKeyPath = process.env.PULSE_NATIVE_PACKED_FIXTURE_BINDING_KEY_PATH;
    let fixturePrivateKey;
    if (fixture) {
      const info = lstatSync(fixtureKeyPath);
      const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
      if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.uid !== currentUID ||
          (info.mode & 0o077) !== 0 || info.size < 1 || info.size > 16 * 1024) {
        throw new PersonalInstallError('synthetic_authority_forbidden');
      }
      fixturePrivateKey = createPrivateKey(readFileSync(fixtureKeyPath));
      if (fixturePrivateKey.asymmetricKeyType !== 'ec' || fixturePrivateKey.asymmetricKeyDetails?.namedCurve !== 'prime256v1') {
        throw new PersonalInstallError('synthetic_authority_forbidden');
      }
    }
    return await createWorkspaceBinding({
      cwd: plan.detected.workspace.canonical_path,
      mode: 'personal',
      ...(fixture ? { port: fixture.port } : {}),
      principalID: principal.principal_id,
      ...(process.env.PULSE_TRUST_MODE === 'test' ? {
        ...paths,
        ...(fixture ? {
          signer: (bytes) => ({
            algorithm: 'es256', signature: cryptoSign('sha256', bytes, fixturePrivateKey).toString('base64'),
          }),
          anchorInstaller: async (bytes, { anchorPath }) => {
            mkdirSync(dirname(anchorPath), { recursive: true, mode: 0o700 });
            writeFileSync(anchorPath, bytes, { flag: 'wx', mode: 0o600 });
            chmodSync(anchorPath, 0o600);
          },
          anchorRemover: async ({ anchorPath }) => rmSync(anchorPath, { force: true }),
        } : {}),
      } : {}),
    });
  } catch (error) {
    if (/presence_denied|presence_invalid/i.test(error?.message ?? '')) {
      throw new PersonalInstallError('presence_denied');
    }
    throw error;
  }
}

function exactPersonalCore(binding) {
  const resolved = resolveCodexMcpRuntime(process.cwd());
  if (resolved.binding.binding_id !== binding?.binding_id ||
      resolved.runtime.store_id !== vaultRuntimeFromBinding(binding).store_id) {
    throw new PersonalInstallError('binding_repair_required');
  }
  return resolved;
}

function personalCoreContext(resolved, edge, liveStatus) {
  return Object.freeze({
    binding: resolved.binding,
    edge,
    live_status: liveStatus,
    resolved,
    store_id: resolved.runtime.store_id,
  });
}

async function inspectPersonalInstallCore(binding) {
  try {
    const resolved = exactPersonalCore(binding);
    const runtime = inspectVaultRuntime(resolved.runtime);
    if (runtime.status !== 'running') return { ready: false, reason_code: 'core_activation_required' };
    const edge = committedCodexProductEdge(readProductActivationBundle(DATA_DIR));
    const liveStatus = await boundPulseRequest(resolved, '/memory/status', { method: 'GET', timeoutMs: 1500 });
    return {
      ready: true,
      full_retrieval: liveStatus.full_retrieval === true,
      reason_code: 'core_verified',
      context: personalCoreContext(resolved, edge, liveStatus),
    };
  } catch {
    return { ready: false, reason_code: 'core_activation_required' };
  }
}

async function activatePersonalInstallCoreTransaction(binding) {
  await recoverBindingAuthority();
  const resolved = exactPersonalCore(binding);
  const releaseVaultActivation = await acquireVaultActivationLock(resolved.runtime);
  try {
      const previousDaemon = inspectVaultRuntime(resolved.runtime);
      if (previousDaemon.status === 'running') await assertVaultRuntimeHealthy(resolved.runtime);
      const snapshots = snapshotLocalFiles(activationFilePaths(resolved.binding));
      let installedRuntime;
      let runtimeInstalled = false;
      try {
        const managedRuntime = await ensureManagedProductRuntime(resolved.runtime, { publishConfig: false });
        if (previousDaemon.status === 'running' &&
            previousDaemon.managed_embedder?.config_digest !== managedRuntime.managed_embedder.config_digest) {
          await stopVaultRuntimeAndWait(resolved.runtime);
        }
        managedRuntime.managed_embedder = activateManagedEmbedderConfig(
          resolved.runtime, managedRuntime.managed_embedder,
        );
        installedRuntime = installCodexRuntime(managedRuntime.product_edge.runtime_root, DATA_DIR, {
          keepPrevious: true,
          signedEdge: managedRuntime.product_edge,
        });
        runtimeInstalled = true;
        if (!installedRuntime.ok) throw new Error('personal_core_runtime_install_failed');
        const runtimeStatus = inspectVaultRuntime(resolved.runtime);
        if (['stopped', 'crashed', 'running'].includes(runtimeStatus.status)) {
          await startVaultRuntime(resolved.runtime, {
            daemonPath: managedRuntime.daemon.path,
            managedEmbedder: managedRuntime.managed_embedder,
            host: 'pulse-product',
            allowRollback: false,
          });
        } else {
          throw new Error('personal_core_runtime_state_invalid');
        }
        await assertVaultRuntimeHealthy(resolved.runtime);
        writeProductDaemonActivation(managedRuntime, installedRuntime);
        const defaults = defaultBindingPaths();
        writeSharedProductLocator({
          codexHome: codexHomePath(),
          binding: resolved.binding,
          dataDir: DATA_DIR,
          registryPath: process.env.PULSE_BINDING_REGISTRY_PATH ?? defaults.registryPath,
          publicKeyPath: process.env.PULSE_BINDING_PUBLIC_KEY_PATH ?? defaults.publicKeyPath,
          anchorPath: process.env.PULSE_BINDING_ANCHOR_PATH ?? defaults.anchorPath,
          trustMode: process.env.PULSE_TRUST_MODE === 'test' ? 'test' : 'production',
        });
		finalizeCodexRuntimeInstall(DATA_DIR);
		commitPersonalRuntimeRelease(managedRuntime.verified_release, { dataDir: DATA_DIR });
      } catch (error) {
        if (process.env.PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION === '1') {
          const diagnostic = [error?.code, error?.message]
            .find((value) => typeof value === 'string' && /^[a-z0-9_]{1,128}$/i.test(value));
          process.stderr.write(`[pulse-native-fixture] core activation failed: ${diagnostic ?? 'activation_failed'}\n`);
        }
        const failures = [];
        if (runtimeInstalled) {
          try {
            const rollback = rollbackCodexRuntimeInstall(DATA_DIR);
            if (!rollback.ok) failures.push(new Error(rollback.detail));
          } catch (failure) { failures.push(failure); }
        }
        try { await stopUpgradedVaultBeforeFileRestore(resolved.runtime, previousDaemon); } catch (failure) { failures.push(failure); }
        try { restoreLocalFiles(snapshots); } catch (failure) { failures.push(failure); }
        try { await restoreVaultAfterFailedConnect(resolved.runtime, previousDaemon); } catch (failure) { failures.push(failure); }
        if (failures.length > 0) throw new Error('personal_core_activation_rollback_failed');
        throw error;
      }
  } finally {
    await releaseVaultActivation();
  }
}

async function activatePersonalInstallCore(binding) {
  const releaseActivation = await acquireProductActivationLock();
  try {
    await activatePersonalInstallCoreTransaction(binding);
  } finally {
    await releaseActivation();
  }
}

function personalInstallHostRegistry(targets) {
  const codexExecutable = targets.get('codex')?.executable_path;
  const claudeExecutable = targets.get('claude-code')?.executable_path;
  const captureFor = (context, host) => {
    const capture = safeReadJSON(join(context.resolved.runtime.data_dir, 'capture-state.json'));
    return captureEnabledForHost(capture, host);
  };
  const accessFor = (context, host) => {
    try {
      readProductHostAccess({ productHome: join(homedir(), '.pulse'), binding: context.binding, host });
      return true;
    } catch { return false; }
  };
  const lifecycleFor = async (context, host) => {
    try {
      const result = await boundPulseRequest(context.resolved, '/memory/lifecycle-readiness', {
        method: 'GET', timeoutMs: 1500,
      });
      if (result?.schema !== 'pulse.supported_host_lifecycle_readiness.v1' ||
          !Array.isArray(result.hosts) || result.hosts.length !== SUPPORTED_HOST_IDS.length) {
        throw new Error('supported_host_lifecycle_invalid');
      }
      const seen = new Set();
      for (const entry of result.hosts) {
        if (!entry || !SUPPORTED_HOST_IDS.includes(entry.host) || seen.has(entry.host) ||
            typeof entry.lifecycle_ready !== 'boolean' ||
            !['first_memory_pending', 'context_offer_pending', 'host_observation_pending', 'ready'].includes(entry.state) ||
            !Array.isArray(entry.milestones) || entry.milestones.some((value) =>
              !['write_receipt', 'session_context', 'prompt_context'].includes(value))) {
          throw new Error('supported_host_lifecycle_invalid');
        }
        seen.add(entry.host);
      }
      const selected = result.hosts.find((entry) => entry.host === host);
      if (!selected || selected.lifecycle_ready !== (selected.state === 'ready')) {
        throw new Error('supported_host_lifecycle_invalid');
      }
      return selected;
    } catch {
      return { host, state: 'first_memory_pending', lifecycle_ready: false, milestones: [] };
    }
  };
  return {
    'claude-code': {
      inspect: async (context) => {
        const result = inspectClaudeNativeProductPlugin(context.edge, claudeExecutable);
        const staticReady = result.ok === true && captureFor(context, 'claude-code') && accessFor(context, 'claude-code');
        const lifecycle = staticReady
          ? await lifecycleFor(context, 'claude-code')
          : { lifecycle_ready: false, milestones: [] };
        return {
          ready: staticReady,
          installed: result.ok === true,
          mcp_ready: staticReady,
          lifecycle_ready: staticReady && lifecycle.lifecycle_ready === true,
          milestones: lifecycle.milestones,
          reason_code: !staticReady
            ? (result.reason ?? 'claude_activation_required')
            : lifecycle.lifecycle_ready ? 'claude_lifecycle_verified' : 'claude_lifecycle_required',
        };
      },
      activate: async (context) => {
        activateClaudePlugin(context.edge, { executable: claudeExecutable });
        removeLegacyClaudeProductRegistration(claudeExecutable);
        writeCaptureStateFiles({
          globalDataDir: DATA_DIR, binding: context.binding, host: 'claude-code', enabled: true,
          reason: 'claude_code_native_plugin_connected',
        });
        writeProductHostAccess({
          productHome: join(homedir(), '.pulse'), binding: context.binding, host: 'claude-code',
        });
      },
    },
    codex: {
      inspect: async (context) => {
        const plugin = codexPluginStatus(codexExecutable);
        const exact = inspectCodexPluginCompatibility(plugin, context.edge);
        const staticReady = exact.ok === true && captureFor(context, 'codex') && accessFor(context, 'codex');
        const lifecycle = staticReady
          ? await lifecycleFor(context, 'codex')
          : { lifecycle_ready: false, milestones: [] };
        return {
          ready: staticReady,
          installed: exact.ok === true,
          mcp_ready: staticReady,
          lifecycle_ready: staticReady && lifecycle.lifecycle_ready === true,
          milestones: lifecycle.milestones,
          reason_code: !staticReady
            ? (exact.reason ?? 'codex_activation_required')
            : lifecycle.lifecycle_ready ? 'codex_lifecycle_verified' : 'codex_lifecycle_required',
        };
      },
      activate: async (context) => {
        const localFiles = snapshotLocalFiles([
          ...captureStatePaths(DATA_DIR, context.binding),
          join(codexHomePath(), 'pulse', 'product-locators.json'),
          productHostAccessPath({ productHome: join(homedir(), '.pulse'), binding: context.binding, host: 'codex' }),
          ...inspectLegacyPulseHookFiles({ cwd: process.cwd() }).files.map((file) => file.path),
        ]);
        const transaction = snapshotCodexHostActivation(codexExecutable);
        try {
          const { source } = activateExactCodexProductEdge(context.edge, transaction, codexExecutable);
          const migration = migrateLegacyPulseHookFiles({ cwd: process.cwd() });
          const defaults = defaultBindingPaths();
          writeCodexProductLocator({
            codexHome: codexHomePath(), binding: context.binding, dataDir: DATA_DIR,
            registryPath: process.env.PULSE_BINDING_REGISTRY_PATH ?? defaults.registryPath,
            publicKeyPath: process.env.PULSE_BINDING_PUBLIC_KEY_PATH ?? defaults.publicKeyPath,
            anchorPath: process.env.PULSE_BINDING_ANCHOR_PATH ?? defaults.anchorPath,
            trustMode: process.env.PULSE_TRUST_MODE === 'test' ? 'test' : 'production',
          });
          writeCaptureStateFiles({
            globalDataDir: DATA_DIR, binding: context.binding, host: 'codex', enabled: true,
            reason: 'codex_plugin_connected',
          });
          writeProductHostAccess({
            productHome: join(homedir(), '.pulse'), binding: context.binding, host: 'codex',
          });
          discardPluginTreeSnapshot(transaction.pluginTree);
          return { migration, source };
        } catch (error) {
          const failures = rollbackCodexHostActivation(transaction, codexExecutable);
          try { restoreLocalFiles(localFiles); } catch (failure) { failures.push(failure); }
          if (failures.length > 0) throw new PersonalInstallError('codex_activation_rollback_failed');
          throw error;
        }
      },
    },
    cursor: {
      inspect: async (context) => {
        const result = inspectCursorPlugin({
          cursorHome: cursorHomePath(), expectedDigest: context.edge.plugin_tree_digest,
        });
        const staticReady = result.ready === true && captureFor(context, 'cursor') && accessFor(context, 'cursor');
        const lifecycle = staticReady
          ? await lifecycleFor(context, 'cursor')
          : { lifecycle_ready: false, milestones: [] };
        return {
          ready: staticReady,
          installed: result.ready === true,
          mcp_ready: staticReady,
          lifecycle_ready: staticReady && lifecycle.lifecycle_ready,
          milestones: lifecycle.milestones,
          reason_code: !staticReady
            ? (result.reason ?? 'cursor_activation_required')
            : lifecycle.lifecycle_ready ? 'cursor_lifecycle_verified' : 'cursor_lifecycle_required',
        };
      },
      activate: async (context) => {
        installCursorPlugin(context.edge.plugin_root, {
          cursorHome: cursorHomePath(), expectedDigest: context.edge.plugin_tree_digest,
        });
        writeCaptureStateFiles({
          globalDataDir: DATA_DIR, binding: context.binding, host: 'cursor', enabled: true,
          reason: 'cursor_plugin_connected',
        });
        writeProductHostAccess({
          productHome: join(homedir(), '.pulse'), binding: context.binding, host: 'cursor',
        });
      },
    },
  };
}

function personalInstallCoreHealth(core, activation) {
  if (core?.ready !== true) {
    return { ready: false, full_retrieval: false, outcome: 'action_required', reason_code: 'daemon_unavailable' };
  }
  if (core.full_retrieval !== true) {
    return { ready: false, full_retrieval: false, outcome: 'action_required', reason_code: 'full_retrieval_unavailable' };
  }
  if (activation?.product_ready !== true) {
    return {
      ready: false, full_retrieval: true, outcome: 'action_required',
      reason_code: activation?.hosts?.[0]?.reason_code ?? 'supported_harness_activation_failed',
      host_status: activation,
    };
  }
  return {
    ready: true,
    full_retrieval: true,
    outcome: 'ready',
    reason_code: activation.parity === 'complete' ? 'personal_live_ready' : 'personal_live_ready_host_parity_degraded',
    host_status: activation,
  };
}

function personalInstallDependencies(plan) {
  const nativeFixture = nativePackedFixtureAttestation({
    cwd: plan?.detected?.workspace?.canonical_path,
    dataDir: DATA_DIR,
    env: process.env,
    home: homedir(),
    plan,
  });
  if (personalInstallUsesSyntheticOverrides() && !nativeFixture) {
    throw new PersonalInstallError('synthetic_authority_forbidden');
  }
  const hosts = plan?.detected?.hosts;
  if (!Array.isArray(hosts) || hosts.filter((host) => host?.activation_target === true).length < 1) {
    throw new PersonalInstallError('supported_harness_missing');
  }
  const targets = new Map(hosts
    .filter((host) => host?.activation_target === true)
    .map((host) => [host.host, host]));
  const target = (host) => targets.get(host);
  const registry = personalInstallHostRegistry(targets);
  let identitiesVerified = false;
  function verifyExactCLIHosts() {
    if (identitiesVerified) return;
    const codex = target('codex');
    if (codex) {
      const codexExecutable = codex.executable_path;
      const expectedCodexDigest = codex.executable_sha256;
      if (typeof codexExecutable !== 'string' || !isAbsolute(codexExecutable) ||
          typeof expectedCodexDigest !== 'string' || !/^[a-f0-9]{64}$/.test(expectedCodexDigest)) {
        throw new PersonalInstallError('codex_identity_invalid');
      }
    const probe = detectCodexCLI({
      codexPath: codexExecutable,
      versionProbe: (path) => {
        const command = path.endsWith('.js') ? process.execPath : path;
        const commandArgs = command === process.execPath ? [path, '--version'] : ['--version'];
        return spawnSync(command, commandArgs, {
          encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5000, killSignal: 'SIGTERM',
        });
      },
    });
    if (!probe.available) throw new PersonalInstallError(probe.reason_code ?? 'codex_probe_failed');
    if (probe.executable_path !== codexExecutable || probe.executable_sha256 !== expectedCodexDigest) {
      throw new PersonalInstallError('codex_identity_changed');
    }
    }
    const claude = target('claude-code');
    if (claude) {
      const claudeExecutable = claude.executable_path;
      const expectedClaudeDigest = claude.executable_sha256;
      if (typeof claudeExecutable !== 'string' || !isAbsolute(claudeExecutable) ||
          typeof expectedClaudeDigest !== 'string' || !/^[a-f0-9]{64}$/.test(expectedClaudeDigest)) {
        throw new PersonalInstallError('claude_identity_invalid');
      }
      const probe = detectClaudeCodeCLI({
        claudePath: claudeExecutable,
        versionProbe: (path) => {
          const command = path.endsWith('.js') ? process.execPath : path;
          const commandArgs = command === process.execPath ? [path, '--version'] : ['--version'];
          return spawnSync(command, commandArgs, {
            encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5000, killSignal: 'SIGTERM',
          });
        },
      });
      if (!probe.available) throw new PersonalInstallError(probe.reason_code ?? 'claude_probe_failed');
      const parts = probe.version.split('.').map(Number);
      if (parts[0] < 2 || (parts[0] === 2 && parts[1] < 1) ||
          (parts[0] === 2 && parts[1] === 1 && parts[2] < 196)) {
        throw new PersonalInstallError('claude_version_invalid');
      }
      if (probe.executable_path !== claudeExecutable || probe.executable_sha256 !== expectedClaudeDigest) {
        throw new PersonalInstallError('claude_identity_changed');
      }
    }
    const cursor = target('cursor');
    if (cursor) {
      const expectedAppPath = cursor.app_path;
      const expectedExecutablePath = cursor.executable_path;
      const expectedExecutableDigest = cursor.executable_sha256;
      if (typeof expectedAppPath !== 'string' || !isAbsolute(expectedAppPath) ||
          typeof expectedExecutablePath !== 'string' || !isAbsolute(expectedExecutablePath) ||
          typeof expectedExecutableDigest !== 'string' || !/^[a-f0-9]{64}$/.test(expectedExecutableDigest)) {
        throw new PersonalInstallError('cursor_identity_invalid');
      }
      const probe = detectCursorInstallation({ appCandidates: [expectedAppPath] });
      if (!probe.available || probe.app_path !== expectedAppPath ||
          probe.executable_path !== expectedExecutablePath || probe.executable_sha256 !== expectedExecutableDigest) {
        throw new PersonalInstallError('cursor_identity_changed');
      }
    }
    identitiesVerified = true;
  }
  return {
    inspectRuntime: async () => {
      verifyExactCLIHosts();
      const activationSetStatus = privateStateFileStatus(join(DATA_DIR, 'artifacts', 'active-release.json'));
      try {
        const inspection = inspectPersonalRuntime(packagedPersonalRuntimeOptions(DATA_DIR));
        if (inspection.ready) return inspection;
        if (activationSetStatus === 'missing') return inspection;
        throw new PersonalInstallError('runtime_repair_required');
      } catch (error) {
        if (error instanceof PersonalInstallError) throw error;
        throw new PersonalInstallError(
          typeof error?.code === 'string' ? error.code : 'runtime_inspection_failed',
        );
      }
    },
    provisionRuntime: async () => provisionPersonalRuntime(packagedPersonalRuntimeOptions(DATA_DIR)),
    inspectPresence: async () => process.env.PULSE_TRUST_MODE === 'test'
      ? { ready: true, status: 'synthetic_test_authority' }
      : inspectPresenceTrust({ probePublicKey: true }),
    installPresence: async () => {
      try {
        await installPresenceTrust({ confirmation: TRUST_INSTALL_CONFIRMATION });
      } catch (error) {
        if (/denied|cancel|helper_public_key_failed/i.test(error?.message ?? '')) {
          throw new PersonalInstallError('presence_denied');
        }
        throw error;
      }
    },
    inspectPrincipal: async () => {
      try { return readPersonalPrincipal(); } catch (error) {
        if (error instanceof PersonalPrincipalError) throw new PersonalInstallError('principal_repair_required');
        throw error;
      }
    },
    createPrincipal: async () => ensurePersonalPrincipal({ consentGranted: true }),
    inspectBinding: async ({ principal, plan }) => existingPersonalBinding(principal, plan),
    createBinding: async ({ principal, plan }) => createPersonalBindingForInstall(principal, plan),
    inspectCore: async ({ binding }) => inspectPersonalInstallCore(binding),
    activateCore: async ({ binding }) => activatePersonalInstallCore(binding),
    inspectActivation: async ({ core }) => inspectDetectedPersonalHosts({
      context: core.context, hosts, registry,
    }),
    activateHosts: async ({ core, activation }) => activateDetectedPersonalHosts({
      context: core.context, hosts, registry, prior: activation,
    }),
    inspectHealth: async ({ core, activation }) => personalInstallCoreHealth(core, activation),
    writeReceipt: async (receipt) => writePersonalInstallReceipt(receipt, { dataDir: resolve(DATA_DIR) }),
  };
}

function readSecret({ create = false } = {}) {
  return readSecretFromDataDir(DATA_DIR, { create });
}

function readSecretFromDataDir(dataDir, { create = false } = {}) {
  const secretPath = join(dataDir, 'secret.key');
  if (!existsSync(secretPath) && create) {
    mkdirSync(dataDir, { recursive: true, mode: 0o700 });
    writeFileSync(secretPath, randomBytes(32).toString('hex'), { mode: 0o600 });
  }
  if (!existsSync(secretPath)) {
    throw new Error(
      `Pulse secret not found at ${secretPath}. Run "pulse init claude-code" or start the Go server once.`,
    );
  }
  return readFileSync(secretPath, 'utf8').trim();
}

async function pulseFetch(path, options = {}) {
  const secret = existsSync(SECRET_PATH) ? readSecret() : '';
  const method = options.method ?? 'POST';
  const requestURL = `${DEFAULT_BASE_URL.replace(/\/$/, '')}${path}`;
  let headers;
  if (isLoopbackPulseBase(DEFAULT_BASE_URL)) {
    headers = buildPulseRequestHeaders(DEFAULT_BASE_URL, { ipcSecret: secret });
  } else {
    if (process.env.PULSE_REMOTE_BEARER?.trim()) {
      throw new Error('remote_auth_static_bearer_forbidden');
    }
    const credentialRef = process.env.PULSE_REMOTE_CREDENTIAL_REF?.trim();
    if (!credentialRef) throw new Error('remote_auth_credential_ref_required');
    headers = buildSenderConstrainedRemoteHeaders(requestURL, {
      method,
      credentialStore: createOSCredentialStore(),
      credentialRef,
    });
  }
	let invocation;
  if (method !== 'GET') {
		invocation = options.idempotencyKey
			? { key: options.idempotencyKey }
			: acquireCLIInvocation(DATA_DIR, path, options.body);
		headers['Idempotency-Key'] = invocation.key;
	}
  const controller = options.timeoutMs ? new AbortController() : undefined;
  const timer = controller ? setTimeout(() => controller.abort(), options.timeoutMs) : undefined;
  let response;
  try {
    response = await fetch(requestURL, {
      method,
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: controller?.signal,
    });
  } finally {
    if (timer) {
      clearTimeout(timer);
    }
  }
	return consumeCLIResponse(response, invocation);
}

function mcpConfig(secret) {
  const localEntrypoint = localMcpEntrypoint();
  // An entrypoint inside the npx cache dangles once the cache is pruned or
  // the dist-tag moves — register the npx package form instead.
  const durableEntrypoint =
    localEntrypoint && !/[\\/]_npx[\\/]/.test(localEntrypoint) ? localEntrypoint : undefined;
  const overridePackage = process.env.PULSE_MCP_PACKAGE;
  const commandConfig = durableEntrypoint
    ? { command: process.execPath, args: [durableEntrypoint] }
    : {
        command: 'npx',
        args: overridePackage
          ? [
              '-y',
              overridePackage,
              // The main package needs the subcommand; the raw connector does not.
              ...(/^@zbs-gg\/pulse(@|$)/.test(overridePackage) ? ['mcp'] : []),
            ]
          : ['-y', '@zbs-gg/pulse@preview', 'mcp'],
      };
  return {
    type: 'stdio',
    ...commandConfig,
    env: {
      PULSE_BASE_URL: DEFAULT_BASE_URL,
      // Daemon installs must fail loudly when the daemon is down instead of
      // silently splitting writes into the standalone lite store.
      PULSE_MCP_MODE: 'daemon',
      PULSE_DATA_DIR: DATA_DIR,
      PULSE_API_KEY: secret,
    },
  };
}

function localMcpEntrypoint() {
  if (process.env.PULSE_MCP_ENTRYPOINT) {
    return existsSync(process.env.PULSE_MCP_ENTRYPOINT) ? process.env.PULSE_MCP_ENTRYPOINT : undefined;
  }
  const candidate = resolve(dirname(CLI_PATH), '..', '..', '..', 'mcp', 'dist', 'index.js');
  return existsSync(candidate) ? candidate : undefined;
}

function mcpServerEntrypoint() {
  if (process.env.PULSE_MCP_ENTRYPOINT) {
    return existsSync(process.env.PULSE_MCP_ENTRYPOINT) ? process.env.PULSE_MCP_ENTRYPOINT : undefined;
  }
  // A repo checkout must use its freshly built MCP server even when a stale
  // prepack artifact is present. Published packages have no sibling mcp/
  // checkout and therefore fall through to the vendored build.
  const checkout = resolve(CLI_PACKAGE_ROOT, '..', '..', 'mcp', 'dist', 'index.js');
  if (existsSync(checkout)) return checkout;
  const vendored = join(CLI_PACKAGE_ROOT, 'vendor', 'pulse-mcp-dist', 'index.js');
  return existsSync(vendored) ? vendored : undefined;
}

async function runMcpServer() {
  const entrypoint = mcpServerEntrypoint();
  if (!entrypoint) {
    console.error(
      '[pulse] MCP server build not found. In a repo checkout run: cd mcp && npm ci && npm run build',
    );
    process.exit(1);
  }
  const module = await import(pathToFileURL(entrypoint).href);
  if (typeof module.runMcpEntrypoint !== 'function') {
    throw new Error('Pulse MCP server entrypoint is incompatible with this CLI');
  }
  await module.runMcpEntrypoint();
}

async function runProductMcpServer(host) {
  if (!['codex', 'claude-code', 'cursor'].includes(host)) throw new Error('unsupported product MCP host');
  await recoverBindingAuthority();
  const inspected = inspectProductWorkspaceBinding({ cwd: process.cwd() });
  if (inspected.status === 'unassigned') {
    process.chdir(inspected.workspace.canonical_path);
    process.env.PULSE_RUNTIME_MODE = 'local-stdio';
    process.env.PULSE_MCP_MODE = 'daemon';
    process.env.PULSE_HOST_ADAPTER = host;
    process.env.PULSE_HOST_WORKSPACE = inspected.workspace.canonical_path;
    process.env.PULSE_PRODUCT_UNASSIGNED = inspected.reason;
    process.env.PULSE_HOST_AUTHORITY_MODULE = pathToFileURL(
      join(CLI_PACKAGE_ROOT, 'src', 'codex-runtime.js'),
    ).href;
    process.env.PULSE_HOST_RUNTIME_MODULE = pathToFileURL(
      join(CLI_PACKAGE_ROOT, 'src', 'codex-runtime.js'),
    ).href;
    await runMcpServer();
    return;
  }
  const resolved = resolveCodexMcpRuntime(process.cwd());
  // Binding resolution intentionally accepts a nested launch directory. The
  // MCP process then pins itself to the signed canonical root so every tool
  // call revalidates the same authority instead of failing on nested cwd.
  process.chdir(resolved.binding.workspace.canonical_path);
  const capturePath = join(resolved.runtime.data_dir, 'capture-state.json');
  const capture = safeReadJSON(capturePath);
  if (!captureEnabledForHost(capture, host)) {
    throw new Error(`Pulse ${host} capture is disabled for this bound workspace`);
  }
	await ensureActivatedVaultRuntime(resolved);
  process.env.PULSE_BASE_URL = resolved.runtime.base_url;
  process.env.PULSE_DATA_DIR = resolved.runtime.data_dir;
  process.env.PULSE_RUNTIME_MODE = 'local-stdio';
  process.env.PULSE_MCP_MODE = 'daemon';
  process.env.PULSE_HOST_ADAPTER = host;
  process.env.PULSE_BINDING_DIGEST = resolved.binding.binding_digest;
  process.env.PULSE_RESOLVER_EPOCH = String(resolved.binding.resolver_epoch);
  process.env.PULSE_HOST_WORKSPACE = resolved.binding.workspace.canonical_path;
  process.env.PULSE_PRODUCT_BINDING_MODE = resolved.binding.mode;
  delete process.env.PULSE_PRODUCT_UNASSIGNED;
  process.env.PULSE_HOST_AUTHORITY_MODULE = pathToFileURL(
    join(CLI_PACKAGE_ROOT, 'src', 'codex-runtime.js'),
  ).href;
  process.env.PULSE_HOST_RUNTIME_MODULE = pathToFileURL(
    join(CLI_PACKAGE_ROOT, 'src', 'codex-runtime.js'),
  ).href;
  await runMcpServer();
}

async function recoverBindingAuthority() {
  const testAuthority = process.env.PULSE_TRUST_MODE === 'test';
  const registryPath = process.env.PULSE_BINDING_REGISTRY_PATH;
  const publicKeyPath = process.env.PULSE_BINDING_PUBLIC_KEY_PATH;
  const anchorPath = process.env.PULSE_BINDING_ANCHOR_PATH;
  if (testAuthority) {
    if (!registryPath || !publicKeyPath || !anchorPath) {
      throw new Error('synthetic test authority requires registry, public key, and anti-rollback anchor paths');
    }
    await recoverWorkspaceBindingTransaction({
      registryPath, publicKeyPath, anchorPath, rootPublicKey: false, rootAnchor: false,
    });
    return;
  }
  await recoverWorkspaceBindingTransaction();
}

async function runCodexMcpServer() {
  await runProductMcpServer('codex');
}

async function runClaudeMcpServer() {
  await runProductMcpServer('claude-code');
}

async function runCursorMcpServer() {
  await runProductMcpServer('cursor');
}

function codexCommand(args, { executable = 'codex' } = {}) {
  if (executable !== 'codex' && (!isAbsolute(executable) || resolve(executable) !== executable)) {
    throw new Error('codex executable must be an absolute canonical path');
  }
  const command = executable !== 'codex' && executable.endsWith('.js') ? process.execPath : executable;
  const commandArgs = command === process.execPath ? [executable, ...args] : args;
  const result = spawnSync(command, commandArgs, {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    timeout: 30_000,
    killSignal: 'SIGTERM',
  });
  if (result.status !== 0) {
    const detail = `${result.stderr || result.stdout || result.error?.message || ''}`.trim().slice(0, 500);
    throw new Error(`codex ${args.join(' ')} failed${detail ? `: ${detail}` : ''}`);
  }
  return `${result.stdout || ''}${result.stderr || ''}`.trim();
}

function codexMarketplaceStatus(codexExecutable = 'codex') {
	try {
		return parseCodexMarketplaceList(codexCommand(
			['plugin', 'marketplace', 'list'], { executable: codexExecutable },
		));
	} catch (error) {
		return { configured: false, root: undefined, error: error.message };
	}
}

function requireExactCodexMarketplace(source, codexExecutable = 'codex', knownStatus) {
	if (typeof source !== 'string' || !isAbsolute(source)) {
		throw new Error('codex_plugin_source_untrusted');
	}
	const canonicalSource = realpathSync(resolve(source));
	const before = knownStatus ?? codexMarketplaceStatus(codexExecutable);
	if (before.error) throw new Error(before.error);
	if (before.configured) {
		if (!before.root || realpathSync(resolve(before.root)) !== canonicalSource) {
			throw new Error('codex_marketplace_source_conflict');
		}
		return before;
	}
	codexCommand(['plugin', 'marketplace', 'add', canonicalSource], { executable: codexExecutable });
	const after = codexMarketplaceStatus(codexExecutable);
	if (!after.configured || !after.root || realpathSync(resolve(after.root)) !== canonicalSource) {
		throw new Error('codex_plugin_source_untrusted');
	}
	return after;
}

function snapshotCodexHostActivation(codexExecutable = 'codex') {
	const plugin = codexPluginStatus(codexExecutable);
	if (plugin.installed && !plugin.enabled) {
		throw new Error('Pulse Codex plugin is installed but disabled; remove it explicitly before product activation');
	}
	return {
		marketplace: codexMarketplaceStatus(codexExecutable),
		plugin,
		pluginTree: snapshotPluginTree(plugin),
	};
}

function activateExactCodexProductEdge(productEdge, transaction, codexExecutable = 'codex') {
	const marketplaceSnapshot = materializeCodexMarketplaceSnapshot(productEdge, DATA_DIR);
	const source = marketplaceSnapshot.marketplace_root;
	let currentMarketplace = transaction.marketplace;
	if (currentMarketplace.configured && currentMarketplace.root &&
			realpathSync(resolve(currentMarketplace.root)) !== source) {
		if (!isManagedCodexMarketplaceRoot(currentMarketplace.root)) {
			throw new Error('codex_marketplace_source_conflict');
		}
		if (codexPluginStatus(codexExecutable).installed) {
			codexCommand(['plugin', 'remove', 'pulse@zbs-gg'], { executable: codexExecutable });
		}
		codexCommand(['plugin', 'marketplace', 'remove', 'zbs-gg'], { executable: codexExecutable });
		currentMarketplace = undefined;
	}
	requireExactCodexMarketplace(source, codexExecutable, currentMarketplace);
	const marketplaceAfterRegistration = inspectCodexMarketplaceSnapshot(productEdge, DATA_DIR);
	if (!marketplaceAfterRegistration.ok) throw new Error(marketplaceAfterRegistration.reason);
	let plugin = codexPluginStatus(codexExecutable);
	let pluginCompatibility = inspectCodexPluginCompatibility(plugin, productEdge);
	if (plugin.installed && !pluginCompatibility.ok) {
		codexCommand(['plugin', 'remove', 'pulse@zbs-gg'], { executable: codexExecutable });
		plugin = { installed: false, enabled: false, path: undefined };
	}
	if (!plugin.installed) {
		codexCommand(['plugin', 'add', 'pulse@zbs-gg'], { executable: codexExecutable });
		plugin = codexPluginStatus(codexExecutable);
		pluginCompatibility = inspectCodexPluginCompatibility(plugin, productEdge);
	}
	const pluginMcp = checkCodexPluginMcp(plugin);
	const marketplaceAfterPluginInstall = inspectCodexMarketplaceSnapshot(productEdge, DATA_DIR);
	if (!pluginCompatibility.ok) {
		throw new Error(pluginCompatibility.reason ?? plugin.error ?? 'Pulse plugin validation failed');
	}
	if (!pluginMcp.ok) throw new Error(pluginMcp.detail ?? 'Pulse plugin validation failed');
	if (!marketplaceAfterPluginInstall.ok) throw new Error(marketplaceAfterPluginInstall.reason);
	return { plugin, source };
}

function rollbackCodexHostActivation(transaction, codexExecutable = 'codex') {
	const failures = [];
	try {
		const currentPlugin = codexPluginStatus(codexExecutable);
		if (currentPlugin.error) throw new Error(currentPlugin.error);
		if (currentPlugin.installed) {
			codexCommand(['plugin', 'remove', 'pulse@zbs-gg'], { executable: codexExecutable });
		}
	} catch (failure) { failures.push(failure); }
	try {
		const currentMarketplace = codexMarketplaceStatus(codexExecutable);
		if (currentMarketplace.error) throw new Error(currentMarketplace.error);
		if (currentMarketplace.configured) {
			codexCommand(['plugin', 'marketplace', 'remove', 'zbs-gg'], { executable: codexExecutable });
		}
		if (transaction.marketplace.configured && transaction.marketplace.root) {
			requireExactCodexMarketplace(transaction.marketplace.root, codexExecutable);
		}
	} catch (failure) { failures.push(failure); }
	if (transaction.plugin.installed) {
		try { restorePluginTree(transaction.pluginTree); } catch (failure) { failures.push(failure); }
	}
	return failures;
}

function isManagedCodexMarketplaceRoot(path) {
	try {
		const candidate = realpathSync(resolve(path));
		const roots = [
			join(DATA_DIR, 'artifacts', 'pulse-plugin-runtime', 'versions'),
			join(DATA_DIR, 'runtime', 'codex-marketplaces'),
		].flatMap((root) => {
			try { return [realpathSync(root)]; } catch { return []; }
		});
		return roots.some((root) => candidate.startsWith(`${root}${sep}`));
	} catch { return false; }
}

function snapshotLocalFiles(paths) {
	return [...new Set(paths)].map((path) => {
		if (!existsSync(path)) return { path, existed: false };
		const info = lstatSync(path);
		if (!info.isFile() || info.isSymbolicLink()) {
			throw new Error(`refusing to snapshot unsafe activation file: ${path}`);
		}
		return { path, existed: true, bytes: readFileSync(path), mode: info.mode & 0o777 };
	});
}

async function acquireProductActivationLock() {
	const directory = join(DATA_DIR, 'runtime');
	mkdirSync(directory, { recursive: true, mode: 0o700 });
	const path = join(directory, 'product-activation.lock');
	const lockf = '/usr/bin/lockf';
	if (!existsSync(lockf)) throw new Error('Pulse product activation requires the OS advisory lock service');
	const helperSource = [
		'process.stdout.write("pulse-product-lock-ready\\n");',
		'process.stdin.resume();',
		'process.stdin.on("end", () => process.exit(0));',
		'process.stdin.on("error", () => process.exit(0));',
	].join('');
	const child = spawn(lockf, ['-k', '-t', '0', path, process.execPath, '-e', helperSource], {
		stdio: ['pipe', 'pipe', 'pipe'],
	});
	let stderr = '';
	child.stderr.setEncoding('utf8');
	child.stderr.on('data', (chunk) => { stderr += chunk; });
	await new Promise((resolveReady, rejectReady) => {
		const timer = setTimeout(() => {
			child.kill('SIGTERM');
			rejectReady(new Error('Pulse product activation lock service timed out'));
		}, 3000);
		child.stdout.setEncoding('utf8');
		child.stdout.once('data', (chunk) => {
			if (!String(chunk).includes('pulse-product-lock-ready')) return;
			clearTimeout(timer);
			resolveReady();
		});
		child.once('exit', (status) => {
			clearTimeout(timer);
			rejectReady(new Error(status === 75
				? 'another Pulse product activation is running'
				: `Pulse product activation lock failed${stderr.trim() ? `: ${stderr.trim()}` : ''}`));
		});
		child.once('error', (error) => {
			clearTimeout(timer);
			rejectReady(error);
		});
	});
	chmodSync(path, 0o600);
	let released = false;
	return async () => {
		if (released) return;
		released = true;
		child.stdin.end();
		await new Promise((resolveExit, rejectExit) => {
			const timer = setTimeout(() => {
				child.kill('SIGTERM');
				rejectExit(new Error('Pulse product activation lock did not release'));
			}, 3000);
			child.once('exit', (status) => {
				clearTimeout(timer);
				if (status === 0) resolveExit();
				else rejectExit(new Error(`Pulse product activation lock exited ${status}`));
			});
		});
	};
}

function snapshotPluginTree(plugin) {
	if (!plugin?.installed || !plugin.path) return undefined;
	const root = dirname(realpathSync(plugin.path));
	const backup = join(DATA_DIR, 'runtime', `plugin-backup-${process.pid}-${randomBytes(8).toString('hex')}`);
	cpSync(root, backup, { recursive: true, dereference: false });
	return { root, backup };
}

function restorePluginTree(snapshot) {
	if (!snapshot) return;
	rmSync(snapshot.root, { recursive: true, force: true });
	renameSync(snapshot.backup, snapshot.root);
}

function discardPluginTreeSnapshot(snapshot) {
	if (snapshot) rmSync(snapshot.backup, { recursive: true, force: true });
}

function restoreLocalFiles(snapshots) {
	for (const snapshot of snapshots) {
		if (!snapshot.existed) {
			rmSync(snapshot.path, { force: true });
			continue;
		}
		mkdirSync(dirname(snapshot.path), { recursive: true, mode: 0o700 });
		const temporary = `${snapshot.path}.${process.pid}.${Date.now()}.restore`;
		try {
			writeFileSync(temporary, snapshot.bytes, { mode: snapshot.mode, flag: 'wx' });
			renameSync(temporary, snapshot.path);
		} finally {
			rmSync(temporary, { force: true });
		}
	}
}

function artifactActivationFilePaths(dataDir) {
	const root = join(resolve(dataDir), 'artifacts');
	const paths = [join(root, 'active-release.json')];
	if (!existsSync(root)) return paths;
	for (const entry of readdirSync(root, { withFileTypes: true })) {
		if (!entry.isDirectory() || !/^[a-z0-9][a-z0-9._-]{0,127}$/.test(entry.name)) continue;
		paths.push(join(root, entry.name, 'current.json'), join(root, entry.name, 'previous.json'));
	}
	return paths;
}

function sameManagedEmbedder(left, right) {
	if (!left || !right) return left === right;
	return left.config_path === right.config_path && left.config_digest === right.config_digest &&
		left.embedder_runtime_activation_digest === right.embedder_runtime_activation_digest &&
		left.model_activation_digest === right.model_activation_digest;
}

async function stopUpgradedVaultBeforeFileRestore(runtime, previous) {
	const current = inspectVaultRuntime(runtime);
	if (current.status !== 'running' && current.status !== 'crashed') return;
	const alreadyPrevious = previous.status === 'running' && current.status === 'running' &&
		current.executable === previous.executable && current.executable_digest === previous.executable_digest &&
		sameManagedEmbedder(current.managed_embedder, previous.managed_embedder);
	if (!alreadyPrevious) await stopVaultRuntimeAndWait(runtime);
}

async function restoreVaultAfterFailedConnect(runtime, previous) {
	const current = inspectVaultRuntime(runtime);
	if (previous.status === 'running') {
		if (current.status !== 'running' || current.executable !== previous.executable ||
			current.executable_digest !== previous.executable_digest ||
			!sameManagedEmbedder(current.managed_embedder, previous.managed_embedder)) {
			if (current.status === 'running' || current.status === 'crashed') {
				await stopVaultRuntimeAndWait(runtime);
			}
			await startVaultRuntime(runtime, {
					daemonPath: previous.executable, managedEmbedder: previous.managed_embedder,
					host: 'pulse-product', allowRollback: false,
			});
		}
		await assertVaultRuntimeHealthy(runtime);
		return;
	}
	if (current.status === 'running' || current.status === 'crashed') {
		await stopVaultRuntimeAndWait(runtime);
		return;
	}
	if (current.status !== 'stopped') {
		throw new Error(`cannot restore vault after failed connect: ${current.status}`);
	}
}

function activationFilePaths(binding, { includeClaude = false, includeCodex = false } = {}) {
	const paths = [
			...artifactActivationFilePaths(DATA_DIR),
			...captureStatePaths(DATA_DIR, binding),
			join(DATA_DIR, 'runtime', 'product-daemon.json'),
			join(homedir(), '.pulse', 'product-locators.json'),
			resolve(process.cwd(), '.gitignore'),
		];
	if (binding) {
		paths.push(join(
			binding.mode === 'personal' ? binding.personal.data_dir : binding.desk.data_dir,
			'runtime', 'managed-embedder.json',
		));
	}
	if (includeClaude) {
		paths.push(
			resolve(process.cwd(), '.mcp.json'),
			resolve(process.cwd(), '.claude', 'settings.local.json'),
		);
		if (binding) paths.push(productHostAccessPath({
			productHome: join(homedir(), '.pulse'), binding, host: 'claude-code',
		}));
	}
	if (includeCodex) {
		paths.push(
			join(codexHomePath(), 'config.toml'),
			join(codexHomePath(), 'pulse', 'product-locators.json'),
		);
		for (const file of inspectLegacyPulseHookFiles({ cwd: process.cwd() }).files) paths.push(file.path);
		if (binding) paths.push(productHostAccessPath({
			productHome: join(homedir(), '.pulse'), binding, host: 'codex',
		}));
	}
	return paths;
}

async function connectCodex({ codexExecutable = 'codex' } = {}) {
	const release = await acquireProductActivationLock();
	try {
		await connectCodexActivation({ codexExecutable });
	} finally {
		await release();
	}
}

async function connectCodexActivation({ codexExecutable = 'codex' } = {}) {
  if (codexExecutable === 'codex') requireCommand('codex');
  await recoverBindingAuthority();
  const resolved = resolveCodexMcpRuntime(process.cwd());
  await activatePersonalInstallCoreTransaction(resolved.binding);
  const core = await inspectPersonalInstallCore(resolved.binding);
  if (core.ready !== true) throw new Error('personal_core_activation_verification_failed');
  const targets = new Map([['codex', { executable_path: codexExecutable }]]);
  const adapter = personalInstallHostRegistry(targets).codex;
  const before = await adapter.inspect(core.context);
  const activated = before.ready === true
    ? { migration: { removed: 0 }, source: 'existing verified plugin' }
    : await adapter.activate(core.context);
  const after = await adapter.inspect(core.context);
  if (after.ready !== true) throw new Error(after.reason_code ?? 'codex_activation_incomplete');
  const installedRuntime = inspectCodexRuntime(DATA_DIR);
  if (!installedRuntime.ok) throw new Error(`Codex runtime inspection failed: ${installedRuntime.detail}`);

  console.log(`[pulse] Codex plugin installed from ${activated?.source ?? 'signed product edge'}`);
  console.log(`[pulse] Trusted local runtime installed: ${installedRuntime.digest}`);
  if ((activated?.migration?.removed ?? 0) > 0) {
    console.log(`[pulse] Removed ${activated.migration.removed} obsolete Pulse hook handler(s); unrelated hooks were preserved.`);
  }
  const liveStatus = core.context.live_status;
  console.log(`[pulse] ${resolved.runtime.kind} vault running; store=${resolved.runtime.store_id}; store_topology_fallback=false`);
  console.log(`[pulse] Retrieval: ${liveStatus.full_retrieval === true
    ? `full via ${liveStatus.embedder}`
    : 'fallback only; full retrieval is not enabled'}`);
  if (process.env.PULSE_TRUST_MODE === 'test') {
    console.log('[pulse] SYNTHETIC TEST AUTHORITY is active; this connection is not production-trusted.');
  }
  console.log('[pulse] Open /hooks in a new Codex task and trust the Pulse hook definition. Automatic mode is not ready until a trusted hook runs.');
}

async function disconnectCodex() {
	const release = await acquireProductActivationLock();
	try {
		disconnectCodexActivation();
	} finally {
		await release();
	}
}

function disconnectCodexActivation() {
	requireCommand('codex');
	const binding = resolveCodexMcpRuntime(process.cwd()).binding;
	const snapshots = snapshotLocalFiles(activationFilePaths(binding, { includeCodex: true }));
	const pluginBefore = codexPluginStatus();
	const pluginSnapshot = snapshotPluginTree(pluginBefore);
	let accessResult;
	try {
		accessResult = removeProductHostAccess({
			productHome: join(homedir(), '.pulse'), binding, host: 'codex',
		});
		removeCodexProductLocator({ codexHome: codexHomePath(), binding });
		writeCaptureStateFiles({
			globalDataDir: DATA_DIR,
			binding,
			host: 'codex',
			enabled: accessResult.remaining_for_host > 0,
			globalEnabled: accessResult.remaining_for_host > 0,
			reason: 'codex_plugin_disconnected',
		});
		if (accessResult.remaining_for_workspace === 0) {
			try { removeProductLocator({ productHome: join(homedir(), '.pulse'), binding }); }
			catch (error) { if (!/missing/i.test(error.message)) throw error; }
		}
		if (accessResult.remaining_for_host === 0 && pluginBefore.installed) {
			codexCommand(['plugin', 'remove', 'pulse@zbs-gg']);
		}
	} catch (error) {
		const failures = [];
		try { restoreLocalFiles(snapshots); } catch (failure) { failures.push(failure); }
		if (pluginSnapshot) {
			try {
				if (!codexPluginStatus().installed) codexCommand(['plugin', 'add', 'pulse@zbs-gg']);
				restorePluginTree(pluginSnapshot);
			} catch (failure) { failures.push(failure); }
		}
		if (failures.length > 0) {
			throw new Error(`Codex disconnect failed (${error.message}); rollback failed: ${failures.map((failure) => failure.message).join('; ')}`);
		}
		throw error;
	}
	discardPluginTreeSnapshot(pluginSnapshot);
	console.log(accessResult.remaining_for_host > 0
		? '[pulse] Codex disconnected from this workspace; the global plugin remains for other connected workspaces.'
		: '[pulse] Codex disconnected and the unused global plugin was removed. Existing Personal/Desk memory was preserved.');
}

function codexPluginStatus(codexExecutable = 'codex') {
  try {
    return parsePulsePluginList(codexCommand(
      ['plugin', 'list', '--marketplace', 'zbs-gg'],
      { executable: codexExecutable },
    ));
	} catch (error) {
		return { installed: false, enabled: false, path: undefined, error: error.message };
  }
}

function checkCodexPluginMcp(plugin) {
  if (!plugin.path) return { ok: false, detail: 'Pulse plugin is not installed' };
  const config = safeReadJSON(join(plugin.path, '.mcp.json'));
  const servers = config?.mcpServers;
  if (!servers || Object.keys(servers).length !== 1 || !servers['pulse-product']) {
    return { ok: false, detail: 'plugin must expose exactly one pulse-product MCP server' };
  }
  const server = servers['pulse-product'];
  if (server.url !== undefined || server.cwd !== undefined || server.command !== 'node' ||
      !Array.isArray(server.args) || server.args.length !== 1 ||
      server.args[0] !== '${PLUGIN_ROOT}/mcp/server.mjs') {
    return { ok: false, detail: 'pulse-product MCP must be stdio without url' };
  }
  return { ok: true, detail: 'one plugin-owned stdio server; no url fallback' };
}

function codexProductConnectedForWorkspace(captureState, binding) {
	if (!captureEnabledForHost(captureState, 'codex')) return false;
	const plugin = codexPluginStatus();
	try {
		const productState = readProductActivationBundle(DATA_DIR);
		const edge = committedCodexProductEdge(productState);
		if (!inspectCodexPluginCompatibility(plugin, edge).ok || !checkCodexPluginMcp(plugin).ok) return false;
		const snapshot = inspectCodexMarketplaceSnapshot(edge, DATA_DIR);
		if (!snapshot.ok) return false;
		const marketplace = codexMarketplaceStatus();
		if (!marketplace.configured || !marketplace.root ||
			realpathSync(resolve(marketplace.root)) !== snapshot.marketplace_root) return false;
		readCodexProductLocator({ codexHome: codexHomePath(), binding });
		readProductHostAccess({ productHome: join(homedir(), '.pulse'), binding, host: 'codex' });
		return true;
	} catch {
		return false;
	}
}

function inspectCodexDoctorProductGeneration({ codexReady, codexExecutable }) {
	const plugin = codexReady
		? codexPluginStatus(codexExecutable)
		: { installed: false, enabled: false, path: undefined };
	const installedRuntime = inspectCodexRuntime(DATA_DIR);
	let productActivation;
	let productEdge;
	let productActivationError;
	try {
		const productState = readProductActivationBundle(DATA_DIR);
		productActivation = productState.activation;
		productEdge = committedCodexProductEdge(productState);
	} catch (error) { productActivationError = error.message; }
	const pluginCompatibility = productEdge
		? inspectCodexPluginCompatibility(plugin, productEdge)
		: { ok: false, reason: 'codex_product_edge_unavailable', detail: productActivationError ?? 'signed Codex product edge unavailable' };
	const cachePluginRoot = productEdge ? join(
		codexHomePath(), 'plugins', 'cache', 'zbs-gg', 'pulse', productEdge.release_version,
	) : undefined;
	const cachePluginCompatibility = productEdge
		? inspectCodexPluginCompatibility({
			installed: true, enabled: true, version: productEdge.release_version, path: cachePluginRoot,
		}, productEdge)
		: { ok: false, reason: 'codex_product_edge_unavailable', detail: 'signed Codex product edge unavailable' };
	const marketplace = codexMarketplaceStatus(codexExecutable);
	const marketplaceSnapshot = productEdge
		? inspectCodexMarketplaceSnapshot(productEdge, DATA_DIR)
		: { ok: false, reason: 'codex_product_edge_unavailable', detail: 'signed Codex product edge unavailable' };
	let marketplaceExact = false;
	try {
		marketplaceExact = Boolean(marketplaceSnapshot.ok && marketplace.configured && marketplace.root &&
			realpathSync(resolve(marketplace.root)) === marketplaceSnapshot.marketplace_root);
	} catch { marketplaceExact = false; }
	return {
		plugin, installedRuntime, productActivation, productEdge, productActivationError,
		pluginCompatibility, cachePluginRoot, cachePluginCompatibility,
		marketplace, marketplaceSnapshot, marketplaceExact,
	};
}

function codexDoctorProductGenerationIdentity(generation) {
	return createHash('sha256').update(JSON.stringify({
		plugin: generation.plugin,
		installed_runtime: generation.installedRuntime,
		activation: generation.productActivation,
		edge: generation.productEdge,
		activation_error: generation.productActivationError,
		plugin_compatibility: generation.pluginCompatibility,
		cache_plugin_root: generation.cachePluginRoot,
		cache_plugin_compatibility: generation.cachePluginCompatibility,
		marketplace: generation.marketplace,
		marketplace_snapshot: generation.marketplaceSnapshot,
		marketplace_exact: generation.marketplaceExact,
	})).digest('hex');
}

async function codexDoctorReport({ codexExecutable = 'codex' } = {}) {
	const syntheticAuthority = process.env.PULSE_TRUST_MODE === 'test';
  const presenceTrust = syntheticAuthority
    ? { ready: true, status: 'synthetic_test_authority', issues: [] }
    : inspectPresenceTrust({ probePublicKey: true });
  const authorityProfile = personalAuthorityProfileForDoctor(presenceTrust, { syntheticAuthority });
  const codex = codexExecutable !== 'codex' && codexExecutable.endsWith('.js')
    ? checkCommandVersion(process.execPath, [codexExecutable, '--version'])
    : checkCommandVersion(codexExecutable, ['--version']);
	const productGenerationBefore = inspectCodexDoctorProductGeneration({
		codexReady: codex.ok, codexExecutable,
	});
	const productGenerationIdentity = codexDoctorProductGenerationIdentity(productGenerationBefore);
	let {
		plugin, installedRuntime, productActivation, productEdge, productActivationError,
		pluginCompatibility, cachePluginRoot,
		marketplace, marketplaceSnapshot, marketplaceExact,
	} = productGenerationBefore;
  const shadowFiles = pulseProductMcpShadowFiles({ cwd: process.cwd(), codexHome: codexHomePath() });
  let legacy;
  try {
    legacy = inspectLegacyPulseHookFiles({ cwd: process.cwd(), codexHome: codexHomePath() });
  } catch (error) {
    legacy = { removed: -1, error: error.message };
  }

  let binding;
  let runtime;
  let bindingError;
  try {
	await recoverBindingAuthority();
    binding = resolveCodexMcpRuntime(process.cwd()).binding;
    runtime = vaultRuntimeFromBinding(binding);
  } catch (error) {
    bindingError = error.message;
  }
  const runtimeStatus = runtime ? inspectVaultRuntime(runtime) : { status: 'unbound' };
	let locatorAuthority = {
		ok: false, detail: 'Codex product locator is unavailable', authority_mode: 'unavailable',
	};
	if (binding) {
		try {
			const { entry } = readCodexProductLocator({ codexHome: codexHomePath(), binding });
			const defaults = defaultBindingPaths();
			const expectedMode = syntheticAuthority ? 'test' : 'production';
			const expectedRegistry = syntheticAuthority ? process.env.PULSE_BINDING_REGISTRY_PATH : defaults.registryPath;
			const expectedKey = syntheticAuthority ? process.env.PULSE_BINDING_PUBLIC_KEY_PATH : defaults.publicKeyPath;
			const expectedAnchor = syntheticAuthority ? process.env.PULSE_BINDING_ANCHOR_PATH : defaults.anchorPath;
			const matches = entry.trust_mode === expectedMode && entry.data_dir === resolve(DATA_DIR) &&
				entry.registry_path === resolve(expectedRegistry ?? '') &&
				entry.public_key_path === resolve(expectedKey ?? '') &&
				entry.anchor_path === resolve(expectedAnchor ?? '');
			const persistedAuthorityMode = entry.trust_mode === 'test' ? 'synthetic-test' :
				entry.trust_mode === 'production' ? 'production' : 'invalid';
			locatorAuthority = matches
				? { ok: true, detail: syntheticAuthority
					? 'synthetic test authority; never production-trusted'
					: 'production default binding authority', authority_mode: persistedAuthorityMode }
				: { ok: false, detail: 'persisted locator does not match the current authority mode',
					authority_mode: persistedAuthorityMode };
		} catch (error) {
			locatorAuthority = { ok: false, detail: error.message, authority_mode: 'unavailable' };
		}
	}
	let codexHostAccess;
	let codexHostAccessError;
	if (binding) {
		try {
			codexHostAccess = readProductHostAccess({
				productHome: join(homedir(), '.pulse'), binding, host: 'codex',
			});
		} catch (error) { codexHostAccessError = error.message; }
	}
  const capture = runtime ? safeReadJSON(join(runtime.data_dir, 'capture-state.json')) : undefined;
  let hookReadiness = { ready: false, reason: 'plugin_hooks_missing' };
  if (plugin.path && installedRuntime.ok) {
    try {
      hookReadiness = validateHookReadiness(
				codexHookExecutionDigest(plugin.path, productActivation?.runtime_path ?? installedRuntime.path),
        safeReadJSON(join(DATA_DIR, 'codex-hook-readiness.json')),
        binding ? {
          binding_digest: binding.binding_digest,
          resolver_epoch: binding.resolver_epoch,
          repository_id: binding.workspace.repository_id,
          workspace_digest: codexWorkspaceDigest(binding.workspace.canonical_path),
        } : { binding_digest: 'binding-unavailable' },
      );
    } catch (error) {
      hookReadiness = { ready: false, reason: error.message };
    }
		hookReadiness = projectCodexLifecycleAttestation({
			syntheticAuthority,
			testAttestor: process.env.PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR === '1',
			readiness: hookReadiness,
		});
  }
	const nativeHookTrustPromise = codex.ok && plugin.path
		? inspectCodexNativeHookTrust({
			codexExecutable, cwd: process.cwd(), pluginRoot: plugin.path,
			marketplacePluginRoot: marketplaceSnapshot.plugin_root,
			cachePluginRoot,
			edge: productEdge,
		})
		: Promise.resolve({
			ready: false,
			reason: 'codex_native_hook_query_unavailable',
			detail: 'Pulse Codex plugin is unavailable',
		});
	const liveProbePromise = (async () => {
		if (!binding || !runtime || runtimeStatus.status !== 'running') return {};
		try {
			const liveStatus = await boundPulseRequest({ binding, runtime }, '/memory/status', {
				method: 'GET', timeoutMs: 1500,
			});
			return { liveStatus };
		} catch (error) {
			return { liveError: error.message };
		}
	})();
	let [nativeHookTrust, { liveStatus, liveError }] = await Promise.all([
		nativeHookTrustPromise,
		liveProbePromise,
	]);
	const productGenerationAfter = inspectCodexDoctorProductGeneration({
		codexReady: codex.ok, codexExecutable,
	});
	const productGenerationStable = productGenerationIdentity ===
		codexDoctorProductGenerationIdentity(productGenerationAfter);
	({
		plugin, installedRuntime, productActivation, productEdge, productActivationError,
		pluginCompatibility, cachePluginRoot,
		marketplace, marketplaceSnapshot, marketplaceExact,
	} = productGenerationAfter);
	if (!productGenerationStable) {
		const changed = {
			ready: false,
			reason: 'codex_product_state_changed_during_inspection',
			detail: 'Codex product state changed while doctor was inspecting it; run doctor again',
		};
		nativeHookTrust = changed;
		hookReadiness = { ...changed, trusted_hook_observed: false };
	}
  const liveVault = liveStatus && liveStatus.storage_path === join(runtime.data_dir, 'pulse.db') &&
    ['pulse-product', 'codex', 'claude-code'].includes(liveStatus.host) && liveStatus.backend_llm_enabled === false &&
    liveStatus.capture_enabled === true && liveStatus.raw_capture_enabled === false;

  const checks = {
		presence_trust: enhancedPresenceDoctorCheck(authorityProfile, presenceTrust),
		authority: locatorAuthority,
    codex,
		host_access: codexHostAccess
			? { ok: true, detail: 'workspace-scoped Codex access marker verified' }
			: { ok: false, detail: codexHostAccessError ?? 'workspace-scoped Codex access marker missing' },
		plugin: productGenerationStable ? pluginCompatibility : {
			ok: false, reason: nativeHookTrust.reason, detail: nativeHookTrust.detail,
		},
		marketplace: productGenerationStable ? codexMarketplaceDoctorCheck({
			exact: marketplaceExact, marketplace, snapshot: marketplaceSnapshot,
		}) : { ok: false, reason: nativeHookTrust.reason, detail: nativeHookTrust.detail },
    plugin_mcp: productGenerationStable ? checkCodexPluginMcp(plugin) : {
			ok: false, reason: nativeHookTrust.reason, detail: nativeHookTrust.detail,
		},
    mcp_shadow: shadowFiles.length === 0
      ? { ok: true, detail: 'pulse-product is owned only by the plugin' }
      : { ok: false, detail: `shadowing config: ${shadowFiles.join(', ')}` },
    legacy_hooks: legacy.removed === 0
      ? { ok: true, detail: 'no obsolete Pulse hook handlers' }
      : { ok: false, detail: legacy.error ?? `${legacy.removed} obsolete Pulse hook handler(s) remain` },
		native_hook_trust: nativeHookTrust.ready
			? { ok: true, detail: nativeHookTrust.detail }
			: { ok: false, reason: nativeHookTrust.reason, detail: nativeHookTrust.detail },
    binding: binding
      ? { ok: true, detail: `${binding.mode}:${binding.workspace.workspace_id}` }
      : { ok: false, detail: bindingError ?? 'workspace is not bound' },
		runtime: productGenerationStable ? installedRuntime : {
			ok: false, reason: nativeHookTrust.reason, detail: nativeHookTrust.detail,
		},
		activation: productGenerationStable && productActivation
			? { ok: true, detail: 'runtime and daemon activation pair verified' }
			: { ok: false, reason: productGenerationStable ? undefined : nativeHookTrust.reason,
				detail: productGenerationStable
					? productActivationError ?? 'product activation unavailable'
					: nativeHookTrust.detail },
    vault: liveVault
      ? { ok: true, detail: `${runtime.kind}:${runtime.store_id}; live authenticated status` }
      : { ok: false, detail: liveError ?? `bound vault is ${runtimeStatus.status}; live status unavailable or mismatched` },
    capture: captureEnabledForHost(capture, 'codex')
      ? { ok: true, detail: 'host-extracted structured capture enabled' }
      : { ok: false, detail: 'automatic capture disabled' },
    retrieval: liveStatus?.full_retrieval === true
      ? { ok: true, detail: `full retrieval via ${liveStatus.embedder}` }
      : { ok: false, detail: 'fallback only; configure local MLX or Cohere embedding' },
    hooks: hookReadiness.ready
		  ? { ok: true, detail: 'SessionStart and one complete same-session lifecycle observed' }
      : { ok: false, reason: hookReadiness.reason, detail: hookReadiness.detail ?? hookReadiness.reason },
  };
  const personalLiveReadiness = projectPersonalLiveReadiness(checks, new Date());
  const ready = personalLiveReadiness.outcome === 'ready';
  return {
	product: `Pulse Codex ${runtime?.kind === 'desk' ? 'Desk' : runtime?.kind === 'personal' ? 'Personal' : 'unbound'} memory`,
    target_host: 'codex',
    authority_profile: authorityProfile,
    verdict: ready
		? syntheticAuthority
			? 'Pulse Codex synthetic test lifecycle ready; production authority is not active.'
			: 'Pulse Codex automatic lifecycle ready.'
      : 'Pulse Codex automatic lifecycle is not ready.',
    checks,
		personal_live_readiness: personalLiveReadiness,
		trust: {
      raw_transcript_capture: liveStatus?.raw_capture_enabled ?? null,
      backend_llm_enabled: liveStatus?.backend_llm_enabled ?? null,
      full_retrieval: liveStatus?.full_retrieval ?? false,
      embedder: liveStatus?.embedder ?? '',
      external_embedding_api: /cohere|^embed-(?:english|multilingual)/i.test(liveStatus?.embedder ?? ''),
			hook_bundle_digest: hookReadiness.hooks_digest ?? '',
			trusted_hook_observed: hookReadiness.trusted_hook_observed === true,
			native_hook_trusted: nativeHookTrust.ready,
			native_hook_set_digest: nativeHookTrust.hook_set_digest ?? '',
			release_manifest_digest: productActivation?.release_manifest_digest ?? '',
			release_version: productActivation?.release_version ?? '',
			release_epoch: productActivation?.release_epoch ?? null,
			plugin_tree_digest: productActivation?.plugin_tree_digest ?? '',
			runtime_tree_digest: productActivation?.runtime_tree_digest ?? '',
		authority_mode: locatorAuthority.authority_mode,
    },
  };
}

async function runCodexDoctor(rest = []) {
  const report = await codexDoctorReport();
  if (rest.includes('--json')) {
    console.log(JSON.stringify(report, null, 2));
  } else {
    console.log('[pulse] doctor codex');
    for (const [label, check] of Object.entries(report.checks)) printDoctorLine(label, check);
    const rawCapture = report.trust.raw_transcript_capture === false
      ? 'off'
      : report.trust.raw_transcript_capture === true ? 'on' : 'unknown';
    console.log(`\nPrivacy: backend LLM ${report.trust.backend_llm_enabled === false ? 'off' : 'unknown'}; raw transcript capture ${rawCapture}; external embedding API ${report.trust.external_embedding_api ? 'on' : 'off'}`);
    console.log(`Retrieval: ${report.trust.full_retrieval ? `full (${report.trust.embedder})` : 'fallback only; full retrieval is not enabled'}`);
    printPersonalAuthorityProfile(report.authority_profile);
    console.log(`Personal readiness: ${report.personal_live_readiness.outcome} (${report.personal_live_readiness.reason_code}) at ${report.personal_live_readiness.checked_at}`);
    console.log(`Verdict: ${report.verdict}`);
		if (report.personal_live_readiness.outcome !== 'ready') {
			console.log(`Next: ${report.personal_live_readiness.next_action.label}`);
		}
  }
  if (Object.values(report.checks).some((check) => !check.ok)) process.exitCode = 1;
}

function checkClaudeProductMCP(runtimePath) {
  if (!commandOnPath('claude')) return { ok: false, detail: 'Claude Code CLI missing' };
  const result = spawnSync('claude', ['mcp', 'get', 'pulse'], {
    encoding: 'utf8', timeout: 3000,
  });
  if (result.status !== 0) return { ok: false, detail: 'Pulse MCP is not registered' };
	const output = `${result.stdout || ''}\n${result.stderr || ''}`;
	const rawLines = output.split(/\r?\n/);
	const lines = rawLines.map((line) => line.trim());
  const commandLine = lines.find((line) => line.startsWith('Command:')) ?? '';
  const argsLine = lines.find((line) => line.startsWith('Args:')) ?? '';
  const environmentIndex = lines.findIndex((line) => line.startsWith('Environment:'));
  const environmentLine = environmentIndex >= 0 ? lines[environmentIndex] : '';
  const unsafe = /PULSE_API_KEY|secret\.key|\bnpx\b|@zbs-gg\/pulse@preview/i.test(output);
	const argsValue = argsLine.slice('Args:'.length).trim();
	const actualRuntimePath = argsValue.endsWith(' claude-mcp')
		? argsValue.slice(0, -' claude-mcp'.length)
		: '';
	const exact = commandLine === `Command: ${process.execPath}` && isAbsolute(actualRuntimePath) &&
		(runtimePath === undefined || actualRuntimePath === runtimePath);
	let actualEnvironment;
  try {
    actualEnvironment = JSON.parse(environmentLine.slice('Environment:'.length).trim());
  } catch {
    actualEnvironment = undefined;
  }
  const expectedEnvironment = claudeProductMcpEnvironment();
  let exactEnvironment = actualEnvironment &&
    JSON.stringify(Object.fromEntries(Object.entries(actualEnvironment).sort())) ===
      JSON.stringify(Object.fromEntries(Object.entries(expectedEnvironment).sort()));
	if (!exactEnvironment && environmentIndex >= 0) {
		const parsed = {};
		let valid = true;
		const candidates = [];
		const inline = environmentLine.slice('Environment:'.length).trim();
		if (inline !== '') candidates.push(inline);
		let startedBlock = false;
		for (const rawLine of rawLines.slice(environmentIndex + 1)) {
			const candidate = rawLine.trim();
			if (candidate === '') {
				if (startedBlock) break;
				continue;
			}
			if (!/^\s/.test(rawLine)) break;
			startedBlock = true;
			candidates.push(candidate);
		}
		for (const candidate of candidates) {
			const match = candidate.match(/^([A-Z][A-Z0-9_]*)\s*(?:=|:)\s*(.*)$/);
			if (!match || Object.hasOwn(parsed, match[1])) {
				valid = false;
				break;
			}
			let value = match[2];
			if ((value.startsWith("'") && value.endsWith("'")) ||
				(value.startsWith('"') && value.endsWith('"'))) value = value.slice(1, -1);
			parsed[match[1]] = value;
		}
		exactEnvironment = valid && candidates.length > 0 &&
			JSON.stringify(Object.fromEntries(Object.entries(parsed).sort())) ===
			JSON.stringify(Object.fromEntries(Object.entries(expectedEnvironment).sort()));
		if (valid) actualEnvironment = parsed;
  }
	const authorityMode = actualEnvironment?.PULSE_TRUST_MODE === 'test'
		? 'synthetic-test'
		: actualEnvironment && !Object.hasOwn(actualEnvironment, 'PULSE_TRUST_MODE')
			? 'production'
			: 'invalid';
  return exact && exactEnvironment && !unsafe
		? { ok: true, detail: 'one secret-free pinned stdio server', authority_mode: authorityMode,
			runtime_path: actualRuntimePath }
		: { ok: false, detail: unsafe
      ? 'legacy mutable or secret-bearing MCP config remains'
			: 'MCP command or authority environment does not match the pinned runtime',
			authority_mode: authorityMode, runtime_path: actualRuntimePath };
}

function checkClaudeProductHooks(installedRuntime, runtime, binding) {
  const settings = safeReadJSON(resolve(process.cwd(), '.claude', 'settings.local.json'));
  if (!settings) return { ok: false, detail: 'Claude hook settings missing or invalid' };
  if (settings.disableAllHooks === true) return { ok: false, detail: 'disableAllHooks is enabled' };
  const expected = hookConfig(installedRuntime.path, installedRuntime.digest).hooks;
  const errors = [];
  for (const event of CLAUDE_HOOK_EVENTS) {
    const entries = Array.isArray(settings.hooks?.[event]) ? settings.hooks[event] : [];
    const pulseHandlers = entries.flatMap((entry) => (Array.isArray(entry?.hooks) ? entry.hooks : []))
      .filter((handler) => isPulseHookCommand(handler?.command));
    const expectedHandler = expected[event][0].hooks[0];
    const exactEntry = entries.some((entry) =>
      (entry.matcher ?? '') === (expected[event][0].matcher ?? '') &&
      Array.isArray(entry.hooks) && entry.hooks.some((handler) =>
        handler?.type === expectedHandler.type && handler?.command === expectedHandler.command &&
        handler?.timeout === expectedHandler.timeout));
    if (pulseHandlers.length !== 1 || !exactEntry) errors.push(event);
  }
  if (errors.length > 0) return { ok: false, detail: `missing, duplicate, or stale: ${errors.join(', ')}` };
	return checkClaudeNativeProductHooks(installedRuntime, runtime, binding);
}

function checkClaudeNativeProductHooks(installedRuntime, runtime, binding) {
	if (!installedRuntime?.ok || !runtime || !binding) {
		return { ok: false, detail: 'runtime or binding unavailable' };
	}
  const digest = claudeHookContractDigest(installedRuntime.digest);
  const receipt = safeReadJSON(join(runtime.data_dir, 'claude-code-hook-readiness.json'));
  const milestones = ['prompt_context', 'write_receipt', 'turn_finalize'];
  const observed = receipt?.schema === 'pulse.claude_code_hook_readiness.v1' &&
    receipt.hooks_digest === digest && receipt.binding_digest === binding.binding_digest &&
    receipt.resolver_epoch === binding.resolver_epoch &&
    receipt.repository_id === binding.workspace.repository_id &&
    receipt.workspace_digest === claudeWorkspaceDigest(binding.workspace.canonical_path) &&
    /^[a-f0-9]{64}$/.test(receipt.turn_proof ?? '') &&
    milestones.every((name) => typeof receipt.milestones?.[name] === 'string' &&
      !Number.isNaN(Date.parse(receipt.milestones[name]))) &&
    CLAUDE_HOOK_EVENTS.includes(receipt.last_event) &&
    typeof receipt.observed_at === 'string' && !Number.isNaN(Date.parse(receipt.observed_at));
  return observed
    ? { ok: true, detail: 'prompt context, truthful Memory Tray receipt, and turn finalization observed' }
    : { ok: false, detail: 'native hooks installed; complete one real prompt that saves a visible Memory Tray candidate' };
}

async function claudeLegacyProductDoctorReport() {
  const syntheticAuthority = process.env.PULSE_TRUST_MODE === 'test';
  const presenceTrust = syntheticAuthority
    ? { ready: true, status: 'synthetic_test_authority', issues: [] }
    : inspectPresenceTrust({ probePublicKey: true });
  const authorityProfile = personalAuthorityProfileForDoctor(presenceTrust, { syntheticAuthority });
  const version = claudeProductVersionCheck();
  let binding;
  let runtime;
  let bindingError;
  try {
	await recoverBindingAuthority();
    binding = resolveCodexMcpRuntime(process.cwd()).binding;
    runtime = vaultRuntimeFromBinding(binding);
  } catch (error) {
    bindingError = error.message;
  }
  const runtimeStatus = runtime ? inspectVaultRuntime(runtime) : { status: 'unbound' };
  const capture = runtime ? safeReadJSON(join(runtime.data_dir, 'capture-state.json')) : undefined;
  let liveStatus;
  let liveError;
  if (binding && runtime && runtimeStatus.status === 'running') {
    try {
      liveStatus = await boundPulseRequest({ binding, runtime }, '/memory/status', {
        method: 'GET', timeoutMs: 1500,
      });
    } catch (error) {
      liveError = error.message;
    }
  }
  const liveVault = liveStatus && liveStatus.storage_path === join(runtime.data_dir, 'pulse.db') &&
    ['pulse-product', 'codex', 'claude-code'].includes(liveStatus.host) &&
    liveStatus.backend_llm_enabled === false && liveStatus.capture_enabled === true &&
    liveStatus.raw_capture_enabled === false;
	const mcp = checkClaudeProductMCP();
	const registeredRuntime = mcp.ok
		? inspectCodexRuntimeAt(mcp.runtime_path)
		: { ok: false, detail: mcp.detail };
	let productActivation;
	let productActivationError;
	try { productActivation = readProductActivation(DATA_DIR); } catch (error) { productActivationError = error.message; }
	const activationMatches = productActivation && registeredRuntime.ok &&
		resolve(productActivation.runtime_path) === resolve(registeredRuntime.path) &&
		productActivation.runtime_tree_digest === registeredRuntime.digest;
	const expectedAuthorityMode = process.env.PULSE_TRUST_MODE === 'test' ? 'synthetic-test' : 'production';
	const authorityMatches = mcp.authority_mode === expectedAuthorityMode;
	const checks = {
		presence_trust: enhancedPresenceDoctorCheck(authorityProfile, presenceTrust),
		authority: authorityMatches
			? { ok: true, detail: mcp.authority_mode === 'synthetic-test'
				? 'synthetic test authority; never production-trusted'
				: 'production default binding authority' }
			: { ok: false, detail: `persisted MCP authority is ${mcp.authority_mode}` },
    claude_code: version,
		runtime: registeredRuntime,
		activation: activationMatches
			? { ok: true, detail: 'registered runtime and daemon activation pair verified' }
			: { ok: false, detail: productActivationError ?? 'registered runtime does not match product activation' },
    binding: binding
      ? { ok: true, detail: `${binding.mode}:${binding.workspace.workspace_id}` }
      : { ok: false, detail: bindingError ?? 'workspace is not bound' },
		mcp,
		hooks: registeredRuntime.ok && runtime
			? checkClaudeProductHooks(registeredRuntime, runtime, binding)
      : { ok: false, detail: 'runtime or binding unavailable' },
    vault: liveVault
      ? { ok: true, detail: `${runtime.kind}:${runtime.store_id}; live authenticated status` }
      : { ok: false, detail: liveError ?? `bound vault is ${runtimeStatus.status}; live status unavailable or mismatched` },
    capture: captureEnabledForHost(capture, 'claude-code')
      ? { ok: true, detail: 'host-extracted structured capture enabled' }
      : { ok: false, detail: 'automatic capture disabled' },
    retrieval: liveStatus?.full_retrieval === true
      ? { ok: true, detail: `full retrieval via ${liveStatus.embedder}` }
      : { ok: false, detail: 'fallback only; configure local MLX or Cohere embedding' },
  };
  const personalLiveReadiness = projectSupportedHostLiveReadiness('claude-code', checks, new Date());
  const ready = personalLiveReadiness.outcome === 'ready';
  return {
	product: `Pulse Claude Code ${runtime?.kind === 'desk' ? 'Desk' : runtime?.kind === 'personal' ? 'Personal' : 'unbound'} memory`,
    target_host: 'claude-code',
    authority_profile: authorityProfile,
    personal_live_readiness: personalLiveReadiness,
    verdict: ready
		? mcp.authority_mode === 'synthetic-test'
			? 'Pulse Claude Code synthetic test lifecycle ready; production authority is not active.'
			: 'Pulse Claude Code automatic lifecycle ready.'
      : 'Pulse Claude Code automatic lifecycle is not ready.',
    checks,
    trust: {
      raw_transcript_capture: liveStatus?.raw_capture_enabled ?? null,
      backend_llm_enabled: liveStatus?.backend_llm_enabled ?? null,
      full_retrieval: liveStatus?.full_retrieval ?? false,
      embedder: liveStatus?.embedder ?? '',
			authority_mode: mcp.authority_mode,
      external_embedding_api: /cohere|^embed-(?:english|multilingual)/i.test(liveStatus?.embedder ?? ''),
			hook_contract_digest: registeredRuntime.ok ? claudeHookContractDigest(registeredRuntime.digest) : '',
      release_manifest_digest: productActivation?.release_manifest_digest ?? '',
      release_version: productActivation?.release_version ?? '',
      release_epoch: productActivation?.release_epoch ?? null,
    },
  };
}

async function claudeNativeProductDoctorReport({ detection, pluginInspection, productEdgeError }) {
  const syntheticAuthority = process.env.PULSE_TRUST_MODE === 'test';
  const presenceTrust = syntheticAuthority
    ? { ready: true, status: 'synthetic_test_authority', issues: [] }
    : inspectPresenceTrust({ probePublicKey: true });
  const authorityProfile = personalAuthorityProfileForDoctor(presenceTrust, { syntheticAuthority });
  let resolved;
  let bindingError;
  try {
    await recoverBindingAuthority();
    resolved = resolveCodexMcpRuntime(process.cwd());
  } catch (error) {
    bindingError = error.message;
  }
  const runtimeStatus = resolved ? inspectVaultRuntime(resolved.runtime) : { status: 'unbound' };
  const capture = resolved ? safeReadJSON(join(resolved.runtime.data_dir, 'capture-state.json')) : undefined;
  const installedRuntime = inspectCodexRuntime(DATA_DIR);
  let activation;
  let activationError;
  try { activation = readProductActivation(DATA_DIR); } catch (error) { activationError = error.message; }
  const activationMatches = activation && installedRuntime.ok &&
    resolve(activation.runtime_path) === resolve(installedRuntime.path) &&
    activation.runtime_tree_digest === installedRuntime.digest;
  let locator;
  let locatorError;
  let hostAccess;
  let hostAccessError;
  if (resolved) {
    try {
      locator = readProductLocator({ productHome: join(homedir(), '.pulse'), binding: resolved.binding });
    } catch (error) { locatorError = error.message; }
    try {
      hostAccess = readProductHostAccess({
        productHome: join(homedir(), '.pulse'), binding: resolved.binding, host: 'claude-code',
      });
    } catch (error) { hostAccessError = error.message; }
  }
  const expectedAuthorityMode = syntheticAuthority ? 'test' : 'production';
  const authorityMatches = locator?.entry?.trust_mode === expectedAuthorityMode &&
    resolve(locator.entry.data_dir) === resolve(DATA_DIR);
  let liveStatus;
  let liveError;
  if (resolved && runtimeStatus.status === 'running') {
    try {
      liveStatus = await boundPulseRequest(resolved, '/memory/status', { method: 'GET', timeoutMs: 1500 });
    } catch (error) { liveError = error.message; }
  }
  const liveVault = liveStatus && liveStatus.storage_path === join(resolved.runtime.data_dir, 'pulse.db') &&
    ['pulse-product', 'codex', 'claude-code', 'cursor'].includes(liveStatus.host) &&
    liveStatus.backend_llm_enabled === false && liveStatus.capture_enabled === true &&
    liveStatus.raw_capture_enabled === false;
  const legacyRegistration = detection?.available
    ? spawnSync(detection.executable_path, ['mcp', 'get', 'pulse'], {
        encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5000, killSignal: 'SIGTERM',
      })
    : undefined;
  const legacyShadowAbsent = detection?.available && legacyRegistration?.status !== 0;
  const checks = {
    presence_trust: enhancedPresenceDoctorCheck(authorityProfile, presenceTrust),
    authority: authorityMatches
      ? { ok: true, detail: syntheticAuthority ? 'synthetic test authority; never production-trusted' : 'production shared locator authority' }
      : { ok: false, detail: locatorError ?? 'shared workspace locator is missing or mismatched' },
    claude_code: detection?.available
      ? claudeProductVersionCheck(detection.executable_path)
      : { ok: false, detail: detection?.reason_code ?? 'Claude Code CLI missing' },
    plugin: pluginInspection?.ok
      ? { ok: true, detail: pluginInspection.detail }
      : { ok: false, detail: productEdgeError ?? pluginInspection?.detail ?? pluginInspection?.reason ?? 'native plugin unavailable' },
    host_access: hostAccess
      ? { ok: true, detail: 'workspace-scoped Claude Code access marker verified' }
      : { ok: false, detail: hostAccessError ?? 'workspace-scoped Claude Code access marker missing' },
    legacy_mcp_shadow: legacyShadowAbsent
      ? { ok: true, detail: 'no obsolete external Pulse MCP registration' }
      : { ok: false, detail: 'obsolete external Pulse MCP registration remains' },
    runtime: installedRuntime,
    activation: activationMatches
      ? { ok: true, detail: 'shared runtime and daemon activation pair verified' }
      : { ok: false, detail: activationError ?? 'shared runtime does not match product activation' },
    binding: resolved
      ? { ok: true, detail: `${resolved.binding.mode}:${resolved.binding.workspace.workspace_id}` }
      : { ok: false, detail: bindingError ?? 'workspace is not bound' },
    hooks: resolved
      ? checkClaudeNativeProductHooks(installedRuntime, resolved.runtime, resolved.binding)
      : { ok: false, detail: 'runtime or binding unavailable' },
    vault: liveVault
      ? { ok: true, detail: `${resolved.runtime.kind}:${resolved.runtime.store_id}; live authenticated status` }
      : { ok: false, detail: liveError ?? `bound vault is ${runtimeStatus.status}; live status unavailable or mismatched` },
    capture: resolved && captureEnabledForHost(capture, 'claude-code')
      ? { ok: true, detail: 'host-extracted structured capture enabled' }
      : { ok: false, detail: 'automatic capture disabled' },
    retrieval: liveStatus?.full_retrieval === true
      ? { ok: true, detail: `full retrieval via ${liveStatus.embedder}` }
      : { ok: false, detail: 'fallback only; configure local MLX or Cohere embedding' },
  };
  const personalLiveReadiness = projectSupportedHostLiveReadiness('claude-code', checks, new Date());
  const ready = personalLiveReadiness.outcome === 'ready';
  return {
    product: `Pulse Claude Code ${resolved?.runtime?.kind === 'desk' ? 'Desk' : resolved?.runtime?.kind === 'personal' ? 'Personal' : 'unbound'} memory`,
    target_host: 'claude-code',
    authority_profile: authorityProfile,
    personal_live_readiness: personalLiveReadiness,
    verdict: ready
      ? syntheticAuthority
        ? 'Pulse Claude Code synthetic test lifecycle ready; production authority is not active.'
        : 'Pulse Claude Code automatic lifecycle ready.'
      : 'Pulse Claude Code automatic lifecycle is not ready.',
    checks,
    trust: {
      raw_transcript_capture: liveStatus?.raw_capture_enabled ?? null,
      backend_llm_enabled: liveStatus?.backend_llm_enabled ?? null,
      full_retrieval: liveStatus?.full_retrieval ?? false,
      embedder: liveStatus?.embedder ?? '',
      authority_mode: syntheticAuthority ? 'synthetic-test' : 'production',
      external_embedding_api: /cohere|^embed-(?:english|multilingual)/i.test(liveStatus?.embedder ?? ''),
      hook_contract_digest: installedRuntime.ok ? claudeHookContractDigest(installedRuntime.digest) : '',
      plugin_tree_digest: pluginInspection?.digest ?? '',
      release_manifest_digest: activation?.release_manifest_digest ?? '',
      release_version: activation?.release_version ?? '',
      release_epoch: activation?.release_epoch ?? null,
    },
  };
}

async function claudeProductDoctorReport() {
  const detection = detectClaudeCodeCLI({ home: homedir() });
  let productEdge;
  let productEdgeError;
  try { productEdge = committedCodexProductEdge(readProductActivationBundle(DATA_DIR)); }
  catch (error) { productEdgeError = error.message; }
  const pluginInspection = detection.available
    ? inspectClaudeNativeProductPlugin(productEdge, detection.executable_path)
    : { ok: false, reason: detection.reason_code };
  if (pluginInspection.plugin?.installed) {
    return claudeNativeProductDoctorReport({ detection, pluginInspection, productEdgeError });
  }
  return claudeLegacyProductDoctorReport();
}

async function runClaudeProductDoctor(rest = []) {
  const report = await claudeProductDoctorReport();
  if (rest.includes('--json')) {
    console.log(JSON.stringify(report, null, 2));
  } else {
    console.log('[pulse] doctor claude-code');
    for (const [label, check] of Object.entries(report.checks)) printDoctorLine(label, check);
    console.log(`\nRetrieval: ${report.trust.full_retrieval ? `full (${report.trust.embedder})` : 'fallback only; full retrieval is not enabled'}`);
    printPersonalAuthorityProfile(report.authority_profile);
    console.log(`Verdict: ${report.verdict}`);
  }
  if (Object.values(report.checks).some((check) => !check.ok)) process.exitCode = 1;
}

async function cursorProductDoctorReport() {
  const syntheticAuthority = process.env.PULSE_TRUST_MODE === 'test';
  const presenceTrust = syntheticAuthority
    ? { ready: true, status: 'synthetic_test_authority', issues: [] }
    : inspectPresenceTrust({ probePublicKey: true });
  const authorityProfile = personalAuthorityProfileForDoctor(presenceTrust, { syntheticAuthority });
  let resolved;
  let bindingError;
  try {
    await recoverBindingAuthority();
    resolved = resolveCodexMcpRuntime(process.cwd());
  } catch (error) {
    bindingError = error.message;
  }
  const runtimeStatus = resolved
    ? inspectVaultRuntime(resolved.runtime)
    : { status: 'unbound' };
  const capture = resolved
    ? safeReadJSON(join(resolved.runtime.data_dir, 'capture-state.json'))
    : undefined;
  let hostAccess;
  let hostAccessError;
  if (resolved) {
    try {
      hostAccess = readProductHostAccess({
        productHome: join(homedir(), '.pulse'), binding: resolved.binding, host: 'cursor',
      });
    } catch (error) { hostAccessError = error.message; }
  }
  let productEdge;
  let productEdgeError;
  try {
    productEdge = committedCodexProductEdge(readProductActivationBundle(DATA_DIR));
  } catch (error) {
    productEdgeError = error.message;
  }
  const plugin = productEdge
    ? inspectCursorPlugin({
        cursorHome: cursorHomePath(),
        expectedDigest: productEdge.plugin_tree_digest,
      })
    : { ready: false, reason: 'cursor_product_edge_unavailable' };
  const lifecycle = resolved
    ? inspectCursorLifecycleReadiness(resolved)
    : { ready: false, reason_code: 'cursor_lifecycle_required' };
  let liveStatus;
  let liveError;
  if (resolved && runtimeStatus.status === 'running') {
    try {
      liveStatus = await boundPulseRequest(resolved, '/memory/status', {
        method: 'GET', timeoutMs: 1500,
      });
    } catch (error) {
      liveError = error.message;
    }
  }
  const liveVault = liveStatus &&
    liveStatus.storage_path === join(resolved.runtime.data_dir, 'pulse.db') &&
    ['pulse-product', 'codex', 'claude-code', 'cursor'].includes(liveStatus.host) &&
    liveStatus.backend_llm_enabled === false && liveStatus.capture_enabled === true &&
    liveStatus.raw_capture_enabled === false;
  const checks = {
    presence_trust: enhancedPresenceDoctorCheck(authorityProfile, presenceTrust),
    authority: {
      ok: true,
      detail: syntheticAuthority ? 'synthetic test authority; never production-trusted' : 'production shared locator authority',
    },
    binding: resolved
      ? { ok: true, detail: `${resolved.binding.mode}:${resolved.binding.workspace.workspace_id}` }
      : { ok: false, detail: bindingError ?? 'workspace is not bound' },
    runtime: runtimeStatus.status === 'running'
      ? { ok: true, detail: 'shared Pulse Core is running' }
      : { ok: false, detail: `shared Pulse Core is ${runtimeStatus.status}` },
    plugin: plugin.ready
      ? { ok: true, detail: 'exact signed Cursor plugin installed' }
      : { ok: false, detail: productEdgeError ?? plugin.reason ?? 'Cursor plugin unavailable' },
    host_access: hostAccess
      ? { ok: true, detail: 'workspace-scoped Cursor access marker verified' }
      : { ok: false, detail: hostAccessError ?? 'workspace-scoped Cursor access marker missing' },
    hooks: lifecycle.ready
      ? { ok: true, detail: 'session context, turn capture, write receipt, and finalize observed' }
      : { ok: false, detail: 'plugin installed; complete one normal Cursor turn' },
    vault: liveVault
      ? { ok: true, detail: `${resolved.runtime.kind}:${resolved.runtime.store_id}; live authenticated status` }
      : { ok: false, detail: liveError ?? `bound vault is ${runtimeStatus.status}; live status unavailable or mismatched` },
    capture: resolved && captureEnabledForHost(capture, 'cursor')
      ? { ok: true, detail: 'host-extracted structured capture enabled' }
      : { ok: false, detail: 'automatic capture disabled' },
    retrieval: liveStatus?.full_retrieval === true
      ? { ok: true, detail: `full retrieval via ${liveStatus.embedder}` }
      : { ok: false, detail: 'fallback only; full retrieval is not enabled' },
  };
  const personalLiveReadiness = projectSupportedHostLiveReadiness('cursor', checks, new Date());
  const ready = personalLiveReadiness.outcome === 'ready';
  return {
    product: `Pulse Cursor ${resolved?.runtime?.kind === 'desk' ? 'Desk' : resolved?.runtime?.kind === 'personal' ? 'Personal' : 'unbound'} memory`,
    target_host: 'cursor',
    authority_profile: authorityProfile,
    personal_live_readiness: personalLiveReadiness,
    verdict: ready
      ? syntheticAuthority
        ? 'Pulse Cursor synthetic test lifecycle ready; production authority is not active.'
        : 'Pulse Cursor automatic lifecycle ready.'
      : 'Pulse Cursor automatic lifecycle is not ready.',
    checks,
    trust: {
      authority_mode: syntheticAuthority ? 'synthetic-test' : 'production',
      raw_transcript_capture: liveStatus?.raw_capture_enabled ?? null,
      backend_llm_enabled: liveStatus?.backend_llm_enabled ?? null,
      full_retrieval: liveStatus?.full_retrieval ?? false,
      embedder: liveStatus?.embedder ?? '',
      external_embedding_api: /cohere|^embed-(?:english|multilingual)/i.test(liveStatus?.embedder ?? ''),
      release_manifest_digest: productEdge?.release_manifest_digest ?? '',
      release_version: productEdge?.release_version ?? '',
      release_epoch: productEdge?.release_epoch ?? null,
    },
  };
}

async function runCursorProductDoctor(rest = []) {
  const report = await cursorProductDoctorReport();
  if (rest.includes('--json')) {
    console.log(JSON.stringify(report, null, 2));
  } else {
    console.log('[pulse] doctor cursor');
    for (const [label, check] of Object.entries(report.checks)) printDoctorLine(label, check);
    console.log(`\nRetrieval: ${report.trust.full_retrieval ? `full (${report.trust.embedder})` : 'fallback only; full retrieval is not enabled'}`);
    printPersonalAuthorityProfile(report.authority_profile);
    console.log(`Verdict: ${report.verdict}`);
    if (!report.checks.hooks.ok && report.checks.plugin.ok) console.log('Next: complete one normal Cursor turn, then run doctor again');
  }
  if (Object.values(report.checks).some((check) => !check.ok)) process.exitCode = 1;
}

function commandOnPath(name) {
  const pathValue = process.env.PATH ?? '';
  for (const dir of pathValue.split(':')) {
    if (dir && existsSync(join(dir, name))) {
      return true;
    }
  }
  return false;
}

function cursorInstallationDetected() {
  return commandOnPath('cursor') || commandOnPath('cursor-agent') ||
    existsSync('/Applications/Cursor.app') || existsSync(join(homedir(), 'Applications', 'Cursor.app'));
}

function inspectClaudeNativeProductPlugin(edge, executable = 'claude') {
	const result = spawnSync(executable, ['plugin', 'list', '--json'], {
		encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5000, killSignal: 'SIGTERM',
	});
	if (result.status !== 0) return { ok: false, reason: 'claude_plugin_list_failed' };
	const plugin = parseClaudePluginList(result.stdout);
	return { ...inspectCodexPluginCompatibility(plugin, edge), plugin };
}

function requireCommand(name) {
  if (!commandOnPath(name)) {
    throw new Error(`missing required command: ${name}`);
  }
}

function requireInteractiveDestructiveCLI(action) {
  if (process.stdin.isTTY !== true || process.stdout.isTTY !== true) {
    throw new Error(`pulse ${action} requires a directly attached interactive terminal; agents and pipes cannot authorize deletion`);
  }
}

function previewSourceRoot() {
  const explicit = process.env.PULSE_PREVIEW_SOURCE_DIR
    ? resolve(process.env.PULSE_PREVIEW_SOURCE_DIR)
    : '';
  const candidates = [
    explicit,
    resolve(CLI_PACKAGE_ROOT, 'vendor', 'pulse-preview-source'),
  ].filter(Boolean);
  for (const candidate of candidates) {
    if (
      existsSync(join(candidate, 'pulse-app', 'cmd', 'pulse'))
      && existsSync(join(candidate, 'mcp', 'package.json'))
    ) {
      return candidate;
    }
  }
  return undefined;
}

function pulseDaemonAddr() {
  const url = new URL(DEFAULT_BASE_URL);
  if (url.protocol !== 'http:') {
    throw new Error('local preview daemon requires an http://127.0.0.1 base URL');
  }
  const host = url.hostname || '127.0.0.1';
  const port = url.port || '80';
  return `${host}:${port}`;
}

async function pulseStatusReady() {
  if (!existsSync(SECRET_PATH)) {
    return false;
  }
  const secret = readSecret();
  try {
    const response = await fetch(`${DEFAULT_BASE_URL.replace(/\/$/, '')}/memory/status`, {
      headers: { 'X-Pulse-Key': secret },
      signal: AbortSignal.timeout(500),
    });
    return response.ok;
  } catch {
    return false;
  }
}

async function pulseStatusDetails(timeoutMs = 700) {
  if (!existsSync(SECRET_PATH)) {
    return { ok: false, error: 'secret missing' };
  }
  const secret = readSecret();
  try {
    const response = await fetch(`${DEFAULT_BASE_URL.replace(/\/$/, '')}/memory/status`, {
      headers: { 'X-Pulse-Key': secret },
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (!response.ok) {
      return { ok: false, error: `HTTP ${response.status}` };
    }
    return { ok: true, data: await response.json() };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) };
  }
}

function runRequired(commandName, commandArgs, { cwd, env = {} } = {}) {
  const result = spawnSync(commandName, commandArgs, {
    cwd,
    env: { ...process.env, ...env },
    encoding: 'utf8',
  });
  if (result.status !== 0) {
    const detail = `${result.stdout ?? ''}\n${result.stderr ?? ''}`.trim().slice(0, 1200);
    throw new Error(`${commandName} ${commandArgs.join(' ')} failed${detail ? `:\n${detail}` : ''}`);
  }
  return result;
}

function writeProductDaemonActivation(managedRuntime, runtime) {
	if (managedRuntime?.schema !== 'pulse.managed_product_runtime.v1') {
		throw new Error('Pulse managed product runtime activation is invalid');
	}
	const productEdge = managedRuntime.product_edge;
	if (productEdge?.schema !== 'pulse.codex_product_edge.v1' ||
		productEdge.release_manifest_digest !== managedRuntime.verified_release?.manifest_digest ||
		productEdge.release_version !== managedRuntime.verified_release?.version ||
		productEdge.release_epoch !== managedRuntime.verified_release?.epoch) {
		throw new Error('Pulse signed Codex product edge is invalid');
	}
	const executable = realpathSync(resolve(managedRuntime.daemon.path));
	const info = lstatSync(executable);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
		(info.mode & 0o077) !== 0 || (info.mode & 0o111) === 0) {
		throw new Error('Pulse product daemon activation executable is unsafe');
	}
	if (!runtime?.ok || !isAbsolute(runtime.path) || !/^[a-f0-9]{64}$/.test(runtime.digest ?? '')) {
		throw new Error('Pulse product runtime activation is invalid');
	}
	atomicWriteJSON(join(DATA_DIR, 'runtime', 'product-daemon.json'), {
		activated_at: new Date().toISOString(),
		daemon_activation_digest: managedRuntime.daemon.activation_digest,
		daemon_artifact_id: managedRuntime.daemon.artifact_id,
		daemon_artifact_sha256: managedRuntime.daemon.artifact_sha256,
		daemon_digest: managedRuntime.daemon.digest,
		daemon_path: executable,
		daemon_tree_digest: managedRuntime.daemon.tree_digest,
		embedder_runtime_activation_digest: managedRuntime.embedder_runtime.activation_digest,
		embedder_runtime_artifact_id: managedRuntime.embedder_runtime.artifact_id,
		embedder_runtime_artifact_sha256: managedRuntime.embedder_runtime.artifact_sha256,
		embedder_runtime_tree_digest: managedRuntime.embedder_runtime.tree_digest,
		model_activation_digest: managedRuntime.model.activation_digest,
		model_artifact_id: managedRuntime.model.artifact_id,
		model_artifact_sha256: managedRuntime.model.artifact_sha256,
		model_tree_digest: managedRuntime.model.tree_digest,
		plugin_runtime_activation_digest: productEdge.plugin_runtime_activation_digest,
		plugin_runtime_artifact_id: productEdge.plugin_runtime_artifact_id,
		plugin_runtime_artifact_sha256: productEdge.plugin_runtime_artifact_sha256,
		plugin_runtime_tree_digest: productEdge.plugin_runtime_tree_digest,
		plugin_tree_digest: productEdge.plugin_tree_digest,
		release_epoch: productEdge.release_epoch,
		release_manifest_digest: productEdge.release_manifest_digest,
		release_version: productEdge.release_version,
		runtime_path: resolve(runtime.path),
		runtime_tree_digest: runtime.digest,
		schema: 'pulse.product_activation.v4',
	});
	return executable;
}

function committedCodexProductEdge({ activation, committedSet: committed }) {
	if (!activation || !committed) throw new Error('codex_product_activation_unavailable');
	if (committed.record.manifest_digest !== activation.release_manifest_digest ||
		committed.record.version !== activation.release_version ||
		committed.record.epoch !== activation.release_epoch) {
		throw new Error('codex_product_release_mismatch');
	}
	return resolveSignedCodexProductEdge({
		release: {
			schema: 'pulse.verified_release_manifest.v1',
			manifest_digest: committed.record.manifest_digest,
			version: committed.record.version,
			epoch: committed.record.epoch,
		},
		activation: committed.activations['plugin-runtime'],
	});
}

async function ensureManagedProductRuntime(runtime, { publishConfig = true } = {}) {
	if (process.env.PULSE_GO_BIN) {
		throw new Error('PULSE_GO_BIN is developer-only and cannot satisfy Pulse product readiness');
	}
	const provisioned = await provisionPersonalRuntime(packagedPersonalRuntimeOptions(DATA_DIR));
	const productEdge = resolveSignedCodexProductEdge({
		release: provisioned.release,
		activation: provisioned.activationSet.activations['plugin-runtime'],
	});
	return {
		...resolveManagedRuntime(runtime, {
			installRoot: join(DATA_DIR, 'artifacts'), publishConfig,
			verifiedActivations: provisioned.activationSet.activations,
		}),
		product_edge: productEdge,
		verified_release: provisioned.release,
	};
}

async function ensureVendoredPreviewRuntime() {
  if (process.env.PULSE_PREVIEW_RUNTIME_SETUP === '0') {
    return { enabled: false };
  }
  const sourceRoot = previewSourceRoot();
  if (!sourceRoot) {
    return { enabled: false };
  }

  requireCommand('go');
  requireCommand('npm');
  requireCommand('claude');

  const appDir = join(sourceRoot, 'pulse-app');
  const mcpDir = join(sourceRoot, 'mcp');
  const binDir = join(DATA_DIR, 'bin');
  const logDir = join(DATA_DIR, 'logs');
  const daemonBin = join(binDir, 'pulse-preview-daemon');
  const daemonLog = join(logDir, 'pulse-preview-daemon.log');
  const pidFile = join(DATA_DIR, 'pulse-preview-daemon.pid');
  const mcpEntrypoint = join(mcpDir, 'dist', 'index.js');

  mkdirSync(binDir, { recursive: true, mode: 0o700 });
  mkdirSync(logDir, { recursive: true, mode: 0o700 });

  console.log('[pulse] Building local Pulse daemon...');
  runRequired('go', ['build', '-o', daemonBin, './cmd/pulse'], { cwd: appDir });

  console.log('[pulse] Building local Pulse MCP package...');
  runRequired('npm', ['ci'], { cwd: mcpDir });
  runRequired('npm', ['run', 'build'], { cwd: mcpDir });
  process.env.PULSE_MCP_ENTRYPOINT = mcpEntrypoint;

  if (await pulseStatusReady()) {
    console.log(`[pulse] Pulse daemon: already running at ${DEFAULT_BASE_URL}`);
    return { enabled: true, alreadyRunning: true };
  }

  console.log(`[pulse] Starting Pulse daemon at ${DEFAULT_BASE_URL}`);
  const logFd = openSync(daemonLog, 'a');
  const child = spawn(daemonBin, ['-addr', pulseDaemonAddr(), '-data-dir', DATA_DIR], {
    cwd: appDir,
    detached: true,
    env: {
      ...process.env,
      PULSE_MODE: 'local-auto',
      ANTHROPIC_API_KEY: '',
      OPENAI_API_KEY: '',
      COHERE_API_KEY: '',
    },
    stdio: ['ignore', logFd, logFd],
  });
  child.unref();
  writeFileSync(pidFile, `${child.pid}\n`, { mode: 0o600 });

  for (let i = 0; i < 80; i += 1) {
    if (await pulseStatusReady()) {
      console.log('[pulse] Pulse daemon: running locally');
      return { enabled: true, started: true };
    }
    await sleep(150);
  }
  throw new Error(`Pulse daemon did not become ready. Log: ${daemonLog}`);
}

function mcpCommandLabel(config) {
  return [config.command, ...(Array.isArray(config.args) ? config.args : [])].join(' ');
}

function claudeProductMcpEnvironment() {
  const env = { PULSE_DATA_DIR: DATA_DIR };
  if (process.env.PULSE_TRUST_MODE === 'test') {
    env.PULSE_TRUST_MODE = 'test';
    for (const name of ['PULSE_BINDING_REGISTRY_PATH', 'PULSE_BINDING_PUBLIC_KEY_PATH', 'PULSE_BINDING_ANCHOR_PATH']) {
      if (process.env[name]) env[name] = process.env[name];
    }
  }
  return env;
}

function claudeProductMcpConfig(runtimePath) {
  return {
    type: 'stdio',
    command: process.execPath,
    args: [runtimePath, 'claude-mcp'],
    env: claudeProductMcpEnvironment(),
  };
}

function claudeMcpMutation(args, executable = 'claude') {
	return spawnSync(executable, args, {
		stdio: 'pipe', encoding: 'utf8', timeout: 10_000, killSignal: 'SIGTERM',
	});
}

function claudeMutationFailure(result, action) {
	const detail = `${result?.stderr || result?.stdout || result?.error?.message || ''}`.trim().slice(0, 500);
	return new Error(`claude mcp ${action} failed${detail ? `: ${detail}` : ''}`);
}

function removeClaudeCodeExternalRegistration(executable = 'claude') {
	const removed = claudeMcpMutation(['mcp', 'remove', 'pulse', '--scope', 'local'], executable);
	const removeDetail = `${removed.stderr || removed.stdout || removed.error?.message || ''}`;
	if (removed.error || (removed.status !== 0 && !/not found|not registered|does not exist/i.test(removeDetail))) {
		throw claudeMutationFailure(removed, 'remove');
	}
	const verified = claudeMcpMutation(['mcp', 'get', 'pulse'], executable);
	if (verified.error) throw claudeMutationFailure(verified, 'get after remove');
	if (verified.status === 0) throw new Error('claude mcp remove verification failed: Pulse is still registered');
}

function installClaudeCode(runtimePath, { requireExternal = false } = {}) {
	const config = claudeProductMcpConfig(runtimePath);
  const json = JSON.stringify(config);
  const path = resolve(process.cwd(), '.mcp.json');
  const current = existsSync(path)
    ? JSON.parse(readFileSync(path, 'utf8'))
    : { mcpServers: {} };
	const removed = claudeMcpMutation(['mcp', 'remove', 'pulse', '--scope', 'local']);
	const removeDetail = `${removed.stderr || removed.stdout || removed.error?.message || ''}`;
	if (removed.status !== 0 && !/not found|not registered|does not exist/i.test(removeDetail) && requireExternal) {
		throw claudeMutationFailure(removed, 'remove');
	}
	const claude = claudeMcpMutation(['mcp', 'add-json', '--scope', 'local', 'pulse', json]);
  if (claude.status === 0) {
    if (current.mcpServers && typeof current.mcpServers === 'object' &&
        Object.hasOwn(current.mcpServers, 'pulse')) {
      delete current.mcpServers.pulse;
      atomicWriteJSON(path, current);
      console.log('[pulse] Removed obsolete project Pulse MCP shadow; unrelated MCP servers were preserved');
    }
		if (requireExternal && !checkClaudeProductMCP(runtimePath).ok) {
			throw new Error('Claude Code MCP rollback verification failed');
		}
		console.log('[pulse] Claude Code MCP registered via claude mcp add-json');
		return { mode: 'external' };
  }
	if (requireExternal) throw claudeMutationFailure(claude, 'add-json');

  current.mcpServers = current.mcpServers ?? {};
  current.mcpServers.pulse = config;
  atomicWriteJSON(path, current);
  ensureGitignoreEntry('.mcp.json');
	console.log(`[pulse] Claude CLI registration failed; wrote project MCP config to ${path}`);
	console.log('[pulse] .mcp.json is secret-free and was added to .gitignore because it contains a machine-local runtime path');
	return { mode: 'project-fallback' };
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", "'\\''")}'`;
}

const CLAUDE_HOOK_EVENTS = [
  'SessionStart', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PreCompact',
  'PostCompact', 'SubagentStart', 'SubagentStop', 'Stop',
];

function pulseHookCommand(eventName, runtimePath) {
  const environment = [
    `PULSE_DATA_DIR=${shellQuote(DATA_DIR)}`,
  ];
  if (process.env.PULSE_TRUST_MODE === 'test') {
    environment.push(`PULSE_TRUST_MODE=${shellQuote('test')}`);
    for (const name of ['PULSE_BINDING_REGISTRY_PATH', 'PULSE_BINDING_PUBLIC_KEY_PATH', 'PULSE_BINDING_ANCHOR_PATH']) {
      if (process.env[name]) environment.push(`${name}=${shellQuote(process.env[name])}`);
    }
  }
  return [
    ...environment,
    shellQuote(process.execPath),
    shellQuote(runtimePath),
    'claude-hook',
    eventName,
  ].join(' ');
}

function hookConfig(runtimePath, runtimeDigest) {
	const command = (eventName) => pulseHookCommand(eventName, runtimePath);
  const handler = (eventName, timeout = 30) => ({
    type: 'command', command: command(eventName), timeout,
  });
  return {
    hooks: {
      SessionStart: [
        {
          matcher: 'startup|resume|clear|compact',
          hooks: [handler('SessionStart', 60)],
        },
      ],
      UserPromptSubmit: [
        {
          hooks: [handler('UserPromptSubmit')],
        },
      ],
      PreToolUse: [{ matcher: '*', hooks: [handler('PreToolUse', 10)] }],
      PostToolUse: [
        {
          matcher: 'mcp__pulse-product__pulse_remember',
          hooks: [handler('PostToolUse', 10)],
        },
      ],
      PreCompact: [{ matcher: 'manual|auto', hooks: [handler('PreCompact')] }],
      PostCompact: [{ matcher: 'manual|auto', hooks: [handler('PostCompact')] }],
      SubagentStart: [{ matcher: '*', hooks: [handler('SubagentStart', 60)] }],
      SubagentStop: [{ matcher: '*', hooks: [handler('SubagentStop')] }],
      Stop: [
        {
          hooks: [handler('Stop', 60)],
        },
      ],
    },
  };
}

function atomicWriteJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  try {
    writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

function installClaudeCodeHooks(runtimePath, runtimeDigest, { dryRun = false } = {}) {
  const hooks = hookConfig(runtimePath, runtimeDigest);
  if (dryRun) {
    console.log(JSON.stringify(hooks, null, 2));
    return;
  }
  const path = resolve(process.cwd(), '.claude', 'settings.local.json');
  const current = existsSync(path) ? JSON.parse(readFileSync(path, 'utf8')) : {};
  current.hooks = mergeHookConfig(current.hooks ?? {}, hooks.hooks);
  atomicWriteJSON(path, current);
  ensureGitignoreEntry('.claude/settings.local.json');
  console.log(`[pulse] Claude Code continuity hooks written to ${path}`);
  console.log('[pulse] .claude/settings.local.json was added to .gitignore');
}

function removeLegacyClaudeProductRegistration(executable = 'claude') {
	removeClaudeCodeExternalRegistration(executable);
	const mcpPath = resolve(process.cwd(), '.mcp.json');
	if (existsSync(mcpPath)) {
		const current = JSON.parse(readFileSync(mcpPath, 'utf8'));
		if (current.mcpServers && typeof current.mcpServers === 'object' && Object.hasOwn(current.mcpServers, 'pulse')) {
			delete current.mcpServers.pulse;
			atomicWriteJSON(mcpPath, current);
		}
	}
	const settingsPath = resolve(process.cwd(), '.claude', 'settings.local.json');
	if (!existsSync(settingsPath)) return;
	const current = JSON.parse(readFileSync(settingsPath, 'utf8'));
	if (!current.hooks || typeof current.hooks !== 'object') return;
	for (const [event, entries] of Object.entries(current.hooks)) {
		const kept = withoutPulseHookEntries(Array.isArray(entries) ? entries : []);
		if (kept.length > 0) current.hooks[event] = kept;
		else delete current.hooks[event];
	}
	if (Object.keys(current.hooks).length === 0) delete current.hooks;
	atomicWriteJSON(settingsPath, current);
}

async function disconnectClaudeCode() {
	const release = await acquireProductActivationLock();
	try {
		disconnectClaudeCodeActivation();
	} finally {
		await release();
	}
}

function disconnectClaudeCodeActivation() {
	let binding;
	try { binding = resolveCodexMcpRuntime(process.cwd()).binding; } catch { /* legacy cleanup can proceed unbound */ }
	const snapshots = snapshotLocalFiles(activationFilePaths(binding, { includeClaude: true }));
	const installedRuntime = inspectCodexRuntime(DATA_DIR);
	const hadExternalRegistration = installedRuntime.ok && checkClaudeProductMCP(installedRuntime.path).ok;
	const detection = detectClaudeCodeCLI({ home: homedir() });
	const claudeExecutable = detection.available ? detection.executable_path : commandOnPath('claude') ? 'claude' : undefined;
	const nativePlugin = claudeExecutable
		? (() => {
			const listed = spawnSync(claudeExecutable, ['plugin', 'list', '--json'], {
				encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5000, killSignal: 'SIGTERM',
			});
			return listed.status === 0 ? parseClaudePluginList(listed.stdout) : { installed: false, enabled: false };
		})()
		: { installed: false, enabled: false };
	const mcpPath = resolve(process.cwd(), '.mcp.json');
	const projectMcp = safeReadJSON(mcpPath);
	const hadProjectFallback = projectMcp?.mcpServers &&
		typeof projectMcp.mcpServers === 'object' && Object.hasOwn(projectMcp.mcpServers, 'pulse');
	let accessResult = { remaining_for_host: 0, remaining_for_workspace: 0 };
	try {
		if (claudeExecutable) {
			removeClaudeCodeExternalRegistration(claudeExecutable);
		} else if (hadExternalRegistration && !hadProjectFallback) {
			throw new Error('Claude Code CLI is required to remove the external Pulse MCP registration');
		}

		const settingsPath = resolve(process.cwd(), '.claude', 'settings.local.json');
		if (existsSync(settingsPath)) {
			const current = JSON.parse(readFileSync(settingsPath, 'utf8'));
			if (current.hooks && typeof current.hooks === 'object') {
				for (const [event, entries] of Object.entries(current.hooks)) {
					const kept = withoutPulseHookEntries(Array.isArray(entries) ? entries : []);
					if (kept.length > 0) current.hooks[event] = kept;
					else delete current.hooks[event];
				}
				if (Object.keys(current.hooks).length === 0) delete current.hooks;
			}
			atomicWriteJSON(settingsPath, current);
		}

		if (existsSync(mcpPath)) {
			const current = JSON.parse(readFileSync(mcpPath, 'utf8'));
			if (current.mcpServers && typeof current.mcpServers === 'object') delete current.mcpServers.pulse;
			atomicWriteJSON(mcpPath, current);
		}

		if (binding) {
			accessResult = removeProductHostAccess({
				productHome: join(homedir(), '.pulse'), binding, host: 'claude-code',
			});
		}
		writeCaptureStateFiles({
			globalDataDir: DATA_DIR, binding, host: 'claude-code',
			enabled: accessResult.remaining_for_host > 0,
			globalEnabled: accessResult.remaining_for_host > 0,
			reason: 'host_disconnected',
		});
		if (binding && accessResult.remaining_for_workspace === 0) {
			try { removeProductLocator({ productHome: join(homedir(), '.pulse'), binding }); }
			catch (error) { if (!/missing/i.test(error.message)) throw error; }
		}
		if (binding && accessResult.remaining_for_host === 0 && claudeExecutable && nativePlugin.installed) {
			disableClaudePlugin({ executable: claudeExecutable });
		}
	} catch (error) {
		const failures = [];
		try { restoreLocalFiles(snapshots); } catch (failure) { failures.push(failure); }
		if (hadExternalRegistration) {
			try { installClaudeCode(installedRuntime.path, { requireExternal: true }); } catch (failure) { failures.push(failure); }
		}
		if (failures.length > 0) {
			throw new Error(`Claude Code disconnect failed (${error.message}); rollback failed: ${failures.map((failure) => failure.message).join('; ')}`);
		}
		throw error;
	}

	console.log(accessResult.remaining_for_workspace > 0
		? '[pulse] Claude Code disconnected; the shared workspace locator remains for other connected hosts.'
		: '[pulse] Claude Code disconnected from this workspace. Existing Personal/Desk memory was preserved.');
}

function safeReadJSON(path) {
  try {
    return JSON.parse(readFileSync(path, 'utf8'));
  } catch {
    return undefined;
  }
}

function checkCommandVersion(name, versionArgs = ['--version']) {
  if (!commandOnPath(name)) {
    return { ok: false, detail: 'missing' };
  }
  const result = spawnSync(name, versionArgs, {
    encoding: 'utf8',
    timeout: 5000,
  });
  if (result.status !== 0) {
    return { ok: false, detail: 'found but did not answer' };
  }
  return {
    ok: true,
    detail: `${result.stdout || result.stderr}`.trim().split(/\r?\n/)[0],
  };
}

function claudeProductVersionCheck(executable) {
  let commandCheck;
  if (typeof executable === 'string' && isAbsolute(executable)) {
    const result = spawnSync(executable, ['--version'], { encoding: 'utf8', timeout: 5000 });
    commandCheck = result.status === 0
      ? { ok: true, detail: `${result.stdout || result.stderr}`.trim().split(/\r?\n/)[0] }
      : { ok: false, detail: 'found but did not answer' };
  } else {
    commandCheck = checkCommandVersion('claude', ['--version']);
  }
  if (!commandCheck.ok) return commandCheck;
  const match = commandCheck.detail.match(/(?:^|\s)(\d+)\.(\d+)\.(\d+)(?:\s|$)/);
  if (!match) return { ok: false, detail: `unrecognized version: ${commandCheck.detail}` };
  const actual = match.slice(1).map(Number);
  const minimum = [2, 1, 196];
  const firstDifference = actual.findIndex((value, index) => value !== minimum[index]);
  const supported = firstDifference === -1 || actual[firstDifference] > minimum[firstDifference];
  return {
    ok: supported,
    detail: supported
      ? `${actual.join('.')} (prompt_id lifecycle supported)`
      : `${actual.join('.')} (needs 2.1.196+; run claude update)`,
  };
}

function requireClaudeProductVersion() {
  const check = claudeProductVersionCheck();
  if (!check.ok) throw new Error(`Claude Code automatic lifecycle unavailable: ${check.detail}`);
  return check;
}

function checkNodeRuntime() {
  let supported = true;
  try { assertSupportedNodeVersion(process.versions.node); } catch { supported = false; }
  return {
    ok: supported,
    detail: `v${process.versions.node}${supported ? '' : ' (needs 20+)'}`,
  };
}

function checkClaudeMCPConfigured() {
  const projectMCP = safeReadJSON(resolve(process.cwd(), '.mcp.json'));
  if (projectMCP?.mcpServers?.pulse) {
    return { ok: true, detail: 'project MCP config has Pulse' };
  }
  if (commandOnPath('claude')) {
    const result = spawnSync('claude', ['mcp', 'list'], {
      encoding: 'utf8',
      timeout: 1500,
    });
    if (result.status === 0 && /\bpulse\b/i.test(`${result.stdout}\n${result.stderr}`)) {
      return { ok: true, detail: 'Claude Code lists Pulse MCP' };
    }
  }
  return { ok: false, detail: 'Pulse MCP not found in this project' };
}

function checkClaudeHooksConfigured() {
  const settings = safeReadJSON(resolve(process.cwd(), '.claude', 'settings.local.json'));
  const required = [
    ['SessionStart', 'session-start'],
    ['UserPromptSubmit', 'user-prompt-submit'],
    ['PostToolUse', 'post-tool-use'],
    ['Stop', 'stop'],
  ];
  const missing = [];
  for (const [event, hookName] of required) {
    const entries = Array.isArray(settings?.hooks?.[event]) ? settings.hooks[event] : [];
    const commands = entries.flatMap((entry) => Array.isArray(entry?.hooks)
      ? entry.hooks.map((hook) => String(hook?.command ?? ''))
      : []);
    if (!commands.some((cmd) => cmd.includes('pulse') && cmd.includes('hook') && cmd.includes(hookName))) {
      missing.push(event);
    }
  }
  if (missing.length > 0) {
    return { ok: false, detail: `missing ${missing.join(', ')}` };
  }
  return { ok: true, detail: 'SessionStart, prompt/tool, and Stop hooks installed' };
}

async function checkViewerReady() {
  if (!existsSync(SECRET_PATH)) {
    return { ok: false, detail: 'secret missing' };
  }
  const threadId = safeThreadID(localThreadContext().threadId);
  const access = await checkViewerAccess(DEFAULT_BASE_URL.replace(/\/$/, ''), readSecret(), threadId);
  if (access.status === 0) {
    return { ok: false, detail: 'viewer data endpoint not reachable' };
  }
  if (access.status >= 200 && access.status < 300) {
    return { ok: true, detail: 'viewer data reachable' };
  }
  return { ok: false, detail: `viewer returned HTTP ${access.status}` };
}

function printDoctorLine(label, check) {
  const status = check.ok ? 'ok' : (check.warn ? 'warn' : 'missing');
  const detail = check.detail ? ` - ${check.detail}` : '';
  console.log(`${label}: ${status}${detail}`);
}

function personalAuthorityProfileForDoctor(presenceTrust, { syntheticAuthority = false } = {}) {
  if (!syntheticAuthority && presenceTrust?.ready === true) {
    return portablePersonalAuthorityProfile({
      schema: 'pulse.enhanced_presence.profile.v1',
      version: 1,
      kind: 'macos_native',
      available: true,
      protected_actions: [...PERSONAL_PROTECTED_ACTIONS],
      reason_code: '',
    });
  }
  return portablePersonalAuthorityProfile();
}

function enhancedPresenceDoctorCheck(profile, presenceTrust) {
  if (profile.enhanced_presence.available) {
    return {
      ok: true,
      detail: `optional enhanced presence ready for ${profile.enhanced_presence.protected_actions.join(', ')}`,
    };
  }
  const status = typeof presenceTrust?.status === 'string' ? presenceTrust.status : 'unavailable';
  const issues = Array.isArray(presenceTrust?.issues) && presenceTrust.issues.length > 0
    ? `: ${presenceTrust.issues.join(', ')}`
    : '';
  return {
    ok: true,
    detail: `optional enhanced presence unavailable (${status}${issues}); ordinary Personal memory remains ready`,
  };
}

function printPersonalAuthorityProfile(profile) {
  console.log(`Authority profile: ${profile.schema} (${profile.kind})`);
  console.log('Ordinary Personal memory: ready without enhanced presence');
  if (profile.enhanced_presence.available) {
    console.log(`Authorized protected actions: ${profile.enhanced_presence.protected_actions.join(', ')}`);
    return;
  }
  console.log('Authorized protected actions: none');
  console.log(`Unavailable protected actions: ${PERSONAL_PROTECTED_ACTIONS.join(', ')}`);
  if (process.platform === 'darwin') {
    console.log(`Optional setup for ${PERSONAL_PROTECTED_ACTIONS.join(' and ')} only: pulse trust install --confirm "${TRUST_INSTALL_CONFIRMATION}"`);
  }
}

async function doctorReport() {
  const daemon = await pulseStatusDetails();
  const checks = {
    node: checkNodeRuntime(),
    npm: checkCommandVersion('npm', ['--version']),
    go: checkCommandVersion('go', ['version']),
    claude_code: checkCommandVersion('claude', ['--version']),
    daemon: daemon.ok
      ? { ok: true, detail: DEFAULT_BASE_URL.replace(/\/$/, '') }
      : { ok: false, detail: daemon.error || 'not reachable' },
    port: daemon.ok
      ? { ok: true, detail: `serving ${DEFAULT_BASE_URL.replace(/\/$/, '')}` }
      : { ok: false, detail: `${DEFAULT_BASE_URL.replace(/\/$/, '')} not reachable` },
    mcp: checkClaudeMCPConfigured(),
    hooks: checkClaudeHooksConfigured(),
    viewer: await checkViewerReady(),
    first_memory: {
      ok: true,
      detail: Number.isFinite(daemon.data?.item_count) && daemon.data.item_count > 0
        ? `${daemon.data.item_count} stored memory item(s)`
        : 'pending until the first memory proof runs',
    },
  };
  const backendEnabled = daemon.ok ? Boolean(daemon.data?.backend_llm_enabled) : false;
  const rawEnabled = daemon.ok ? Boolean(daemon.data?.raw_capture_enabled) : false;
  const fullRetrieval = daemon.ok ? Boolean(daemon.data?.full_retrieval) : false;
  const cohereKeyPresent =
    Boolean(process.env.COHERE_API_KEY) || existsSync(join(DATA_DIR, 'cohere-key.txt'));
  const localEmbedModel = process.env.PULSE_LOCAL_EMBED_MODEL ?? '';
  return {
    product: 'Pulse Local Preview',
    version: PREVIEW_VERSION,
    target_host: 'claude-code',
    mode: 'developer_preview',
    base_url: DEFAULT_BASE_URL.replace(/\/$/, ''),
    // Headline verdict. Never claim "Pulse ready" while full retrieval is off:
    // without an embedder this machine runs the safe fallback, not Pulse.
    verdict: fullRetrieval
      ? 'Pulse Local Preview ready.'
      : 'Pulse MCP fallback is ready. Full retrieval is not enabled.',
    checks,
    engine: {
      full_retrieval: fullRetrieval,
      embedder: daemon.ok ? String(daemon.data?.embedder ?? '') : '',
      embedding_path: fullRetrieval
        ? cohereKeyPresent && !localEmbedModel
          ? 'external embedding API'
          : 'local'
        : 'none',
    },
    local_resources: {
      disk_free: diskFreeLabel(),
      memory_total: memoryTotalLabel(),
      local_embedding_model: localEmbedModel
        ? existsSync(localEmbedModel)
          ? 'found'
          : 'configured but missing'
        : 'not configured (optional)',
      external_embedding_key: cohereKeyPresent ? 'present' : 'absent',
    },
    trust: {
      backend_llm_enabled: backendEnabled,
      raw_capture_enabled: rawEnabled,
      external_embedding_api: fullRetrieval && cohereKeyPresent && !localEmbedModel,
      old_chat_import_default: false,
      wipe_available: true,
    },
    next_steps: [
      'pulse init claude-code --yes',
      'pulse demo',
      'pulse viewer',
      'pulse wipe --confirm "wipe pulse memory"',
    ],
  };
}

function diskFreeLabel() {
  try {
    const result = spawnSync('df', ['-k', DATA_DIR], { encoding: 'utf8' });
    const line = (result.stdout ?? '').trim().split('\n').pop() ?? '';
    const fields = line.split(/\s+/);
    const availKB = Number.parseInt(fields[3] ?? '', 10);
    if (Number.isFinite(availKB)) {
      return `${Math.round(availKB / 1024 / 1024)}GB`;
    }
  } catch {
    // best effort
  }
  return 'unknown';
}

function memoryTotalLabel() {
  try {
    if (process.platform === 'darwin') {
      const result = spawnSync('sysctl', ['-n', 'hw.memsize'], { encoding: 'utf8' });
      const bytes = Number.parseInt((result.stdout ?? '').trim(), 10);
      if (Number.isFinite(bytes)) {
        return `${Math.round(bytes / 1024 / 1024 / 1024)}GB`;
      }
    } else if (existsSync('/proc/meminfo')) {
      const match = readFileSync('/proc/meminfo', 'utf8').match(/MemTotal:\s+(\d+) kB/);
      if (match) {
        return `${Math.round(Number.parseInt(match[1], 10) / 1024 / 1024)}GB`;
      }
    }
  } catch {
    // best effort
  }
  return 'unknown';
}

async function runDoctor(rest = []) {
  if (rest[0] === 'codex') {
    await runCodexDoctor(rest.slice(1));
    return;
  }
  if (rest[0] === 'claude-code') {
    await runClaudeProductDoctor(rest.slice(1));
    return;
  }
  if (rest[0] === 'cursor') {
    await runCursorProductDoctor(rest.slice(1));
    return;
  }
  const report = await doctorReport();
  if (rest.includes('--json')) {
    console.log(JSON.stringify(report, null, 2));
    if (Object.values(report.checks).some((check) => !check.ok)) {
      process.exitCode = 1;
    }
    return;
  }

  console.log('[pulse] doctor');
  console.log('Checking the Zero-to-Wow path for Claude Code.\n');

  const checks = [
    ['Node', report.checks.node],
    ['npm', report.checks.npm],
    ['Go', report.checks.go],
    ['Claude Code CLI', report.checks.claude_code],
    ['Pulse daemon', report.checks.daemon],
    ['Port', report.checks.port],
    ['MCP', report.checks.mcp],
    ['Hooks', report.checks.hooks],
    ['Viewer', report.checks.viewer],
    ['First memory', report.checks.first_memory],
  ];

  for (const [label, check] of checks) {
    printDoctorLine(label, check);
  }

  console.log('\nEngine:');
  console.log(`  full retrieval: ${report.engine.full_retrieval ? 'enabled' : 'disabled'}`);
  console.log(`  embedder: ${report.engine.embedder || 'none'}`);
  console.log(`  embedding path: ${report.engine.embedding_path}`);

  console.log('Local resources:');
  console.log(`  disk free: ${report.local_resources.disk_free}`);
  console.log(`  memory: ${report.local_resources.memory_total}`);
  console.log(`  local embedding model: ${report.local_resources.local_embedding_model}`);
  console.log(`  external embedding key: ${report.local_resources.external_embedding_key}`);

  if (report.checks.daemon.ok) {
    const backend = report.trust.backend_llm_enabled ? 'on' : 'off';
    const raw = report.trust.raw_capture_enabled ? 'on' : 'off';
    const extEmbed = report.trust.external_embedding_api ? 'on' : 'off';
    console.log(`\nPrivacy: backend LLM ${backend}; raw transcript capture ${raw}; external embedding API ${extEmbed}; raw import off`);
    console.log('What Pulse will tell Claude next: pulse viewer');
  } else {
    console.log('\nPrivacy: backend LLM unknown; raw transcript capture unknown until daemon answers.');
  }

  console.log(`\nVerdict: ${report.verdict}`);

  const failed = checks.filter(([, check]) => !check.ok);
  if (failed.length > 0) {
    console.log('\nNext:');
    console.log('  pulse init claude-code --yes');
    console.log('  pulse demo');
    console.log('  pulse viewer');
    process.exitCode = 1;
    return;
  }

  console.log('\nPulse is ready for the first memory proof.');
  console.log(`Ask Claude Code: "${FIRST_PROOF_REMEMBER_PROMPT}"`);
}

function stopPreviewDaemon() {
  const pidFile = join(DATA_DIR, 'pulse-preview-daemon.pid');
  if (!existsSync(pidFile)) {
    console.log('[pulse] No Pulse preview daemon pid found.');
    return;
  }
  const pid = Number.parseInt(readFileSync(pidFile, 'utf8').trim(), 10);
  let stopped = false;
  if (Number.isFinite(pid) && pid > 0) {
    try {
      process.kill(pid, 'SIGTERM');
      stopped = true;
    } catch {
      stopped = false;
    }
  }
  try {
    unlinkSync(pidFile);
  } catch {
    // Nothing useful to do; the next init will overwrite the pid file.
  }
  if (stopped) {
    console.log(`[pulse] Stopped Pulse preview daemon pid ${pid}.`);
  } else {
    console.log('[pulse] No running Pulse preview daemon found. Removed stale pid file.');
  }
}

function mergeHookConfig(existing, incoming) {
  const out = { ...existing };
  for (const [event, entries] of Object.entries(incoming)) {
    const existingEntries = Array.isArray(out[event]) ? out[event] : [];
    out[event] = [...withoutPulseHookEntries(existingEntries), ...entries];
  }
  return out;
}

function withoutPulseHookEntries(entries) {
  const kept = [];
  for (const entry of entries) {
    const hooks = Array.isArray(entry?.hooks)
      ? entry.hooks.filter((hook) => !isPulseHookCommand(hook?.command))
      : [];
    if (hooks.length > 0) {
      kept.push({ ...entry, hooks });
    }
  }
  return kept;
}

function isPulseHookCommand(command) {
  const text = String(command ?? '');
  const hookName = String.raw`(?:session-start|user-prompt-submit|post-tool-use|stop)`;
  return new RegExp(String.raw`\bpulse\s+hook\s+${hookName}\b`).test(text)
		|| isPulseProductHookCommand(text)
    || (
      text.includes('PULSE_BASE_URL=')
      && text.includes('PULSE_DATA_DIR=')
      && new RegExp(String.raw`\bhook\s+${hookName}\b`).test(text)
    );
}

function isPulseProductHookCommand(command) {
	const text = String(command ?? '');
	const nativeEvent = CLAUDE_HOOK_EVENTS.join('|');
	return new RegExp(String.raw`(?:^|\s)(?:'[^']*\/src\/cli\.js'|"[^"]*\/src\/cli\.js"|\S*\/src\/cli\.js)\s+claude-hook\s+(?:${nativeEvent})(?:\s|$)`).test(text);
}

function writeLocalAutoMode() {
	mkdirSync(DATA_DIR, { recursive: true, mode: 0o700 });
	writeFileSync(MODE_PATH, 'local-auto\n', { mode: 0o600 });
}

function harnessDisplayName(host) {
  switch (host) {
    case 'claude-code':
      return 'Claude Code';
    case 'codex':
      return 'Codex';
    case 'gemini-cli':
      return 'Gemini CLI';
    case 'cursor':
      return 'Cursor';
    default:
      return 'Claude Code';
  }
}

function harnessTag(host) {
  return String(host || 'claude-code').replace(/-/g, '_');
}

function firstRunViewerURL(host = 'claude-code', threadId = localThreadContext().threadId) {
  const baseURL = DEFAULT_BASE_URL.replace(/\/$/, '');
  const secret = readSecret({ create: true });
  const url = new URL(`${baseURL}/viewer`);
  url.searchParams.set('key', secret);
  url.searchParams.set('thread_id', safeThreadID(threadId));
  url.searchParams.set('first_run', '1');
  url.searchParams.set('host', host);
  return url.toString();
}

function shouldAnimatePulseConnect() {
  if (process.env.CI || process.env.NO_COLOR || args.includes('--no-animate')) {
    return false;
  }
  if (process.env.PULSE_CLI_ANIMATION === '0') {
    return false;
  }
  return process.env.PULSE_CLI_ANIMATION === '1' || Boolean(process.stdout.isTTY);
}

async function showPulseConnectAnimation() {
  await pulseBreath({ cycles: 2 });
}

const PRODUCT_WRITE_STATUSES = new Set(['pending', 'created', 'updated', 'deduplicated', 'canceled', 'rejected', 'failed']);
const PRODUCT_OBJECT_STATUSES = new Set(['created', 'updated', 'deduplicated']);

function validateProductWriteReceipt(receipt) {
  const stableID = (value) => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/.test(value);
  if (!receipt || typeof receipt !== 'object' || receipt.schema !== 'pulse.write_receipt.v1' ||
      !stableID(receipt.receipt_id) || !stableID(receipt.ledger_id) ||
      !PRODUCT_WRITE_STATUSES.has(receipt.status) ||
      !['personal', 'desk'].includes(receipt.destination) ||
      typeof receipt.destination_store_id !== 'string' || !/^store_[a-z0-9][a-z0-9_]{2,127}$/.test(receipt.destination_store_id) ||
      !receipt.safe_provenance || typeof receipt.safe_provenance !== 'object' ||
      typeof receipt.safe_provenance.host !== 'string' || !receipt.safe_provenance.host ||
      !stableID(receipt.safe_provenance.session_id) || !stableID(receipt.safe_provenance.turn_id) ||
      !stableID(receipt.safe_provenance.source_event_key) ||
      !Number.isInteger(receipt.policy_epoch) || receipt.policy_epoch < 0 ||
      !Number.isInteger(receipt.resolver_epoch) || receipt.resolver_epoch < 0 ||
      typeof receipt.measurement_method !== 'string' || !receipt.measurement_method ||
      typeof receipt.created_at !== 'string' || Number.isNaN(Date.parse(receipt.created_at))) {
    throw new Error('invalid product write receipt');
  }
  const hasObject = typeof receipt.object_id === 'string' && receipt.object_id.length > 0;
  if (hasObject && !stableID(receipt.object_id)) {
    throw new Error('invalid product write receipt object identity');
  }
  if (PRODUCT_OBJECT_STATUSES.has(receipt.status) !== hasObject) {
    throw new Error('invalid product write receipt object identity');
  }
  if (receipt.status === 'pending' &&
      (typeof receipt.candidate_id !== 'string' || !receipt.candidate_id ||
       !Number.isInteger(receipt.candidate_version) || receipt.candidate_version < 1 ||
       typeof receipt.content_digest !== 'string' || !/^[a-f0-9]{64}$/.test(receipt.content_digest))) {
    throw new Error('invalid pending product write receipt');
  }
  if (['canceled', 'rejected', 'failed'].includes(receipt.status) &&
      (typeof receipt.reason_code !== 'string' || !receipt.reason_code)) {
    throw new Error('invalid terminal product write receipt');
  }
  return receipt;
}

function productWriteReceipts(out) {
  if (!Array.isArray(out?.receipts)) return null;
  if (out.receipts.length === 0) throw new Error('product write response has no item receipt');
  return out.receipts.map(validateProductWriteReceipt);
}

async function rememberInstallMemory(host = 'claude-code') {
  const display = harnessDisplayName(host);
  const capsule = {
    schema: 'pulse.memory_capsule.v1',
    source: {
      host: 'pulse-cli',
      conversation_scope: 'install_event',
      timestamp: new Date().toISOString(),
    },
    items: [{
      kind: 'system_event',
      redacted_summary: `User installed Pulse MCP and connected it to ${display}.`,
      confidence: 1.0,
      evidence_hint: 'user_confirmed',
      privacy_tier: 'private',
      retention: 'project',
      tags: ['pulse_install', 'first_memory', harnessTag(host)],
    }],
    raw_input_included: false,
  };
  try {
    const out = await pulseFetch('/memory/remember', { body: capsule, timeoutMs: 2500 });
    let receipts;
    try {
      receipts = productWriteReceipts(out) || [];
    } catch (error) {
      return { ok: false, status: 'invalid_receipt', reason: error.message, ids: [], receiptIds: [] };
    }
    const pending = receipts.find((receipt) => receipt?.status === 'pending');
    const materialized = receipts.find((receipt) => ['created', 'deduplicated', 'updated'].includes(receipt?.status));
    const terminalFailure = receipts.find((receipt) => ['canceled', 'rejected', 'failed'].includes(receipt?.status));
    if (terminalFailure) {
      return {
        ok: false,
        status: terminalFailure.status,
        reason: terminalFailure.reason_code || 'write_not_materialized',
        ids: [],
        receiptIds: [terminalFailure.receipt_id].filter(Boolean),
      };
    }
    if (receipts.length > 0 && !pending && !materialized) {
      return { ok: false, status: 'invalid_receipt', reason: 'unknown_receipt_status', ids: [], receiptIds: [] };
    }
    if (pending || materialized) {
      return {
        ok: true,
        status: pending ? 'pending' : materialized.status,
        ids: materialized?.object_id ? [materialized.object_id] : [],
        receiptIds: receipts.map((receipt) => receipt?.receipt_id).filter(Boolean),
      };
    }
    if (out?.ok === true && Array.isArray(out.ids)) {
      return { ok: true, status: 'preview_created', ids: out.ids, receiptIds: [] };
    }
    return { ok: false, status: 'invalid_receipt', reason: 'missing_write_receipt', ids: [], receiptIds: [] };
  } catch {
    return { ok: false, status: 'unavailable', reason: 'daemon_unavailable', ids: [], receiptIds: [] };
  }
}

async function connectClaudeCode() {
	const remoteControl = args.includes('--remote-control');
	if (args.includes('--dry-run')) {
    const runtimePath = join(DATA_DIR, 'runtime', 'codex', 'current', 'src', 'cli.js');
    const runtimeDigest = '0'.repeat(64);
    const config = claudeProductMcpConfig(runtimePath);
		console.log('[pulse] Claude Code connect dry run');
		console.log(`[pulse] MCP command: ${mcpCommandLabel(config)}`);
    installClaudeCodeHooks(runtimePath, runtimeDigest, { dryRun: true });
    if (remoteControl) {
      printRemoteControlNextSteps();
    }
    return;
  }
	const release = await acquireProductActivationLock();
	try {
		await connectClaudeCodeActivation(remoteControl);
	} finally {
		await release();
	}
}

async function connectCursor() {
	const release = await acquireProductActivationLock();
	try {
		await recoverBindingAuthority();
		const resolved = resolveCodexMcpRuntime(process.cwd());
		const productState = readProductActivationBundle(DATA_DIR);
		const edge = committedCodexProductEdge(productState);
		const installed = installCursorPlugin(edge.plugin_root, {
			cursorHome: cursorHomePath(), expectedDigest: edge.plugin_tree_digest,
		});
		writeCaptureStateFiles({
			globalDataDir: DATA_DIR, binding: resolved.binding, host: 'cursor', enabled: true,
			reason: 'cursor_plugin_connected',
		});
		const defaults = defaultBindingPaths();
		writeProductLocators({
			codexHome: codexHomePath(), binding: resolved.binding, dataDir: DATA_DIR,
			registryPath: process.env.PULSE_BINDING_REGISTRY_PATH ?? defaults.registryPath,
			publicKeyPath: process.env.PULSE_BINDING_PUBLIC_KEY_PATH ?? defaults.publicKeyPath,
			anchorPath: process.env.PULSE_BINDING_ANCHOR_PATH ?? defaults.anchorPath,
			trustMode: process.env.PULSE_TRUST_MODE === 'test' ? 'test' : 'production',
		});
		writeProductHostAccess({
			productHome: join(homedir(), '.pulse'), binding: resolved.binding, host: 'cursor',
		});
		console.log(`[pulse] Cursor plugin ${installed.reused ? 'already connected' : 'connected'} to the shared ${resolved.runtime.kind} vault.`);
		console.log('[pulse] Reload Cursor once so it discovers the local Pulse plugin.');
	} finally {
		await release();
	}
}

async function disconnectCursor() {
	const release = await acquireProductActivationLock();
	try {
		let binding;
		try {
			await recoverBindingAuthority();
			binding = resolveCodexMcpRuntime(process.cwd()).binding;
		} catch { /* plugin removal remains possible after a lost binding */ }
		let accessResult = { remaining_for_host: 0, remaining_for_workspace: 0 };
		if (binding) {
			accessResult = removeProductHostAccess({
				productHome: join(homedir(), '.pulse'), binding, host: 'cursor',
			});
			writeCaptureStateFiles({
				globalDataDir: DATA_DIR, binding, host: 'cursor',
				enabled: accessResult.remaining_for_host > 0,
				globalEnabled: accessResult.remaining_for_host > 0,
				reason: 'host_disconnected',
			});
			if (accessResult.remaining_for_workspace === 0) {
				try { removeProductLocator({ productHome: join(homedir(), '.pulse'), binding }); }
				catch (error) { if (!/missing/i.test(error.message)) throw error; }
			}
		}
		const result = accessResult.remaining_for_host === 0
			? removeCursorPlugin({ cursorHome: cursorHomePath() })
			: { removed: false };
		console.log(accessResult.remaining_for_host > 0
			? '[pulse] Cursor disconnected from this workspace; the global plugin remains for another workspace.'
			: `[pulse] Cursor ${result.removed ? 'disconnected' : 'was not connected'}. Local memory was preserved.`);
	} finally {
		await release();
	}
}

async function connectClaudeCodeActivation(remoteControl) {
  requireClaudeProductVersion();
  for (const path of [
    resolve(process.cwd(), '.mcp.json'),
    resolve(process.cwd(), '.claude', 'settings.local.json'),
  ]) {
    if (existsSync(path)) JSON.parse(readFileSync(path, 'utf8'));
  }
  await recoverBindingAuthority();
  const resolved = resolveCodexMcpRuntime(process.cwd());
	const releaseVaultActivation = await acquireVaultActivationLock(resolved.runtime);
	try {
	const previousDaemon = inspectVaultRuntime(resolved.runtime);
	if (previousDaemon.status === 'running') await assertVaultRuntimeHealthy(resolved.runtime);
	const previousRuntime = inspectCodexRuntime(DATA_DIR);
	const captureBefore = safeReadJSON(join(resolved.runtime.data_dir, 'capture-state.json'));
	const claudeWasActive = captureEnabledForHost(captureBefore, 'claude-code');
	const codexActive = codexProductConnectedForWorkspace(captureBefore, resolved.binding);
	const previousClaudeMcp = claudeWasActive ? checkClaudeProductMCP() : undefined;
	const previousClaudeRuntime = previousClaudeMcp?.ok
		? inspectCodexRuntimeAt(previousClaudeMcp.runtime_path)
		: { ok: false, detail: previousClaudeMcp?.detail ?? 'Claude Code MCP is unavailable' };
	if (claudeWasActive && !previousClaudeRuntime.ok) {
		throw new Error('cannot upgrade Claude Code: active registration has no restorable Pulse runtime');
	}
	const snapshots = snapshotLocalFiles(activationFilePaths(resolved.binding, {
		includeClaude: true, includeCodex: codexActive,
	}));
	const codexTransaction = codexActive ? snapshotCodexHostActivation() : undefined;
	let installedRuntime;
	let runtimeInstalled = false;
	let codexMutationStarted = false;
	  let managedRuntime;
	  try {
			managedRuntime = await ensureManagedProductRuntime(resolved.runtime, { publishConfig: false });
			if (previousDaemon.status === 'running' &&
				previousDaemon.managed_embedder?.config_digest !== managedRuntime.managed_embedder.config_digest) {
				await stopVaultRuntimeAndWait(resolved.runtime);
			}
			managedRuntime.managed_embedder = activateManagedEmbedderConfig(
				resolved.runtime, managedRuntime.managed_embedder,
			);
			if (codexTransaction) {
				codexMutationStarted = true;
				activateExactCodexProductEdge(managedRuntime.product_edge, codexTransaction);
			}
					installedRuntime = installCodexRuntime(managedRuntime.product_edge.runtime_root, DATA_DIR, {
						keepPrevious: true, signedEdge: managedRuntime.product_edge,
					});
		runtimeInstalled = true;
			if (!installedRuntime.ok) throw new Error(`Claude Code runtime install failed: ${installedRuntime.detail}`);
	    const runtimeStatus = inspectVaultRuntime(resolved.runtime);
	    if (['stopped', 'crashed', 'running'].includes(runtimeStatus.status)) {
	      await startVaultRuntime(resolved.runtime, {
	        daemonPath: managedRuntime.daemon.path,
				managedEmbedder: managedRuntime.managed_embedder,
				host: 'pulse-product', allowRollback: false,
	      });
	    } else {
	      throw new Error(`Pulse bound vault is ${runtimeStatus.status}`);
	    }
			await assertVaultRuntimeHealthy(resolved.runtime);
				writeProductDaemonActivation(managedRuntime, installedRuntime);
		  installClaudeCode(installedRuntime.path, { requireExternal: true });
		  installClaudeCodeHooks(installedRuntime.path, installedRuntime.digest);
    writeCaptureStateFiles({
      globalDataDir: DATA_DIR, binding: resolved.binding, host: 'claude-code', enabled: true,
      reason: 'claude_code_product_connected',
    });
		const defaults = defaultBindingPaths();
		writeProductLocators({
			codexHome: codexHomePath(), binding: resolved.binding, dataDir: DATA_DIR,
			registryPath: process.env.PULSE_BINDING_REGISTRY_PATH ?? defaults.registryPath,
			publicKeyPath: process.env.PULSE_BINDING_PUBLIC_KEY_PATH ?? defaults.publicKeyPath,
			anchorPath: process.env.PULSE_BINDING_ANCHOR_PATH ?? defaults.anchorPath,
			trustMode: process.env.PULSE_TRUST_MODE === 'test' ? 'test' : 'production',
		});
    finalizeCodexRuntimeInstall(DATA_DIR);
		commitPersonalRuntimeRelease(managedRuntime.verified_release, { dataDir: DATA_DIR });
  } catch (error) {
		const failures = [];
		if (runtimeInstalled) {
			try {
				const rollback = rollbackCodexRuntimeInstall(DATA_DIR);
				if (!rollback.ok) failures.push(new Error(`runtime rollback failed: ${rollback.detail}`));
			} catch (failure) { failures.push(failure); }
		}
		try {
			if (claudeWasActive && previousClaudeRuntime.ok) {
				installClaudeCode(previousClaudeRuntime.path, { requireExternal: true });
				} else if (!claudeWasActive) {
					removeClaudeCodeExternalRegistration();
			}
		} catch (failure) { failures.push(failure); }
		try { await stopUpgradedVaultBeforeFileRestore(resolved.runtime, previousDaemon); } catch (failure) { failures.push(failure); }
		if (codexMutationStarted) failures.push(...rollbackCodexHostActivation(codexTransaction));
		try { restoreLocalFiles(snapshots); } catch (failure) { failures.push(failure); }
		try { await restoreVaultAfterFailedConnect(resolved.runtime, previousDaemon); } catch (failure) { failures.push(failure); }
		if (failures.length > 0) {
			throw new Error(`Claude Code connect failed (${error.message}); rollback failed: ${failures.map((failure) => failure.message).join('; ')}`);
		}
    throw error;
  }
	if (codexTransaction) discardPluginTreeSnapshot(codexTransaction.pluginTree);
	if (process.env.PULSE_TRUST_MODE === 'test') {
		console.log('[pulse] SYNTHETIC TEST AUTHORITY is active; this connection is not production-trusted.');
	}
	const liveStatus = await boundPulseRequest(resolved, '/memory/status', { method: 'GET', timeoutMs: 1500 });
  await showPulseConnectAnimation();
	const harnessSummary = codexActive
		? 'one bound vault, two connected harnesses'
		: 'one bound vault, Claude Code connected';
	const memorySummary = codexActive
		? 'Automatic memory uses the same Memory Tray, receipt ledger, and continuity pack in Claude Code and Codex.'
		: 'Automatic memory uses the bound Memory Tray, receipt ledger, and continuity pack. Codex will join this same vault when connected.';
	console.log(`
[pulse] pulse  .  :  o  ♥  o  :  .
[pulse] ${harnessSummary}

[pulse] Pulse is breathing locally.
Pulse wave:
  .  :  o  ♥  o  :  .
──────────────────────────────────

Claude Code:             connected to ${resolved.runtime.kind}
MCP:                     pinned local runtime
Hooks:                   ${CLAUDE_HOOK_EVENTS.length} native lifecycle events
Binding:                 ${resolved.binding.binding_id}
Store:                   ${resolved.runtime.store_id}
backend LLM off
raw transcript capture off
Storage:                 local SQLite
Retrieval:               ${liveStatus.full_retrieval === true ? `full via ${liveStatus.embedder}` : 'fallback only; full retrieval is not enabled'}

${memorySummary}
Every candidate is visible before commit; dangerous content is rejected before SQLite.

Memory Tray:
  available in the bound local Viewer; no authenticated URL or IPC secret is printed

Import old chats later:
  pulse migrate start --open
`);
	if (remoteControl) {
		printRemoteControlNextSteps();
	}
	} finally {
		await releaseVaultActivation();
	}
}

function demoMemoryCapsule() {
  return {
    schema: 'pulse.memory_capsule.v1',
    source: {
      host: 'pulse-cli',
      conversation_scope: 'current_turn',
      timestamp: new Date().toISOString(),
    },
    items: [{
      kind: 'decision',
      redacted_summary: FIRST_PROOF_MEMORY,
      confidence: 1.0,
      evidence_hint: 'user_selected',
      privacy_tier: 'private',
      retention: 'project',
      tags: ['pulse_demo', 'first_proof', 'claude_code'],
    }],
    raw_input_included: false,
  };
}

function printDemoRitual({ daemonMissing = false } = {}) {
  console.log(`
[pulse] Pulse demo

Stop re-explaining your project to Claude Code.
Pulse keeps the thread.

This is a local-first developer preview:
- backend LLM off by default
- raw transcript capture off
- structured host-extracted memory
- viewer before import

${daemonMissing ? 'Pulse daemon is not reachable yet.\n' : ''}Without Pulse:
  A fresh Claude Code session would not know the Atlas decision.

First proof:
1. Run:
   pulse demo
2. Ask Claude Code:
   "${FIRST_PROOF_REMEMBER_PROMPT}"
3. Open a fresh Claude Code session and ask:
   "${FIRST_PROOF_RECALL_PROMPT}"
4. Inspect what Claude will see next:
   pulse viewer

Control:
  pulse wipe --confirm "wipe pulse memory"
  pulse disconnect claude-code

Start with one memory first. Old chats can wait.
`);
}

// --- Stateful preview demo -------------------------------------------------
//
// The demo Pulse actually has to prove: same query, different user state →
// different retrieved episodes, with visible reasons; old anchors beating
// recent noise; and the continuity pack the next agent receives. It runs on
// an ISOLATED demo instance (own port, own data dir, simulated corpus) so it
// never touches the user's real store and wipes clean in one command.

const DEMO_BASE_URL = process.env.PULSE_DEMO_BASE_URL || 'http://127.0.0.1:18790';
const DEMO_DATA_DIR = join(DATA_DIR, 'preview-demo');
const DEMO_SECRET_PATH = join(DEMO_DATA_DIR, 'secret.key');
const DEMO_PID_FILE = join(DEMO_DATA_DIR, 'pulse-demo-daemon.pid');

function demoCorpus() {
  const corpusPath = join(dirname(CLI_PATH), 'demo-corpus.json');
  return JSON.parse(readFileSync(corpusPath, 'utf8'));
}

function demoSecret() {
  mkdirSync(DEMO_DATA_DIR, { recursive: true, mode: 0o700 });
  if (!existsSync(DEMO_SECRET_PATH)) {
    writeFileSync(DEMO_SECRET_PATH, randomBytes(32).toString('hex'), { mode: 0o600 });
  }
  return readFileSync(DEMO_SECRET_PATH, 'utf8').trim();
}

async function demoFetch(path, { body, method, timeoutMs = 15000 } = {}) {
  const response = await fetch(`${DEMO_BASE_URL.replace(/\/$/, '')}${path}`, {
    method: method ?? (body === undefined ? 'GET' : 'POST'),
    headers: {
      'Content-Type': 'application/json',
      'X-Pulse-Key': demoSecret(),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(timeoutMs),
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`demo daemon HTTP ${response.status} on ${path}: ${text.slice(0, 400)}`);
  }
  if (response.status === 204) {
    return { ok: true };
  }
  return response.json();
}

async function startDemoDaemon() {
  const daemonBin = join(DATA_DIR, 'bin', 'pulse-preview-daemon');
  if (!existsSync(daemonBin)) {
    throw new Error(
      'Pulse Local Preview daemon is not built yet. Run `pulse init claude-code --yes` first.',
    );
  }
  demoSecret();
  const logDir = join(DEMO_DATA_DIR, 'logs');
  mkdirSync(logDir, { recursive: true, mode: 0o700 });
  const logFd = openSync(join(logDir, 'pulse-demo-daemon.log'), 'a');
  const addr = DEMO_BASE_URL.replace(/^https?:\/\//, '');
  const child = spawn(daemonBin, ['-addr', addr, '-data-dir', DEMO_DATA_DIR], {
    detached: true,
    stdio: ['ignore', logFd, logFd],
    env: { ...process.env, ANTHROPIC_API_KEY: '', PULSE_MODE: 'local-auto' },
  });
  child.unref();
  writeFileSync(DEMO_PID_FILE, String(child.pid), { mode: 0o600 });
  for (let attempt = 0; attempt < 80; attempt += 1) {
    try {
      return await demoFetch('/memory/status', { timeoutMs: 600 });
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 150));
    }
  }
  throw new Error(`demo daemon did not become ready at ${DEMO_BASE_URL}`);
}

function stopDemoDaemon() {
  if (!existsSync(DEMO_PID_FILE)) {
    return false;
  }
  const pid = Number.parseInt(readFileSync(DEMO_PID_FILE, 'utf8').trim(), 10);
  try {
    if (Number.isFinite(pid) && pid > 1) {
      process.kill(pid, 'SIGTERM');
    }
  } catch {
    // already gone
  }
  unlinkSync(DEMO_PID_FILE);
  return true;
}

function demoCleanup() {
  const stopped = stopDemoDaemon();
  rmSync(DEMO_DATA_DIR, { recursive: true, force: true });
  console.log(`[pulse] Demo ${stopped ? 'daemon stopped and ' : ''}preview corpus removed: ${DEMO_DATA_DIR}`);
}

function formatBreakdown(breakdown) {
  if (!breakdown) {
    return 'base ranking (cosine x recency)';
  }
  const parts = [`cos ${Number(breakdown.cosine ?? 0).toFixed(2)}`, `recency ${Number(breakdown.recency ?? 0).toFixed(2)}`];
  const boosts = [
    ['state', breakdown.state_boost],
    ['anchor', breakdown.anchor_boost],
    ['emotion', breakdown.emotion_boost],
    ['date', breakdown.date_boost],
  ];
  for (const [name, value] of boosts) {
    if (typeof value === 'number' && Math.abs(value - 1) > 0.0005) {
      parts.push(`${name} x${value.toFixed(2)}`);
    }
  }
  return parts.join(' · ');
}


// Micro animation: the pulse line breathes. Same motif as the install
// banner ( .  :  o  \u2665  o  :  . ) \u2014 a heartbeat, not a loading screen.
// Gated by shouldAnimatePulseConnect (TTY, no CI, --no-animate to skip).
async function pulseBreath({ cycles = 2 } = {}) {
  if (!shouldAnimatePulseConnect()) {
    return;
  }
  const red = (t) => `\x1b[31m${t}\x1b[0m`;
  const frames = [
    `[pulse]        .   :   o   \u2661   o   :   .`,
    `[pulse]        .  :  o  ${red('\u2665')}  o  :  .`,
    `[pulse]       .  :  O  ${red('\u2665')}  O  :  .`,
    `[pulse]        .  :  o  ${red('\u2665')}  o  :  .`,
    `[pulse]         . : o ${red('\u2665')} o : .`,
    `[pulse]        .  :  o  ${red('\u2665')}  o  :  .`,
  ];
  process.stdout.write('\x1b[?25l');
  try {
    for (let c = 0; c < cycles; c += 1) {
      for (const frame of frames) {
        process.stdout.write(`\r\x1b[2K${frame}`);
        await sleep(110);
      }
    }
  } finally {
    process.stdout.write('\r\x1b[2K\x1b[?25h');
  }
}

async function runStatefulDemo() {
  const corpus = demoCorpus();
  console.log(`
[pulse] Pulse Local Preview demo
${corpus.label}

One question, three user states. Watch which memory surfaces — and why.
`);

  await pulseBreath();
  console.log('[pulse] Starting isolated demo instance...');
  const status = await startDemoDaemon();
  try {
    if (!status.full_retrieval) {
      console.log(`
Pulse MCP fallback is ready. Full retrieval is NOT enabled, so the stateful
demo cannot run honestly on this machine.

To enable full retrieval, give the engine an embedder:
  - Cohere key in ~/.pulse/cohere-key.txt (external embedding API), or
  - local MLX embeddings (Apple Silicon): set PULSE_LOCAL_EMBED_PYTHON,
    PULSE_LOCAL_EMBED_HELPER, PULSE_LOCAL_EMBED_MODEL.

Run \`pulse doctor\` to see the full picture. No fake demo will be shown.`);
      return;
    }
    console.log(`[pulse] Full retrieval: ON (embedder: ${status.embedder || 'configured'})`);

    const now = Date.now();
    const events = corpus.events.map(({ occurred_days_ago: daysAgo, ...event }) => ({
      ...event,
      occurred_at: new Date(now - daysAgo * 24 * 3600 * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z'),
    }));
    console.log(`[pulse] Seeding ${events.length} simulated memories (anchors, noise, state-typed episodes)...`);
    const seeded = await demoFetch('/graph/delta', {
      body: {
        schema: 'pulse.semantic_delta.v1',
        source: {
          host: 'claude-code',
          conversation_scope: 'project_context',
          timestamp: new Date().toISOString().replace(/\.\d{3}Z$/, 'Z'),
          thread_id: corpus.thread_id,
          project_id: corpus.project_id,
        },
        nodes: corpus.nodes,
        events,
        continuity: corpus.continuity,
        raw_input_included: false,
      },
      timeoutMs: 120000,
    });
    if (seeded.events_indexed !== true) {
      throw new Error('seeded events were not indexed for retrieval — demo cannot proceed honestly');
    }

    const titleById = new Map();
    const daysById = new Map();
    corpus.events.forEach((event, index) => {
      const id = seeded.event_ids?.[index];
      if (id !== undefined) {
        titleById.set(id, event.title);
        daysById.set(id, event.occurred_days_ago);
      }
    });

    console.log(`\nQUERY (same every time): "${corpus.query}"`);
    // Human pacing: give each state block a beat on screen. --fast disables.
    const paceMs = args.includes('--fast') ? 0 : 2200;
    const pace = () => new Promise((resolve) => setTimeout(resolve, paceMs));
    const topSets = [];
    for (const state of corpus.states) {
      await pace();
      const result = await demoFetch('/context/query', {
        body: {
          query: corpus.query,
          mode: 'empathic',
          top_k: 3,
          include_trace: true,
          user_state: state.user_state,
        },
        timeoutMs: 60000,
      });
      const ids = result?.trace?.retrieval?.event_ids ?? [];
      const breakdowns = result?.trace?.retrieval?.score_breakdowns ?? {};
      topSets.push(new Set(ids.slice(0, 3)));
      console.log(`\n— state: ${state.label}`);
      ids.slice(0, 3).forEach((id, rank) => {
        const title = titleById.get(id) ?? `event ${id}`;
        const age = daysById.has(id) ? `${daysById.get(id)}d ago` : '';
        console.log(`  ${rank + 1}. ${title} ${age ? `(${age})` : ''}`);
        console.log(`     why: ${formatBreakdown(breakdowns[String(id)] ?? breakdowns[id])}`);
      });
    }

    const allSame = topSets.every(
      (set) => set.size === topSets[0].size && [...set].every((id) => topSets[0].has(id)),
    );
    console.log(
      allSame
        ? '\n[pulse] NOTE: top results did not differ across states on this run.'
        : '\n[pulse] Same question. Different state. Different memory — with the reason on every line.',
    );

    await pace();
    const resume = await demoFetch('/continuity/resume', {
      body: {
        thread_id: corpus.thread_id,
        project_id: corpus.project_id,
        host: 'claude-code',
        token_budget: 1200,
      },
      timeoutMs: 30000,
    });
    console.log(`\nWHAT THE NEXT AGENT GETS (continuity pack, injected at session start):\n`);
    console.log(String(resume?.resume_markdown ?? '').trim());

    console.log(`
---
${corpus.label}
Inspect the store: ${DEMO_DATA_DIR}
Erase everything:  pulse demo --clean
`);
  } finally {
    stopDemoDaemon();
  }
}

function printRemoteControlNextSteps() {
  console.log(`
[pulse] Claude Code + mobile continuity ready
Remote Control:    claude --remote-control "Pulse Memory"
Claude mobile:     open Claude app -> Code, or scan the QR shown by Claude Code
Local memory:      Pulse MCP + hooks installed
Graph ingestion:   host-extracted via pulse_graph_delta, backend LLM off
Viewer:            pulse viewer
`);
}

function normalizePublicOrigin(value) {
  const raw = value ?? process.env.PULSE_PUBLIC_ORIGIN ?? '';
  if (!raw) {
    throw new Error('pulse connect chatgpt|claude-chat requires --base <https-origin-or-mcp-url> or PULSE_PUBLIC_ORIGIN');
  }
  const url = new URL(raw);
  if (url.protocol !== 'https:') {
    throw new Error('Remote custom connectors require a public HTTPS origin');
  }
  url.search = '';
  url.hash = '';
  url.pathname = url.pathname.replace(/\/mcp\/?$/, '');
  if (url.pathname === '/') {
    url.pathname = '';
  }
  return url.toString().replace(/\/$/, '');
}

const REMOTE_HOSTS = {
  chatgpt: {
    title: 'ChatGPT custom connector handoff',
    ui: [
      '1. Open ChatGPT -> Settings -> Connectors (developer mode).',
      '2. Add a custom connector / MCP server.',
      '3. Paste the Connector URL above.',
      '4. Complete the auth flow ChatGPT shows you.',
    ],
    persist: 'The final install is a persistent ChatGPT account change.',
    settingsURL: 'https://chatgpt.com/',
    defaultThread: 'pulse-live-chatgpt-smoke',
  },
  'claude-chat': {
    title: 'Claude Chat custom connector handoff',
    ui: [
      '1. Open Claude settings -> Connectors.',
      '2. Add a custom connector.',
      '3. Paste the Connector URL above.',
      '4. Complete OAuth.',
    ],
    persist: 'The final install is a persistent Claude account/workspace change.',
    settingsURL: 'https://claude.ai/settings/connectors',
    defaultThread: 'pulse-live-claude-ui-smoke',
  },
};

function connectRemoteHost(hostKey) {
  const host = REMOTE_HOSTS[hostKey];
  const publicOrigin = normalizePublicOrigin(getArg('--base'));
  const connectorURL = `${publicOrigin}/mcp`;
  const threadId = getArg('--thread') ?? host.defaultThread;

  console.log(`
[pulse] ${host.title}
──────────────────────────────────────────

Connector URL:
  ${connectorURL}

Preflight (runs the OAuth dev loop + status + graph delta + resume):
  pulse connect-smoke --base ${publicOrigin} --thread ${threadId} --json

${hostKey === 'chatgpt' ? 'ChatGPT UI:' : 'Claude UI:'}
${host.ui.map((line) => `  ${line}`).join('\n')}

Important:
  ${host.persist}
  Confirm it in the logged-in UI only when you intentionally want Pulse connected.
  This is a developer-preview handoff to your own hosted endpoint — not a
  store/directory listing.

Proof prompt 1:
  Use Pulse to save this decision: the demo ships doctor-gated — no demo
  without full retrieval proven on this machine. Thread id: ${threadId}.

Expected tool:
  pulse_graph_delta

Proof prompt 2, in a fresh chat:
  Use Pulse to resume thread ${threadId}. What did we decide?

Expected tool:
  pulse_resume
`);

  if (args.includes('--open')) {
    spawnSync('open', [host.settingsURL], { stdio: 'ignore' });
  }
}

function runConnectorSmoke(rest) {
  const script = join(CLI_PACKAGE_ROOT, 'scripts', 'connector-smoke.mjs');
  const result = spawnSync(process.execPath, [script, ...rest], { stdio: 'inherit' });
  process.exitCode = result.status ?? 1;
}

function daemon(extraArgs) {
  mkdirSync(DATA_DIR, { recursive: true, mode: 0o700 });
  const goBin = getArg('--go-bin');
  const bin = goBin ?? process.env.PULSE_GO_BIN;
  if (!bin) {
    throw new Error('pulse daemon requires --go-bin <path> or PULSE_GO_BIN. The npm "pulse" bin is the CLI, not the Go server.');
  }
  const serverArgs = ['-data-dir', DATA_DIR, '-addr', new URL(DEFAULT_BASE_URL).host, ...extraArgs.filter((arg, i) => arg !== '--go-bin' && extraArgs[i - 1] !== '--go-bin')];
  const child = spawn(bin, serverArgs, { stdio: 'inherit' });
  child.on('error', (err) => {
    console.error(`[pulse] failed to start Go server ${bin}: ${err.message}`);
    process.exit(1);
  });
  child.on('exit', (code, signal) => {
    if (signal) {
      process.kill(process.pid, signal);
      return;
    }
    process.exit(code ?? 0);
  });
}

function ensureGitignoreEntry(entry) {
  const path = resolve(process.cwd(), '.gitignore');
  const current = existsSync(path) ? readFileSync(path, 'utf8') : '';
  const lines = current.split(/\r?\n/).map((line) => line.trim());
  if (lines.includes(entry)) {
    return;
  }
  appendFileSync(path, `${current.endsWith('\n') || current === '' ? '' : '\n'}${entry}\n`, {
    mode: 0o600,
  });
}

function localThreadContext() {
  const host = process.env.PULSE_HOST ?? 'claude-code';
  const projectId = process.env.PULSE_PROJECT_ID ?? slug(basename(process.cwd()));
  const threadId = process.env.PULSE_THREAD_ID ?? projectId;
  const sessionId =
    process.env.PULSE_SESSION_ID ??
    process.env.CLAUDE_SESSION_ID ??
    process.env.CODEX_SESSION_ID ??
    `${host}:${threadId}:local`;
  return { host, projectId, threadId, sessionId };
}

function slug(value) {
  const out = String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._:-]+/g, '-')
    .replace(/^[.:\-_]+|[.:\-_]+$/g, '')
    .slice(0, 96);
  return out || 'default';
}

async function readStdin() {
  if (process.stdin.isTTY) {
    return '';
  }
  let data = '';
  for await (const chunk of process.stdin) {
    data += chunk;
  }
  return data;
}

function parseHookPayload(raw) {
  raw = String(raw ?? '').trim();
  if (!raw) {
    return {};
  }
  try {
    return JSON.parse(raw);
  } catch {
    return { text: raw };
  }
}

function safeText(value, max = 260) {
  if (value === undefined || value === null) {
    return '';
  }
  let text = typeof value === 'string' ? value : JSON.stringify(value);
  text = text.replace(/\s+/g, ' ').trim();
  if (!text) {
    return '';
  }
  const lower = text.toLowerCase();
  for (const marker of [
    '/users/',
    'file://',
    'token=',
    'api_key',
    'apikey',
    'password',
    'secret',
    'private_key',
    'begin private key',
    'sk-',
    'akia',
    'xoxb-',
    'ghp_',
  ]) {
    if (lower.includes(marker)) {
      return '';
    }
  }
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

function firstSafeText(payload, keys) {
  for (const key of keys) {
    const value = payload?.[key];
    const text = safeText(value);
    if (text) {
      return text;
    }
  }
  return '';
}

function safeStringArray(value) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((item) => safeText(item, 360)).filter(Boolean).slice(0, 20);
}

function assertSafeZipListing(zipPath) {
  const listed = spawnSync('unzip', ['-Z1', zipPath], {
    encoding: 'utf8',
    maxBuffer: 5 * 1024 * 1024,
  });
  if (listed.status !== 0) {
    throw new Error(`could not inspect zip archive: ${listed.stderr || listed.stdout}`);
  }
  const files = listed.stdout.split(/\r?\n/).filter(Boolean);
  if (files.length === 0) {
    throw new Error('zip archive is empty');
  }
  for (const file of files) {
    if (
      file.startsWith('/') ||
      file.startsWith('\\') ||
      file.includes('..') ||
      /^[A-Za-z]:[\\/]/.test(file)
    ) {
      throw new Error(`zip archive contains unsafe path: ${file}`);
    }
  }
}

function unpackZipArchive(zipPath) {
  if (!existsSync(zipPath)) {
    throw new Error(`archive path does not exist: ${zipPath}`);
  }
  if (spawnSync('unzip', ['-v'], { stdio: 'ignore' }).status !== 0) {
    throw new Error('zip preview requires the system "unzip" command');
  }
  assertSafeZipListing(zipPath);
  const outDir = mkdtempSync(join(tmpdir(), 'pulse-migrate-unzip.'));
  const unzipped = spawnSync('unzip', ['-qq', zipPath, '-d', outDir], {
    encoding: 'utf8',
    maxBuffer: 5 * 1024 * 1024,
  });
  if (unzipped.status !== 0) {
    throw new Error(`could not unpack zip archive: ${unzipped.stderr || unzipped.stdout}`);
  }
  return outDir;
}

function migrationSizeLimitLabel() {
  const mb = MAX_MIGRATION_FILE_BYTES / (1024 * 1024);
  return Number.isInteger(mb) ? `${mb}MB` : `${MAX_MIGRATION_FILE_BYTES} bytes`;
}

function shouldSkipMigrationEntry(path, entryName = basename(path)) {
  const name = entryName.toLowerCase();
  if (name === 'node_modules' || name === '__macosx' || name === '.git' || name === '.obsidian') {
    return true;
  }
  if (name === 'attachments' || name === 'reports' || name === 'compiled_dialogs') {
    return true;
  }
  if (name === '.ds_store') {
    return true;
  }
  return path
    .toLowerCase()
    .split(/[\\/]+/)
    .some((part) => part === 'attachments' || part === 'reports' || part === 'compiled_dialogs');
}

function isMigrationSourceFile(path) {
  const lowerPath = path.toLowerCase();
  return lowerPath.endsWith('.json') || lowerPath.endsWith('.jsonl') || lowerPath.endsWith('.md');
}

function discoverMigrationFiles(target) {
  let root = resolve(target);
  let archiveWasUnpacked = false;
  if (!existsSync(root)) {
    throw new Error(`archive path does not exist: ${root}`);
  }
  if (statSync(root).isFile() && root.toLowerCase().endsWith('.zip')) {
    root = unpackZipArchive(root);
    archiveWasUnpacked = true;
  }
  const files = [];
  const skipped = [];

  function visit(path, depth) {
    if (files.length >= MAX_MIGRATION_FILES) {
      return;
    }
    const info = statSync(path);
    if (info.isDirectory()) {
      if (shouldSkipMigrationEntry(path)) {
        return;
      }
      if (depth > 8) {
        return;
      }
      for (const entry of readdirSync(path, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
        if (shouldSkipMigrationEntry(join(path, entry.name), entry.name)) {
          continue;
        }
        visit(join(path, entry.name), depth + 1);
      }
      return;
    }
    if (!info.isFile() || shouldSkipMigrationEntry(path) || !isMigrationSourceFile(path)) {
      return;
    }
    if (info.size > MAX_MIGRATION_FILE_BYTES) {
      skipped.push({ file: basename(path), reason: `larger than ${migrationSizeLimitLabel()} preview limit` });
      return;
    }
    files.push(path);
  }

  visit(root, 0);
  if (files.length === 0) {
    throw new Error(`no JSON/JSONL/Markdown export files found at archive path: ${root}`);
  }
  return { root, files, skipped, archiveWasUnpacked };
}

function conversationRoots(value) {
  if (Array.isArray(value)) {
    return value;
  }
  for (const key of ['conversations', 'chats', 'items']) {
    if (Array.isArray(value?.[key])) {
      return value[key];
    }
  }
  return [value];
}

function contentText(content) {
  if (typeof content === 'string') {
    return content;
  }
  if (Array.isArray(content?.parts)) {
    return content.parts.map(contentText).filter(Boolean).join(' ');
  }
  if (Array.isArray(content)) {
    return content.map(contentText).filter(Boolean).join(' ');
  }
  for (const key of ['text', 'value', 'content']) {
    if (typeof content?.[key] === 'string') {
      return content[key];
    }
  }
  return '';
}

function messageText(message) {
  if (!message || typeof message !== 'object') {
    return typeof message === 'string' ? message : '';
  }
  for (const key of ['text', 'content', 'message', 'prompt', 'response']) {
    const text = contentText(message[key]);
    if (text) {
      return text;
    }
  }
  return '';
}

function extractChatGPTMessages(record) {
  const mapping = record?.mapping;
  if (!mapping || typeof mapping !== 'object') {
    return [];
  }
  return Object.values(mapping)
    .map((node) => messageText(node?.message))
    .filter(Boolean);
}

function extractLinearMessages(record) {
  for (const key of ['chat_messages', 'messages', 'turns']) {
    if (Array.isArray(record?.[key])) {
      return record[key].map(messageText).filter(Boolean);
    }
  }
  return [];
}

function extractMigrationConversations(parsed, file) {
  const out = [];
  for (const record of conversationRoots(parsed)) {
    if (!record || typeof record !== 'object') {
      continue;
    }
    const chatgpt = extractChatGPTMessages(record);
    if (chatgpt.length > 0) {
      out.push({
        source: 'chatgpt',
        title: humanThreadTitle(record.title ?? record.name ?? basename(file, '.json'), 'chatgpt'),
        messages: chatgpt,
      });
      continue;
    }
    const linear = extractLinearMessages(record);
    if (linear.length > 0) {
      out.push({
        source: record.chat_messages ? 'claude' : 'unknown',
        title: humanThreadTitle(record.name ?? record.title ?? basename(file, '.json'), record.chat_messages ? 'claude' : 'unknown'),
        messages: linear,
      });
    }
  }
  return out;
}

function markdownFrontmatterValue(markdown, key) {
  const text = String(markdown ?? '');
  const frontmatter = text.match(/^---\r?\n([\s\S]*?)\r?\n---/);
  if (!frontmatter) {
    return '';
  }
  const escaped = key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = frontmatter[1].match(new RegExp(`^${escaped}:\\s*(.+)$`, 'im'));
  return safeText((match?.[1] ?? '').replace(/^["']|["']$/g, ''), 120);
}

function inferMarkdownConversationSource(file, root, markdown) {
  const provider = markdownFrontmatterValue(markdown, 'provider').toLowerCase();
  if (provider === 'chatgpt' || provider === 'claude') {
    return provider;
  }
  const relative = relativeEvidenceRef(file, root).toLowerCase();
  if (/(^|\/)chatgpt(\/|$)/.test(relative)) {
    return 'chatgpt';
  }
  if (/(^|\/)claude(\/|$)/.test(relative)) {
    return 'claude';
  }
  return 'unknown';
}

function markdownConversationTitle(markdown, file, source) {
  const text = String(markdown ?? '');
  const explicitTitle = text.match(/^#\s+Title:\s*(.+)$/im)?.[1]
    ?? text.match(/^#\s+(.+)$/m)?.[1]
    ?? '';
  return humanThreadTitle(explicitTitle || basename(file, '.md'), source);
}

function isNexusMarkdownConversation(markdown) {
  const text = String(markdown ?? '');
  return (
    /nexus:\s*nexus-ai-chat-importer/i.test(text) ||
    /^\s*>\s*\[!nexus_(?:user|agent|assistant|system)\]/im.test(text)
  );
}

function extractNexusMarkdownMessages(markdown) {
  const messages = [];
  let current = [];
  const flush = () => {
    const text = current.join('\n').replace(/\n{3,}/g, '\n\n').trim();
    if (text) {
      messages.push(text);
    }
    current = [];
  };
  for (const line of String(markdown ?? '').split(/\r?\n/)) {
    if (/^\s*>\s*\[!nexus_(?:user|agent|assistant|system)\]/i.test(line)) {
      flush();
      continue;
    }
    if (current.length === 0 && !/^\s*>/.test(line)) {
      continue;
    }
    if (/^\s*>/.test(line)) {
      const text = line.replace(/^\s*>\s?/, '').trimEnd();
      if (!text.trim()) {
        continue;
      }
      if (/^\*\*(?:User|Human|Assistant|Claude|ChatGPT|System)\*\*\s*(?:[-:]\s*)?(?:\d{4}-\d{2}-\d{2}|\d{1,2}:\d{2}|$)/i.test(text)) {
        continue;
      }
      current.push(text);
      continue;
    }
    flush();
  }
  flush();
  return messages;
}

function extractMarkdownConversations(file, root) {
  const markdown = readFileSync(file, 'utf8');
  if (!isNexusMarkdownConversation(markdown)) {
    return [];
  }
  const messages = extractNexusMarkdownMessages(markdown).filter(Boolean);
  if (messages.length === 0) {
    return [];
  }
  const source = inferMarkdownConversationSource(file, root, markdown);
  return [{
    source,
    title: markdownConversationTitle(markdown, file, source),
    messages,
  }];
}

function jsonlSource(record) {
  if (record?.payload && (record.type === 'response_item' || record.type === 'session_meta')) {
    return 'codex';
  }
  if (record?.sessionId && Object.hasOwn(record, 'content')) {
    return 'claude-code';
  }
  return 'unknown';
}

function hostDisplayName(source) {
  if (source === 'claude-code') return 'Claude Code';
  if (source === 'chatgpt') return 'ChatGPT';
  if (source === 'claude') return 'Claude';
  if (source === 'codex') return 'Codex';
  return 'Pulse';
}

function isTechnicalRef(value) {
  const text = String(value ?? '').trim();
  return (
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(text) ||
    /^rollout-\d{4}-\d{2}-\d{2}t\d{2}-\d{2}-\d{2}-[0-9a-f-]{20,}$/i.test(text) ||
    /^session-[0-9a-f-]{12,}$/i.test(text) ||
    /^agent-[0-9a-f]{8,}$/i.test(text) ||
    /^source:memory\//i.test(text) ||
    /^people\/person-[0-9a-f]+\.md$/i.test(text)
  );
}

function humanThreadTitle(title, source) {
  const text = safeText(title, 120);
  if (!text || isTechnicalRef(text) || text === 'session') {
    return `${hostDisplayName(source)} session`;
  }
  if (/^rollout-/i.test(text)) {
    return `${hostDisplayName(source)} session`;
  }
  return text;
}

function isSpecificHumanThreadTitle(title, source) {
  const text = safeText(title, 120);
  return Boolean(text && text !== `${hostDisplayName(source)} session` && !isTechnicalRef(text));
}

function extractJSONLConversations(file) {
  const messages = [];
  const sourceCounts = { codex: 0, 'claude-code': 0, unknown: 0 };
  const lines = readFileSync(file, 'utf8').split(/\r?\n/);
  for (const line of lines) {
    if (!line.trim()) {
      continue;
    }
    let record;
    try {
      record = JSON.parse(line);
    } catch {
      continue;
    }
    const source = jsonlSource(record);
    sourceCounts[source] = (sourceCounts[source] ?? 0) + 1;
    const text =
      messageText(record.payload) ||
      messageText(record.message) ||
      messageText(record);
    if (text) {
      messages.push(text);
    }
  }
  if (messages.length === 0) {
    return [];
  }
  const source = dominantSource(sourceCounts);
  return [{
    source,
    title: humanThreadTitle(basename(file, '.jsonl'), source),
    messages,
  }];
}

function dominantSource(sourceCounts) {
  const active = Object.entries(sourceCounts).filter(([, count]) => count > 0);
  if (active.length === 0) {
    return 'unknown';
  }
  const known = active.filter(([source]) => source !== 'unknown');
  if (known.length === 1) {
    return known[0][0];
  }
  if (known.length > 1) {
    return 'mixed';
  }
  if (active.length === 1) {
    return active[0][0];
  }
  return 'mixed';
}

function collectPersonCandidates(text) {
  const safe = safeText(text, 800);
  if (!safe) {
    return [];
  }
  const blocked = new Set([
    'Accents', 'Action', 'Actionable', 'Actions', 'Active', 'Added', 'After', 'Agent', 'All', 'Always', 'Anthropic', 'Any', 'Applications', 'Approvals', 'Architecture', 'Archive', 'Apr', 'Asia', 'Assistant', 'Async', 'Atlas', 'Auto', 'Avoid',
    'Bangkok', 'Bash', 'Benchmark', 'Bench', 'Bitwarden', 'Both', 'Browser', 'Build', 'Built', 'Bundle',
    'Background', 'Before',
    'Caches', 'Chat', 'ChatGPT', 'Check', 'Click', 'Claude', 'Cloudflare', 'Code', 'Codex', 'Collaboration', 'Command', 'Commit', 'Companion', 'Complete', 'Confirmed', 'Content', 'Context', 'Conversation', 'Correction', 'Create', 'Created',
    'Choose', 'Continuity', 'Conversations', 'Consulting', 'Current', 'Data', 'Decision', 'Default', 'Demo', 'Download',
    'Emo', 'Error', 'Even', 'Evidence', 'Export', 'Extraction',
    'Exit', 'Explore', 'Failed', 'File', 'Files', 'Filesystem', 'Final', 'First', 'Fix', 'Full', 'Further',
    'Garden', 'Gateway', 'Google', 'Graph',
    'Connector', 'Cron',
    'Group', 'Guidance', 'Harness', 'Has', 'Hearth', 'Heart', 'Heavy', 'Hello', 'Hermes', 'History', 'However',
    'Import', 'Insight', 'Insights', 'Instead', 'Invalid',
    'June',
    'Keep', 'Known', 'Let', 'Library', 'Live',
    'Make', 'Making', 'Map', 'Mark', 'Marked', 'May', 'Mem', 'Memory', 'Mode', 'Module', 'Move',
    'Network', 'New', 'Newsreader', 'Not', 'Now',
    'Obsidian', 'Only', 'Open', 'OpenAI', 'Opus', 'Output',
    'Pages', 'Pass', 'People', 'Personal', 'Phase', 'Plan', 'Planning', 'Port', 'Preview', 'Primary', 'Privacy', 'Pro', 'Progression', 'Project', 'Prompt', 'Pulse', 'Python',
    'Process', 'Questions',
    'Read', 'Real', 'Record', 'Remember', 'Request', 'Review', 'Run', 'Running', 'Russian',
    'Script', 'Section', 'Server', 'Small', 'Sonnet', 'Source', 'State', 'Step',
    'Task', 'Telegram', 'Telethon', 'Terminal', 'Test', 'Tests', 'The', 'This', 'Thread', 'Threads', 'Three', 'Ticker', 'Timestamps', 'Tool', 'Total', 'True', 'Tweaks', 'Two',
    'Updated', 'Usage', 'Use', 'User', 'Users',
    'Verification', 'Verified', 'Verifier', 'Verify', 'Viewer',
    'Waiting', 'Worker',
    'Work', 'Write',
    'You', 'Your',
    'Акт', 'Аудитория', 'Автор', 'Без', 'Все', 'Вместо', 'Вообще', 'Вот', 'Всё', 'Готово', 'Два', 'Движение', 'Для', 'Его', 'Если', 'Ещё', 'Жду', 'Запускаю', 'Или', 'Как', 'Карта', 'Когда', 'Курс', 'Лекция', 'Медленно', 'Мне', 'Мои', 'Назови', 'Напиши', 'Обращ', 'Один', 'Она', 'Пауза', 'Плюс', 'После', 'Пока', 'Потом', 'Потому', 'Приоритет', 'Проверю', 'Привет', 'Просто', 'Пхукет', 'Сейчас', 'Сквозная', 'Сначала', 'Тайланд', 'Теперь', 'Тематика', 'Тело', 'Тихо', 'Только', 'Три', 'Цель', 'Через', 'Что', 'Чтобы', 'Это',
  ]);
  const out = [];
  const regex = /(?:^|[^\p{L}])(\p{Lu}[\p{Ll}\p{Mn}'-]{2,})(?=$|[^\p{L}])/gu;
  for (const match of safe.matchAll(regex)) {
    const candidate = normalizePersonCandidate(match[1]);
    if (
      !blocked.has(candidate)
      && !/[a-z]+-[a-z]+/i.test(candidate)
      && !candidate.includes("'")
      && !out.includes(candidate)
    ) {
      out.push(candidate);
    }
  }
  return out;
}

function normalizePersonCandidate(candidate) {
  const text = safeText(candidate, 80);
  const aliases = new Map([
    ['Эли', 'Элли'],
    ['Никите', 'Никита'],
    ['Никиты', 'Никита'],
    ['Никиту', 'Никита'],
    ['Никитой', 'Никита'],
    ['Сони', 'Соня'],
    ['Соню', 'Соня'],
    ['Соней', 'Соня'],
    ['Сонин', 'Соня'],
  ]);
  return aliases.get(text) ?? text;
}

function collectEmotionCandidates(text) {
  const safe = safeText(text, 800).toLowerCase();
  if (!safe) {
    return [];
  }
  const markers = [
    ['relief', ['relief', 'облегч']],
    ['hurt', ['hurt', 'pain', 'боль', 'задел']],
    ['anxiety', ['anxiety', 'anxious', 'тревог', 'страх']],
    ['joy', ['joy', 'happy', 'радост', 'счаст']],
    ['anger', ['anger', 'angry', 'злост', 'злюсь']],
    ['shame', ['shame', 'стыд']],
    ['excitement', ['excited', 'exciting', 'вдохнов', 'азарт']],
  ];
  const out = [];
  for (const [label, terms] of markers) {
    if (terms.some((term) => safe.includes(term))) {
      out.push(label);
    }
  }
  return out;
}

function addMigrationSignals(previewState, conversation) {
  const title = conversation.title || conversation.source || 'Untitled thread';
  previewState.memoryCounts.set(
    title,
    (previewState.memoryCounts.get(title) ?? 0) + conversation.messages.length,
  );
  if (!previewState.threadPeople.has(title)) {
    previewState.threadPeople.set(title, new Set());
  }
  if (!previewState.threadEmotions.has(title)) {
    previewState.threadEmotions.set(title, new Set());
  }
  for (const message of conversation.messages) {
    previewState.messages += 1;
    const safe = safeText(message, 800);
    if (!safe && String(message ?? '').trim()) {
      previewState.redactedFragments += 1;
      continue;
    }
    const peopleInMessage = collectPersonCandidates(safe);
    for (const candidate of peopleInMessage) {
      previewState.people.add(candidate);
      previewState.threadPeople.get(title).add(candidate);
      previewState.personCounts.set(candidate, (previewState.personCounts.get(candidate) ?? 0) + 1);
      if (isSpecificHumanThreadTitle(title, conversation.source)) {
        previewState.relationships.add(formatMentionedRelationship(candidate, title));
      }
    }
    for (let i = 0; i < peopleInMessage.length; i += 1) {
      for (let j = i + 1; j < peopleInMessage.length; j += 1) {
        previewState.relationships.add(formatRelatedRelationship(peopleInMessage[i], peopleInMessage[j]));
      }
    }
    for (const emotion of collectEmotionCandidates(safe)) {
      previewState.emotions.add(emotion);
      previewState.threadEmotions.get(title).add(emotion);
    }
  }
}

function registerMigrationConversation(state, conversation, file, root) {
  const title = conversation.title || conversation.source || 'Untitled thread';
  state.conversations += 1;
  state.sourceCounts[conversation.source] = (state.sourceCounts[conversation.source] ?? 0) + 1;
  if (title) {
    state.threads.add(title);
  }
  state.scannedSessions.push({
    id: `${conversation.source || 'unknown'}:${slug(title)}:${state.scannedSessions.length + 1}`,
    source: conversation.source || 'unknown',
    title,
    message_count: Array.isArray(conversation.messages) ? conversation.messages.length : 0,
    evidence_ref: relativeEvidenceRef(file, root),
  });
  addMigrationSignals(state, conversation);
}

function migrationPreview(target) {
  const discovered = discoverMigrationFiles(target);
  const state = {
    people: new Set(),
    personCounts: new Map(),
    threads: new Set(),
    threadPeople: new Map(),
    threadEmotions: new Map(),
    memoryCounts: new Map(),
    emotions: new Set(),
    relationships: new Set(),
    scannedSessions: [],
    sourceCounts: { chatgpt: 0, claude: 0, codex: 0, 'claude-code': 0, unknown: 0 },
    conversations: 0,
    messages: 0,
    redactedFragments: 0,
  };

  for (const file of discovered.files) {
    if (file.toLowerCase().endsWith('.md')) {
      for (const conversation of extractMarkdownConversations(file, discovered.root)) {
        registerMigrationConversation(state, conversation, file, discovered.root);
      }
      continue;
    }
    if (file.toLowerCase().endsWith('.jsonl')) {
      for (const conversation of extractJSONLConversations(file)) {
        registerMigrationConversation(state, conversation, file, discovered.root);
      }
      continue;
    }
    let parsed;
    try {
      parsed = JSON.parse(readFileSync(file, 'utf8'));
    } catch {
      discovered.skipped.push({ file: basename(file), reason: 'invalid JSON' });
      continue;
    }
    for (const conversation of extractMigrationConversations(parsed, file)) {
      registerMigrationConversation(state, conversation, file, discovered.root);
    }
  }

  const promotedPeople = [...state.personCounts.entries()]
    .filter(([name, count]) => isLikelyPreviewPersonCandidate(name, count, state.messages))
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  const reviewCandidates = [...state.personCounts.entries()]
    .filter(([name, count]) => !isLikelyPreviewPersonCandidate(name, count, state.messages) && shouldShowReviewCandidate(name, count, state.messages))
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, 24)
    .map(([name]) => name);
  const peopleCandidates = promotedPeople.slice(0, 24).map(([name]) => name);
  const peopleSet = new Set(peopleCandidates);
  const funFacts = promotedPeople
    .slice(0, 12)
    .map(([name, count]) => `${name} appeared in ${count} bounded source snippet${count === 1 ? '' : 's'}`);
  const previewPeopleGroups = groupImportPeople(peopleCandidates);
  const relationships = uniqueLimited([...state.relationships]
    .map((candidate) => normalizeRelationshipForPreview(candidate, previewPeopleGroups.canonicalByName))
    .filter(Boolean)
    .filter((candidate) => relationshipAllowedForPreview(candidate, peopleSet, state.threads))
    , 24);

  return attachImportPreviewFlow({
    ok: true,
    source: dominantSource(state.sourceCounts),
    path: '<local archive>',
    archive_was_unpacked: discovered.archiveWasUnpacked,
    files_scanned: discovered.files.length,
    files_skipped: discovered.skipped,
    conversations: state.conversations,
    messages: state.messages,
    people_candidates: peopleCandidates,
    review_candidates: reviewCandidates,
    thread_candidates: [...state.threads].slice(0, 24),
    memory_candidates: [...state.memoryCounts.entries()]
      .slice(0, 24)
      .map(([title, count]) => `${title}: ${count} source snippet${count === 1 ? '' : 's'}`),
    emotion_candidates: [...state.emotions].slice(0, 24),
    relationship_candidates: relationships,
    fun_fact_candidates: funFacts,
    redacted_fragments: state.redactedFragments,
    raw_text_written: false,
    next: 'pulse migrate commit <preview.json> --confirm "import pulse graph"',
  }, state);
}

function markdownCell(value) {
  return safeText(String(value ?? '')
    .replace(/\\\|/g, '|')
    .replace(/\*\*/g, '')
    .replace(/\[[^\]]+\]\(([^)]+)\)/g, '$1')
    .trim(), 420);
}

function relativeEvidenceRef(file, rootDir) {
  const absoluteFile = resolve(file);
  const absoluteRoot = resolve(rootDir);
  if (!absoluteFile.startsWith(absoluteRoot)) {
    return basename(absoluteFile);
  }
  return absoluteFile.slice(absoluteRoot.length).replace(/^\/+/, '');
}

function peopleGraphIndexPath(target) {
  const path = resolve(target);
  if (!existsSync(path)) {
    throw new Error(`people graph path does not exist: ${path}`);
  }
  const stat = statSync(path);
  if (stat.isFile()) {
    return path;
  }
  const candidates = [
    join(path, 'people', 'INDEX.md'),
    join(path, 'INDEX.md'),
  ];
  const found = candidates.find((candidate) => existsSync(candidate) && statSync(candidate).isFile());
  if (!found) {
    throw new Error(`people graph index not found under ${path}; expected people/INDEX.md or INDEX.md`);
  }
  return found;
}

function readMarkdownSection(markdown, title) {
  const lines = String(markdown ?? '').split(/\r?\n/);
  const heading = title.toLowerCase();
  const out = [];
  let active = false;
  for (const line of lines) {
    const match = line.match(/^#{2,6}\s+(.+?)\s*$/);
    if (match) {
      if (active) break;
      active = match[1].trim().toLowerCase() === heading;
      continue;
    }
    if (active) {
      out.push(line);
    }
  }
  return safeText(out.join(' ').replace(/\s+/g, ' ').trim(), 900);
}

function markdownMeta(markdown, key) {
  const escaped = key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = String(markdown ?? '').match(new RegExp(`^\\*\\*${escaped}:\\*\\*\\s*(.+)$`, 'im'));
  return safeText(match?.[1] ?? '', 240);
}

function markdownLinks(markdown) {
  const section = readMarkdownSection(markdown, 'Links');
  if (!section) {
    return [];
  }
  return section
    .split(/\s+-\s+/)
    .map((item) => humanReferenceLabel(item.replace(/^-+\s*/, '')))
    .filter(Boolean)
    .slice(0, 6);
}

function humanReferenceLabel(value) {
  const text = safeText(value, 220);
  if (!text) {
    return '';
  }
  if (/^source:memory\/contacts\/personal-contacts\.md$/i.test(text)) {
    return 'Personal contacts';
  }
  if (/^source:memory\/contacts\/freeman-team\.md$/i.test(text)) {
    return 'Freeman team';
  }
  if (/^source:memory\/contacts\/krisp-people-graph\.md$/i.test(text)) {
    return 'Krisp people graph';
  }
  if (/^source:memory\/anchors\/core-priority-entities\.md$/i.test(text)) {
    return 'Core priority entities';
  }
  if (/^source:memory\/graph\.jsonl$/i.test(text)) {
    return 'Legacy memory graph';
  }
  if (/^source:memory\//i.test(text)) {
    return '';
  }
  if (/^people\/.+\.md$/i.test(text)) {
    return 'Curated people profile';
  }
  return text;
}

function parsePeopleGraphRows(indexText) {
  const rows = [];
  for (const line of String(indexText ?? '').split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed.startsWith('|') || /^\|\s*-+\s*\|/.test(trimmed)) {
      continue;
    }
    const cells = trimmed
      .slice(1, trimmed.endsWith('|') ? -1 : undefined)
      .split('|')
      .map(markdownCell);
    if (cells.length < 5 || cells[0].toLowerCase() === 'name') {
      continue;
    }
    const closeness = Number.parseInt(cells[2], 10);
    rows.push({
      name: cells[0],
      role: cells[1],
      closeness: Number.isFinite(closeness) ? closeness : 1,
      relevance: cells[3],
      file: cells[4],
    });
  }
  return rows.filter((row) => row.name);
}

function peopleGraphPreview(target) {
  const indexPath = peopleGraphIndexPath(target);
  const peopleDir = dirname(indexPath);
  const graphRoot = basename(peopleDir) === 'people' ? dirname(peopleDir) : peopleDir;
  const rows = parsePeopleGraphRows(readFileSync(indexPath, 'utf8'));
  const profiles = rows.slice(0, 96).map((row) => {
    const profilePath = row.file ? join(peopleDir, row.file) : '';
    const markdown = profilePath && existsSync(profilePath) && statSync(profilePath).isFile()
      ? readFileSync(profilePath, 'utf8')
      : '';
    const summary = readMarkdownSection(markdown, 'Summary') || row.role;
    return {
      name: row.name,
      role: row.role,
      closeness: row.closeness,
      relevance: row.relevance,
      status: markdownMeta(markdown, 'Status'),
      handle: markdownMeta(markdown, 'Telegram'),
      summary,
      links: markdownLinks(markdown),
      evidence_ref: profilePath ? relativeEvidenceRef(profilePath, graphRoot) : relativeEvidenceRef(indexPath, graphRoot),
    };
  });
  const threadCandidates = uniqueLimited(profiles.map((profile) => profile.relevance).filter((item) => item && item !== 'life'), 24);
  const relationshipCandidates = [];
  for (const profile of profiles) {
    const relationships = readMarkdownSection(
      profile.evidence_ref ? readFileSync(join(graphRoot, profile.evidence_ref), 'utf8') : '',
      'Relationships',
    );
    for (const relationship of relationships.split(/\s+-\s+/).map((item) => safeText(item.replace(/^-+\s*/, ''), 220)).filter(Boolean)) {
      if (!isTechnicalRef(relationship) && !relationship.includes('source:memory/')) {
        relationshipCandidates.push(`${profile.name} <-> ${relationship}`);
      }
    }
  }
  return attachImportPreviewFlow({
    ok: true,
    source: 'people-graph',
    source_kind: 'curated_people_graph',
    source_priority: 100,
    path: '<local people graph>',
    archive_was_unpacked: false,
    files_scanned: 1 + profiles.filter((profile) => profile.evidence_ref !== relativeEvidenceRef(indexPath, graphRoot)).length,
    files_skipped: [],
    conversations: 0,
    messages: profiles.length,
    people_candidates: profiles.map((profile) => profile.name).slice(0, 48),
    person_profiles: profiles,
    review_candidates: [],
    thread_candidates: threadCandidates,
    memory_candidates: profiles
      .map((profile) => `${profile.name}: ${profile.summary || profile.role}`)
      .slice(0, 48),
    emotion_candidates: [],
    relationship_candidates: uniqueLimited(relationshipCandidates, 72),
    fun_fact_candidates: profiles
      .map((profile) => `${profile.name}: ${[
        profile.role,
        profile.status,
        profile.relevance ? `relevance ${profile.relevance}` : '',
        `closeness ${profile.closeness}/5`,
      ].filter(Boolean).join('; ')}`)
      .slice(0, 48),
    redacted_fragments: 0,
    raw_text_written: false,
    next: 'pulse migrate commit <preview.json> --confirm "import pulse graph"',
  }, {
    scannedSessions: profiles.map((profile, index) => ({
      id: `people-graph:${slug(profile.name)}:${index + 1}`,
      source: 'people-graph',
      title: profile.name,
      message_count: 1,
      evidence_ref: profile.evidence_ref || `people:${index + 1}`,
    })),
  });
}

function printMigrationPreview(preview) {
  const people = preview.people_candidates.length > 0
    ? preview.people_candidates.join(', ')
    : 'none yet';
  const threads = preview.thread_candidates.length > 0
    ? preview.thread_candidates.join(', ')
    : 'none yet';
  console.log(`[pulse] migration preview
─────────────────────────

source: ${preview.source}
files scanned: ${preview.files_scanned}
conversations: ${preview.conversations}
messages: ${preview.messages}
people found: ${people}
thread candidates: ${threads}
redacted fragments: ${preview.redacted_fragments}

privacy:
  raw text will not be written by preview.
  commit will require confirmation and will write structured graph deltas only.

next:
  ${preview.next}
`);
}

function htmlEscape(value) {
  return String(value ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function previewList(items, emptyText = 'No candidates yet.') {
  const safeItems = Array.isArray(items) ? items.filter(Boolean) : [];
  if (safeItems.length === 0) {
    return `<p class="empty">${htmlEscape(emptyText)}</p>`;
  }
  return `<ul>${safeItems.map((item) => `<li class="filter-item" data-search="${htmlEscape(item)}">${htmlEscape(item)}</li>`).join('')}</ul>`;
}

function continuityPreviewText(value) {
  return String(value ?? '')
    .replace(/safe message signals?/gi, 'source snippets')
    .replace(/\bmemories\b/gi, 'saved memory')
    .trim();
}

function previewThreadTitle(value, fallback = 'Archive thread') {
  const text = continuityPreviewText(value);
  const title = text.split(':')[0]?.trim();
  return title || fallback;
}

function previewArchiveSizing(preview) {
  const conversations = Number.isFinite(preview.conversations) ? preview.conversations : 0;
  const messages = Number.isFinite(preview.messages) ? preview.messages : 0;
  return {
		conversations,
		messages,
		structured_candidates:
			preview.memory_candidates.length +
			preview.emotion_candidates.length +
			preview.relationship_candidates.length +
			preview.people_candidates.length,
  };
}

function isEmptyMigrationPreview(preview) {
  return (preview.conversations || 0) === 0 &&
    (preview.messages || 0) === 0 &&
    safeArrayLength(preview.people_candidates) === 0 &&
    safeArrayLength(preview.review_candidates) === 0 &&
    safeArrayLength(preview.thread_candidates) === 0 &&
    safeArrayLength(preview.memory_candidates) === 0 &&
    safeArrayLength(preview.emotion_candidates) === 0 &&
    safeArrayLength(preview.relationship_candidates) === 0;
}

function previewSourceScan(preview) {
  return {
    status: isEmptyMigrationPreview(preview) ? 'no_supported_conversations' : 'scanned',
    source: preview.source || 'unknown',
    files_scanned: Number.isFinite(preview.files_scanned) ? preview.files_scanned : 0,
    files_skipped: Array.isArray(preview.files_skipped) ? preview.files_skipped : [],
    sessions_scanned: Number.isFinite(preview.conversations) ? preview.conversations : 0,
    messages_scanned: Number.isFinite(preview.messages) ? preview.messages : 0,
    archive_was_unpacked: Boolean(preview.archive_was_unpacked),
    raw_text_written: preview.raw_text_written === true,
  };
}

function defaultScannedSessions(preview) {
  const existing = Array.isArray(preview.scanned_sessions) ? preview.scanned_sessions : [];
  if (existing.length > 0) {
    return existing.map((session, index) => ({
      id: safeText(session.id, 120) || `${preview.source || 'source'}:session:${index + 1}`,
      source: safeText(session.source, 80) || preview.source || 'unknown',
      title: safeText(session.title, 160) || `${hostDisplayName(preview.source)} session`,
      message_count: Number.isFinite(session.message_count) ? session.message_count : 0,
      evidence_ref: safeText(session.evidence_ref, 220) || `source:${preview.source || 'unknown'}:${index + 1}`,
    }));
  }

  const titles = uniqueLimited([
    ...previewArray(preview, 'thread_candidates'),
    ...previewArray(preview, 'memory_candidates').map(previewThreadTitle),
  ], 24);
  const count = Math.max(1, titles.length);
  const messagesPerSession = Math.max(0, Math.ceil((preview.messages || 0) / count));
  return titles.map((title, index) => ({
    id: `${preview.source || 'source'}:${slug(title)}:${index + 1}`,
    source: preview.source || 'unknown',
    title,
    message_count: messagesPerSession,
    evidence_ref: `source:${preview.source || 'unknown'}:${index + 1}`,
  }));
}

function threadPeopleFromState(state) {
  const out = {};
  if (!state?.threadPeople) {
    return out;
  }
  for (const [title, people] of state.threadPeople.entries()) {
    out[title] = [...people].filter((name) => !isReviewEntityName(name)).slice(0, 12);
  }
  return out;
}

function threadEmotionsFromState(state) {
  const out = {};
  if (!state?.threadEmotions) {
    return out;
  }
  for (const [title, emotions] of state.threadEmotions.entries()) {
    out[title] = [...emotions].slice(0, 8);
  }
  return out;
}

function relationshipsForThread(relationships, title) {
  return relationships
    .filter((item) => {
      const parsed = parseRelationshipCandidate(item);
      return parsed?.right === title || parsed?.left === title;
    })
    .map(continuityPreviewText)
    .slice(0, 6);
}

function previewSizingForThread(messageCount, thread) {
  return {
		source_snippets: Math.max(0, messageCount),
		structured_candidates:
			safeArrayLength(thread.decisions) +
			safeArrayLength(thread.open_loops) +
			safeArrayLength(thread.do_not_repeat) +
			safeArrayLength(thread.emotional_anchors) +
			safeArrayLength(thread.people_found),
  };
}

function buildPulseInsights(preview) {
  const threads = Array.isArray(preview.candidate_threads) ? preview.candidate_threads : [];
  return threads
    .slice(0, 6)
    .map((thread) => {
      const decisions = safeArrayLength(thread.decisions);
      const openLoops = safeArrayLength(thread.open_loops);
      const people = safeArrayLength(thread.people_found);
      const firstDecision = safeText((thread.decisions ?? [])[0], 150);
      const firstOpenLoop = safeText((thread.open_loops ?? [])[0], 150);
      const firstPerson = safeText((thread.people_found ?? [])[0], 90);
      const threadTitle = safeText(thread.title, 160);
      const reasons = [
        firstDecision ? `Decision candidate: ${firstDecision}` : '',
        firstPerson ? `${firstPerson} appears as a related person in ${threadTitle}.` : '',
        people > 0 ? `${people} related ${people === 1 ? 'person' : 'people'} found may make this thread easier to resume.` : '',
        firstOpenLoop ? `Open loop candidate: ${firstOpenLoop}` : '',
        decisions > 0 ? `${decisions} candidate decision${decisions === 1 ? '' : 's'} can become resume context.` : '',
				openLoops > 0 ? `${openLoops} open loop${openLoops === 1 ? '' : 's'} may need follow-up.` : '',
      ].filter(Boolean);
      if (reasons.length === 0) {
        return null;
      }
      return {
        kind: 'why_this_matters_now',
        thread_title: threadTitle,
        title: `Why this may matter now: ${safeText(thread.title, 140)}`,
        summary: `Pulse found structured continuity in ${threadTitle}. Treat this as a private preview hypothesis: review it before import so the next Claude or Codex session can continue from a smaller, safer context block.`,
        reasons: reasons.slice(0, 4),
        suggested_next_step: 'Review this thread before import, then decide whether to make it an active Pulse thread.',
        related_entities: uniqueLimited([...(thread.people_found ?? []), thread.title].filter(Boolean), 8),
        privacy_tier: 'private',
        confidence: 0.6,
      };
    })
    .filter(Boolean);
}

function buildCandidateThreads(preview, scannedSessions) {
  const memoryItems = previewArray(preview, 'memory_candidates');
  const titles = uniqueLimited([
    ...previewArray(preview, 'thread_candidates'),
    ...memoryItems.map(previewThreadTitle),
    ...scannedSessions.map((session) => session.title),
  ], 24);
  const visibleRelationships = previewArray(preview, 'relationship_candidates')
    .filter((item) => !relationshipTouchesReviewEntity(item));
  const threadPeople = preview.thread_people && typeof preview.thread_people === 'object' ? preview.thread_people : {};
  const threadEmotions = preview.thread_emotions && typeof preview.thread_emotions === 'object' ? preview.thread_emotions : {};
  const reviewItems = previewArray(preview, 'review_candidates').slice(0, 8);

  return titles.map((title, index) => {
    const sessions = scannedSessions.filter((session) => session.title === title);
    const messageCount = sessions.reduce((sum, session) => sum + (session.message_count || 0), 0);
    const decisions = memoryItems
      .filter((item) => previewThreadTitle(item) === title)
      .map(continuityPreviewText);
    const thread = {
      thread_id: `thread:${slug(title)}:${index + 1}`,
      title,
      sources: uniqueLimited((sessions.length > 0 ? sessions : scannedSessions).map((session) => session.source), 8),
      source_sessions: sessions.map((session) => session.id),
      decisions: decisions.length > 0 ? decisions : [`${title}: ${messageCount || preview.messages || 0} source snippets`],
      open_loops: relationshipsForThread(visibleRelationships, title),
      do_not_repeat: ['raw_text_import_disabled'],
      emotional_anchors: uniqueLimited(threadEmotions[title] ?? previewArray(preview, 'emotion_candidates'), 6),
      people_found: uniqueLimited(threadPeople[title] ?? previewArray(preview, 'people_candidates'), 12)
        .filter((name) => !isReviewEntityName(name)),
      review_items: reviewItems,
      privacy_tier: 'private',
    };
		thread.preview_sizing = previewSizingForThread(messageCount, thread);
    return thread;
  });
}

function importGateForPreview(preview) {
  return {
    requires_confirmation: 'import pulse graph',
    default_privacy: 'private',
    will_save: [
      'candidate_threads',
      'decisions',
      'open_loops',
      'do_not_repeat_warnings',
      'confirmed_emotional_anchors',
    ],
    will_not_save: [
      'raw_text',
      'local_paths',
      'secrets_or_tokens',
      'unreviewed_ambiguous_people',
    ],
    raw_text_written: preview.raw_text_written === true,
  };
}

function attachImportPreviewFlow(preview, state = {}) {
  const withState = {
    ...preview,
    thread_people: preview.thread_people ?? threadPeopleFromState(state),
    thread_emotions: preview.thread_emotions ?? threadEmotionsFromState(state),
  };
  const scannedSessions = defaultScannedSessions({
    ...withState,
    scanned_sessions: state.scannedSessions ?? withState.scanned_sessions,
  });
  const staged = {
    ...withState,
    flow: IMPORT_PREVIEW_FLOW,
    source_scan: previewSourceScan(withState),
    scanned_sessions: scannedSessions,
  };
  const candidateThreads = buildCandidateThreads(staged, scannedSessions);
  const withThreads = {
    ...staged,
    candidate_threads: candidateThreads,
    import_gate: importGateForPreview(staged),
  };
  return {
    ...withThreads,
    pulse_insights: Array.isArray(preview.pulse_insights) && preview.pulse_insights.length > 0
      ? preview.pulse_insights
      : buildPulseInsights(withThreads),
  };
}

function overrideImportPreviewSource(preview, source) {
  if (!source) {
    return preview;
  }
  const updated = {
    ...preview,
    source,
    source_scan: preview.source_scan ? { ...preview.source_scan, source } : undefined,
    scanned_sessions: Array.isArray(preview.scanned_sessions)
      ? preview.scanned_sessions.map((session) => ({
        ...session,
        source: session.source === 'unknown' || session.source === 'mixed' ? source : session.source,
      }))
      : preview.scanned_sessions,
  };
  return attachImportPreviewFlow(updated);
}

function demoMigrationThread() {
  return {
    title: 'Thread: Pulse MCP preview',
    decisions: ['Atlas must not own the People Graph.'],
    openLoops: ['run Claude Code E2E without API retries blocking tool use.'],
    doNotRepeat: ['do not claim production readiness.'],
    emotionalAnchors: ['Trust comes from showing the exact resume block before injection.'],
  };
}

function renderEmptyPreviewNotice() {
  const demo = demoMigrationThread();
  return `<section class="trust" id="empty-preview-demo">
    <h2>No real data yet</h2>
    <p>This source did not contain a supported chat export, so Pulse has nothing real to import from it. Here is how a real thread looks when a Claude, Codex, ChatGPT, or Claude Code source is detected.</p>
    <div class="gate-step">
      <h3>Here is how a real thread looks</h3>
      <p><strong>${htmlEscape(demo.title)}</strong></p>
      <ul>
        <li>Decision: ${htmlEscape(demo.decisions[0])}</li>
        <li>Open loop: ${htmlEscape(demo.openLoops[0])}</li>
        <li>Do-not-repeat: ${htmlEscape(demo.doNotRepeat[0])}</li>
        <li>Emotional anchor: ${htmlEscape(demo.emotionalAnchors[0])}</li>
      </ul>
      <p class="empty">Demo only; not imported. Choose a real archive or local session source to save structured continuity.</p>
    </div>
  </section>`;
}

function renderPreviewThreadCards(preview) {
  if (isEmptyMigrationPreview(preview)) {
    const demo = demoMigrationThread();
    return `<div class="profile-grid"><article class="person-card filter-item" data-search="${htmlEscape(`${demo.title} ${demo.decisions.join(' ')} ${demo.openLoops.join(' ')} ${demo.doNotRepeat.join(' ')}`)}">
      <div class="person-head">
        <h3>${htmlEscape(demo.title)}</h3>
        <span>demo only</span>
      </div>
      <p class="empty">Demo only; not imported.</p>
      <div class="mini-block"><h4>Decisions</h4>${previewList(demo.decisions)}</div>
      <div class="mini-block"><h4>Open loops</h4>${previewList(demo.openLoops)}</div>
      <div class="mini-block"><h4>Do-not-repeat</h4>${previewList(demo.doNotRepeat)}</div>
      <div class="mini-block"><h4>Emotional anchors</h4>${previewList(demo.emotionalAnchors)}</div>
    </article></div>`;
  }
  const candidateThreads = Array.isArray(preview.candidate_threads) ? preview.candidate_threads.slice(0, 4) : [];
  if (candidateThreads.length > 0) {
    return `<div class="profile-grid">${candidateThreads.map((thread) => `<article class="person-card filter-item" data-search="${htmlEscape([
      thread.title,
      ...(thread.decisions ?? []),
      ...(thread.open_loops ?? []),
      ...(thread.do_not_repeat ?? []),
      ...(thread.emotional_anchors ?? []),
      ...(thread.people_found ?? []),
    ].join(' '))}">
      <div class="person-head">
        <h3>${htmlEscape(thread.title)}</h3>
        <span>${htmlEscape(thread.privacy_tier || 'private')} preview</span>
      </div>
      <div class="mini-block"><h4>Decisions</h4>${previewList((thread.decisions ?? []).slice(0, 3), 'No saved decisions yet.')}</div>
      <div class="mini-block"><h4>Open loops</h4>${previewList((thread.open_loops ?? []).slice(0, 3), 'No open loops detected yet.')}</div>
      <div class="mini-block"><h4>Do-not-repeat</h4>${previewList((thread.do_not_repeat ?? []).slice(0, 3).map(humanGateItem), 'No do-not-repeat warnings yet.')}</div>
      <div class="mini-block"><h4>Emotional anchors</h4>${previewList((thread.emotional_anchors ?? []).slice(0, 3), 'No emotional anchors yet.')}</div>
    </article>`).join('')}</div>`;
  }
  const memoryItems = Array.isArray(preview.memory_candidates) ? preview.memory_candidates : [];
  const threads = memoryItems.length > 0 ? memoryItems.slice(0, 4) : [`${preview.source || 'Archive'} thread: ${preview.messages || 0} source snippets`];
  return `<div class="profile-grid">${threads.map((item) => {
    const title = previewThreadTitle(item, `${preview.source || 'Archive'} thread`);
    const decisions = Math.max(1, preview.memory_candidates.length);
    const openLoops = preview.relationship_candidates.length > 0 ? 1 : 0;
    const anchors = preview.emotion_candidates.length;
    return `<article class="person-card filter-item" data-search="${htmlEscape(`${title} ${continuityPreviewText(item)}`)}">
      <div class="person-head">
        <h3>${htmlEscape(title)}</h3>
        <span>private preview</span>
      </div>
      <div class="mini-block"><h4>Decisions</h4>${previewList(preview.memory_candidates.map(continuityPreviewText).slice(0, 3), 'No saved decisions yet.')}</div>
      <div class="mini-block"><h4>Open loops</h4>${previewList(openLoops ? ['Review this source before importing it.'] : [], 'No open loops detected yet.')}</div>
      <div class="mini-block"><h4>Do-not-repeat</h4>${previewList(['Do not import raw transcript text.'])}</div>
      <div class="mini-block"><h4>Emotional anchors</h4>${previewList(preview.emotion_candidates.slice(0, 3), anchors ? 'No emotional anchors yet.' : 'No emotional anchors yet.')}</div>
    </article>`;
  }).join('')}</div>`;
}

function renderPreviewTimeline(preview) {
  const title = previewThreadTitle(preview.memory_candidates[0], preview.source || 'Archive');
  const rows = [
    `${hostDisplayName(preview.source)} source scanned: ${preview.conversations || 0} conversations`,
    `${title}: ${preview.memory_candidates.length || 0} candidate continuity items`,
    `Review gate: ${preview.review_candidates?.length || 0} items need a human decision`,
  ];
  return previewList(rows.map(continuityPreviewText));
}

function renderSourceScanFlow(preview) {
  const scan = preview.source_scan ?? previewSourceScan(preview);
  const skipped = Array.isArray(scan.files_skipped) ? scan.files_skipped : [];
  return `<section id="sources-scanned" class="flow-section">
    <h2>Sources scanned</h2>
    <p class="subhead">Pulse scans local/exported source files first and writes only bounded preview metadata. Raw transcript text stays out of the preview.</p>
    <div class="source-flow-grid">
      <article class="flow-card"><span>Status</span><strong>${htmlEscape(scan.status)}</strong></article>
      <article class="flow-card"><span>Source</span><strong>${htmlEscape(scan.source)}</strong></article>
      <article class="flow-card"><span>Files scanned</span><strong>${htmlEscape(scan.files_scanned)}</strong></article>
      <article class="flow-card"><span>Sessions scanned</span><strong>${htmlEscape(scan.sessions_scanned)}</strong></article>
      <article class="flow-card"><span>Messages scanned</span><strong>${htmlEscape(scan.messages_scanned)}</strong></article>
      <article class="flow-card"><span>Raw text written</span><strong>${scan.raw_text_written ? 'yes' : 'no'}</strong></article>
    </div>
    ${skipped.length > 0 ? `<p class="empty section-note">${htmlEscape(skipped.length)} files skipped by preview safety limits.</p>` : ''}
  </section>`;
}

function renderScannedSessionsFlow(preview) {
  const sessions = defaultScannedSessions(preview);
  if (sessions.length === 0) {
    return `<section id="scanned-sessions" class="flow-section">
      <h2>Scanned sessions</h2>
      <p class="empty">No supported chat sessions were found in this source yet.</p>
    </section>`;
  }
  return `<section id="scanned-sessions" class="flow-section">
    <h2>Scanned sessions</h2>
    <p class="subhead">Session rows show only source, title, count, and a local evidence reference. No raw messages are shown here.</p>
    <div class="session-grid">
      ${sessions.slice(0, 12).map((session) => {
        const evidence = humanReferenceLabel(session.evidence_ref) || 'Local source';
        return `<article class="session-card filter-item" data-search="${htmlEscape(`${session.source} ${session.title} ${evidence}`)}">
        <span>${htmlEscape(session.source)}</span>
        <h3>${htmlEscape(session.title)}</h3>
        <p>${htmlEscape(session.message_count)} source snippets · ${htmlEscape(evidence)}</p>
      </article>`;
      }).join('')}
    </div>
  </section>`;
}

function renderCandidateThreadsFlow(preview) {
  const threads = Array.isArray(preview.candidate_threads) ? preview.candidate_threads : buildCandidateThreads(preview, defaultScannedSessions(preview));
  if (threads.length === 0) {
    return `<section id="candidate-threads" class="flow-section">
      <h2>Candidate threads</h2>
      <p class="empty">No candidate threads yet. Choose a supported archive or local history source and preview again.</p>
    </section>`;
  }
  return `<section id="candidate-threads" class="flow-section">
    <h2>Candidate threads</h2>
    <p class="subhead">Pulse groups source sessions into thread-sized continuity candidates before showing people or graph details.</p>
    <div class="thread-flow-grid">
      ${threads.slice(0, 8).map((thread) => `<article class="thread-flow-card filter-item" data-search="${htmlEscape([
        thread.title,
        ...(thread.decisions ?? []),
        ...(thread.open_loops ?? []),
        ...(thread.do_not_repeat ?? []),
        ...(thread.emotional_anchors ?? []),
        ...(thread.people_found ?? []),
      ].join(' '))}">
        <div class="thread-flow-head">
          <h3>${htmlEscape(thread.title)}</h3>
          <span>${htmlEscape(thread.privacy_tier || 'private')}</span>
        </div>
        <div class="thread-flow-metrics">
          <span>${htmlEscape(safeArrayLength(thread.decisions))} decisions</span>
          <span>${htmlEscape(safeArrayLength(thread.open_loops))} open loops</span>
          <span>${htmlEscape(safeArrayLength(thread.do_not_repeat))} do-not-repeat</span>
						<span>${htmlEscape(thread.preview_sizing?.source_snippets ?? 0)} source snippets</span>
        </div>
        <div class="mini-block"><h4>Decisions</h4>${previewList((thread.decisions ?? []).slice(0, 3), 'No decisions detected yet.')}</div>
        <div class="mini-block"><h4>Open loops</h4>${previewList((thread.open_loops ?? []).slice(0, 3), 'No open loops detected yet.')}</div>
        <div class="mini-block"><h4>Do-not-repeat</h4>${previewList((thread.do_not_repeat ?? []).slice(0, 3).map(humanGateItem))}</div>
      </article>`).join('')}
    </div>
  </section>`;
}

function renderPulseInsightsFlow(preview) {
  const insights = Array.isArray(preview.pulse_insights) ? preview.pulse_insights.slice(0, 6) : [];
  if (insights.length === 0) {
    return `<section id="pulse-insights" class="flow-section">
      <h2>Why this may matter now</h2>
      <p class="empty">Pulse will show a short private preview hypothesis here after it finds a thread with decisions, open loops, people, or token savings.</p>
    </section>`;
  }
  return `<section id="pulse-insights" class="flow-section insight-section">
    <h2>Why this may matter now</h2>
    <p class="subhead">Pulse insight turns the graph into a review action. It is a private preview hypothesis, not an automatic claim.</p>
    <div class="insight-grid">
      ${insights.map((insight) => {
        const activeThreadTitle = safeText(insight.thread_title || insight.title || 'Pulse insight', 160);
        return `<article class="insight-card filter-item" data-search="${htmlEscape([
        insight.title,
        insight.summary,
        ...(insight.reasons ?? []),
        insight.suggested_next_step,
        ...(insight.related_entities ?? []),
      ].join(' '))}">
        <span>Pulse insight</span>
        <h3>${htmlEscape(insight.title || 'Why this may matter now')}</h3>
        <p>${htmlEscape(insight.summary || 'Pulse found continuity worth reviewing before import.')}</p>
        <div class="mini-block"><h4>Because</h4>${previewList((insight.reasons ?? []).slice(0, 4))}</div>
        <div class="mini-block"><h4>Next</h4>${previewList([insight.suggested_next_step || 'Review this thread before import.'])}</div>
        <p class="review-status" data-active-thread-status>Not active yet. Mark active only if this thread should appear in the next resume.</p>
        <div class="decision-actions">
          <button type="button" data-active-thread="${htmlEscape(activeThreadTitle)}" data-active-reason="User marked the Pulse insight as active during review.">Make active</button>
        </div>
      </article>`;
      }).join('')}
    </div>
  </section>`;
}

function reviewActionCandidates(preview) {
  const fromThreads = Array.isArray(preview.candidate_threads)
    ? preview.candidate_threads.flatMap((thread) => thread.review_items ?? [])
    : [];
  return uniqueLimited([
    ...fromThreads,
    ...previewArray(preview, 'review_candidates'),
  ], 12);
}

function renderReviewActionsFlow(preview) {
  const candidates = reviewActionCandidates(preview);
  if (candidates.length === 0) {
    return `<section id="review-actions" class="flow-section">
      <h2>Review actions</h2>
      <p class="empty">No ambiguous entities need a decision in this preview.</p>
      <p id="review-counter" class="review-counter" data-review-total="0">reviewed 0 of 0 · No reviewed JSON needed.</p>
    </section>`;
  }
  return `<section id="review-actions" class="flow-section">
    <h2>Review actions</h2>
    <p class="subhead">Resolve ambiguous models, tools, projects, or people before import. These buttons update a downloadable reviewed JSON file. Import still waits for you to download and commit that reviewed file.</p>
    <p id="review-counter" class="review-counter" data-review-total="${htmlEscape(candidates.length)}">reviewed 0 of ${htmlEscape(candidates.length)} · Download reviewed JSON before import.</p>
    <div class="decision-grid">
      ${candidates.slice(0, 8).map((item, index) => `<article class="decision-card filter-item" data-review-card data-review-item="${htmlEscape(item)}" data-search="${htmlEscape(item)}">
        <h3>Review: ${htmlEscape(item)}</h3>
        <p>Suggested action: decide whether this is a person, project/component, private context, or noise.</p>
        <p class="review-status" data-review-status>Needs your decision. Download reviewed JSON before import.</p>
        <div class="decision-actions">
          <button type="button" data-review-action="confirm" data-review-result="confirmed" data-review-kind="project">${index === 0 ? 'Confirm' : 'Confirm'}</button>
          <button type="button" data-review-action="edit" data-review-result="edit needed">Edit</button>
          <button type="button" data-review-action="ignore" data-review-result="ignored">Ignore</button>
          <button type="button" data-review-action="private" data-review-result="marked private" data-review-kind="project">Mark private</button>
        </div>
      </article>`).join('')}
    </div>
    <div class="review-export">
      <h3>Reviewed JSON</h3>
      <p>Download <code>pulse-preview.reviewed.json</code>, then run the reviewed import command in the import gate.</p>
      <button type="button" id="download-reviewed-json">Download reviewed JSON</button>
      <p id="review-download-status" class="review-status">review_decisions will be written into pulse-preview.reviewed.json.</p>
    </div>
  </section>`;
}

function renderPreviewPersonCards(preview) {
  const people = Array.isArray(preview.people_candidates)
    ? preview.people_candidates.filter(Boolean).slice(0, 12)
    : [];
  if (people.length === 0) {
    return '<p class="empty">No people found in this source yet.</p>';
  }
  const relationships = Array.isArray(preview.relationship_candidates) ? preview.relationship_candidates : [];
  const funFacts = Array.isArray(preview.fun_fact_candidates) ? preview.fun_fact_candidates : [];
  const memories = Array.isArray(preview.memory_candidates) ? preview.memory_candidates : [];
  const profiles = Array.isArray(preview.person_profiles) ? preview.person_profiles : [];
  const profileByName = new Map(profiles.map((profile) => [profile.name, profile]));
  return `<div class="profile-grid">${people.map((person) => {
    const profile = profileByName.get(person);
    const relatedThreads = relationships
      .filter((item) => item.startsWith(`${person} -> `))
      .map((item) => item.slice(`${person} -> `.length))
      .filter((item) => !isReviewEntityName(item))
      .slice(0, 4);
    const personRelationships = relationships
      .filter((item) => item.includes(person) && !item.startsWith(`${person} -> `) && !relationshipTouchesReviewEntity(item))
      .slice(0, 4);
    const personFacts = funFacts
      .filter((item) => item.startsWith(person))
      .slice(0, 3);
    const mentionText = personFacts[0]?.match(/appeared in (\d+) (?:safe preview signal|bounded source snippet|preview source snippet)/)?.[1] ?? '1';
    const profileFacts = profile ? [
      profile.role ? `Role: ${profile.role}` : '',
      profile.status ? `Status: ${profile.status}` : '',
      profile.relevance ? `Relevant: ${profile.relevance}` : '',
      Number.isFinite(profile.closeness) ? `Closeness: ${profile.closeness}/5` : '',
      profile.evidence_ref ? `Source: ${humanReferenceLabel(profile.evidence_ref) || 'Curated people graph'}` : '',
    ].filter(Boolean) : [];
    const searchText = [
      person,
      profile?.role,
      profile?.status,
      profile?.relevance,
      profile?.summary,
      ...personFacts,
      ...relatedThreads,
      ...personRelationships,
    ].join(' ');
    return `<article class="person-card filter-item" data-search="${htmlEscape(searchText)}">
      <div class="person-head">
        <h3>${htmlEscape(person)}</h3>
        <span>${profile ? 'curated' : `${htmlEscape(mentionText)} mentions`}</span>
      </div>
      ${profile?.summary ? `<p class="empty">${htmlEscape(profile.summary)}</p>` : ''}
      <div class="mini-block">
        <h4>${profile ? 'Profile' : 'Evidence'}</h4>
        ${previewList(profileFacts.length > 0 ? profileFacts : (personFacts.length > 0 ? personFacts : [`${person} appeared in ${mentionText} bounded source snippet${mentionText === '1' ? '' : 's'}`]))}
      </div>
      <div class="mini-block">
        <h4>${profile ? 'Context' : 'Related continuity'}</h4>
        ${previewList(profile?.links?.length > 0 ? profile.links : (relatedThreads.length > 0 ? relatedThreads : memories.map(continuityPreviewText).slice(0, 3)))}
      </div>
      <div class="mini-block">
        <h4>Relationships</h4>
        ${previewList(personRelationships)}
      </div>
    </article>`;
  }).join('')}</div>`;
}

function renderCopyCommand(command) {
  if (!command) {
    return '';
  }
  return `<div class="command"><code>${htmlEscape(command)}</code><button class="copy" data-copy="${htmlEscape(command)}">Copy command</button></div>`;
}

function reviewedCommitCommand(preview) {
  const openFlag = typeof preview.commit_command === 'string' && preview.commit_command.includes(' --open') ? ' --open' : '';
  return `pulse migrate commit pulse-preview.reviewed.json --confirm "import pulse graph"${openFlag}`;
}

function viewerCommandForPreview(preview) {
  const source = safeText(preview.viewer_command, 600);
  if (!source) {
    return '';
  }
  return source
    .replace(/\s+--data-dir\s+(?:"[^"]+"|'[^']+'|\S+)/g, '')
    .replace(/\s+--base\s+http:\/\/127\.0\.0\.1:\d+/g, ' --base http://127.0.0.1:<pulse-port>');
}

function reviewedPreviewPayload(preview) {
  return {
    flow: IMPORT_PREVIEW_FLOW,
    ok: preview.ok,
    source: preview.source,
    source_kind: preview.source_kind,
    conversations: preview.conversations,
    messages: preview.messages,
    people_candidates: previewArray(preview, 'people_candidates'),
    review_candidates: previewArray(preview, 'review_candidates'),
    thread_candidates: previewArray(preview, 'thread_candidates'),
    memory_candidates: previewArray(preview, 'memory_candidates'),
    emotion_candidates: previewArray(preview, 'emotion_candidates'),
    relationship_candidates: previewArray(preview, 'relationship_candidates'),
    fun_fact_candidates: previewArray(preview, 'fun_fact_candidates'),
    candidate_threads: Array.isArray(preview.candidate_threads) ? preview.candidate_threads.map((thread) => ({
      title: safeText(thread.title, 160),
      decisions: Array.isArray(thread.decisions) ? thread.decisions.map((item) => safeText(item, 240)).filter(Boolean) : [],
      open_loops: Array.isArray(thread.open_loops) ? thread.open_loops.map((item) => safeText(item, 240)).filter(Boolean) : [],
      do_not_repeat: Array.isArray(thread.do_not_repeat) ? thread.do_not_repeat.map((item) => safeText(item, 240)).filter(Boolean) : [],
      emotional_anchors: Array.isArray(thread.emotional_anchors) ? thread.emotional_anchors.map((item) => safeText(item, 240)).filter(Boolean) : [],
      review_items: Array.isArray(thread.review_items) ? thread.review_items.map((item) => safeText(item, 120)).filter(Boolean) : [],
      privacy_tier: safeText(thread.privacy_tier, 40) || 'private',
    })) : [],
    pulse_insights: Array.isArray(preview.pulse_insights) ? preview.pulse_insights.map((insight) => ({
      kind: safeText(insight.kind, 80) || 'why_this_matters_now',
      thread_title: safeText(insight.thread_title, 160),
      title: safeText(insight.title, 180),
      summary: safeText(insight.summary, 360),
      reasons: Array.isArray(insight.reasons) ? insight.reasons.map((item) => safeText(item, 220)).filter(Boolean) : [],
      suggested_next_step: safeText(insight.suggested_next_step, 220),
      related_entities: Array.isArray(insight.related_entities) ? insight.related_entities.map((item) => safeText(item, 120)).filter(Boolean) : [],
      privacy_tier: safeText(insight.privacy_tier, 40) || 'private',
      confidence: Number.isFinite(insight.confidence) ? Math.max(0, Math.min(1, insight.confidence)) : 0.6,
    })).filter((insight) => insight.title || insight.summary) : [],
    active_threads: Array.isArray(preview.active_threads) ? preview.active_threads.map((thread) => {
      if (typeof thread === 'string') {
        return { thread_title: safeText(thread, 160), source: 'pulse_review' };
      }
      return {
        thread_title: safeText(thread?.thread_title ?? thread?.title, 160),
        reason: safeText(thread?.reason, 240),
        source: safeText(thread?.source, 80) || 'pulse_review',
      };
    }).filter((thread) => thread.thread_title) : [],
    raw_text_written: preview.raw_text_written,
    review_decisions: preview.review_decisions && typeof preview.review_decisions === 'object' && !Array.isArray(preview.review_decisions)
      ? preview.review_decisions
      : {},
  };
}

function humanGateItem(item) {
  const labels = {
    candidate_threads: 'candidate threads',
    open_loops: 'open loops',
    do_not_repeat_warnings: 'do-not-repeat warnings',
    confirmed_emotional_anchors: 'confirmed emotional anchors',
    raw_text: 'raw transcript',
    raw_text_import_disabled: 'Do not import raw transcript text.',
    local_paths: 'local paths',
    secrets_or_tokens: 'secrets or tokens',
    unreviewed_ambiguous_people: 'unreviewed ambiguous people',
  };
  return labels[item] ?? String(item ?? '').replace(/_/g, ' ');
}

function renderPreviewImportGate(preview) {
  const gate = preview.import_gate ?? importGateForPreview(preview);
  const willSave = Array.isArray(gate.will_save) ? gate.will_save.map(humanGateItem) : [];
  const willNotSave = Array.isArray(gate.will_not_save) ? gate.will_not_save.map(humanGateItem) : [];
  const hasReview = reviewActionCandidates(preview).length > 0;
  const commitCommand = hasReview ? reviewedCommitCommand(preview) : preview.commit_command;
  if (isEmptyMigrationPreview(preview)) {
    return `<section class="trust">
      <h2>Import gate</h2>
      <p>No real source items were found yet, so there is nothing to import from this preview. The demo thread above is only an example and will not be written into Pulse.</p>
      <div class="gate-summary-grid">
        <article class="gate-summary"><h3>Will save</h3>${previewList([], 'Nothing yet.')}</article>
        <article class="gate-summary"><h3>Will not save</h3>${previewList(willNotSave)}</article>
      </div>
      <div class="gate-step">
        <h3>Next</h3>
        <p>Choose a supported ChatGPT/Claude archive or local Codex/Claude Code history folder, then preview again before importing.</p>
      </div>
    </section>`;
  }
  if (!preview.commit_command) {
    return `<section class="trust">
      <h2>Import gate</h2>
      <p>Raw chat text was not written. This file shows bounded candidates only. Nothing is imported until you create a JSON preview and run the exact confirmation phrase.</p>
      <div class="gate-summary-grid">
        <article class="gate-summary"><h3>Will save</h3>${previewList(willSave)}</article>
        <article class="gate-summary"><h3>Will not save</h3>${previewList(willNotSave)}</article>
      </div>
      <div class="gate-step">
        <h3>Make this importable</h3>
        <p>${hasReview ? 'Download reviewed JSON above, or re-run preview with <code>--out pulse-preview.json</code> to make a copyable import command.' : 'Re-run preview with <code>--out pulse-preview.json</code> to make a copyable import command.'}</p>
        ${hasReview ? renderCopyCommand(commitCommand) : ''}
      </div>
    </section>`;
  }
  return `<section class="trust">
      <h2>Import gate</h2>
      <p>Raw chat text was not written. This file shows bounded candidates only. Nothing is imported until you run the command with the exact confirmation phrase.</p>
      <div class="gate-summary-grid">
        <article class="gate-summary"><h3>Will save</h3>${previewList(willSave)}</article>
        <article class="gate-summary"><h3>Will not save</h3>${previewList(willNotSave)}</article>
      </div>
      ${hasReview ? `<div class="gate-step">
        <h3>1. Download reviewed JSON</h3>
        <p>Review actions write decisions into <code>pulse-preview.reviewed.json</code>. The import command below uses that reviewed file.</p>
      </div>` : ''}
      <div class="gate-step">
        <h3>${hasReview ? '2' : '1'}. Import structured continuity and open viewer</h3>
        <p>This writes threads, decisions, open loops, do-not-repeat, and emotional anchors into Pulse, then opens the memory viewer.</p>
        ${renderCopyCommand(commitCommand)}
      </div>
      <div class="gate-step">
        <h3>${hasReview ? '3' : '2'}. Open the memory viewer again later</h3>
        <p>This shows resume context, saved decisions, open loops, and review decisions before the next session.</p>
        ${renderCopyCommand(viewerCommandForPreview(preview))}
      </div>
    </section>`;
}

function renderMigrationPreviewHTML(preview) {
  const generatedAt = new Date().toISOString();
  const isPeopleGraph = preview.source_kind === 'curated_people_graph';
  const previewTitle = isPeopleGraph ? 'Real people graph' : 'Pulse Import Preview';
  const previewSubtitle = isPeopleGraph
    ? 'Curated people from an existing local graph. Pulse treats this as higher-trust real-person context, not noisy chat extraction.'
    : 'Preview candidate threads first. Pulse imports only structured continuity after you confirm.';
  const previewPeople = splitGalleryPersonCandidates(preview.people_candidates);
  const reviewCandidates = uniqueLimited([
    ...previewPeople.review,
    ...(Array.isArray(preview.review_candidates) ? preview.review_candidates : []),
  ], 96);
  const visibleRelationships = preview.relationship_candidates.filter((item) => !relationshipTouchesReviewEntity(item));
  const visibleFunFacts = preview.fun_fact_candidates.filter((item) => !funFactTouchesReviewEntity(item));
	const archiveSizing = previewArchiveSizing(preview);
  const emptyPreview = isEmptyMigrationPreview(preview);
  const profilePreview = {
    ...preview,
    people_candidates: previewPeople.confident,
    relationship_candidates: visibleRelationships,
    fun_fact_candidates: visibleFunFacts,
  };
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pulse Import Preview</title>
  <style>
    :root { color-scheme: light; --bg:#fbf7f4; --ink:#463b3d; --muted:#8f8180; --line:rgba(136,116,112,.14); --panel:rgba(255,255,255,.54); --rail:rgba(255,255,255,.34); --rail-muted:#9a8c89; --accent:#d99a8f; --accent-soft:#f7cbc2; --good:#8bb79f; --soft:rgba(255,255,255,.46); --pastel-a:#f8d9d0; --pastel-b:#dceaf7; --pastel-c:#e7e0fa; --pastel-d:#e4f2df; }
    * { box-sizing: border-box; }
    body { margin:0; min-height:100vh; font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; color:var(--ink); background:
      radial-gradient(circle at 8% 8%, rgba(248,217,208,.92), transparent 32%),
      radial-gradient(circle at 82% 12%, rgba(220,234,247,.86), transparent 30%),
      radial-gradient(circle at 68% 72%, rgba(231,224,250,.72), transparent 36%),
      linear-gradient(145deg,#fffaf7 0%,#f6fbff 48%,#fff7fb 100%); }
    .workbench { min-height:100vh; display:grid; grid-template-columns:260px minmax(0,1fr); }
    .rail { position:sticky; top:0; height:100vh; padding:26px 22px; background:var(--rail); color:var(--ink); display:flex; flex-direction:column; gap:26px; border-right:1px solid rgba(255,255,255,.55); backdrop-filter:blur(24px) saturate(1.35); -webkit-backdrop-filter:blur(24px) saturate(1.35); }
    .brand { display:grid; gap:8px; }
    .brand b { font-size:22px; letter-spacing:0; font-weight:680; color:#5a4d51; }
    .brand span, .rail small { color:var(--rail-muted); line-height:1.45; }
    .rail-nav { display:grid; gap:8px; }
    .rail-nav a, .rail-pill { color:var(--ink); text-decoration:none; border:1px solid rgba(255,255,255,.58); border-radius:14px; padding:10px 12px; background:rgba(255,255,255,.42); box-shadow:0 10px 26px rgba(115,92,84,.08); backdrop-filter:blur(16px); -webkit-backdrop-filter:blur(16px); }
    .rail-pill { color:var(--rail-muted); }
    .workspace { min-width:0; padding:34px clamp(18px,4vw,54px) 54px; }
    header { display:grid; grid-template-columns:minmax(0,1fr) minmax(190px,280px); gap:24px; align-items:end; padding-bottom:24px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:46px; line-height:1.08; letter-spacing:0; font-weight:680; color:#4a3f42; max-width:760px; }
    h2 { margin:0 0 12px; font-size:18px; letter-spacing:0; }
    h3 { margin:0 0 6px; font-size:16px; letter-spacing:0; }
    p { margin:0; line-height:1.48; }
    .subhead { margin:12px 0 0; max-width:760px; color:var(--muted); font-size:16px; line-height:1.5; }
    .glass-card, .stamp, section, .person-card, .metric, .decision-card { background:var(--panel); border:1px solid rgba(255,255,255,.62); border-radius:16px; box-shadow:0 18px 50px rgba(125,102,94,.12), inset 0 1px 0 rgba(255,255,255,.74); backdrop-filter:blur(22px) saturate(1.24); -webkit-backdrop-filter:blur(22px) saturate(1.24); }
    .stamp { padding:18px; }
    .stamp strong { display:block; font-size:22px; letter-spacing:0; font-weight:680; line-height:1; }
    .stamp span { display:block; margin-top:8px; color:var(--muted); font-size:13px; line-height:1.35; }
    .metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(130px,1fr)); gap:12px; margin:18px 0; }
    .metric { min-height:88px; padding:14px 14px 14px 16px; position:relative; overflow:hidden; }
    .metric::before { content:""; position:absolute; inset:0 auto 0 0; width:4px; background:linear-gradient(180deg,var(--pastel-a),var(--pastel-c)); }
    .metric b { display:block; font-size:28px; line-height:1.05; letter-spacing:0; font-weight:680; }
    .metric span { display:block; margin-top:8px; color:var(--muted); font-size:13px; }
    section { padding:18px; margin-top:16px; }
    .filter-panel { margin-top:16px; }
    .filter-panel p { margin:0 0 12px; color:var(--muted); }
    .filter-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:10px; align-items:center; }
    .filter-row input { min-height:42px; width:100%; border:1px solid rgba(255,255,255,.68); border-radius:999px; padding:9px 14px; font:inherit; background:rgba(255,255,255,.62); color:var(--ink); backdrop-filter:blur(14px); -webkit-backdrop-filter:blur(14px); }
    .filter-row button, .copy { min-height:40px; padding:9px 13px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.58)); color:#6d5653; cursor:pointer; font:inherit; white-space:nowrap; box-shadow:0 10px 24px rgba(160,126,118,.11); }
    .filter-row button:hover, .copy:hover { transform:translateY(-1px); }
    #preview-filter-status { margin-top:10px; color:var(--muted); font-size:14px; }
    .source-flow-grid, .session-grid, .thread-flow-grid, .gate-summary-grid, .insight-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(220px,1fr)); gap:12px; }
    .flow-card, .session-card, .thread-flow-card, .gate-summary, .insight-card { background:rgba(255,255,255,.50); border:1px solid rgba(255,255,255,.62); border-radius:14px; padding:14px; box-shadow:inset 0 1px 0 rgba(255,255,255,.66); }
    .flow-card span, .session-card span, .thread-flow-head span { display:block; color:var(--muted); font-size:12px; letter-spacing:.08em; text-transform:uppercase; }
    .flow-card strong { display:block; margin-top:7px; font-size:22px; font-weight:680; overflow-wrap:anywhere; }
    .session-card h3 { margin-top:7px; font-size:18px; }
    .session-card p { color:var(--muted); overflow-wrap:anywhere; }
    .thread-flow-grid { grid-template-columns:repeat(auto-fit,minmax(280px,1fr)); }
    .insight-grid { grid-template-columns:repeat(auto-fit,minmax(300px,1fr)); }
    .insight-section { background:linear-gradient(145deg,rgba(255,250,247,.66),rgba(232,240,251,.50)); }
    .insight-card { border-color:rgba(217,154,143,.30); }
    .insight-card.active-thread { border-color:rgba(139,183,159,.52); background:rgba(244,252,247,.62); }
    .insight-card > span { display:inline-flex; margin-bottom:9px; border:1px solid rgba(217,154,143,.30); background:rgba(255,255,255,.50); border-radius:999px; padding:5px 9px; color:#8b6d67; font-size:12px; letter-spacing:.06em; text-transform:uppercase; }
    .insight-card h3 { font-size:20px; }
    .insight-card p { color:#745f5b; margin-bottom:12px; }
    .thread-flow-head { display:flex; justify-content:space-between; gap:10px; align-items:baseline; border-bottom:1px solid var(--line); padding-bottom:10px; margin-bottom:12px; }
    .thread-flow-head h3 { margin:0; font-size:21px; }
    .thread-flow-metrics { display:flex; flex-wrap:wrap; gap:7px; margin:0 0 12px; }
    .thread-flow-metrics span, .review-counter { border:1px solid rgba(255,255,255,.62); background:rgba(255,255,255,.48); border-radius:999px; padding:6px 9px; color:var(--muted); font-size:13px; }
    .review-counter { display:inline-flex; margin:0 0 12px; }
    .review-status { margin-top:10px; color:var(--muted); }
    .decision-card.reviewed { border-color:rgba(139,183,159,.48); background:rgba(244,252,247,.66); }
    .decision-card.ignored { opacity:.72; }
    .section-note { margin-top:10px; }
    .profile-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(250px,1fr)); gap:12px; }
    .person-card { padding:16px; border-top:1px solid rgba(217,154,143,.36); }
    .person-head { display:flex; justify-content:space-between; gap:10px; align-items:baseline; border-bottom:1px solid var(--line); padding-bottom:10px; margin-bottom:12px; }
    .person-head h3 { margin:0; font-size:22px; letter-spacing:0; font-weight:680; }
    .person-head span { color:var(--muted); font-size:13px; white-space:nowrap; }
    .mini-block + .mini-block { margin-top:13px; }
    .mini-block h4 { margin:0 0 7px; color:var(--muted); font-size:12px; letter-spacing:.08em; text-transform:uppercase; }
    .grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; }
    .decision-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(240px,1fr)); gap:12px; }
    .decision-card { padding:15px; }
    .decision-actions { display:flex; flex-wrap:wrap; gap:8px; margin-top:12px; }
    .decision-actions button { min-height:34px; padding:7px 11px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.54)); color:#6d5653; cursor:pointer; font:inherit; font-size:13px; line-height:1; white-space:nowrap; box-shadow:0 8px 18px rgba(160,126,118,.10); }
    .decision-actions button:hover { transform:translateY(-1px); }
    .review-export { margin-top:14px; border-top:1px solid rgba(232,137,119,.22); padding-top:12px; }
    .review-export button { min-height:38px; margin-top:10px; padding:8px 12px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.58)); color:#6d5653; cursor:pointer; font:inherit; box-shadow:0 10px 24px rgba(160,126,118,.11); }
    .wide { grid-column:span 2; }
    ul { list-style:none; padding:0; margin:0; display:grid; gap:8px; }
    li { border:1px solid rgba(255,255,255,.62); padding:10px 11px; background:rgba(255,255,255,.46); border-radius:12px; line-height:1.35; overflow-wrap:anywhere; }
    .empty { color:var(--muted); }
    .graph-stage { background:linear-gradient(145deg,rgba(255,255,255,.64),rgba(232,240,251,.48)); color:var(--ink); border-color:rgba(255,255,255,.72); }
    .graph-stage .subhead, .graph-stage .empty { color:var(--muted); }
    .graph-stage .grid section { background:rgba(255,255,255,.44); border-color:rgba(255,255,255,.62); color:var(--ink); box-shadow:inset 0 1px 0 rgba(255,255,255,.72); }
    .graph-stage li { background:rgba(255,255,255,.42); border-color:rgba(255,255,255,.62); color:var(--ink); }
    .graph-stage ul { max-height:380px; overflow:auto; padding-right:4px; }
    #review-queue ul { max-height:260px; overflow:auto; padding-right:4px; }
    .trust { border-color:rgba(217,154,143,.24); background:rgba(255,246,242,.56); color:#6d5653; }
    .trust p { color:#846a65; }
    .gate-step { margin-top:14px; border-top:1px solid rgba(232,137,119,.22); padding-top:12px; }
    .command { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:8px; align-items:start; margin-top:10px; }
    code { display:block; padding:11px 12px; border:1px solid rgba(255,255,255,.62); border-radius:12px; background:rgba(255,255,255,.46); color:#4b5558; overflow-wrap:anywhere; font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:12px; line-height:1.45; }
    .review-export p code, .gate-step p code { display:inline; padding:2px 6px; border-radius:7px; font-size:13px; line-height:1.3; }
    .copy:focus-visible, .filter-row button:focus-visible, .decision-actions button:focus-visible, input:focus-visible, .rail-nav a:focus-visible { outline:3px solid rgba(232,137,119,.32); outline-offset:2px; }
    @media (max-width:900px) { .workbench { grid-template-columns:1fr; } .rail { position:relative; height:auto; } header, .grid, .filter-row, .command { grid-template-columns:1fr; } .wide { grid-column:auto; } h1 { font-size:34px; line-height:1.08; } }
  </style>
</head>
<body data-design="pulse-glass-v3" data-import-flow="${IMPORT_PREVIEW_FLOW}">
  <main class="workbench">
    <aside class="rail" aria-label="Pulse preview navigation">
      <div class="brand">
        <b>Pulse Import</b>
        <span>Thread preview before continuity import.</span>
      </div>
      <nav class="rail-nav">
        <a href="#sources-scanned">Scan</a>
        <a href="#scanned-sessions">Sessions</a>
        <a href="#candidate-threads">Threads</a>
        <a href="#pulse-insights">Insight</a>
        <a href="#review-actions">Review</a>
        <a href="#import-gate">Gate</a>
      </nav>
      <div class="rail-pill">Review first. Import later.</div>
      <small>No raw transcript is written by this preview.</small>
    </aside>
    <div class="workspace">
    <header id="source-preview">
      <div>
        <h1>${htmlEscape(previewTitle)}</h1>
        <p class="subhead">${htmlEscape(previewSubtitle)}</p>
      </div>
      <div class="stamp">
        <strong>${htmlEscape(isPeopleGraph ? 'Curated people' : preview.source)}</strong>
        <span>Generated ${htmlEscape(generatedAt)}</span>
      </div>
    </header>

    ${renderSourceScanFlow(preview)}
    ${renderScannedSessionsFlow(preview)}
    ${renderCandidateThreadsFlow(preview)}
    ${renderPulseInsightsFlow(preview)}
    ${renderReviewActionsFlow(preview)}

    <div class="metrics" aria-label="Migration preview metrics">
      <div class="metric"><b>${htmlEscape(preview.conversations)}</b><span>conversations</span></div>
      <div class="metric"><b>${htmlEscape(preview.messages)}</b><span>source snippets</span></div>
      <div class="metric"><b>${htmlEscape(Math.max(1, preview.memory_candidates.length))}</b><span>threads</span></div>
      <div class="metric"><b>${htmlEscape(preview.memory_candidates.length)}</b><span>decisions</span></div>
      <div class="metric"><b>${htmlEscape(visibleRelationships.length > 0 ? 1 : 0)}</b><span>open loops</span></div>
      <div class="metric"><b>${htmlEscape(reviewCandidates.length)}</b><span>needs decision</span></div>
      <div class="metric"><b>${htmlEscape(preview.emotion_candidates.length)}</b><span>emotional anchors</span></div>
      <div class="metric"><b>${htmlEscape(previewPeople.confident.length)}</b><span>people found</span></div>
			<div class="metric"><b>${htmlEscape(archiveSizing.structured_candidates)}</b><span>structured candidates</span></div>
      <div class="metric"><b>${htmlEscape(preview.redacted_fragments)}</b><span>redacted fragments</span></div>
    </div>

    ${emptyPreview ? renderEmptyPreviewNotice() : ''}

    <section id="thread-preview">
      <h2>Thread preview</h2>
      <p class="subhead">What Pulse will turn into continuity. Start here, then inspect people and graph details only if needed.</p>
      ${renderPreviewThreadCards(preview)}
    </section>

		<section id="archive-sizing">
			<h2>Archive sizing</h2>
			<div class="metrics" aria-label="Archive sizing">
				<div class="metric"><b>${htmlEscape(archiveSizing.conversations)}</b><span>conversations</span></div>
				<div class="metric"><b>${htmlEscape(archiveSizing.messages)}</b><span>source snippets</span></div>
				<div class="metric"><b>${htmlEscape(archiveSizing.structured_candidates)}</b><span>structured candidates</span></div>
			</div>
			<p class="empty">This preview reports source and candidate counts only. Token economy starts from immutable delivery receipts after context is actually offered.</p>
    </section>

    <section class="filter-panel">
      <h2>Filter preview</h2>
      <p>Search locally across threads, decisions, open loops, people found, and review items before importing anything.</p>
      <div class="filter-row">
        <input id="preview-filter" placeholder="Type a thread, person, decision, or review item">
        <button id="preview-filter-clear">Clear</button>
      </div>
      <div id="preview-filter-status">Showing all preview candidates.</div>
    </section>

    <section id="person-profiles">
      <h2>People found</h2>
      ${renderPreviewPersonCards(profilePreview)}
    </section>

    <section id="review-queue">
      <h2>Needs your decision</h2>
      <p class="empty"><strong>Review before import:</strong> these may be models, tools, projects, archive labels, or ambiguous entities. Pulse keeps them out of people/context memory until you decide.</p>
      <div class="decision-grid">
        ${(reviewCandidates.length > 0 ? reviewCandidates.slice(0, 8) : ['No ambiguous people candidates yet.']).map((item) => `<article class="decision-card filter-item" data-search="${htmlEscape(item)}"><h3>Review: ${htmlEscape(item)}</h3><p>Suggested action: review before import.</p><div class="decision-actions"><button type="button">Confirm</button><button type="button">Edit</button><button type="button">Ignore</button><button type="button">Mark private</button></div></article>`).join('')}
      </div>
    </section>

    <section class="graph-stage" id="thread-map">
      <h2>Thread map and source details</h2>
      <p class="subhead">Lower-level details from this selected source. They stay behind review and do not become raw memory by default.</p>
      <div class="grid">
        <section>
          <h2>Threads</h2>
          ${renderPreviewTimeline(preview)}
        </section>
        <section>
          <h2>Decisions</h2>
          ${previewList(preview.memory_candidates.map(continuityPreviewText))}
        </section>
        <section>
          <h2>Open loops</h2>
          ${previewList(visibleRelationships.slice(0, 12).map(continuityPreviewText), 'No open loops yet.')}
        </section>
        <section>
          <h2>Do-not-repeat</h2>
          ${previewList(['Do not import raw transcript text.'])}
        </section>
        <section>
          <h2>Emotional anchors</h2>
          ${previewList(preview.emotion_candidates)}
        </section>
        <section>
          <h2>People found</h2>
          ${previewList(previewPeople.confident)}
        </section>
      </div>
    </section>

    <div id="import-gate">${renderPreviewImportGate(preview)}</div>
    </div>
  </main>
  <script type="application/json" id="reviewed-preview-data">${htmlEscape(JSON.stringify(reviewedPreviewPayload(preview)))}</script>
  <script>
    async function copyCommand(button) {
      const command = button.getAttribute("data-copy") || "";
      try {
        await navigator.clipboard.writeText(command);
        button.textContent = "Copied";
      } catch {
        const textarea = document.createElement("textarea");
        textarea.value = command;
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
        button.textContent = "Copied";
      }
      setTimeout(() => { button.textContent = "Copy command"; }, 1400);
    }
    for (const button of document.querySelectorAll("[data-copy]")) {
      button.addEventListener("click", () => copyCommand(button));
    }
    const reviewDecisions = {};
    const activeThreads = {};
    function updateReviewCounter() {
      const counter = document.getElementById("review-counter");
      if (!counter) return;
      const total = Number(counter.getAttribute("data-review-total") || 0);
      const reviewed = document.querySelectorAll("[data-review-card].reviewed").length;
      counter.textContent = "reviewed " + reviewed + " of " + total + " · Download reviewed JSON before import.";
    }
    function reviewedPreviewBase() {
      const node = document.getElementById("reviewed-preview-data");
      try {
        return JSON.parse(node ? node.textContent || "{}" : "{}");
      } catch {
        return {};
      }
    }
    function seedActiveThreads() {
      const base = reviewedPreviewBase();
      const raw = Array.isArray(base.active_threads) ? base.active_threads : [];
      for (const thread of raw) {
        const title = typeof thread === "string" ? thread : thread && (thread.thread_title || thread.title);
        if (!title) continue;
        activeThreads[title] = typeof thread === "string"
          ? { thread_title: title, source: "pulse_review" }
          : { ...thread, thread_title: title };
      }
    }
    function downloadReviewedJSON() {
      const reviewed = {
        ...reviewedPreviewBase(),
        review_decisions: reviewDecisions,
        active_threads: Object.values(activeThreads),
        reviewed_at: new Date().toISOString(),
        flow: "${IMPORT_PREVIEW_FLOW}"
      };
      const blob = new Blob([JSON.stringify(reviewed, null, 2) + "\\n"], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = "pulse-preview.reviewed.json";
      document.body.appendChild(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      const status = document.getElementById("review-download-status");
      if (status) status.textContent = "pulse-preview.reviewed.json downloaded with review_decisions and active_threads.";
    }
    seedActiveThreads();
    for (const button of document.querySelectorAll("[data-active-thread]")) {
      button.addEventListener("click", () => {
        const title = button.getAttribute("data-active-thread") || "";
        const reason = button.getAttribute("data-active-reason") || "User marked this Pulse insight as active during review.";
        if (!title) return;
        activeThreads[title] = {
          thread_title: title,
          reason,
          source: "pulse_insight",
          reviewed_at: new Date().toISOString()
        };
        const card = button.closest(".insight-card");
        if (card) card.classList.add("active-thread");
        const status = card ? card.querySelector("[data-active-thread-status]") : null;
        if (status) status.textContent = "Active thread staged for reviewed JSON. Download before import.";
        const downloadStatus = document.getElementById("review-download-status");
        if (downloadStatus) downloadStatus.textContent = "active_threads updated. Download pulse-preview.reviewed.json before import.";
      });
    }
    for (const button of document.querySelectorAll("[data-review-action]")) {
      button.addEventListener("click", () => {
        const card = button.closest("[data-review-card]");
        if (!card) return;
        const status = card.querySelector("[data-review-status]");
        const item = card.getAttribute("data-review-item") || "";
        const result = button.getAttribute("data-review-result") || button.getAttribute("data-review-action") || "reviewed";
        const action = button.getAttribute("data-review-action") || "reviewed";
        const kind = button.getAttribute("data-review-kind") || "unknown";
        if (item) {
          reviewDecisions[item] = {
            action,
            result,
            kind,
            privacy_tier: "private",
            reviewed_at: new Date().toISOString()
          };
        }
        card.classList.add("reviewed");
        card.classList.toggle("ignored", result === "ignored");
        if (status) {
          status.textContent = "Review decision staged for reviewed JSON: " + result + ". Download before import.";
        }
        const downloadStatus = document.getElementById("review-download-status");
        if (downloadStatus) downloadStatus.textContent = "review_decisions updated. Download pulse-preview.reviewed.json before import.";
        updateReviewCounter();
      });
    }
    const reviewedDownload = document.getElementById("download-reviewed-json");
    if (reviewedDownload) reviewedDownload.addEventListener("click", downloadReviewedJSON);
    updateReviewCounter();
    function applyPreviewFilter() {
      const input = document.getElementById("preview-filter");
      if (!input) return;
      const query = input.value.trim().toLowerCase();
      let shown = 0;
      let total = 0;
      for (const item of document.querySelectorAll(".filter-item")) {
        total += 1;
        const text = (item.getAttribute("data-search") || item.textContent || "").toLowerCase();
        const match = !query || text.includes(query);
        item.hidden = !match;
        if (match) shown += 1;
      }
      const status = document.getElementById("preview-filter-status");
      if (status) {
        status.textContent = query ? "Showing " + shown + " of " + total + " preview items for " + JSON.stringify(query) + "." : "Showing all preview candidates.";
      }
    }
    const previewFilter = document.getElementById("preview-filter");
    if (previewFilter) {
      previewFilter.addEventListener("input", applyPreviewFilter);
    }
    const previewFilterClear = document.getElementById("preview-filter-clear");
    if (previewFilterClear) {
      previewFilterClear.addEventListener("click", () => {
        document.getElementById("preview-filter").value = "";
        applyPreviewFilter();
      });
    }
  </script>
</body>
</html>
`;
}

function renderMigrationConciergeHTML() {
  const generatedAt = new Date().toISOString();
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pulse Archive Concierge</title>
  <style>
    :root { color-scheme: light; --bg:#fbf7f4; --ink:#463b3d; --muted:#8f8180; --line:rgba(136,116,112,.14); --panel:rgba(255,255,255,.54); --rail:rgba(255,255,255,.34); --rail-muted:#9a8c89; --accent:#d99a8f; --accent-soft:#f7cbc2; --good:#8bb79f; --warn:#d6a95f; --soft:rgba(255,255,255,.46); --pastel-a:#f8d9d0; --pastel-b:#dceaf7; --pastel-c:#e7e0fa; --pastel-d:#e4f2df; }
    * { box-sizing: border-box; }
    body { margin:0; min-height:100vh; font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; color:var(--ink); background:
      radial-gradient(circle at 8% 8%, rgba(248,217,208,.92), transparent 32%),
      radial-gradient(circle at 82% 12%, rgba(220,234,247,.86), transparent 30%),
      radial-gradient(circle at 68% 72%, rgba(231,224,250,.72), transparent 36%),
      linear-gradient(145deg,#fffaf7 0%,#f6fbff 48%,#fff7fb 100%); }
    .workbench { min-height:100vh; display:grid; grid-template-columns:260px minmax(0,1fr); }
    .rail { position:sticky; top:0; height:100vh; padding:26px 22px; background:var(--rail); color:var(--ink); display:flex; flex-direction:column; gap:26px; border-right:1px solid rgba(255,255,255,.55); backdrop-filter:blur(24px) saturate(1.35); -webkit-backdrop-filter:blur(24px) saturate(1.35); }
    .brand { display:grid; gap:8px; }
    .brand b { font-size:22px; letter-spacing:0; font-weight:680; color:#5a4d51; }
    .brand span, .rail small { color:var(--rail-muted); line-height:1.45; }
    .rail-nav { display:grid; gap:8px; }
    .rail-nav a, .rail-pill { color:var(--ink); text-decoration:none; border:1px solid rgba(255,255,255,.58); border-radius:14px; padding:10px 12px; background:rgba(255,255,255,.42); box-shadow:0 10px 26px rgba(115,92,84,.08); backdrop-filter:blur(16px); -webkit-backdrop-filter:blur(16px); }
    .rail-pill { color:var(--rail-muted); }
    .workspace { min-width:0; padding:34px clamp(18px,4vw,54px) 54px; }
    header { display:grid; grid-template-columns:minmax(0,1fr) minmax(190px,280px); gap:24px; align-items:end; padding-bottom:24px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:46px; line-height:1.08; letter-spacing:0; font-weight:680; color:#4a3f42; max-width:760px; }
    h2 { margin: 0 0 12px; font-size:18px; letter-spacing:0; }
    p { margin: 0; }
    .subhead { margin:12px 0 0; color:var(--muted); font-size:16px; line-height:1.5; max-width:760px; }
    .glass-card, .stamp, section, .path-step { background:var(--panel); border:1px solid rgba(255,255,255,.62); border-radius:16px; box-shadow:0 18px 50px rgba(125,102,94,.12), inset 0 1px 0 rgba(255,255,255,.74); backdrop-filter:blur(22px) saturate(1.24); -webkit-backdrop-filter:blur(22px) saturate(1.24); }
    .stamp { padding:18px; }
    .stamp strong { display:block; font-size:22px; letter-spacing:0; font-weight:680; line-height:1; }
    .stamp span { display:block; margin-top:8px; color:var(--muted); font-size:13px; line-height:1.35; }
    .grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap:12px; margin-top:16px; }
    .path {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 12px;
      margin-top:16px;
    }
    .path-step {
      padding:16px;
      min-height: 124px;
    }
    .path-step b {
      display: inline-grid;
      place-items: center;
      width: 28px;
      height: 28px;
      border-radius: 999px;
      background: linear-gradient(135deg,var(--pastel-a),var(--pastel-c));
      color:#5f4039;
      margin-bottom: 10px;
    }
    .path-step strong {
      display: block;
      margin-bottom: 6px;
      font-size: 17px;
    }
    .path-step span {
      color:var(--muted);
      display:block;
      line-height:1.4;
    }
    section {
      padding: 18px;
      min-height: 230px;
    }
    .grid section { position:relative; overflow:hidden; }
    .grid section::before { content:""; position:absolute; inset:0 auto 0 0; width:4px; background:linear-gradient(180deg,var(--pastel-a),var(--pastel-c)); }
    .grid section:nth-child(3)::before, .grid section:nth-child(4)::before { background:var(--good); }
    .action {
      display: inline-flex;
      align-items: center;
      justify-content:center;
      min-height: 38px;
      padding:9px 13px;
      margin: 12px 0 10px;
      border: 1px solid rgba(232,137,119,.42);
      border-radius:999px;
      color:#5f4039;
      text-decoration: none;
      background:linear-gradient(135deg,rgba(255,255,255,.86),rgba(248,217,208,.76));
      box-shadow:0 10px 24px rgba(160,126,118,.11);
    }
    ol, ul { margin: 10px 0 0; padding-left: 20px; }
    li { margin: 7px 0; }
    code {
      display: block;
      padding:11px 12px;
      border:1px solid rgba(255,255,255,.62);
      border-radius:12px;
      background:rgba(255,255,255,.46);
      color:#4b5558;
      overflow-wrap: anywhere;
      font-family:ui-monospace,SFMono-Regular,Menlo,monospace;
      font-size:12px;
      line-height:1.45;
    }
    .command {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 8px;
      align-items: start;
      margin-top: 10px;
    }
    .copy {
      min-height:40px;
      padding:9px 13px;
      border:1px solid rgba(232,137,119,.42);
      border-radius:999px;
      background:linear-gradient(135deg,rgba(255,255,255,.86),rgba(248,217,208,.76));
      color:#5f4039;
      box-shadow:0 10px 24px rgba(160,126,118,.11);
      cursor: pointer;
      font: inherit;
      white-space:nowrap;
    }
    .copy:hover, .action:hover { transform:translateY(-1px); }
    .copy:focus-visible, .action:focus-visible {
      outline:3px solid rgba(232,137,119,.32);
      outline-offset: 2px;
    }
    .trust {
      margin-top: 14px;
      border-color:rgba(232,137,119,.32);
      background:rgba(255,244,239,.62);
      color:#5f4039;
      min-height:auto;
    }
    @media (max-width: 900px) {
      .workbench { grid-template-columns:1fr; }
      .rail { position:relative; height:auto; }
      header, .grid, .path, .command { grid-template-columns:1fr; }
      h1 { font-size:34px; line-height:1.08; }
    }
  </style>
</head>
<body data-design="pulse-glass-v3">
  <main class="workbench">
    <aside class="rail" aria-label="Pulse archive navigation">
      <div class="brand">
        <b>Pulse Import</b>
        <span>Archive migration without blind import.</span>
      </div>
      <nav class="rail-nav">
        <a href="#archive-request">Archive request</a>
        <a href="#host-pages">Host pages</a>
        <a href="#local-sources">Local sources</a>
        <a href="#import-gate">Import gate</a>
      </nav>
      <div class="rail-pill">Human click first. Graph approval last.</div>
      <small>Pulse opens the right places. You request the archives.</small>
    </aside>
    <div class="workspace">
    <header id="archive-request">
      <div>
        <h1>Pulse Archive Concierge</h1>
        <p class="subhead">Ask each host for an archive, preview it locally, then import only the structured graph that you approve.</p>
      </div>
      <div class="stamp">
        <strong>Local review</strong>
        <span>Generated ${htmlEscape(generatedAt)}</span>
      </div>
    </header>

    <div class="path" aria-label="Pulse archive migration path">
      <div class="path-step"><b>1</b><strong>Open the host page</strong><span>Pulse can open ChatGPT Data Controls or Claude Privacy for you.</span></div>
      <div class="path-step"><b>2</b><strong>Human click only</strong><span>You click Request archive / Export data. Pulse never bypasses consent.</span></div>
      <div class="path-step"><b>3</b><strong>Pulse waits for the zip</strong><span>Run request and keep working; Pulse previews the archive when it lands.</span></div>
      <div class="path-step"><b>4</b><strong>Review gate</strong><span>Inspect threads, decisions, open loops, people found, and review items before import.</span></div>
    </div>

    <div class="grid" id="host-pages">
      <section>
        <h2>ChatGPT</h2>
        <p>Open Data Controls, click Request archive, then let Pulse wait for the export zip in Downloads.</p>
        <a class="action" href="https://chatgpt.com/#settings/DataControls" target="_blank" rel="noreferrer">Open ChatGPT Data Controls</a>
        <ol>
          <li>Click Request archive.</li>
          <li>Download the archive into Downloads.</li>
          <li>Pulse previews it before importing.</li>
        </ol>
        <div class="command"><code>pulse migrate request chatgpt --open</code><button class="copy" data-copy="pulse migrate request chatgpt --open">Copy command</button></div>
      </section>

      <section>
        <h2>Claude</h2>
        <p>Open Privacy settings, click Export data, then let Pulse wait for the export zip in Downloads.</p>
        <a class="action" href="https://claude.ai/settings/privacy" target="_blank" rel="noreferrer">Open Claude Privacy</a>
        <ol>
          <li>Click Export data.</li>
          <li>Download the archive into Downloads.</li>
          <li>Pulse previews it before importing.</li>
        </ol>
        <div class="command"><code>pulse migrate request claude --open</code><button class="copy" data-copy="pulse migrate request claude --open">Copy command</button></div>
      </section>

      <section id="local-sources">
        <h2>Codex local history</h2>
        <p>Codex sessions are already local on this Mac. Pulse can preview them immediately.</p>
        <div class="command"><code>pulse migrate request codex --open</code><button class="copy" data-copy="pulse migrate request codex --open">Copy command</button></div>
      </section>

      <section>
        <h2>Claude Code local history</h2>
        <p>Claude Code project sessions are already local on this Mac. Pulse can preview them immediately.</p>
        <div class="command"><code>pulse migrate request claude-code --open</code><button class="copy" data-copy="pulse migrate request claude-code --open">Copy command</button></div>
      </section>
    </div>

    <section class="trust" id="import-gate">
      <h2>Import gate</h2>
      <p>Raw chat text is not written by preview. Import still needs an explicit graph confirmation.</p>
      <div class="command"><code>pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open</code><button class="copy" data-copy='pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open'>Copy command</button></div>
      <div class="command"><code>pulse viewer --thread-id archive-import --open</code><button class="copy" data-copy="pulse viewer --thread-id archive-import --open">Copy command</button></div>
    </section>
    <section>
      <h2>Paper boundary</h2>
      <p>This migration/viewer flow is a product extension, not an evaluated paper result. It belongs in Future Work or productization notes until migration quality and privacy have their own evaluation.</p>
    </section>
    </div>
  </main>
  <script>
    async function copyCommand(button) {
      const command = button.getAttribute("data-copy") || "";
      try {
        await navigator.clipboard.writeText(command);
        button.textContent = "Copied";
      } catch {
        const textarea = document.createElement("textarea");
        textarea.value = command;
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
        button.textContent = "Copied";
      }
      setTimeout(() => { button.textContent = "Copy command"; }, 1400);
    }
    for (const button of document.querySelectorAll("[data-copy]")) {
      button.addEventListener("click", () => copyCommand(button));
    }
  </script>
</body>
</html>
`;
}

function renderMigrationConciergeBrief() {
  return `# Pulse Archive Migration Hand-Hold

This handoff is for importing existing AI chat history into Pulse without writing raw chat text into Pulse memory.

## What the user does

1. ChatGPT: open Data Controls and click Request archive.
2. Claude: open Privacy settings and click Export data.
3. Codex: preview local history from ~/.codex/sessions.
4. Claude Code: preview local history from ~/.claude/projects.

## Human path

Pulse opens the right page when possible. The human clicks the archive request
button. Pulse waits for the zip, builds a local preview, and imports only after
the explicit graph confirmation.

## Commands

\`\`\`bash
pulse migrate request chatgpt --open
pulse migrate request claude --open
pulse migrate request codex --open
pulse migrate request claude-code --open
pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open
pulse viewer --thread-id archive-import --open
\`\`\`

## What Pulse shows

The preview and viewer are meant to make imported continuity inspectable: candidate threads, decisions, open loops, do-not-repeat notes, emotional anchors, people found, and review cards.

## Trust boundary

Preview is local and offline. Raw chat text is not written by preview. Commit requires the exact confirmation phrase and writes structured graph deltas, not a full transcript.

## Paper impact

This is a product-facing migration and inspection layer, not a new paper result. It should stay outside the evaluated retrieval claims until it has its own migration-quality and privacy evaluation. Repository note: docs/archive-migration-paper-boundary.md.
`;
}

function openExternalURL(url) {
  if (process.env.PULSE_OPEN_DRY_RUN === '1') {
    console.log(`[pulse] opened browser: ${url}`);
    return;
  }
	const { opener, openerArgs } = browserOpenCommand(url);
	const result = spawnSync(opener, openerArgs, { stdio: 'ignore' });
	if (result.status !== 0) {
		throw new Error(`could not open browser automatically; open this URL manually: ${url}`);
	}
}

function browserOpenCommand(url) {
	if (process.platform === 'darwin') {
		return { opener: 'open', openerArgs: [url] };
	} else if (process.platform === 'win32') {
		return { opener: 'cmd', openerArgs: ['/c', 'start', '', url] };
	}
	return { opener: 'xdg-open', openerArgs: [url] };
}

async function loginTeam() {
	const profilePath = getArg('--profile');
	if (!profilePath) {
		throw new Error('pulse team login requires --profile <root-owned-json>');
	}
	await recoverBindingAuthority();
	const binding = resolveWorkspaceBinding({ cwd: getArg('--cwd') ?? process.cwd() });
	if (binding.mode !== 'team') {
		throw new Error('pulse team login requires a trusted Team workspace binding');
	}
	const profile = readTeamAuthProfile(resolve(profilePath));
	const outputPath = getArg('--out')
		? resolve(getArg('--out'))
		: join(
			DATA_DIR,
			'supervisor',
			'enrollment-requests',
			`${binding.commons.team_id}-${Date.now()}-${randomBytes(6).toString('hex')}.json`,
		);
	const noOpen = args.includes('--no-open');
	const result = await runTeamLogin({
		profile,
		binding,
		credentialStore: createOSCredentialStore(),
		outputPath,
		openAuthorizationURL: async (url) => {
			if (noOpen) {
				console.log(`[pulse] Open this exact sign-in URL in your browser:\n${url}`);
				return;
			}
			openExternalURL(url);
			console.log('[pulse] Opened the Team sign-in in your browser.');
		},
	});
	console.log(`[pulse] Team installation authenticated: ${result.enrollmentID}`);
	console.log(`[pulse] Public enrollment request: ${result.requestPath}`);
	console.log(`[pulse] Request digest: ${result.requestDigest}`);
	console.log('[pulse] Status: pending Owner approval. Remote Commons stays blocked until the exact public entry is installed in the protected gateway registry.');
}

async function teamStatus() {
	await recoverBindingAuthority();
	const binding = resolveWorkspaceBinding({ cwd: getArg('--cwd') ?? process.cwd() });
	const result = await inspectTeamInstallation(binding);
	setTeamStatusExitCode(result);
	if (args.includes('--json')) {
		console.log(JSON.stringify(result, null, 2));
		return;
	}
	console.log(`[pulse] Team Commons readiness: ${result.status}`);
	console.log(`[pulse] Team/store: ${result.team_id} / ${result.store_id}`);
	console.log(`[pulse] Membership: ${result.membership_role}; capabilities=${result.capabilities.join(',')}`);
	console.log(`[pulse] Projection: ${result.projection_state}; degraded=${result.degraded}; reasons=${result.degraded_reasons.join(',') || 'none'}.`);
	console.log('[pulse] Enrollment accepted; DPoP sender constraint verified; refresh is configured; fallback=false.');
	console.log('[pulse] Agent writes to Commons: disabled. Publication: exact-envelope human Airlock only.');
}

function requiredTeamOwnerArg(flag, message) {
	const value = getArg(flag);
	if (!value) throw new Error(message);
	return value;
}

function teamOwnerMutationFromArgs() {
	const surface = args[2];
	const action = args[3];
	if (surface === 'member' && action === 'create') {
		const message = 'pulse team owner member create requires --issuer, --subject, and --role';
		return {
			action: 'membership.create',
			issuer: requiredTeamOwnerArg('--issuer', message),
			subject: requiredTeamOwnerArg('--subject', message),
			role: requiredTeamOwnerArg('--role', message),
		};
	}
	if (surface === 'member' && action === 'revoke') {
		return {
			action: 'membership.revoke',
			target_id: requiredTeamOwnerArg(
				'--principal-id', 'pulse team owner member revoke requires --principal-id',
			),
		};
	}
	if (surface === 'binding' && action === 'create') {
		const message = 'pulse team owner binding create requires --issuer, --subject, and --client-id';
		return {
			action: 'agent_binding.create',
			issuer: requiredTeamOwnerArg('--issuer', message),
			subject: requiredTeamOwnerArg('--subject', message),
			client_id: requiredTeamOwnerArg('--client-id', message),
		};
	}
	if (surface === 'binding' && action === 'revoke') {
		return {
			action: 'agent_binding.revoke',
			target_id: requiredTeamOwnerArg(
				'--binding-id', 'pulse team owner binding revoke requires --binding-id',
			),
		};
	}
	if (surface === 'project' && action === 'create') {
		return {
			action: 'project.create',
			name: requiredTeamOwnerArg('--name', 'pulse team owner project create requires --name'),
		};
	}
	if (surface === 'project' && action === 'grant') {
		const message = 'pulse team owner project grant requires --project-id, --principal-id, and --access';
		return {
			action: 'project_grant.create',
			project_id: requiredTeamOwnerArg('--project-id', message),
			target_principal_id: requiredTeamOwnerArg('--principal-id', message),
			access_level: requiredTeamOwnerArg('--access', message),
		};
	}
	if (surface === 'project' && action === 'revoke-grant') {
		return {
			action: 'project_grant.revoke',
			target_id: requiredTeamOwnerArg(
				'--grant-id', 'pulse team owner project revoke-grant requires --grant-id',
			),
		};
	}
	throw new Error('pulse team owner supports: login | member create|revoke | binding create|revoke | project create|grant|revoke-grant');
}

async function teamOwnerLogin() {
	const profilePath = getArg('--profile');
	if (!profilePath) throw new Error('pulse team owner login requires --profile <root-owned-json>');
	await recoverBindingAuthority();
	const binding = resolveWorkspaceBinding({ cwd: getArg('--cwd') ?? process.cwd() });
	if (binding.mode !== 'team') throw new Error('pulse team owner login requires a trusted Team workspace binding');
	const profile = readTeamOwnerAuthProfile(resolve(profilePath));
	const outputPath = getArg('--out')
		? resolve(getArg('--out'))
		: join(
			DATA_DIR, 'supervisor', 'enrollment-requests',
			`${binding.commons.team_id}-owner-${Date.now()}-${randomBytes(6).toString('hex')}.json`,
		);
	const noOpen = args.includes('--no-open');
	const result = await runTeamOwnerLogin({
		profile,
		binding,
		credentialStore: createOSCredentialStore(),
		outputPath,
		openAuthorizationURL: async (url) => {
			if (noOpen) {
				console.log(`[pulse] Open this exact Owner step-up URL in your browser:\n${url}`);
				return;
			}
			openExternalURL(url);
			console.log('[pulse] Opened the Owner step-up in your browser.');
		},
	});
	if (args.includes('--json')) {
		console.log(JSON.stringify(result, null, 2));
		return;
	}
	console.log(`[pulse] Owner authentication: ${result.status}.`);
	console.log(`[pulse] Enrollment: ${result.enrollmentID}.`);
	if (result.requestDigest) {
		console.log(`[pulse] Public Owner enrollment request: ${result.requestPath}.`);
		console.log(`[pulse] Enrollment request digest: ${result.requestDigest}.`);
		console.log('[pulse] Owner API remains blocked until this separate Owner enrollment is accepted.');
	} else {
		console.log('[pulse] Owner authentication refreshed; enrollment acceptance is still verified only by the remote gateway.');
		console.log('[pulse] Each mutation performs its own action-bound browser step-up.');
	}
}

async function teamOwnerMutation() {
	const input = teamOwnerMutationFromArgs();
	const profilePath = getArg('--profile');
	if (!profilePath) throw new Error('pulse team owner mutations require --profile <root-owned-json>');
	await recoverBindingAuthority();
	const binding = resolveWorkspaceBinding({ cwd: getArg('--cwd') ?? process.cwd() });
	if (binding.mode !== 'team') throw new Error('pulse team owner requires a trusted Team workspace binding');
	const profile = readTeamOwnerAuthProfile(resolve(profilePath));
	if ((input.action === 'membership.create' || input.action === 'agent_binding.create') &&
		input.issuer !== profile.issuer) {
		throw new TeamOwnerError('request_issuer_mismatch');
	}
	const pending = buildTeamOwnerStepUp(binding, input);
	const noOpen = args.includes('--no-open');
	const credentialStore = createOSCredentialStore();
	const browserStepUp = await runTeamOwnerStepUp({
		profile,
		binding,
		credentialStore,
		operationNonce: pending.nonce,
		openAuthorizationURL: async (url) => {
			console.log(`[pulse] Owner approval: ${pending.operation.approval.action} ${pending.operation.approval.target_kind}/${pending.operation.approval.target_id}.`);
			console.log('[pulse] Exact Owner approval payload bound to this browser step-up:');
			console.log(pending.approvalText);
			if (noOpen) {
				console.log(`[pulse] Open this exact action-bound Owner URL in your browser:\n${url}`);
				return;
			}
			openExternalURL(url);
			console.log('[pulse] Opened the action-bound Owner step-up in your browser.');
		},
	});
	let receipt;
	try {
		receipt = await runTeamOwnerOperation(binding, input, {
			post: createTeamOwnerRemotePost(binding, { credentialStore }),
			stepUp: {
				idToken: browserStepUp.idToken,
				operationChallenge: pending.challenge,
				authorizationStartedAt: browserStepUp.authorizationStartedAt,
			},
		});
	} catch (error) {
		if (error instanceof TeamOwnerError && error.code === 'step_up_required') {
			throw new Error('Owner action-bound browser approval was rejected or expired; rerun the same Owner mutation');
		}
		throw error;
	}
	if (args.includes('--json')) {
		console.log(JSON.stringify(receipt, null, 2));
		return;
	}
	console.log(`[pulse] Owner action: ${receipt.action}; status=${receipt.status}.`);
	console.log(`[pulse] Target: ${receipt.target_kind}/${receipt.target_id}.`);
	if (receipt.principal_id) console.log(`[pulse] Principal: ${receipt.principal_id}.`);
	console.log(`[pulse] Receipt: audit=${receipt.audit_event_id}; auth_epoch=${receipt.auth_epoch}; fallback=false.`);
}

async function teamOwnerCommand() {
	if (args[2] === 'login') {
		await teamOwnerLogin();
		return;
	}
	await teamOwnerMutation();
}

function parsePulseDataDirFromCommand(command) {
  const text = String(command ?? '');
  const match = text.match(/(?:^|\s)-{1,2}data-dir(?:=|\s+)(?:"([^"]+)"|'([^']+)'|(\S+))/);
  return match?.[1] ?? match?.[2] ?? match?.[3] ?? '';
}

function isLoopbackBaseURL(baseURL) {
  try {
    const { hostname } = new URL(baseURL);
    return hostname === '127.0.0.1' || hostname === 'localhost' || hostname === '::1';
  } catch {
    return false;
  }
}

function detectRunningPulseDataDir(baseURL) {
  const injected = parsePulseDataDirFromCommand(process.env.PULSE_RUNNING_PULSE_COMMAND);
  if (injected) return injected;
  if (!isLoopbackBaseURL(baseURL)) return '';

  let port;
  try {
    const url = new URL(baseURL);
    port = url.port || (url.protocol === 'https:' ? '443' : '80');
  } catch {
    return '';
  }

  const lsofArgs = ['-nP', `-iTCP:${port}`, '-sTCP:LISTEN', '-t'];
  const lsofCandidates = ['lsof', '/usr/sbin/lsof'];
  let pid = '';
  for (const candidate of lsofCandidates) {
    const result = spawnSync(candidate, lsofArgs, { encoding: 'utf8', timeout: 500 });
    if (result.status === 0 && result.stdout.trim()) {
      pid = result.stdout.trim().split(/\s+/)[0];
      break;
    }
  }
  if (!pid) return '';

  const ps = spawnSync('ps', ['-p', pid, '-o', 'command='], { encoding: 'utf8', timeout: 500 });
  if (ps.status !== 0) return '';
  return parsePulseDataDirFromCommand(ps.stdout);
}

async function checkViewerAccess(baseURL, secret, threadId) {
  if (process.env.PULSE_VIEWER_SKIP_AUTH_CHECK === '1') {
    return { status: 0, text: '' };
  }
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 500);
  try {
    const dataURL = new URL(`${baseURL.replace(/\/$/, '')}/viewer/data`);
    dataURL.searchParams.set('key', secret);
    dataURL.searchParams.set('thread_id', threadId);
    const response = await fetch(dataURL, { method: 'GET', signal: controller.signal });
    return { status: response.status, text: await response.text() };
  } catch {
    return { status: 0, text: '' };
  } finally {
    clearTimeout(timer);
  }
}

function productActivationEvidenceForViewer() {
	let workspace;
	try { workspace = canonicalizeWorkspace(process.cwd()); } catch { /* Preview may run outside Git */ }
	if (workspace) {
		try {
			readCodexProductLocator({ codexHome: codexHomePath(), binding: { workspace } });
			return true;
		} catch (error) {
			const locatorPath = join(codexHomePath(), 'pulse', 'product-locators.json');
			if (existsSync(locatorPath) && !/missing for this workspace/.test(error.message)) throw error;
		}
	}
	const settings = safeReadJSON(resolve(process.cwd(), '.claude', 'settings.local.json'));
	return settings?.hooks && Object.values(settings.hooks).some((entries) =>
		Array.isArray(entries) && entries.some((entry) =>
			Array.isArray(entry?.hooks) && entry.hooks.some((handler) => isPulseProductHookCommand(handler?.command))));
}

const HOME_SESSION_RESPONSE_MAX_BYTES = 16 * 1024;
const HOME_SESSION_MAX_AGE_SECONDS = 60 * 60;
const HOME_REQUEST_TIMEOUT_MS = 90_000;
const HOME_REQUEST_TIMEOUT_MAX_MS = 120_000;
const HOME_HANDOFF_TIMEOUT_MS = 60_000;

function boundedHomeTimeout(name, fallback, maximum) {
	return Math.min(positiveEnvInt(name, fallback), maximum);
}

function homeSessionCookie(session) {
	return `${session.cookie_name}=${session.cookie_value}; Max-Age=${session.max_age_seconds}; Path=${session.cookie_path}; HttpOnly; SameSite=Strict`;
}

function canonicalHomeRoutePath(value) {
	const match = /^\/home\/s\/([A-Za-z0-9_-]{43})\/$/.exec(value);
	if (!match) return false;
	try {
		const decoded = Buffer.from(match[1], 'base64url');
		return decoded.length === 32 && decoded.toString('base64url') === match[1];
	} catch {
		return false;
	}
}

function validateHomeSessionResponse(value, baseURL) {
	if (!value || typeof value !== 'object' || Array.isArray(value)) {
		throw new Error('Pulse daemon returned an invalid Memory Home session.');
	}
	const cookieName = String(value.cookie_name ?? '');
	const cookieValue = String(value.cookie_value ?? '');
	const cookiePath = String(value.cookie_path ?? '');
	const maxAgeSeconds = value.max_age_seconds;
	if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(cookieName)) {
		throw new Error('Pulse daemon returned an invalid Memory Home session.');
	}
	if (!/^[\x21\x23-\x2B\x2D-\x3A\x3C-\x5B\x5D-\x7E]+$/.test(cookieValue)) {
		throw new Error('Pulse daemon returned an invalid Memory Home session.');
	}
	if (!canonicalHomeRoutePath(cookiePath) || !Number.isInteger(maxAgeSeconds) ||
		maxAgeSeconds < 1 || maxAgeSeconds > HOME_SESSION_MAX_AGE_SECONDS) {
		throw new Error('Pulse daemon returned an invalid Memory Home session.');
	}

	let base;
	let target;
	try {
		base = requireLoopbackPulseIPC(baseURL);
		target = new URL(String(value.target_url ?? ''));
	} catch {
		throw new Error('Pulse daemon returned an invalid Memory Home target.');
	}
	const basePort = base.port || (base.protocol === 'https:' ? '443' : '80');
	const targetPort = target.port || (target.protocol === 'https:' ? '443' : '80');
	if (target.protocol !== base.protocol || target.hostname !== '127.0.0.1' ||
		targetPort !== basePort || target.pathname !== cookiePath || !canonicalHomeRoutePath(target.pathname) ||
		target.search || target.hash || target.username || target.password ||
		target.href !== `${base.protocol}//${base.host}${cookiePath}`) {
		throw new Error('Pulse daemon must return the exact queryless scoped /home target.');
	}
	return {
		cookie_name: cookieName,
		cookie_value: cookieValue,
		cookie_path: cookiePath,
		max_age_seconds: maxAgeSeconds,
		target_url: target.href,
	};
}

async function readBoundedHomeResponse(response) {
	if (!response.body || typeof response.body.getReader !== 'function') {
		throw new Error('home_session_response_invalid');
	}
	const reader = response.body.getReader();
	const chunks = [];
	let total = 0;
	try {
		while (true) {
			const { done, value } = await reader.read();
			if (done) break;
			total += value.byteLength;
			if (total > HOME_SESSION_RESPONSE_MAX_BYTES) {
				await reader.cancel();
				throw new Error('home_session_response_oversized');
			}
			chunks.push(Buffer.from(value));
		}
	} finally {
		reader.releaseLock();
	}
	return Buffer.concat(chunks, total).toString('utf8');
}

async function requestHomeSession(baseURL, secret, liveReadiness) {
	requireLoopbackPulseIPC(baseURL);
	const timeoutMs = boundedHomeTimeout(
		'PULSE_HOME_REQUEST_TIMEOUT_MS', HOME_REQUEST_TIMEOUT_MS, HOME_REQUEST_TIMEOUT_MAX_MS,
	);
	const controller = new AbortController();
	let timedOut = false;
	const timer = setTimeout(() => {
		timedOut = true;
		controller.abort();
	}, timeoutMs);
	let response;
	let responseText;
	try {
		response = await fetch(`${baseURL.replace(/\/$/, '')}/home/session`, {
			method: 'POST',
			headers: {
				...buildPulseRequestHeaders(baseURL, { ipcSecret: secret }),
				'Content-Type': 'application/json',
			},
			body: JSON.stringify({ live_readiness: liveReadiness }),
			signal: controller.signal,
		});
		if (!response.ok) {
			throw new Error('daemon_rejected_home_session');
		}
		responseText = await readBoundedHomeResponse(response);
	} catch (error) {
		if (timedOut) throw new Error('Memory Home session request timed out.');
		if (error?.message === 'daemon_rejected_home_session') {
			throw new Error('Pulse daemon rejected the Memory Home session request.');
		}
		if (error?.message === 'home_session_response_oversized') {
			throw new Error('Pulse daemon returned an oversized Memory Home session.');
		}
		if (error?.message === 'home_session_response_invalid') {
			throw new Error('Pulse daemon returned an invalid Memory Home session.');
		}
		throw new Error('Memory Home session request failed.');
	} finally {
		clearTimeout(timer);
	}
	let parsed;
	try {
		parsed = JSON.parse(responseText);
	} catch {
		throw new Error('Pulse daemon returned an invalid Memory Home session.');
	}
	return validateHomeSessionResponse(parsed, baseURL);
}

function writeHomeRelayError(res, status, message, allow = '') {
	res.statusCode = status;
	res.setHeader('Content-Type', 'text/plain; charset=utf-8');
	res.setHeader('Cache-Control', 'no-store');
	res.setHeader('Connection', 'close');
	if (allow) res.setHeader('Allow', allow);
	res.end(message);
}

function startHomeBrowserRelay(session) {
	const timeoutMs = boundedHomeTimeout(
		'PULSE_HOME_HANDOFF_TIMEOUT_MS', HOME_HANDOFF_TIMEOUT_MS, HOME_HANDOFF_TIMEOUT_MS,
	);
	let expectedHost = '';
	let completed = false;
	let timer;
	let resolveCompletion;
	let rejectCompletion;
	const completion = new Promise((resolve, reject) => {
		resolveCompletion = resolve;
		rejectCompletion = reject;
	});
	const failRelay = (error) => {
		if (completed) return;
		completed = true;
		clearTimeout(timer);
		if (server.listening) server.close();
		rejectCompletion(error);
	};
	const server = createServer((req, res) => {
		if (req.headers.host !== expectedHost) {
			writeHomeRelayError(res, 421, 'Misdirected request.');
			return;
		}
		if (req.method !== 'GET') {
			writeHomeRelayError(res, 405, 'Method not allowed.', 'GET');
			return;
		}
		if (req.url !== '/') {
			writeHomeRelayError(res, 404, 'Not found.');
			return;
		}
		const fetchMode = String(req.headers['sec-fetch-mode'] ?? '').toLowerCase();
		const fetchDestination = String(req.headers['sec-fetch-dest'] ?? 'document').toLowerCase();
		if (fetchMode !== 'navigate' || fetchDestination !== 'document') {
			writeHomeRelayError(res, 403, 'Navigation required.');
			return;
		}
		if (completed) {
			writeHomeRelayError(res, 410, 'Gone.');
			return;
		}
		completed = true;
		clearTimeout(timer);
		res.statusCode = 303;
		res.setHeader('Location', session.target_url);
		res.setHeader('Set-Cookie', homeSessionCookie(session));
		res.setHeader('Cache-Control', 'no-store');
		res.setHeader('Referrer-Policy', 'no-referrer');
		res.setHeader('Connection', 'close');
		server.close();
		res.end(() => resolveCompletion());
	});

	return new Promise((resolve, reject) => {
		server.once('error', reject);
		server.listen(0, '127.0.0.1', () => {
			server.removeListener('error', reject);
			server.on('error', (error) => {
				failRelay(new Error(`Memory Home browser handoff failed: ${error.code ?? 'listener_error'}.`));
			});
			const address = server.address();
			expectedHost = `127.0.0.1:${address.port}`;
			timer = setTimeout(() => {
				failRelay(new Error('Memory Home browser handoff timed out.'));
			}, timeoutMs);
			resolve({
				url: `http://${expectedHost}/`,
				completion,
				close: () => failRelay(new Error('Memory Home browser handoff interrupted.')),
			});
		});
	});
}

async function openHomeBrowserURL(url, session) {
	if (process.env.PULSE_OPEN_DRY_RUN === '1') {
		const navigate = () => new Promise((resolve, reject) => {
			const request = httpRequest(url, {
				method: 'GET',
				headers: { 'Sec-Fetch-Mode': 'navigate', Connection: 'close' },
			}, (response) => {
				response.resume();
				response.once('end', () => resolve(response));
			});
			request.once('error', reject);
			request.end();
		});
		const response = await navigate();
		if (response.statusCode !== 303 || response.headers.location !== session.target_url ||
			response.headers['set-cookie']?.[0] !== homeSessionCookie(session)) {
			throw new Error('Memory Home browser handoff failed.');
		}
		let replayed = false;
		try {
			const replay = await navigate();
			replayed = replay.statusCode === 303;
		} catch {
			// A closed listener is the expected one-shot result.
		}
		if (replayed) throw new Error('Memory Home browser handoff replayed unexpectedly.');
		return;
	}

	const { opener, openerArgs } = browserOpenCommand(url);
	const result = spawnSync(opener, openerArgs, { stdio: 'ignore' });
	if (result.status !== 0) {
		throw new Error('Could not open Memory Home automatically.');
	}
}

async function personalDoctorForHost(host) {
	if (host === 'claude-code') return claudeProductDoctorReport();
	if (host === 'codex') return codexDoctorReport();
	if (host === 'cursor') return cursorProductDoctorReport();
	throw new Error('pulse home --host must be claude-code, codex, or cursor.');
}

async function homeDoctorReport(product, requestedHost) {
	const capture = product ? safeReadJSON(join(product.runtime.data_dir, 'capture-state.json')) : undefined;
	const enabledHosts = product
		? SUPPORTED_HOST_IDS.filter((host) => captureEnabledForHost(capture, host))
		: ['codex'];
	return selectHomeDoctorReport({ requestedHost, enabledHosts, doctorForHost: personalDoctorForHost });
}

async function runHome(rest) {
	if (rest.includes('--print-url')) {
		throw new Error('pulse home does not support --print-url because its browser handoff is one-shot.');
	}
	const explicitDataDir = getRestArg(rest, '--data-dir');
	const explicitBaseURL = getRestArg(rest, '--base');
	const explicitHost = getRestArg(rest, '--host');
	if (rest.includes('--host') && explicitHost === undefined) {
		throw new Error('pulse home --host must be claude-code, codex, or cursor.');
	}
	let product;
	if (explicitDataDir === undefined && explicitBaseURL === undefined) {
		try {
			await recoverBindingAuthority();
			product = resolveCodexMcpRuntime(process.cwd());
		} catch (error) {
			if (productActivationEvidenceForViewer()) {
				throw new Error(`Pulse product activation exists, but its bound vault cannot be trusted: ${error.message}`);
			}
			// Local Preview remains available only when this workspace has no product activation evidence.
		}
	}
	const dataDir = resolve(explicitDataDir ?? product?.runtime.data_dir ?? DATA_DIR);
	const baseURL = (explicitBaseURL ?? product?.runtime.base_url ?? DEFAULT_BASE_URL).replace(/\/$/, '');
	const secret = readSecretFromDataDir(dataDir, { create: product === undefined });
	const doctor = await homeDoctorReport(product, explicitHost);
	const session = await requestHomeSession(baseURL, secret, doctor.personal_live_readiness);
	const relay = await startHomeBrowserRelay(session);
	const interrupt = () => relay.close();
	process.once('SIGINT', interrupt);
	process.once('SIGTERM', interrupt);
	try {
		await openHomeBrowserURL(relay.url, session);
		await relay.completion;
		console.log('[pulse] Memory Home opened.');
	} catch (error) {
		relay.close();
		await relay.completion.catch(() => {});
		throw error;
	} finally {
		process.removeListener('SIGINT', interrupt);
		process.removeListener('SIGTERM', interrupt);
		relay.close();
	}
}

async function runViewer(rest) {
	const explicitDataDir = getRestArg(rest, '--data-dir');
	const explicitBaseURL = getRestArg(rest, '--base');
	let product;
	if (explicitDataDir === undefined && explicitBaseURL === undefined) {
		try {
			await recoverBindingAuthority();
			product = resolveCodexMcpRuntime(process.cwd());
		} catch (error) {
			if (productActivationEvidenceForViewer()) {
				throw new Error(`Pulse product activation exists, but its bound vault cannot be trusted: ${error.message}`);
			}
			// Local Preview remains available only when this workspace has no product activation evidence.
		}
	}
	const dataDir = resolve(explicitDataDir ?? product?.runtime.data_dir ?? DATA_DIR);
	const baseURL = (explicitBaseURL ?? product?.runtime.base_url ?? DEFAULT_BASE_URL).replace(/\/$/, '');
	const secret = readSecretFromDataDir(dataDir, { create: product === undefined });
	const ctx = localThreadContext();
	const threadId = safeThreadID(getRestArg(rest, '--thread-id') ??
		product?.binding.workspace.repository_id ?? ctx.threadId);
  const url = `${baseURL}/viewer?key=${encodeURIComponent(secret)}&thread_id=${encodeURIComponent(threadId)}`;

  if (rest.includes('--print-url')) {
    console.log(url);
    return;
  }

  const access = await checkViewerAccess(baseURL, secret, threadId);
  if (access.status === 401 || access.status === 403) {
    console.log(`[pulse] viewer auth check failed for data dir: ${dataDir}`);
    console.log('[pulse] The running Pulse daemon is using a different local secret.');
    const detectedDataDir = detectRunningPulseDataDir(baseURL);
    if (detectedDataDir && resolve(detectedDataDir) !== dataDir) {
      console.log(`[pulse] Detected Pulse daemon data dir: ${detectedDataDir}`);
      console.log('[pulse] Try:');
      console.log(`  pulse viewer --base ${shellArg(baseURL)} --data-dir ${shellArg(detectedDataDir)} --thread-id ${shellArg(threadId)} --open`);
    } else {
      console.log('[pulse] Start the daemon and viewer with the same --data-dir, or pass --data-dir from the running daemon.');
    }
    return;
  }

  if (rest.includes('--open')) {
    openExternalURL(url);
  }
  console.log(`[pulse] local viewer: ${url}`);
  console.log(`[pulse] data dir: ${dataDir}`);
  console.log(`[pulse] thread id: ${threadId}`);
	console.log('[pulse] Shows the bounded continuity pack for the next connected harness session.');
}

function safeThreadID(value) {
  const thread = String(value ?? '').trim();
  if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$/.test(thread)) {
    throw new Error('thread id must start with a letter/number and contain only letters, numbers, dot, underscore, colon, or dash');
  }
  return thread;
}

function shellArg(value) {
  if (/^[A-Za-z0-9_./:-]+$/.test(value)) {
    return value;
  }
  return `'${String(value).replace(/'/g, `'\\''`)}'`;
}

function viewerNextStepCommand(threadId = localThreadContext().threadId) {
  const baseURL = DEFAULT_BASE_URL.replace(/\/$/, '');
  const dataDir = process.env.PULSE_VIEWER_SKIP_AUTH_CHECK === '1'
    ? DATA_DIR
    : (detectRunningPulseDataDir(baseURL) || DATA_DIR);
  return `pulse viewer --base ${shellArg(baseURL)} --data-dir ${shellArg(dataDir)} --thread-id ${shellArg(safeThreadID(threadId))} --open`;
}

function remoteArchiveInfo(source) {
  if (source === 'chatgpt') {
    return {
      source,
      label: 'ChatGPT',
      url: 'https://chatgpt.com/#settings/DataControls',
      buttonText: 'Request archive',
    };
  }
  if (source === 'claude') {
    return {
      source,
      label: 'Claude',
      url: 'https://claude.ai/settings/privacy',
      buttonText: 'Export data',
    };
  }
  throw new Error('pulse migrate request supports: chatgpt, claude, codex, claude-code');
}

function localHistoryInfo(source) {
  if (source === 'codex') {
    return {
      source,
      label: 'Codex',
      path: join(homedir(), '.codex', 'sessions'),
      displayPath: '~/.codex/sessions',
    };
  }
  if (source === 'claude-code') {
    return {
      source,
      label: 'Claude Code',
      path: join(homedir(), '.claude', 'projects'),
      displayPath: '~/.claude/projects',
    };
  }
  return null;
}

function printRemoteArchiveGuide(source, label, url, buttonText) {
  console.log(`[pulse] ${label} archive handoff
────────────────────────────────

Browser:
  ${url}

What to do:
  1. Open the page above.
  2. Sign in if the host asks.
  3. Click "${buttonText}".
  4. Keep Pulse waiting for the zip:
     pulse migrate request ${source} --open
  5. If the zip is already downloaded:
     pulse migrate preview-latest ${source} --html pulse-preview.html --out pulse-preview.json --open
  6. Manual path:
     pulse migrate preview <${source}-export.zip-or-folder> --html pulse-preview.html --out pulse-preview.json --open

Want Pulse to open the page for you:
  pulse migrate guide ${source} --open

Privacy:
  preview shows counts and safe candidates only.
  raw chat text is not written by preview.
`);
}

function printLocalHistoryGuide(source, label, path) {
  console.log(`[pulse] ${label} local history handoff
─────────────────────────────────────

Local history:
  ${path}

What to do:
  1. Keep this local. No archive request is needed.
  2. Run:
     pulse migrate request ${source} --open
  3. Review people/thread candidates before any future commit.

Manual path:
  pulse migrate preview ${path} --html pulse-preview.html --out pulse-preview.json --open

Privacy:
  preview shows counts and safe candidates only.
  raw prompt or transcript text is not written by preview.
`);
}

function migrationGuide(source) {
  if (source === 'chatgpt') {
    const info = remoteArchiveInfo(source);
    if (args.includes('--open')) {
      openExternalURL(info.url);
    }
    printRemoteArchiveGuide(info.source, info.label, info.url, info.buttonText);
    return;
  }
  if (source === 'claude') {
    const info = remoteArchiveInfo(source);
    if (args.includes('--open')) {
      openExternalURL(info.url);
    }
    printRemoteArchiveGuide(info.source, info.label, info.url, info.buttonText);
    return;
  }
  if (source === 'codex') {
    const info = localHistoryInfo(source);
    printLocalHistoryGuide(info.source, info.label, info.displayPath);
    return;
  }
  if (source === 'claude-code') {
    const info = localHistoryInfo(source);
    printLocalHistoryGuide(info.source, info.label, info.displayPath);
    return;
  }
  throw new Error('pulse migrate guide supports: chatgpt, claude, codex, claude-code');
}

function runMigrationConcierge(rest) {
  const htmlPath = resolve(restArg(rest, '--html') ?? 'pulse-migrate-concierge.html');
  writeFileSync(htmlPath, renderMigrationConciergeHTML(), { mode: 0o600 });
  const briefArg = restArg(rest, '--brief');
  if (briefArg) {
    const briefPath = resolve(briefArg);
    writeFileSync(briefPath, renderMigrationConciergeBrief(), { mode: 0o600 });
    console.log(`[pulse] migration brief: ${briefPath}`);
  }
  if (rest.includes('--open')) {
    openExternalURL(htmlPath);
  }
  console.log(`[pulse] migration concierge: ${htmlPath}`);
  console.log('[pulse] Open it in a browser, request archives, then run request/wait-latest/preview before commit.');
}

function renderMigrationStatus(status) {
  const lines = [
    '# Pulse Import Status',
    '',
    `Generated: ${new Date().toISOString()}`,
    `Output directory: ${status.outDir}`,
    `Downloads directory: ${resolve(status.downloadsDir)}`,
    '',
    '## Open First',
    '',
    `- Concierge: ${status.conciergeHTML}`,
    `- Brief: ${status.conciergeBrief}`,
    ...(status.galleryHTML ? [`- Memory gallery: ${status.galleryHTML}`] : []),
    '',
    '## Remote Archives',
    '',
    `- ChatGPT archive: ${status.chatgpt}`,
    `- Claude archive: ${status.claude}`,
    '',
    '## Local Previews',
    '',
    ...(status.peopleGraph ? [`- People Graph preview: ${status.peopleGraph}`] : []),
    `- Codex preview: ${status.codex}`,
    `- Claude Code preview: ${status.claudeCode}`,
    '',
    '## Next Commands',
    '',
    '```bash',
    `pulse migrate wait-latest chatgpt --downloads ${shellArg(resolve(status.downloadsDir))} --html ${shellArg(join(status.outDir, 'pulse-chatgpt-preview.html'))} --out ${shellArg(join(status.outDir, 'pulse-chatgpt-preview.json'))} --open`,
    `pulse migrate wait-latest claude --downloads ${shellArg(resolve(status.downloadsDir))} --html ${shellArg(join(status.outDir, 'pulse-claude-preview.html'))} --out ${shellArg(join(status.outDir, 'pulse-claude-preview.json'))} --open`,
    '```',
    '',
    'Nothing has been imported yet. Review preview pages first, then use the explicit commit command shown inside the preview.',
    '',
  ];
  return `${lines.join('\n')}\n`;
}

function statusBadge(value) {
  const text = String(value ?? '');
  if (/preview ready|ready:/i.test(text)) return 'ready';
  if (/not ready|waiting/i.test(text)) return 'waiting';
  return 'idle';
}

function statusTitle(value) {
  const text = String(value ?? '');
  if (/preview ready/i.test(text)) return 'Preview ready';
  if (/ready:/i.test(text)) return 'Ready';
  if (/not ready/i.test(text)) return 'Not ready yet';
  if (/waiting for Request archive/i.test(text)) return 'Waiting for Request archive';
  if (/waiting for Export data/i.test(text)) return 'Waiting for Export data';
  return 'Pending';
}

function previewPathFromStatus(value) {
  const text = String(value ?? '');
  const match = text.match(/(?:ready|preview ready):\s+(.+)$/i);
  return match?.[1] ?? '';
}

function previewJsonPathFromHTML(previewPath) {
  if (!previewPath || !/\.html$/i.test(previewPath)) return '';
  return previewPath.replace(/\.html$/i, '.json');
}

function safeArrayLength(value) {
  return Array.isArray(value) ? value.length : 0;
}

function readPreviewGraphSummary(previewPath) {
  const jsonPath = previewJsonPathFromHTML(previewPath);
  if (!jsonPath || !existsSync(jsonPath)) return null;
  try {
    const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
    return {
      conversations: Number.isFinite(preview.conversations) ? preview.conversations : 0,
      messages: Number.isFinite(preview.messages) ? preview.messages : 0,
      people: safeArrayLength(preview.people_candidates),
      memories: safeArrayLength(preview.memory_candidates),
      emotions: safeArrayLength(preview.emotion_candidates),
      relationships: safeArrayLength(preview.relationship_candidates),
      funFacts: safeArrayLength(preview.fun_fact_candidates),
    };
  } catch {
    return null;
  }
}

function renderPreviewGraphSummary(summary) {
  if (!summary) return '';
  const metrics = [
    [Math.max(1, summary.memories), 'threads'],
    [summary.memories, 'decisions'],
    [summary.relationships, 'open loops'],
    [summary.emotions, 'emotional anchors'],
    [summary.people, 'people found'],
  ];
  return `<div class="graph-summary" aria-label="Continuity summary">
    <strong>Continuity summary</strong>
    <div class="mini-metrics">${metrics.map(([value, label]) => `<span aria-label="${htmlEscape(value)} ${htmlEscape(label)}"><b>${htmlEscape(value)}</b> ${htmlEscape(label)}</span>`).join('')}</div>
    <small>${htmlEscape(summary.conversations)} conversations, ${htmlEscape(summary.messages)} messages scanned</small>
  </div>`;
}

function uniqueLimited(items, limit = 48) {
  const out = [];
  const seen = new Set();
  for (const item of Array.isArray(items) ? items : []) {
    const text = String(item ?? '').trim();
    if (!text || seen.has(text)) continue;
    seen.add(text);
    out.push(text);
    if (out.length >= limit) break;
  }
  return out;
}

function readyPreviewEntries(status) {
  return [
    ['People Graph', status.peopleGraph],
    ['ChatGPT', status.chatgpt],
    ['Claude', status.claude],
    ['Codex', status.codex],
    ['Claude Code', status.claudeCode],
  ]
    .map(([label, value]) => ({ label, htmlPath: previewPathFromStatus(value), jsonPath: previewJsonPathFromHTML(previewPathFromStatus(value)) }))
    .filter((entry) => entry.htmlPath && entry.jsonPath && existsSync(entry.jsonPath));
}

function readPreviewJSON(jsonPath) {
  try {
    return JSON.parse(readFileSync(jsonPath, 'utf8'));
  } catch {
    return null;
  }
}

function buildGalleryPreview(entries) {
  const previews = entries
    .map((entry) => ({ ...entry, preview: readPreviewJSON(entry.jsonPath) }))
    .filter((entry) => entry.preview);
  const combined = {
    source: 'combined',
    conversations: 0,
    messages: 0,
    people_candidates: [],
    review_candidates: [],
    person_profiles: [],
    memory_candidates: [],
    emotion_candidates: [],
    relationship_candidates: [],
    fun_fact_candidates: [],
    redacted_fragments: 0,
  };
  for (const entry of previews) {
    const preview = entry.preview;
    combined.conversations += Number.isFinite(preview.conversations) ? preview.conversations : 0;
    combined.messages += Number.isFinite(preview.messages) ? preview.messages : 0;
    combined.redacted_fragments += Number.isFinite(preview.redacted_fragments) ? preview.redacted_fragments : 0;
    combined.people_candidates.push(...(Array.isArray(preview.people_candidates) ? preview.people_candidates : []));
    combined.review_candidates.push(...(Array.isArray(preview.review_candidates) ? preview.review_candidates : []));
    combined.person_profiles.push(...(Array.isArray(preview.person_profiles) ? preview.person_profiles : []));
    combined.memory_candidates.push(...(Array.isArray(preview.memory_candidates) ? preview.memory_candidates.map((item) => `${entry.label}: ${item}`) : []));
    combined.emotion_candidates.push(...(Array.isArray(preview.emotion_candidates) ? preview.emotion_candidates : []));
    combined.relationship_candidates.push(...(Array.isArray(preview.relationship_candidates) ? preview.relationship_candidates.map((item) => `${entry.label}: ${item}`) : []));
    combined.fun_fact_candidates.push(...(Array.isArray(preview.fun_fact_candidates) ? preview.fun_fact_candidates.map((item) => `${entry.label}: ${item}`) : []));
  }
  combined.people_candidates = uniqueLimited(combined.people_candidates, 72);
  combined.review_candidates = uniqueLimited(combined.review_candidates, 72);
  combined.person_profiles = combined.person_profiles.filter((profile, index, all) => (
    profile?.name && all.findIndex((candidate) => candidate?.name === profile.name) === index
  )).slice(0, 48);
  combined.memory_candidates = uniqueLimited(combined.memory_candidates, 72);
  combined.emotion_candidates = uniqueLimited(combined.emotion_candidates, 32);
  combined.relationship_candidates = uniqueLimited(combined.relationship_candidates, 72);
  combined.fun_fact_candidates = uniqueLimited(combined.fun_fact_candidates, 72);
  return { combined, previews };
}

const GALLERY_REVIEW_ENTITIES = new Set([
  'Benchmark',
  'Accents',
  'Avoid',
  'Background',
  'Cartographer',
  'Chat',
  'Cinema',
  'Conversation',
  'Conversations',
  'Before',
  'Built',
  'Choose',
  'Consulting',
  'Current',
  'Data',
  'Declare',
  'Density',
  'Direction',
  'Dossier',
  'Emo',
  'Empathic',
  'Explore',
  'Export',
  'Fonts',
  'Foreground',
  'Google',
  'Guidance',
  'Harness',
  'Heavy',
  'Let',
  'Memory',
  'Newsreader',
  'Only',
  'Progression',
  'Grok',
  'Kimi',
  'Qwen',
  'Questions',
  'Tide',
  'Paper',
  'Process',
  'Real',
  'Total',
  'Two',
  'Three',
  'Step',
  'Files',
  'Port',
  'Pasted',
  'Running',
  'Worker',
  'Bitwarden',
  'Created',
  'History',
  'Primary',
  'Privacy',
  'Source',
  'Terminal',
  'Ticker',
  'Tweaks',
  'Verifier',
  'Hearth',
  'Автор',
  'Вместо',
  'Вообще',
  'Курс',
  'Лекция',
  'Плюс',
  'Пхукет',
  'Сквозная',
  'Тайланд',
  'Тематика',
  'Аудитория',
]);

const KNOWN_SINGLE_PERSON_NAMES = new Set([
  'Anya',
  'Daria',
  'Dan',
  'Drew',
  'Egor',
  'Elle',
  'Elena',
  'Ilya',
  'Katya',
  'Maria',
  'Mila',
  'Nik',
  'Nikita',
  'Paul',
  'Pavel',
  'Sonya',
  'Vitaly',
  'Анжелика',
  'Аня',
  'Виталий',
  'Вова',
  'Граф',
  'Дан',
  'Даша',
  'Ева',
  'Егор',
  'Игорь',
  'Катя',
  'Лада',
  'Лея',
  'Мама',
  'Мария',
  'Мика',
  'Мила',
  'Ник',
  'Никита',
  'Павел',
  'Папа',
  'Сема',
  'Соня',
  'Федя',
  'Элли',
]);

function isReviewEntityName(name) {
  return GALLERY_REVIEW_ENTITIES.has(String(name ?? '').trim());
}

function stripGallerySourcePrefix(value) {
  return String(value ?? '').replace(/^[A-Z][A-Za-z ]{1,24}:\s+/, '');
}

function formatMentionedRelationship(person, topic) {
  return `${safeText(person, 120)} - mentioned in - ${safeText(topic, 160)}`;
}

function formatRelatedRelationship(left, right) {
  return `${safeText(left, 120)} - related to - ${safeText(right, 120)}`;
}

function parseRelationshipCandidate(candidate) {
  const text = stripGallerySourcePrefix(candidate);
  let match = text.match(/^(.+?)\s+-\s+mentioned in\s+-\s+(.+)$/i);
  if (match) {
    return {
      kind: 'mentioned_in',
      left: safeText(match[1].trim(), 120),
      right: safeText(match[2].trim(), 160),
    };
  }
  match = text.match(/^(.+?)\s+-\s+related to\s+-\s+(.+)$/i);
  if (match) {
    return {
      kind: 'related_to',
      left: safeText(match[1].trim(), 120),
      right: safeText(match[2].trim(), 120),
    };
  }
  if (text.includes('<->')) {
    const [left, right] = text.split('<->').map((part) => safeText(part.trim(), 120));
    return { kind: 'related_to', left, right };
  }
  if (text.includes('->')) {
    const [left, right] = text.split('->').map((part) => safeText(part.trim(), 120));
    return { kind: 'mentioned_in', left, right };
  }
  return null;
}

function normalizeRelationshipForPreview(candidate, canonicalByName = new Map()) {
  const parsed = parseRelationshipCandidate(candidate);
  if (!parsed) {
    return '';
  }
  const left = canonicalByName.get(parsed.left) ?? parsed.left;
  const right = canonicalByName.get(parsed.right) ?? parsed.right;
  if (isSamePersonAlias(left, right)) {
    return '';
  }
  if (parsed.kind === 'related_to') {
    return formatRelatedRelationship(left, right);
  }
  if (parsed.kind === 'mentioned_in') {
    return formatMentionedRelationship(left, right);
  }
  return '';
}

function relationshipTouchesReviewEntity(item) {
  const parsed = parseRelationshipCandidate(item);
  if (!parsed) {
    return false;
  }
  return isReviewEntityName(parsed.left) || isReviewEntityName(parsed.right);
}

function funFactTouchesReviewEntity(item) {
  const text = stripGallerySourcePrefix(item);
  const subject = text.split(' appeared in ')[0];
  return isReviewEntityName(subject);
}

function isLikelyPreviewPersonCandidate(name, count, totalMessages) {
  const trimmed = String(name ?? '').trim();
  if (!trimmed || isReviewEntityName(trimmed)) {
    return false;
  }
  if (!looksLikePreviewPersonName(trimmed)) {
    return false;
  }
  if (trimmed.includes('-')) {
    return false;
  }
  if (totalMessages > 12 && count < 2) {
    return false;
  }
  return true;
}

function looksLikePreviewPersonName(name) {
  const trimmed = safeText(name, 120);
  if (!trimmed || isTechnicalRef(trimmed)) {
    return false;
  }
  if (KNOWN_SINGLE_PERSON_NAMES.has(trimmed)) {
    return true;
  }
  const words = trimmed
    .split(/\s+/)
    .map((word) => word.replace(/[(),]/g, '').trim())
    .filter(Boolean);
  if (words.length >= 2 && words.length <= 4) {
    return words.every((word) => (
      KNOWN_SINGLE_PERSON_NAMES.has(word) ||
      /^[A-Z][a-z]{2,}$/.test(word) ||
      /^[А-ЯЁ][а-яё]{2,}$/.test(word)
    ));
  }
  return false;
}

function shouldShowReviewCandidate(name, count, totalMessages) {
  const trimmed = String(name ?? '').trim();
  if (!trimmed) {
    return false;
  }
  if (isReviewEntityName(trimmed)) {
    return true;
  }
  if (!looksLikePreviewPersonName(trimmed)) {
    return false;
  }
  if (totalMessages <= 12) {
    return false;
  }
  return count > 1;
}

function relationshipAllowedForPreview(candidate, peopleSet, threadSet) {
  if (relationshipTouchesReviewEntity(candidate)) {
    return false;
  }
  const parsed = parseRelationshipCandidate(candidate);
  if (!parsed) {
    return false;
  }
  if (isSamePersonAlias(parsed.left, parsed.right)) {
    return false;
  }
  if (parsed.kind === 'related_to') {
    return peopleSet.has(parsed.left) && peopleSet.has(parsed.right);
  }
  if (parsed.kind === 'mentioned_in') {
    return peopleSet.has(parsed.left) && !isReviewEntityName(parsed.right) && (peopleSet.has(parsed.right) || threadSet.has(parsed.right));
  }
  return false;
}

function splitGalleryPersonCandidates(people) {
  const confident = [];
  const review = [];
  for (const name of uniqueLimited(people, 96)) {
    if (isReviewEntityName(name)) {
      review.push(name);
    } else {
      confident.push(name);
    }
  }
  return { confident, review };
}

function renderGallerySourceCards(previews) {
  if (previews.length === 0) {
    return '<p class="empty">No preview sources are ready yet.</p>';
  }
  return `<div class="source-grid">${previews.map(({ label, htmlPath, preview }) => {
    const sourceTitle = preview.source_kind === 'curated_people_graph' ? 'Curated people graph' : label;
    const sourceSubtitle = preview.source_kind === 'curated_people_graph' ? 'Real people graph' : label;
    return `<article class="source-card filter-item" data-search="${htmlEscape(label)} ${htmlEscape(sourceTitle)} ${htmlEscape((preview.people_candidates ?? []).join(' '))}">
    <div class="source-label">${htmlEscape(label)}</div>
    <strong>${htmlEscape(sourceTitle)}</strong>
    <span>${htmlEscape(sourceSubtitle)} · ${htmlEscape(preview.people_candidates?.length ?? 0)} people candidates</span>
    <span>${htmlEscape(preview.memory_candidates?.length ?? 0)} decisions · ${htmlEscape(preview.emotion_candidates?.length ?? 0)} emotional anchors · ${htmlEscape(preview.relationship_candidates?.length ?? 0)} open loops</span>
    <a class="button" href="${htmlEscape(htmlPath)}">Open source preview</a>
  </article>`;
  }).join('')}</div>`;
}

function renderMemoryGalleryHTML(status) {
  const generatedAt = new Date().toISOString();
  const { combined, previews } = buildGalleryPreview(readyPreviewEntries(status));
  const galleryPeople = splitGalleryPersonCandidates(combined.people_candidates);
  const galleryReviewCandidates = uniqueLimited([
    ...galleryPeople.review,
    ...(Array.isArray(combined.review_candidates) ? combined.review_candidates : []),
  ], 96);
  const visibleRelationships = combined.relationship_candidates.filter((item) => !relationshipTouchesReviewEntity(item));
  const visibleFunFacts = combined.fun_fact_candidates.filter((item) => !funFactTouchesReviewEntity(item));
  const profilePreview = {
    ...combined,
    people_candidates: galleryPeople.confident,
    relationship_candidates: visibleRelationships,
    fun_fact_candidates: visibleFunFacts,
  };
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pulse Thread Gallery</title>
  <style>
    :root { color-scheme: light; --bg:#fbf7f4; --ink:#463b3d; --muted:#8f8180; --line:rgba(136,116,112,.14); --panel:rgba(255,255,255,.54); --rail:rgba(255,255,255,.34); --rail-muted:#9a8c89; --accent:#d99a8f; --accent-soft:#f7cbc2; --good:#8bb79f; --soft:rgba(255,255,255,.46); --pastel-a:#f8d9d0; --pastel-b:#dceaf7; --pastel-c:#e7e0fa; --pastel-d:#e4f2df; }
    * { box-sizing:border-box; }
    body { margin:0; min-height:100vh; font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; color:var(--ink); background:
      radial-gradient(circle at 8% 8%, rgba(248,217,208,.92), transparent 32%),
      radial-gradient(circle at 82% 12%, rgba(220,234,247,.86), transparent 30%),
      radial-gradient(circle at 68% 72%, rgba(231,224,250,.72), transparent 36%),
      linear-gradient(145deg,#fffaf7 0%,#f6fbff 48%,#fff7fb 100%); }
    .workbench { min-height:100vh; display:grid; grid-template-columns:260px minmax(0,1fr); }
    .rail { position:sticky; top:0; height:100vh; padding:26px 22px; background:var(--rail); color:var(--ink); display:flex; flex-direction:column; gap:26px; border-right:1px solid rgba(255,255,255,.55); backdrop-filter:blur(24px) saturate(1.35); -webkit-backdrop-filter:blur(24px) saturate(1.35); }
    .brand { display:grid; gap:8px; }
    .brand b { font-size:22px; letter-spacing:0; font-weight:680; color:#5a4d51; }
    .brand span, .rail small { color:var(--rail-muted); line-height:1.45; }
    .rail-nav { display:grid; gap:8px; }
    .rail-nav a, .rail-pill { color:var(--ink); text-decoration:none; border:1px solid rgba(255,255,255,.58); border-radius:14px; padding:10px 12px; background:rgba(255,255,255,.42); box-shadow:0 10px 26px rgba(115,92,84,.08); backdrop-filter:blur(16px); -webkit-backdrop-filter:blur(16px); }
    .rail-pill { color:var(--rail-muted); }
    .workspace { min-width:0; padding:34px clamp(18px,4vw,54px) 54px; }
    header { display:grid; grid-template-columns:minmax(0,1fr) minmax(180px,260px); gap:24px; align-items:end; padding-bottom:24px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:46px; line-height:1.08; letter-spacing:0; font-weight:680; color:#4a3f42; max-width:800px; }
    h2 { margin:0 0 12px; font-size:18px; letter-spacing:0; }
    .subhead { margin:14px 0 0; max-width:760px; color:var(--muted); font-size:16px; line-height:1.5; }
    .glass-card, .stamp, section, .source-card, .person-card, .metric { background:var(--panel); border:1px solid rgba(255,255,255,.62); border-radius:16px; box-shadow:0 18px 50px rgba(125,102,94,.12), inset 0 1px 0 rgba(255,255,255,.74); backdrop-filter:blur(22px) saturate(1.24); -webkit-backdrop-filter:blur(22px) saturate(1.24); }
    .stamp { padding:18px; }
    .stamp strong { display:block; font-size:36px; letter-spacing:0; line-height:1.05; }
    .stamp span { display:block; margin-top:8px; color:var(--muted); font-size:13px; line-height:1.35; }
    .metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(138px,1fr)); gap:12px; margin:18px 0; }
    .metric { min-height:96px; padding:16px; position:relative; overflow:hidden; }
    .metric::before { content:""; position:absolute; inset:0 auto 0 0; width:4px; background:linear-gradient(180deg,var(--pastel-a),var(--pastel-c)); }
    .metric b { display:block; font-size:30px; line-height:1.05; letter-spacing:0; font-weight:680; }
    .metric span { display:block; margin-top:8px; color:var(--muted); font-size:13px; }
    section { padding:18px; margin-top:16px; }
    .source-grid, .profile-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(250px,1fr)); gap:12px; }
    .source-card { padding:16px; }
    .source-label { color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:.12em; margin-bottom:8px; }
    .source-card strong { display:block; font-size:22px; letter-spacing:0; font-weight:680; }
    .source-card span { display:block; margin-top:6px; color:var(--muted); line-height:1.4; }
    .button, button { display:inline-flex; align-items:center; justify-content:center; min-height:40px; margin-top:12px; padding:9px 13px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.58)); color:#6d5653; text-decoration:none; cursor:pointer; font:inherit; white-space:nowrap; box-shadow:0 10px 24px rgba(160,126,118,.11); }
    .button:hover, button:hover { transform:translateY(-1px); }
    .filter-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:10px; }
    input { min-height:42px; width:100%; border:1px solid rgba(255,255,255,.68); border-radius:999px; padding:9px 14px; font:inherit; background:rgba(255,255,255,.62); color:var(--ink); backdrop-filter:blur(14px); -webkit-backdrop-filter:blur(14px); }
    #gallery-filter-status { margin-top:10px; color:var(--muted); font-size:14px; }
    .person-card { padding:16px; border-top:1px solid rgba(217,154,143,.36); }
    .person-head { display:flex; justify-content:space-between; gap:10px; align-items:baseline; border-bottom:1px solid var(--line); padding-bottom:10px; margin-bottom:12px; }
    .person-head h3 { margin:0; font-size:22px; letter-spacing:0; font-weight:680; }
    .person-head span { color:var(--muted); font-size:13px; white-space:nowrap; }
    .mini-block + .mini-block { margin-top:13px; }
    .mini-block h4 { margin:0 0 7px; color:var(--muted); font-size:12px; letter-spacing:.12em; text-transform:uppercase; }
    .grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; }
    .wide { grid-column:span 2; }
    ul { list-style:none; padding:0; margin:0; display:grid; gap:8px; }
    li { border:1px solid rgba(255,255,255,.62); padding:10px 11px; background:rgba(255,255,255,.46); border-radius:12px; line-height:1.35; overflow-wrap:anywhere; }
    .empty { color:var(--muted); }
    .trust { border-color:rgba(217,154,143,.24); background:rgba(255,246,242,.56); color:#6d5653; }
    .graph-stage { background:linear-gradient(145deg,rgba(255,255,255,.64),rgba(232,240,251,.48)); color:var(--ink); border-color:rgba(255,255,255,.72); }
    .graph-stage .subhead, .graph-stage .empty { color:var(--muted); }
    .graph-stage .grid section { background:rgba(255,255,255,.44); border-color:rgba(255,255,255,.62); color:var(--ink); box-shadow:inset 0 1px 0 rgba(255,255,255,.72); }
    .graph-stage li { background:rgba(255,255,255,.42); border-color:rgba(255,255,255,.62); color:var(--ink); }
    .graph-stage ul { max-height:380px; overflow:auto; padding-right:4px; }
    #review-queue ul { max-height:260px; overflow:auto; padding-right:4px; }
    @media (max-width:900px) { .workbench { grid-template-columns:1fr; } .rail { position:relative; height:auto; } header, .grid, .filter-row { grid-template-columns:1fr; } .wide { grid-column:auto; } h1 { font-size:34px; line-height:1.08; } }
  </style>
</head>
<body data-design="pulse-glass-v3">
  <main class="workbench">
    <aside class="rail" aria-label="Pulse memory navigation">
      <div class="brand">
        <b>Pulse Import</b>
        <span>Thread gallery before continuity import.</span>
      </div>
      <nav class="rail-nav">
        <a href="#memory-graph">Thread map</a>
        <a href="#person-profiles">People found</a>
        <a href="#review-queue">Needs your decision</a>
        <a href="#sources">Sources</a>
      </nav>
      <div class="rail-pill">Review first. Import later.</div>
      <small>No raw transcript is written by this preview.</small>
    </aside>
    <div class="workspace">
    <header>
      <div>
        <h1>Pulse Thread Gallery</h1>
        <p class="subhead">What Pulse can turn into continuity after review. Start with threads, decisions, open loops, and what will be injected next.</p>
      </div>
      <div class="stamp">
        <strong>${htmlEscape(previews.length)}</strong>
        <span>ready sources · Generated ${htmlEscape(generatedAt)}</span>
      </div>
    </header>
    <div class="metrics" aria-label="Pulse memory gallery metrics">
      <div class="metric"><b>${htmlEscape(previews.length)}</b><span>sources ready</span></div>
      <div class="metric"><b>${htmlEscape(Math.max(1, combined.memory_candidates.length))}</b><span>threads</span></div>
      <div class="metric"><b>${htmlEscape(combined.memory_candidates.length)}</b><span>decisions</span></div>
      <div class="metric"><b>${htmlEscape(visibleRelationships.length)}</b><span>open loops</span></div>
      <div class="metric"><b>${htmlEscape(combined.emotion_candidates.length)}</b><span>emotional anchors</span></div>
      <div class="metric"><b>${htmlEscape(galleryPeople.confident.length)}</b><span>people found</span></div>
      <div class="metric"><b>${htmlEscape(galleryReviewCandidates.length)}</b><span>needs decision</span></div>
    </div>
    <section id="sources">
      <h2>Sources</h2>
      ${renderGallerySourceCards(previews)}
    </section>
    <section>
      <h2>Filter gallery</h2>
      <div class="filter-row">
        <input id="gallery-filter" placeholder="Type a thread, person, decision, or review item">
        <button id="gallery-filter-clear">Clear</button>
      </div>
      <div id="gallery-filter-status">Showing all gallery candidates.</div>
    </section>
    <section id="person-profiles">
      <h2>People found in reviewed sources</h2>
      ${renderPreviewPersonCards(profilePreview)}
    </section>
    <section id="review-queue">
      <h2>Needs your decision</h2>
      <p class="empty"><strong>Review before import:</strong> these may be models, tools, projects, or ambiguous entities. Pulse keeps them out of people/context memory until you decide.</p>
      ${previewList(galleryReviewCandidates, 'No ambiguous people candidates yet.')}
    </section>
    <section class="graph-stage" id="memory-graph">
      <h2>Thread map</h2>
      <p class="subhead">What Pulse understood across ready sources, expressed as continuity objects instead of parser categories.</p>
      <div class="grid">
        <section><h2>Threads</h2>${previewList(combined.memory_candidates.map(previewThreadTitle))}</section>
        <section class="wide"><h2>Decisions</h2>${previewList(combined.memory_candidates.map(continuityPreviewText))}</section>
        <section><h2>Open loops</h2>${previewList(visibleRelationships.map(continuityPreviewText), 'No open loops yet.')}</section>
        <section><h2>Emotional anchors</h2>${previewList(combined.emotion_candidates)}</section>
        <section><h2>People found</h2>${previewList(galleryPeople.confident)}</section>
      </div>
    </section>
    <section class="trust">
      <h2>Nothing imported yet</h2>
      <p>This gallery is local preview only. Pulse writes structured continuity only after you open a source preview and run its explicit import command.</p>
    </section>
    </div>
  </main>
  <script>
    const filter = document.getElementById("gallery-filter");
    const clear = document.getElementById("gallery-filter-clear");
    const status = document.getElementById("gallery-filter-status");
    const items = Array.from(document.querySelectorAll(".filter-item"));
    function applyFilter() {
      const query = filter.value.trim().toLowerCase();
      let shown = 0;
      for (const item of items) {
        const text = (item.getAttribute("data-search") || item.textContent || "").toLowerCase();
        const visible = !query || text.includes(query);
        item.hidden = !visible;
        if (visible) shown += 1;
      }
      status.textContent = query ? "Showing " + shown + " matching gallery candidates." : "Showing all gallery candidates.";
    }
    filter.addEventListener("input", applyFilter);
    clear.addEventListener("click", () => { filter.value = ""; applyFilter(); filter.focus(); });
  </script>
</body>
</html>
`;
}

function renderStatusCard(title, value) {
  const kind = statusBadge(value);
  const previewPath = previewPathFromStatus(value);
  const graphSummary = renderPreviewGraphSummary(readPreviewGraphSummary(previewPath));
  const previewLink = previewPath
    ? `<a class="button" href="${htmlEscape(previewPath)}">Open preview</a>`
    : '';
  return `<article class="status-card ${kind}">
    <div class="status-label">${htmlEscape(title)}</div>
    <h2>${htmlEscape(statusTitle(value))}</h2>
    <p>${htmlEscape(value)}</p>
    ${graphSummary}
    ${previewLink}
  </article>`;
}

function renderMigrationStatusHTML(status) {
  const chatgptCommand = `pulse migrate wait-latest chatgpt --downloads ${shellArg(resolve(status.downloadsDir))} --html ${shellArg(join(status.outDir, 'pulse-chatgpt-preview.html'))} --out ${shellArg(join(status.outDir, 'pulse-chatgpt-preview.json'))} --open`;
  const claudeCommand = `pulse migrate wait-latest claude --downloads ${shellArg(resolve(status.downloadsDir))} --html ${shellArg(join(status.outDir, 'pulse-claude-preview.html'))} --out ${shellArg(join(status.outDir, 'pulse-claude-preview.json'))} --open`;
  const liveWaitCommand = `pulse migrate start --dir ${shellArg(resolve(status.outDir))} --downloads ${shellArg(resolve(status.downloadsDir))} --open --watch`;
  const refreshMeta = status.autoRefresh ? '  <meta http-equiv="refresh" content="5">\n' : '';
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
${refreshMeta}  <meta name="referrer" content="no-referrer">
  <title>Pulse Import Status</title>
  <style>
    :root { color-scheme: light; --bg:#fbf7f4; --ink:#463b3d; --muted:#8f8180; --line:rgba(136,116,112,.14); --panel:rgba(255,255,255,.54); --rail:rgba(255,255,255,.34); --rail-muted:#9a8c89; --accent:#d99a8f; --accent-soft:#f7cbc2; --good:#8bb79f; --warn:#d6a95f; --soft:rgba(255,255,255,.46); --pastel-a:#f8d9d0; --pastel-b:#dceaf7; --pastel-c:#e7e0fa; --pastel-d:#e4f2df; }
    * { box-sizing: border-box; }
    body { margin:0; min-height:100vh; font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color:var(--ink); background:
      radial-gradient(circle at 8% 8%, rgba(248,217,208,.92), transparent 32%),
      radial-gradient(circle at 82% 12%, rgba(220,234,247,.86), transparent 30%),
      radial-gradient(circle at 68% 72%, rgba(231,224,250,.72), transparent 36%),
      linear-gradient(145deg,#fffaf7 0%,#f6fbff 48%,#fff7fb 100%); }
    .workbench { min-height:100vh; display:grid; grid-template-columns:260px minmax(0,1fr); }
    .rail { position:sticky; top:0; height:100vh; padding:26px 22px; background:var(--rail); color:var(--ink); display:flex; flex-direction:column; gap:26px; border-right:1px solid rgba(255,255,255,.55); backdrop-filter:blur(24px) saturate(1.35); -webkit-backdrop-filter:blur(24px) saturate(1.35); }
    .brand { display:grid; gap:8px; }
    .brand b { font-size:22px; letter-spacing:0; font-weight:680; color:#5a4d51; }
    .brand span, .rail small { color:var(--rail-muted); line-height:1.45; }
    .rail-nav { display:grid; gap:8px; }
    .rail-nav a, .rail-pill { color:var(--ink); text-decoration:none; border:1px solid rgba(255,255,255,.58); border-radius:14px; padding:10px 12px; background:rgba(255,255,255,.42); box-shadow:0 10px 26px rgba(115,92,84,.08); backdrop-filter:blur(16px); -webkit-backdrop-filter:blur(16px); }
    .rail-pill { color:var(--rail-muted); }
    .workspace { min-width:0; padding:34px clamp(18px,4vw,54px) 54px; }
    header { display:grid; grid-template-columns:minmax(0,1fr) minmax(220px,340px); gap:24px; align-items:end; padding-bottom:24px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:46px; line-height:1.08; letter-spacing:0; font-weight:680; color:#4a3f42; max-width:800px; }
    h2 { margin:0 0 12px; font-size:18px; letter-spacing:0; }
    p { margin:0; line-height:1.48; }
    .meta { color:var(--muted); line-height:1.45; }
    .grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(230px,1fr)); gap:12px; margin:16px 0; }
    .glass-card, section, .status-card { background:var(--panel); border:1px solid rgba(255,255,255,.62); border-radius:16px; padding:18px; box-shadow:0 18px 50px rgba(125,102,94,.12), inset 0 1px 0 rgba(255,255,255,.74); backdrop-filter:blur(22px) saturate(1.24); -webkit-backdrop-filter:blur(22px) saturate(1.24); }
    section { margin-top:16px; }
    .status-card { min-height:182px; position:relative; overflow:hidden; }
    .status-card::before { content:""; position:absolute; inset:0 auto 0 0; width:4px; background:linear-gradient(180deg,var(--pastel-a),var(--pastel-c)); }
    .status-card.ready::before { background:var(--good); }
    .status-card.waiting::before { background:var(--warn); }
    .status-label { color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:.12em; }
    .button, button { display:inline-flex; align-items:center; justify-content:center; min-height:40px; margin-top:12px; padding:9px 13px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.58)); color:#6d5653; text-decoration:none; cursor:pointer; font:inherit; white-space:nowrap; box-shadow:0 10px 24px rgba(160,126,118,.11); }
    .button:hover, button:hover { transform:translateY(-1px); }
    .graph-summary { margin-top:12px; padding:12px; border:1px solid var(--line); border-radius:14px; background:var(--soft); }
    .graph-summary strong, .graph-summary small { display:block; }
    .graph-summary small { margin-top:8px; color:var(--muted); }
    .mini-metrics { display:flex; flex-wrap:wrap; gap:6px; margin-top:8px; }
    .mini-metrics span { display:inline-flex; gap:4px; align-items:baseline; padding:5px 8px; border-radius:999px; background:rgba(255,255,255,.56); border:1px solid rgba(255,255,255,.62); font-size:13px; }
    .now { border-color:rgba(255,255,255,.72); background:linear-gradient(145deg,rgba(255,255,255,.66),rgba(232,240,251,.5)); color:var(--ink); }
    .now p { color:var(--muted); max-width:760px; }
    .now-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; margin-top:16px; }
    .now-step { border:1px solid rgba(255,255,255,.62); border-radius:16px; padding:16px; background:rgba(255,255,255,.42); box-shadow:inset 0 1px 0 rgba(255,255,255,.72); }
    .now-step strong { display:block; font-size:17px; margin-bottom:6px; }
    .now-step span { color:var(--muted); display:block; line-height:1.4; }
    .now .button, .now button { border-color:rgba(232,137,119,.42); background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(248,217,208,.72)); color:#5f4039; }
    .command { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:8px; align-items:start; margin-top:10px; }
    .now-step .command { grid-template-columns:1fr; }
    .now-step .command .button, .now-step .command button { justify-self:start; }
    code { display:block; padding:11px 12px; border:1px solid rgba(255,255,255,.62); border-radius:12px; background:rgba(255,255,255,.46); color:#4b5558; overflow-wrap:anywhere; font-family:ui-monospace, SFMono-Regular, Menlo, monospace; font-size:12px; line-height:1.45; }
    .now code { border-color:rgba(255,255,255,.62); background:rgba(255,255,255,.46); color:#4b5558; }
    .trust { border-color:rgba(217,154,143,.24); background:rgba(255,246,242,.56); color:#6d5653; }
    ul { margin:0; padding-left:18px; }
    li + li { margin-top:6px; }
    @media (max-width:900px) { .workbench { grid-template-columns:1fr; } .rail { position:relative; height:auto; } header, .now-grid, .command { grid-template-columns:1fr; } h1 { font-size:34px; line-height:1.08; } }
  </style>
</head>
<body data-design="pulse-glass-v3">
  <main class="workbench">
    <aside class="rail" aria-label="Pulse migration navigation">
      <div class="brand">
        <b>Pulse Import</b>
        <span>Archive migration without blind import.</span>
      </div>
      <nav class="rail-nav">
        <a href="#archive-request">Archive request</a>
        <a href="#local-sources">Local sources</a>
        <a href="${htmlEscape(status.galleryHTML ?? '#')}">Thread gallery</a>
      </nav>
      <div class="rail-pill">Nothing imported until graph approval.</div>
      <small>Local preview only. Raw chat text is not written by this page.</small>
    </aside>
    <div class="workspace">
    <header>
      <div>
        <h1>Pulse Import Status</h1>
        <p class="meta">One place to see what is ready, what still needs a human click, and what Pulse will import only after review.</p>
      </div>
      <div class="meta">Generated ${htmlEscape(new Date().toISOString())}<br>Output: ${htmlEscape(status.outDir)}</div>
    </header>
    <section class="now" id="archive-request">
      <h2>What to do right now</h2>
      <p>Click only these two host buttons if you want ChatGPT and Claude archives. Pulse has already prepared the local previews and will not import anything until you approve the graph.</p>
      <div class="now-grid">
        <div class="now-step">
          <strong>ChatGPT request page</strong>
          <span>Open Data Controls, then press Request archive inside ChatGPT.</span>
          <a class="button" href="https://chatgpt.com/#settings/DataControls" target="_blank" rel="noreferrer">Open ChatGPT Data Controls</a>
        </div>
        <div class="now-step">
          <strong>Claude export page</strong>
          <span>Open Privacy settings, then press Export data inside Claude.</span>
          <a class="button" href="https://claude.ai/settings/privacy" target="_blank" rel="noreferrer">Open Claude Privacy</a>
        </div>
        <div class="now-step">
          <strong>Ready to browse now</strong>
          <span>Codex and Claude Code are local, so their thread preview can be inspected immediately.</span>
          ${status.galleryHTML ? `<a class="button" href="${htmlEscape(status.galleryHTML)}">Preview thread gallery</a>` : ''}
        </div>
        <div class="now-step">
          <strong>After you clicked</strong>
          <span>When the zip appears in Downloads, Pulse will show candidate threads, decisions, open loops, and review items before import.</span>
          <div class="command"><code>${htmlEscape(liveWaitCommand)}</code><button class="copy" data-copy="${htmlEscape(liveWaitCommand)}">Start live waiting</button></div>
        </div>
      </div>
    </section>
    <section>
      <h2>Human click</h2>
      <ul>
        <li>ChatGPT: click Request archive in Data Controls.</li>
        <li>Claude: click Export data in Privacy settings.</li>
      </ul>
      <p>
        <a class="button" href="https://chatgpt.com/#settings/DataControls" target="_blank" rel="noreferrer">Open ChatGPT Data Controls</a>
        <a class="button" href="https://claude.ai/settings/privacy" target="_blank" rel="noreferrer">Open Claude Privacy</a>
      </p>
    </section>
    <div class="grid" id="local-sources">
      ${renderStatusCard('ChatGPT archive', status.chatgpt)}
      ${renderStatusCard('Claude archive', status.claude)}
      ${status.peopleGraph ? renderStatusCard('People Graph preview', status.peopleGraph) : ''}
      ${renderStatusCard('Codex preview', status.codex)}
      ${renderStatusCard('Claude Code preview', status.claudeCode)}
    </div>
    <section>
      <h2>Open first</h2>
      <p><a class="button" href="${htmlEscape(status.conciergeHTML)}">Open concierge</a> <a class="button" href="${htmlEscape(status.conciergeBrief)}">Open brief</a>${status.galleryHTML ? ` <a class="button" href="${htmlEscape(status.galleryHTML)}">Preview thread gallery</a>` : ''}</p>
    </section>
    <section>
      <h2>Wait for remote archives</h2>
      <div class="command"><code>${htmlEscape(chatgptCommand)}</code><button class="copy" data-copy="${htmlEscape(chatgptCommand)}">Copy command</button></div>
      <div class="command"><code>${htmlEscape(claudeCommand)}</code><button class="copy" data-copy="${htmlEscape(claudeCommand)}">Copy command</button></div>
    </section>
    <section class="trust">
      <h2>Nothing imported</h2>
      <p>Pulse has only built preview files. Review candidate threads, decisions, open loops, people found, and review cards before running the explicit commit command shown inside a preview.</p>
    </section>
    <section>
      <h2>Paper boundary</h2>
      <p>This migration/viewer flow is a product extension, not an evaluated paper result. It can be mentioned as Future Work or productization, but it does not expand the paper's evaluated retrieval claims.</p>
    </section>
    </div>
  </main>
  <script>
    async function copyCommand(button) {
      const command = button.getAttribute("data-copy") || "";
      try {
        await navigator.clipboard.writeText(command);
        button.textContent = "Copied";
      } catch {
        const textarea = document.createElement("textarea");
        textarea.value = command;
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
        button.textContent = "Copied";
      }
      setTimeout(() => { button.textContent = "Copy command"; }, 1400);
    }
    for (const button of document.querySelectorAll("[data-copy]")) {
      button.addEventListener("click", () => copyCommand(button));
    }
  </script>
</body>
</html>
`;
}

function writeMigrationStatus(statusPath, status) {
  if (status.galleryHTML) {
    writeFileSync(status.galleryHTML, renderMemoryGalleryHTML(status), { mode: 0o600 });
    console.log(`[pulse] memory gallery: ${status.galleryHTML}`);
  }
  writeFileSync(statusPath, renderMigrationStatus(status), { mode: 0o600 });
  if (status.statusHTML) {
    writeFileSync(status.statusHTML, renderMigrationStatusHTML(status), { mode: 0o600 });
    console.log(`[pulse] migration status page: ${status.statusHTML}`);
  }
  console.log(`[pulse] migration status: ${statusPath}`);
}

async function runMigrationStart(rest) {
  const outDir = resolve(restArg(rest, '--dir') ?? 'pulse-migrate');
  mkdirSync(outDir, { recursive: true, mode: 0o700 });
  const shouldOpen = rest.includes('--open');
  const openRest = shouldOpen ? ['--open'] : [];
  const downloadsDir = restArg(rest, '--downloads') ?? join(homedir(), 'Downloads');
  const peopleGraphPath = restArg(rest, '--people-graph');
  const timeoutMs = positiveIntArg(rest, '--timeout-ms', 15 * 60 * 1000);
  const intervalMs = positiveIntArg(rest, '--interval-ms', 2000);
  const conciergeHTML = join(outDir, 'pulse-migrate-concierge.html');
  const conciergeBrief = join(outDir, 'pulse-migrate-brief.md');
  const statusPath = join(outDir, 'pulse-migrate-status.md');
  const statusHTML = join(outDir, 'pulse-migrate-status.html');
  const galleryHTML = join(outDir, 'pulse-memory-gallery.html');
  const migrationStatus = {
    outDir,
    downloadsDir,
    autoRefresh: rest.includes('--watch'),
    conciergeHTML,
    conciergeBrief,
    statusHTML,
    galleryHTML,
    chatgpt: 'waiting for Request archive',
    claude: 'waiting for Export data',
    peopleGraph: '',
    codex: 'not found',
    claudeCode: 'not found',
  };

  runMigrationConcierge([
    '--html',
    conciergeHTML,
    '--brief',
    conciergeBrief,
    ...openRest,
  ]);

  for (const source of ['chatgpt', 'claude']) {
    const info = remoteArchiveInfo(source);
    if (shouldOpen) {
      openExternalURL(info.url);
    }
    console.log(`[pulse] ${info.label}: click "${info.buttonText}" in the opened browser page.`);
  }

  if (peopleGraphPath) {
    console.log('[pulse] People Graph local graph selected');
    console.log(`[pulse] Local graph: ${resolve(peopleGraphPath)}`);
    emitPeopleGraphPreview(peopleGraphPath, [
      '--html',
      join(outDir, 'pulse-people-graph-preview.html'),
      '--out',
      join(outDir, 'pulse-people-graph-preview.json'),
      ...openRest,
    ]);
    migrationStatus.peopleGraph = `ready: ${join(outDir, 'pulse-people-graph-preview.html')}`;
  }

  for (const source of ['codex', 'claude-code']) {
    const info = localHistoryInfo(source);
    if (!existsSync(info.path)) {
      console.log(`[pulse] ${info.label} local history not found at ${info.displayPath}; skipping local preview.`);
      continue;
    }
    requestLocalHistoryPreview(source, [
      '--html',
      join(outDir, `pulse-${source}-preview.html`),
      '--out',
      join(outDir, `pulse-${source}-preview.json`),
      ...openRest,
    ]);
    if (source === 'codex') {
      migrationStatus.codex = `ready: ${join(outDir, 'pulse-codex-preview.html')}`;
    } else {
      migrationStatus.claudeCode = `ready: ${join(outDir, 'pulse-claude-code-preview.html')}`;
    }
  }

  writeMigrationStatus(statusPath, migrationStatus);
  if (shouldOpen) {
    openExternalURL(statusHTML);
  }

  console.log(`
[pulse] Pulse archive migration started
──────────────────────────────────────

Open files:
  ${conciergeHTML}
  ${conciergeBrief}
  ${statusPath}
  ${statusHTML}
  ${galleryHTML}

Human click:
  ChatGPT: click "Request archive"
  Claude: click "Export data"

When the zips arrive:
  pulse migrate wait-latest chatgpt --downloads ${shellArg(resolve(downloadsDir))} --html ${shellArg(join(outDir, 'pulse-chatgpt-preview.html'))} --out ${shellArg(join(outDir, 'pulse-chatgpt-preview.json'))} --open
  pulse migrate wait-latest claude --downloads ${shellArg(resolve(downloadsDir))} --html ${shellArg(join(outDir, 'pulse-claude-preview.html'))} --out ${shellArg(join(outDir, 'pulse-claude-preview.json'))} --open

Nothing has been imported yet. Review preview pages first, then use the explicit commit command.
`);

  if (!rest.includes('--watch')) {
    return;
  }

  console.log(`[pulse] watching for ChatGPT and Claude archives in ${resolve(downloadsDir)} (${timeoutMs}ms timeout each, in parallel)`);
  await Promise.all(['chatgpt', 'claude'].map(async (source) => {
    try {
      const latest = await waitForLatestMigrationArchive(source, downloadsDir, timeoutMs, intervalMs);
      console.log(`[pulse] latest ${source} archive: ${latest}`);
      emitMigrationPreview(latest, [
        '--html',
        join(outDir, `pulse-${source}-preview.html`),
        '--out',
        join(outDir, `pulse-${source}-preview.json`),
        '--source',
        source,
        ...openRest,
      ]);
      if (source === 'chatgpt') {
        migrationStatus.chatgpt = `preview ready: ${join(outDir, 'pulse-chatgpt-preview.html')}`;
      } else {
        migrationStatus.claude = `preview ready: ${join(outDir, 'pulse-claude-preview.html')}`;
      }
      writeMigrationStatus(statusPath, migrationStatus);
    } catch (err) {
      const label = source === 'chatgpt' ? 'ChatGPT' : 'Claude';
      if (err instanceof Error && err.message.includes('without seeing a matching archive')) {
        console.log(`[pulse] ${label} archive not ready yet. Keep the export email/download open, then run wait-latest later.`);
        if (source === 'chatgpt') {
          migrationStatus.chatgpt = 'not ready yet';
        } else {
          migrationStatus.claude = 'not ready yet';
        }
        writeMigrationStatus(statusPath, migrationStatus);
        return;
      }
      throw err;
    }
  }));
}

function validMigrationHost(host) {
  return [
    'chatgpt',
    'claude',
    'codex',
    'claude-code',
    'gemini-cli',
    'cursor',
    'langchain',
    'crewai',
  ].includes(host);
}

function migrationPrivacyTier(value) {
  const tier = String(value ?? 'private').trim();
  if (tier === 'normal' || tier === 'sensitive' || tier === 'private') {
    return tier;
  }
  throw new Error('pulse migrate commit --privacy must be normal, sensitive, or private');
}

function semanticClientID(kind, name, index) {
  const base = slug(name);
  return `${kind}:${base === 'default' ? index : base}`;
}

function previewArray(preview, key) {
  const value = preview?.[key];
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((item) => safeText(item, 240)).filter(Boolean);
}

const PERSON_ALIAS_GROUPS = [
  ['Nik', 'Nikita', 'Ник', 'Никита'],
  ['Anya', 'Аня'],
  ['Elle', 'Элли', 'Эли'],
  ['Sonya', 'Соня'],
  ['Fedya', 'Fedor', 'Федя', 'Фёдор'],
];

function aliasKey(value) {
  return safeText(value, 120)
    .toLowerCase()
    .replace(/ё/g, 'е')
    .replace(/[^\p{L}\p{N}]+/gu, '');
}

function aliasGroupForPerson(name) {
  const key = aliasKey(name);
  if (!key) {
    return [safeText(name, 120)];
  }
  return PERSON_ALIAS_GROUPS.find((group) => group.some((item) => aliasKey(item) === key)) ?? [safeText(name, 120)];
}

function aliasGroupKeyForPerson(name) {
  return aliasGroupForPerson(name).map(aliasKey).filter(Boolean).sort().join('|');
}

function isSamePersonAlias(left, right) {
  const leftKey = aliasKey(left);
  const rightKey = aliasKey(right);
  if (!leftKey || !rightKey) {
    return false;
  }
  if (leftKey === rightKey) {
    return true;
  }
  const leftGroup = aliasGroupKeyForPerson(left);
  const rightGroup = aliasGroupKeyForPerson(right);
  return Boolean(leftGroup && rightGroup && leftGroup === rightGroup);
}

function groupImportPeople(people) {
  const canonicalByGroup = new Map();
  const aliasesByCanonical = new Map();
  const canonicalByName = new Map();
  const groupedPeople = [];

  for (const person of people) {
    const name = safeText(person, 120);
    if (!name) {
      continue;
    }
    const groupKey = aliasGroupForPerson(name).map(aliasKey).filter(Boolean).sort().join('|') || aliasKey(name);
    let canonical = canonicalByGroup.get(groupKey);
    if (!canonical) {
      canonical = name;
      canonicalByGroup.set(groupKey, canonical);
      aliasesByCanonical.set(canonical, []);
      groupedPeople.push(canonical);
    } else if (aliasKey(name) !== aliasKey(canonical)) {
      const aliases = aliasesByCanonical.get(canonical);
      if (!aliases.some((alias) => aliasKey(alias) === aliasKey(name))) {
        aliases.push(name);
      }
    }
    canonicalByName.set(name, canonical);
  }

  return {
    people: groupedPeople,
    aliasesByCanonical,
    canonicalByName,
  };
}

function relationshipTargetTopics(candidates, canonicalByName) {
  const topics = [];
  for (const candidate of candidates) {
    if (relationshipTouchesReviewEntity(candidate)) {
      continue;
    }
    const parsed = parseRelationshipCandidate(candidate);
    if (!parsed) {
      continue;
    }
    const { left, right } = parsed;
    const leftIsPerson = canonicalByName.has(left);
    const rightIsPerson = canonicalByName.has(right);
    if (leftIsPerson && !rightIsPerson && right) {
      topics.push(right);
    } else if (rightIsPerson && !leftIsPerson && left) {
      topics.push(left);
    }
  }
  return topics;
}

function memoryCandidateTitle(candidate) {
  const text = safeText(candidate, 160);
  return text.replace(/:\s*\d+\s+(?:safe message signals?|source snippets?|bounded source snippets?|preview source snippets?)$/i, '').trim() || text;
}

function normalizedReviewDecisions(preview) {
  const raw = preview?.review_decisions;
  const decisions = new Map();
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    return decisions;
  }
  for (const [name, value] of Object.entries(raw)) {
    const key = safeText(name, 120);
    if (!key) {
      continue;
    }
    if (typeof value === 'string') {
      decisions.set(key, { action: value });
    } else if (value && typeof value === 'object') {
      decisions.set(key, {
        ...value,
        action: safeText(value.action ?? value.result ?? '', 40),
      });
    }
  }
  return decisions;
}

function reviewActionName(decision) {
  return safeText(decision?.action ?? '', 40).toLowerCase().replace(/\s+/g, '_');
}

function ignoredReviewNames(decisions) {
  return new Set([...decisions.entries()]
    .filter(([, decision]) => ['ignore', 'ignored'].includes(reviewActionName(decision)))
    .map(([name]) => name));
}

function confirmedReviewProjectNames(decisions, ignored) {
  return confirmedReviewEntities(decisions, ignored)
    .filter((entity) => entity.kind === 'project')
    .map((entity) => entity.name);
}

function confirmedReviewEntities(decisions, ignored) {
  const confirmedActions = new Set(['confirm', 'confirmed', 'private', 'mark_private', 'marked_private']);
  return [...decisions.entries()]
    .filter(([name, decision]) => !ignored.has(name) && confirmedActions.has(reviewActionName(decision)))
    .map(([name, decision]) => {
      const kind = safeText(decision.kind, 40).toLowerCase() === 'person' ? 'person' : 'project';
      return { name, kind };
    });
}

function relationshipTouchesIgnoredReviewEntity(item, ignored) {
  const parsed = parseRelationshipCandidate(item);
  if (!parsed) {
    return false;
  }
  return ignored.has(parsed.left) || ignored.has(parsed.right);
}

function relationshipTouchesConfirmedReviewEntity(item, confirmed) {
  const parsed = parseRelationshipCandidate(item);
  if (!parsed) {
    return false;
  }
  return confirmed.has(parsed.left) || confirmed.has(parsed.right);
}

function funFactSubject(item) {
  const text = stripGallerySourcePrefix(item);
  return text.split(' appeared in ')[0];
}

function funFactTouchesIgnoredReviewEntity(item, ignored) {
  return ignored.has(funFactSubject(item));
}

function textMentionsAny(text, names) {
  const value = String(text ?? '').toLowerCase();
  return [...names].some((name) => {
    const needle = String(name ?? '').trim().toLowerCase();
    return needle && value.includes(needle);
  });
}

function materializedPulseInsights(preview, ignoredReviews, nodeIDs, privacyTier) {
  const insights = Array.isArray(preview.pulse_insights) ? preview.pulse_insights : [];
  return insights
    .slice(0, 8)
    .map((insight, index) => {
      const title = safeText(insight.title, 180);
      const summary = safeText(insight.summary, 520);
      const suggested = safeText(insight.suggested_next_step, 240);
      if (!title || !summary || textMentionsAny(title, ignoredReviews) || textMentionsAny(summary, ignoredReviews) || textMentionsAny(suggested, ignoredReviews)) {
        return null;
      }
      const reasons = Array.isArray(insight.reasons)
        ? insight.reasons
          .map((item) => safeText(item, 220))
          .filter((item) => item && !textMentionsAny(item, ignoredReviews))
          .slice(0, 4)
        : [];
      const relatedEntities = Array.isArray(insight.related_entities)
        ? insight.related_entities
          .map((item) => safeText(item, 120))
          .filter((item) => item && !textMentionsAny(item, ignoredReviews))
          .slice(0, 8)
        : [];
      const entityRefs = relatedEntities
        .map((name) => nodeIDs.get(name))
        .filter(Boolean)
        .slice(0, 8);
      const body = [
        summary,
        reasons.length > 0 ? `Because: ${reasons.join(' ')}` : '',
        suggested ? `Next: ${suggested}` : '',
      ].filter(Boolean).join(' ');
      return {
        event: {
          client_id: semanticClientID('event', `pulse-insight-${insight.thread_title || insight.title || index}`, index),
          title: `Pulse insight: ${title}`,
          summary: body,
          entity_refs: entityRefs,
          sentiment: '',
          emotional_weight: 0.24,
          confidence: Number.isFinite(insight.confidence) ? Math.max(0, Math.min(1, insight.confidence)) : 0.6,
          privacy_tier: privacyTier,
          domain: 'real',
        },
        reviewInsight: `Pulse insight: ${title}. ${suggested ? `Next: ${suggested}` : summary}`,
      };
    })
    .filter(Boolean);
}

function materializedActiveThreads(preview, ignoredReviews) {
  const raw = Array.isArray(preview?.active_threads) ? preview.active_threads : [];
  const threads = [];
  const seen = new Set();
  for (const item of raw) {
    const title = typeof item === 'string'
      ? safeText(item, 160)
      : safeText(item?.thread_title ?? item?.title, 160);
    if (!title || textMentionsAny(title, ignoredReviews)) {
      continue;
    }
    const key = title.toLowerCase();
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    const reason = typeof item === 'string'
      ? 'User marked this thread as active during review.'
      : safeText(item?.reason, 240) || 'User marked this thread as active during review.';
    if (textMentionsAny(reason, ignoredReviews)) {
      continue;
    }
    threads.push({ title, reason });
  }
  return threads.slice(0, 8);
}

function activeScopedContinuityItems(items, activeThreads) {
  if (!Array.isArray(items) || items.length === 0) {
    return [];
  }
  if (!Array.isArray(activeThreads) || activeThreads.length === 0) {
    return items;
  }
  const activeTitles = activeThreads
    .map((thread) => String(thread?.title ?? '').trim().toLowerCase())
    .filter(Boolean);
  if (activeTitles.length === 0) {
    return items;
  }
  return items.filter((item) => {
    const text = String(item).toLowerCase();
    return activeTitles.some((title) => text.includes(title));
  });
}

function buildSemanticDeltaFromPreview(preview, options = {}) {
  if (!preview || typeof preview !== 'object') {
    throw new Error('preview JSON must be an object');
  }
  if (preview.raw_text_written !== false) {
    throw new Error('preview raw_text_written must be false before commit');
  }

  const ctx = localThreadContext();
  const privacyTier = migrationPrivacyTier(options.privacy);
  const sourceHost = validMigrationHost(preview.source) ? preview.source : ctx.host;
  const reviewDecisions = normalizedReviewDecisions(preview);
  const ignoredReviews = ignoredReviewNames(reviewDecisions);
  const confirmedReviewItems = confirmedReviewEntities(reviewDecisions, ignoredReviews);
  const confirmedReviews = new Set(confirmedReviewItems.map((entity) => entity.name));
  const confirmedReviewPeople = confirmedReviewItems
    .filter((entity) => entity.kind === 'person')
    .map((entity) => entity.name);
  const confirmedProjects = confirmedReviewProjectNames(reviewDecisions, ignoredReviews);
  const peopleSource = previewArray(preview, 'people_candidates')
    .filter((name) => !ignoredReviews.has(name));
  const grouped = groupImportPeople(splitGalleryPersonCandidates(peopleSource).confident.slice(0, 18));
  const people = uniqueLimited([...grouped.people, ...confirmedReviewPeople], 18);
  const relationshipCandidates = previewArray(preview, 'relationship_candidates')
    .filter((candidate) => !relationshipTouchesIgnoredReviewEntity(candidate, ignoredReviews))
    .slice(0, 24);
  const memoryCandidates = previewArray(preview, 'memory_candidates').slice(0, 12);
  const projectLimit = Math.max(0, 30 - people.length);
  const threads = uniqueLimited([
    ...relationshipTargetTopics(relationshipCandidates, grouped.canonicalByName),
    ...previewArray(preview, 'thread_candidates'),
    ...memoryCandidates.map(memoryCandidateTitle),
    ...confirmedProjects,
  ], projectLimit);
  const nodeIDs = new Map();
  const nodes = [];

  people.forEach((name, index) => {
    const id = semanticClientID('person', name, index);
    nodeIDs.set(name, id);
    nodes.push({
      client_id: id,
      kind: 'person',
      canonical_name: name,
      aliases: grouped.aliasesByCanonical.get(name) ?? [],
      summary: 'Person candidate from safe Pulse archive import preview.',
      salience: 0.55,
      emotional_weight: 0.35,
      privacy_tier: privacyTier,
      domain: 'real',
    });
  });

  threads.forEach((name, index) => {
    const id = semanticClientID('project', name, index);
    nodeIDs.set(name, id);
    nodes.push({
      client_id: id,
      kind: 'project',
      canonical_name: name,
      summary: 'Thread candidate from safe Pulse archive import preview.',
      salience: 0.5,
      emotional_weight: 0.25,
      privacy_tier: privacyTier,
      domain: 'real',
    });
  });

  const edges = [];
  for (const candidate of relationshipCandidates) {
    if (relationshipTouchesReviewEntity(candidate) && !relationshipTouchesConfirmedReviewEntity(candidate, confirmedReviews)) {
      continue;
    }
    const parsed = parseRelationshipCandidate(candidate);
    if (!parsed) {
      continue;
    }
    if (parsed.kind === 'related_to') {
      const { left, right } = parsed;
      const canonicalLeft = grouped.canonicalByName.get(left) ?? left;
      const canonicalRight = grouped.canonicalByName.get(right) ?? right;
      if (nodeIDs.has(canonicalLeft) && nodeIDs.has(canonicalRight) && canonicalLeft !== canonicalRight) {
        edges.push({
          from: nodeIDs.get(canonicalLeft),
          to: nodeIDs.get(canonicalRight),
          kind: 'related_to',
          summary: `Safe archive preview linked ${canonicalLeft} and ${canonicalRight}.`,
          strength: 0.45,
          privacy_tier: privacyTier,
        });
      }
      continue;
    }
    if (parsed.kind === 'mentioned_in') {
      const { left, right } = parsed;
      const canonicalLeft = grouped.canonicalByName.get(left) ?? left;
      const canonicalRight = grouped.canonicalByName.get(right) ?? right;
      if (nodeIDs.has(canonicalLeft) && nodeIDs.has(canonicalRight) && canonicalLeft !== canonicalRight) {
        edges.push({
          from: nodeIDs.get(canonicalLeft),
          to: nodeIDs.get(canonicalRight),
          kind: 'mentioned_in',
          summary: `Safe archive preview placed ${canonicalLeft} in ${canonicalRight}.`,
          strength: 0.4,
          privacy_tier: privacyTier,
        });
      }
    }
  }

  const facts = [];
  for (const fact of previewArray(preview, 'fun_fact_candidates').slice(0, 20)) {
    const factSubject = funFactSubject(fact);
    if (funFactTouchesIgnoredReviewEntity(fact, ignoredReviews)) {
      continue;
    }
    if (funFactTouchesReviewEntity(fact) && !confirmedReviews.has(factSubject)) {
      continue;
    }
    const matched = [...grouped.canonicalByName.keys()].find((person) => fact.startsWith(person));
    const canonical = matched ? grouped.canonicalByName.get(matched) : (confirmedReviews.has(factSubject) ? factSubject : '');
    if (canonical && nodeIDs.has(canonical)) {
      facts.push({
        node: nodeIDs.get(canonical),
        text: fact,
        confidence: 0.55,
        privacy_tier: privacyTier,
        domain: 'real',
      });
    }
  }

  const entityRefs = [...new Set([...edges.flatMap((edge) => [edge.from, edge.to]), ...facts.map((fact) => fact.node)])]
    .filter((ref) => nodes.some((node) => node.client_id === ref))
    .slice(0, 12);
  const dedupedEdges = [];
  const seenEdges = new Set();
  for (const edge of edges) {
    const key = `${edge.from}|${edge.to}|${edge.kind}`;
    if (seenEdges.has(key)) {
      continue;
    }
    seenEdges.add(key);
    dedupedEdges.push(edge);
  }
  const emotions = previewArray(preview, 'emotion_candidates').slice(0, 8);
  const insights = materializedPulseInsights(preview, ignoredReviews, nodeIDs, privacyTier);
  const activeThreads = materializedActiveThreads(preview, ignoredReviews);
  const continuityMemoryCandidates = activeScopedContinuityItems(memoryCandidates, activeThreads).slice(0, 8);
  const reviewInsights = [
    ...insights.map((insight) => insight.reviewInsight),
    ...activeThreads.map((thread) => `Active thread: ${thread.title}. ${thread.reason}`),
  ].slice(0, 8);
  const events = [];
  if (nodes.length > 0) {
    events.push({
      client_id: 'event:archive-import-preview',
      title: 'Pulse archive import preview committed',
      summary: `Committed ${preview.conversations ?? 0} conversations and ${preview.messages ?? 0} source snippets from a Pulse archive preview.`,
      entity_refs: entityRefs,
      sentiment: emotions.join(', '),
      emotional_weight: emotions.length > 0 ? 0.45 : 0.2,
      confidence: 0.6,
      privacy_tier: privacyTier,
      domain: 'real',
    });
    memoryCandidates.forEach((candidate, index) => {
      const title = memoryCandidateTitle(candidate);
      const refs = nodeIDs.has(title) ? [nodeIDs.get(title)] : entityRefs.slice(0, 4);
      events.push({
        client_id: semanticClientID('event', title, index),
        title,
        summary: candidate,
        entity_refs: refs,
        sentiment: emotions.join(', '),
        emotional_weight: emotions.length > 0 ? 0.35 : 0.2,
        confidence: 0.55,
        privacy_tier: privacyTier,
        domain: 'real',
      });
    });
  }
  for (const insight of insights) {
    events.push(insight.event);
  }

  return {
    schema: 'pulse.semantic_delta.v1',
    source: {
      host: sourceHost,
      conversation_scope: 'project_context',
      timestamp: new Date().toISOString(),
      thread_id: ctx.threadId,
      session_id: ctx.sessionId,
      project_id: ctx.projectId,
    },
    nodes,
    edges: dedupedEdges.slice(0, 50),
    facts,
    events,
    continuity: {
      summary: `Imported safe Pulse archive preview with ${preview.conversations ?? 0} conversations and ${preview.messages ?? 0} source snippets.`,
      emotional_anchors: emotions.map((emotion) => `Archive preview emotion candidate: ${emotion}`),
      state_signals: continuityMemoryCandidates,
      active_threads: activeThreads.map((thread) => thread.title),
      review_insights: reviewInsights,
    },
    raw_input_included: false,
  };
}

async function commitMigrationPreview(previewPath, rest) {
  if (restArg(rest, '--confirm') !== 'import pulse graph') {
    throw new Error('pulse migrate commit requires --confirm "import pulse graph"');
  }
  if (!previewPath) {
    throw new Error('pulse migrate commit requires <preview-json-file>');
  }
  const preview = JSON.parse(readFileSync(resolve(previewPath), 'utf8'));
  const previewFlow = preview?.flow;
  if (previewFlow !== undefined && previewFlow !== LEGACY_IMPORT_PREVIEW_FLOW &&
      previewFlow !== IMPORT_PREVIEW_FLOW) {
    throw new Error(`unsupported Pulse import preview flow: ${String(previewFlow)}`);
  }
  const delta = buildSemanticDeltaFromPreview(preview, {
    privacy: restArg(rest, '--privacy'),
  });
  if (
    delta.nodes.length === 0 &&
    delta.events.length === 0 &&
    delta.continuity.state_signals.length === 0 &&
    delta.continuity.active_threads.length === 0 &&
    delta.continuity.review_insights.length === 0
  ) {
    throw new Error('preview has no safe graph candidates to commit');
  }
  const out = await pulseFetch('/graph/delta', { body: delta });
  const receipts = productWriteReceipts(out) || [];
  const pendingReceipts = receipts.filter((receipt) => receipt?.status === 'pending');
  const materializedReceipts = receipts.filter((receipt) => ['created', 'deduplicated', 'updated'].includes(receipt?.status));
  const failedReceipt = receipts.find((receipt) => ['canceled', 'rejected', 'failed'].includes(receipt?.status));
  if (failedReceipt) {
    throw new Error(`Pulse graph delta was not committed (${failedReceipt.status}: ${failedReceipt.reason_code || 'write_not_materialized'}; receipt ${failedReceipt.receipt_id || 'unknown'})`);
  }
  if (pendingReceipts.length > 0) {
    console.log(`[pulse] Pulse graph delta is visible in Memory Tray and not committed yet (${pendingReceipts.length} receipt${pendingReceipts.length === 1 ? '' : 's'})`);
  } else if (materializedReceipts.length > 0 || (receipts.length === 0 && out?.ok === true)) {
    console.log('[pulse] committed Pulse graph delta');
  } else {
    throw new Error('Pulse graph delta returned no truthful pending or committed receipt');
  }
  console.log(`nodes: ${delta.nodes.length}`);
  console.log(`edges: ${delta.edges.length}`);
  console.log(`facts: ${delta.facts.length}`);
  console.log(`events: ${delta.events.length}`);
  console.log(JSON.stringify(out, null, 2));
  console.log('Next:');
  console.log(`  ${viewerNextStepCommand()}`);
  if (rest.includes('--open')) {
    runViewer(['--open']);
  }
}

function hostArchivePattern(host) {
  if (host === 'chatgpt') {
    return /(?:chatgpt|openai).*\.zip$/i;
  }
  if (host === 'claude') {
    return /claude.*\.zip$/i;
  }
  throw new Error('pulse migrate preview-latest supports: chatgpt, claude');
}

function latestArchiveMissingMessage(host, dir) {
  if (host === 'chatgpt') {
    return `No ChatGPT archive zip found in ${dir}.

What to do:
  1. Open ChatGPT Data Controls.
  2. Click "Request archive".
  3. Wait for the email/download. The archive may still be preparing.
  4. Save the zip into Downloads, or re-run with --downloads <dir>.`;
  }
  if (host === 'claude') {
    return `No Claude archive zip found in ${dir}.

What to do:
  1. Open Claude Privacy settings.
  2. Click "Export data".
  3. Wait for the email/download. The archive may still be preparing.
  4. Save the zip into Downloads, or re-run with --downloads <dir>.`;
  }
  return `No ${host} archive zip found in ${dir}. Re-run with --downloads <dir>.`;
}

function findLatestMigrationArchive(host, downloadsDir) {
  const dir = resolve(downloadsDir);
  if (!existsSync(dir)) {
    throw new Error(`downloads path does not exist: ${dir}`);
  }
  const pattern = hostArchivePattern(host);
  const candidates = readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && pattern.test(entry.name))
    .map((entry) => {
      const path = join(dir, entry.name);
      return { path, name: entry.name, mtimeMs: statSync(path).mtimeMs };
    })
    .sort((a, b) => b.mtimeMs - a.mtimeMs || b.name.localeCompare(a.name));
  if (candidates.length === 0) {
    throw new Error(latestArchiveMissingMessage(host, dir));
  }
  return candidates[0].path;
}

function tryFindLatestMigrationArchive(host, downloadsDir) {
  try {
    return findLatestMigrationArchive(host, downloadsDir);
  } catch (err) {
    if (err instanceof Error && /^No (ChatGPT|Claude) archive zip found/.test(err.message)) {
      return '';
    }
    throw err;
  }
}

function positiveIntArg(rest, name, fallback) {
  const raw = restArg(rest, name);
  if (raw === undefined) {
    return fallback;
  }
  const value = Number.parseInt(raw, 10);
  if (!Number.isFinite(value) || value <= 0) {
    throw new Error(`${name} must be a positive integer`);
  }
  return value;
}

function sleep(ms) {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms));
}

async function waitForLatestMigrationArchive(host, downloadsDir, timeoutMs, intervalMs) {
  const dir = resolve(downloadsDir);
  const started = Date.now();
  while (Date.now() - started <= timeoutMs) {
    const latest = tryFindLatestMigrationArchive(host, dir);
    if (latest) {
      return latest;
    }
    await sleep(intervalMs);
  }
  throw new Error(`${latestArchiveMissingMessage(host, dir)}

Waited ${timeoutMs}ms without seeing a matching archive.`);
}

function requestPreviewRest(rest) {
  const out = [...rest];
  if (!out.includes('--json') && restArg(out, '--html') === undefined) {
    out.push('--html', 'pulse-preview.html');
  }
  if (!out.includes('--json') && restArg(out, '--out') === undefined) {
    out.push('--out', 'pulse-preview.json');
  }
  return out;
}

async function requestLatestMigrationArchive(source, rest) {
  const info = remoteArchiveInfo(source);
  const previewRest = requestPreviewRest(rest);
  const downloadsDir = restArg(rest, '--downloads') ?? join(homedir(), 'Downloads');
  const timeoutMs = positiveIntArg(rest, '--timeout-ms', 15 * 60 * 1000);
  const intervalMs = positiveIntArg(rest, '--interval-ms', 2000);
  openExternalURL(info.url);
  console.log(`[pulse] ${info.label} archive page opened`);
  console.log(`[pulse] Click "${info.buttonText}". Pulse is waiting for the export zip in ${resolve(downloadsDir)}.`);
  console.log(`[pulse] waiting for ${info.source} archive in ${resolve(downloadsDir)} (${timeoutMs}ms timeout)`);
  const latest = await waitForLatestMigrationArchive(info.source, downloadsDir, timeoutMs, intervalMs);
  console.log(`[pulse] latest ${info.source} archive: ${latest}`);
  emitMigrationPreview(latest, [...previewRest, '--source', info.source]);
}

function requestLocalHistoryPreview(source, rest) {
  const info = localHistoryInfo(source);
  if (!info) {
    return false;
  }
  const previewRest = requestPreviewRest(rest);
  console.log(`[pulse] ${info.label} local history selected`);
  console.log(`[pulse] Local history: ${info.displayPath}`);
  console.log('[pulse] No archive request is needed. Previewing local history now.');
  emitMigrationPreview(info.path, [...previewRest, '--source', info.source]);
  return true;
}

async function requestMigrationSource(source, rest) {
  if (requestLocalHistoryPreview(source, rest)) {
    return;
  }
  await requestLatestMigrationArchive(source, rest);
}

function emitPeopleGraphPreview(target, rest) {
  const preview = peopleGraphPreview(target);
  const htmlPath = restArg(rest, '--html');
  const jsonOutPath = restArg(rest, '--out');
  const resolvedJsonOutPath = jsonOutPath ? resolve(jsonOutPath) : '';
  const htmlPreview = {
    ...preview,
    commit_command: resolvedJsonOutPath
      ? `pulse migrate commit ${shellArg(resolvedJsonOutPath)} --confirm "import pulse graph" --open`
      : '',
    viewer_command: viewerNextStepCommand(),
  };
  if (htmlPath) {
    const outPath = resolve(htmlPath);
    writeFileSync(outPath, renderMigrationPreviewHTML(htmlPreview), { mode: 0o600 });
    console.log(`[pulse] people graph HTML preview: ${outPath}`);
    if (rest.includes('--open')) {
      openExternalURL(outPath);
    }
  }
  if (jsonOutPath) {
    const outPath = resolvedJsonOutPath;
    const jsonPreview = {
      ...preview,
      html_path: htmlPath ? resolve(htmlPath) : undefined,
      next: htmlPreview.commit_command || preview.next,
      commit_command: htmlPreview.commit_command || undefined,
      viewer_command: htmlPreview.viewer_command,
    };
    writeFileSync(outPath, `${JSON.stringify(jsonPreview, null, 2)}\n`, { mode: 0o600 });
    console.log(`[pulse] people graph JSON preview: ${outPath}`);
    console.log('Next:');
    console.log(`  ${htmlPreview.commit_command}`);
  }
  if (rest.includes('--json')) {
    console.log(JSON.stringify({ ...preview, html_path: htmlPath ? resolve(htmlPath) : undefined }, null, 2));
    return;
  }
  if (htmlPath || jsonOutPath) {
    return;
  }
  printMigrationPreview(preview);
}

function emitMigrationPreview(target, rest) {
  const sourceHint = restArg(rest, '--source');
  const basePreview = migrationPreview(target);
  const preview = overrideImportPreviewSource(basePreview, sourceHint);
  const htmlPath = restArg(rest, '--html');
  const jsonOutPath = restArg(rest, '--out');
  const resolvedJsonOutPath = jsonOutPath ? resolve(jsonOutPath) : '';
  const htmlPreview = {
    ...preview,
    commit_command: resolvedJsonOutPath
      ? `pulse migrate commit ${shellArg(resolvedJsonOutPath)} --confirm "import pulse graph" --open`
      : '',
    viewer_command: viewerNextStepCommand(),
  };
  if (htmlPath) {
    const outPath = resolve(htmlPath);
    writeFileSync(outPath, renderMigrationPreviewHTML(htmlPreview), { mode: 0o600 });
    console.log(`[pulse] migration HTML preview: ${outPath}`);
    if (rest.includes('--open')) {
      openExternalURL(outPath);
    }
  }
  if (jsonOutPath) {
    const outPath = resolvedJsonOutPath;
    const jsonPreview = {
      ...preview,
      html_path: htmlPath ? resolve(htmlPath) : undefined,
      next: htmlPreview.commit_command || preview.next,
      commit_command: htmlPreview.commit_command || undefined,
      viewer_command: htmlPreview.viewer_command,
    };
    writeFileSync(outPath, `${JSON.stringify(jsonPreview, null, 2)}\n`, { mode: 0o600 });
    console.log(`[pulse] migration JSON preview: ${outPath}`);
    console.log('Next:');
    console.log(`  ${htmlPreview.commit_command}`);
  }
  if (rest.includes('--json')) {
    console.log(JSON.stringify({ ...preview, html_path: htmlPath ? resolve(htmlPath) : undefined }, null, 2));
    return;
  }
  if (htmlPath || jsonOutPath) {
    return;
  }
  printMigrationPreview(preview);
}

async function runMigrate(subcommand, rest) {
  if (subcommand === 'start') {
    await runMigrationStart(rest);
    return;
  }
  if (subcommand === 'concierge') {
    runMigrationConcierge(rest);
    return;
  }
  if (subcommand === 'guide') {
    const source = rest.find((arg) => !arg.startsWith('--'));
    if (!source) {
      throw new Error('pulse migrate guide requires chatgpt, claude, codex, or claude-code');
    }
    migrationGuide(source);
    return;
  }
  if (subcommand === 'commit') {
    await commitMigrationPreview(rest.find((arg) => !arg.startsWith('--')), rest);
    return;
  }
  if (subcommand === 'request') {
    const source = rest.find((arg) => !arg.startsWith('--'));
    if (!source) {
      throw new Error('pulse migrate request requires chatgpt, claude, codex, or claude-code');
    }
    await requestMigrationSource(source, rest);
    return;
  }
  if (subcommand === 'preview-latest') {
    const source = rest.find((arg) => !arg.startsWith('--'));
    if (!source) {
      throw new Error('pulse migrate preview-latest requires chatgpt or claude');
    }
    const downloadsDir = restArg(rest, '--downloads') ?? join(homedir(), 'Downloads');
    const latest = findLatestMigrationArchive(source, downloadsDir);
    console.log(`[pulse] latest ${source} archive: ${latest}`);
    emitMigrationPreview(latest, [...rest, '--source', source]);
    return;
  }
  if (subcommand === 'preview-people-graph') {
    const target = rest.find((arg) => !arg.startsWith('--'));
    if (!target) {
      throw new Error('pulse migrate preview-people-graph requires <graph-dir-or-people-index>');
    }
    emitPeopleGraphPreview(target, rest);
    return;
  }
  if (subcommand === 'wait-latest') {
    const source = rest.find((arg) => !arg.startsWith('--'));
    if (!source) {
      throw new Error('pulse migrate wait-latest requires chatgpt or claude');
    }
    const downloadsDir = restArg(rest, '--downloads') ?? join(homedir(), 'Downloads');
    const timeoutMs = positiveIntArg(rest, '--timeout-ms', 15 * 60 * 1000);
    const intervalMs = positiveIntArg(rest, '--interval-ms', 2000);
    console.log(`[pulse] waiting for ${source} archive in ${resolve(downloadsDir)} (${timeoutMs}ms timeout)`);
    const latest = await waitForLatestMigrationArchive(source, downloadsDir, timeoutMs, intervalMs);
    console.log(`[pulse] latest ${source} archive: ${latest}`);
    emitMigrationPreview(latest, [...rest, '--source', source]);
    return;
  }
  if (subcommand !== 'preview') {
    throw new Error('pulse migrate supports: start, guide, concierge, request, wait-latest, preview-latest, preview-people-graph, preview, commit');
  }
  const target = rest.find((arg) => !arg.startsWith('--'));
  if (!target) {
    throw new Error('pulse migrate preview requires <export-folder-or-json>');
  }
  emitMigrationPreview(target, rest);
}

function restArg(rest, name) {
  const i = rest.indexOf(name);
  return i >= 0 ? rest[i + 1] : undefined;
}

function observationSummary(eventType, payload) {
  if (eventType === 'UserPromptSubmit') {
    return 'Prompt noticed. Raw text is hidden by default.';
  }

  const toolName = safeText(payload?.tool_name ?? payload?.tool);
  if (eventType === 'PostToolUse' && toolName) {
    return `Tool ran: ${toolName}. Raw tool input and output are hidden by default.`;
  }

  const text = firstSafeText(payload, [
    'summary',
    'description',
  ]);
  if (text) {
    return `Session note: ${text}`;
  }
  if (toolName) {
    return `Tool ran: ${toolName}. Raw tool input and output are hidden by default.`;
  }
  return 'Local activity captured. Raw prompt and tool content are hidden by default.';
}

function parseObjectLike(value) {
  if (!value) {
    return {};
  }
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value);
      return parsed && typeof parsed === 'object' ? parsed : {};
    } catch {
      return {};
    }
  }
  return typeof value === 'object' ? value : {};
}

function hookToolName(payload) {
  return safeText(
    payload?.tool_name ??
    payload?.toolName ??
    payload?.tool ??
    payload?.name ??
    payload?.hook_name,
    160,
  );
}

function hookToolInput(payload) {
  for (const candidate of [
    payload?.tool_input,
    payload?.toolInput,
    payload?.input,
    payload?.tool?.input,
    payload?.tool_use?.input,
    payload?.toolUse?.input,
  ]) {
    const parsed = parseObjectLike(candidate);
    if (Object.keys(parsed).length > 0) {
      return parsed;
    }
  }
  return {};
}

function memoryKindLabel(kind) {
  switch (kind) {
    case 'decision':
      return 'Decision';
    case 'open_loop':
      return 'Open loop';
    case 'do_not_repeat':
      return 'Do-not-repeat';
    case 'preference':
      return 'Preference';
    case 'project_state':
      return 'Project state';
    case 'correction':
      return 'Correction';
    case 'relationship_note':
      return 'Relationship note';
    case 'state_signal':
      return 'State signal';
    case 'system_event':
      return 'System event';
    case 'fact':
      return 'Fact';
    default:
      return '';
  }
}

function pulseRememberObservationSummaries(payload) {
  const toolName = hookToolName(payload);
  if (!toolName.includes('pulse_remember')) {
    return [];
  }
  const capsule = hookToolInput(payload);
  if (capsule.schema !== 'pulse.memory_capsule.v1' || capsule.raw_input_included !== false || !Array.isArray(capsule.items)) {
    return [];
  }
  return capsule.items
    .map((item) => {
      const label = memoryKindLabel(item?.kind);
      const summary = safeText(item?.redacted_summary, 720);
      return label && summary ? `${label}: ${summary}` : '';
    })
    .filter(Boolean)
    .slice(0, 20);
}

function observationSummaries(eventType, payload) {
  if (eventType === 'PostToolUse') {
    const capsuleSummaries = pulseRememberObservationSummaries(payload);
    if (capsuleSummaries.length > 0) {
      return capsuleSummaries;
    }
  }
  return [observationSummary(eventType, payload)];
}

async function runHook(kind) {
  const strict = process.env.PULSE_HOOK_STRICT === '1';
  try {
    if (kind === 'session-start') {
      await hookSessionStart();
      return;
    }
    if (kind === 'user-prompt-submit') {
      await hookObserve('UserPromptSubmit');
      return;
    }
    if (kind === 'post-tool-use') {
      await hookObserve('PostToolUse');
      return;
    }
    if (kind === 'stop') {
      await hookStop();
      return;
    }
    throw new Error('pulse hook supports: session-start, user-prompt-submit, post-tool-use, stop');
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (strict) {
      throw err;
    }
    console.error(`[pulse] hook ${kind} skipped: ${message}`);
  }
}

async function hookSessionStart() {
  const ctx = localThreadContext();
  const resume = await pulseFetch('/continuity/resume', {
    body: {
      thread_id: ctx.threadId,
      project_id: ctx.projectId,
      session_id: ctx.sessionId,
      host: ctx.host,
      token_budget: Number(process.env.PULSE_RESUME_TOKENS ?? 1200),
    },
  });
  if (resume?.resume_markdown) {
    console.log(resume.resume_markdown);
  }
  console.log(`
# Pulse Graph Guidance
- When the user makes a durable decision, open loop, correction, preference, relationship note, project-state change, or emotional/state anchor, call pulse_graph_delta with pulse.semantic_delta.v1.
- Do not send raw transcript, secrets, credentials, local file paths, or store-everything payloads.
`);
}

async function hookObserve(eventType) {
  const payload = parseHookPayload(await readStdin());
  const ctx = localThreadContext();
  const summaries = observationSummaries(eventType, payload);
  const stamp = new Date().toISOString();
  for (let i = 0; i < summaries.length; i += 1) {
    await pulseFetch('/continuity/observe', {
      body: {
        thread_id: ctx.threadId,
        project_id: ctx.projectId,
        session_id: ctx.sessionId,
        host: ctx.host,
        event_type: eventType,
        redacted_summary: summaries[i],
        source_ref: `pulse:hook:${eventType}:${stamp}${summaries.length > 1 ? `:${i}` : ''}`,
      },
    });
  }
}

async function hookStop() {
  const payload = parseHookPayload(await readStdin());
  const ctx = localThreadContext();
  const summary =
    firstSafeText(payload, ['summary', 'session_summary', 'checkpoint_summary']) ||
    `Session ended for ${ctx.threadId}. Pulse recorded a local-auto checkpoint.`;
  await pulseFetch('/continuity/checkpoint', {
    body: {
      thread_id: ctx.threadId,
      project_id: ctx.projectId,
      session_id: ctx.sessionId,
      host: ctx.host,
      summary,
      decisions: safeStringArray(payload.decisions),
      open_loops: safeStringArray(payload.open_loops).length > 0
        ? safeStringArray(payload.open_loops)
        : ['Review what changed since the last Pulse checkpoint.'],
      do_not_repeat: safeStringArray(payload.do_not_repeat),
      emotional_anchors: safeStringArray(payload.emotional_anchors),
      state_signals: safeStringArray(payload.state_signals).length > 0
        ? safeStringArray(payload.state_signals)
        : ['Local-auto continuity is enabled for this project.'],
      source_refs: [`pulse:hook:Stop:${new Date().toISOString()}`],
      confidence: 0.6,
    },
  });
}

function getArg(name) {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
}

function getRestArg(rest, name) {
  const i = rest.indexOf(name);
  return i >= 0 ? rest[i + 1] : undefined;
}

function optionalLocalPort() {
	const value = getArg('--port');
	if (value === undefined) return undefined;
	if (!/^[0-9]{4,5}$/.test(value)) throw new Error('pulse binding --port must be an integer from 1024 to 65535');
	const port = Number.parseInt(value, 10);
	if (port < 1024 || port > 65535) throw new Error('pulse binding --port must be an integer from 1024 to 65535');
	return port;
}

function requiredBindingArg(name) {
	const value = getArg(name);
	if (!value) throw new Error(`pulse binding requires ${name} <id>`);
	return value;
}

async function createBinding(subcommand) {
	const team = subcommand === 'create-team';
	const confirmation = team ? 'bind pulse team workspace' : 'bind pulse personal workspace';
	if (getArg('--confirm') !== confirmation) {
		throw new Error(`pulse binding ${subcommand} requires --confirm "${confirmation}"`);
	}
	const binding = await createWorkspaceBinding({
		cwd: getArg('--cwd') ?? process.cwd(),
		mode: team ? 'team' : 'personal',
		port: optionalLocalPort(),
		principalID: requiredBindingArg('--principal-id'),
		teamID: team ? requiredBindingArg('--team-id') : undefined,
		commonsStoreID: team ? requiredBindingArg('--commons-store-id') : undefined,
		commonsProjectID: team ? requiredBindingArg('--commons-project-id') : undefined,
		commonsResource: team ? requiredBindingArg('--commons-resource') : undefined,
	});
	console.log(`[pulse] trusted ${binding.mode} binding created: ${binding.binding_id}`);
	console.log(`[pulse] workspace ${binding.workspace.workspace_id}; resolver epoch ${binding.resolver_epoch}; fallback=false`);
	if (binding.mode === 'team') {
		console.log(`[pulse] private Desk ${binding.desk.store_id} -> ${binding.desk.base_url}`);
		console.log(`[pulse] Team Commons ${binding.commons.team_id}/${binding.commons.store_id}; project ${binding.commons.project_id} -> ${binding.commons.resource}`);
		console.log('[pulse] Commons is read-only to the installed agent. Publication requires the human Airlock.');
	} else {
		console.log(`[pulse] private Personal Vault ${binding.personal.store_id} -> ${binding.personal.base_url}`);
	}
}

async function trustCommand() {
	const subcommand = args[1];
	if (subcommand === 'status') {
		const result = inspectPresenceTrust({ probePublicKey: true });
		if (args.includes('--json')) console.log(JSON.stringify(result, null, 2));
		else {
			console.log(`[pulse] presence trust: ${result.status}; ready=${result.ready}`);
			console.log(`[pulse] helper: ${result.helper.path}`);
			console.log(`[pulse] binding key: ${result.public_key.path}`);
			if (result.issues.length > 0) console.log(`[pulse] issues: ${result.issues.join(', ')}`);
		}
		return;
	}
	if (subcommand === 'install') {
		const result = await installPresenceTrust({ confirmation: getArg('--confirm') });
		console.log(`[pulse] presence trust ready; installed=${result.installed}`);
		console.log('[pulse] workspace binding changes now require review of the exact registry bytes and macOS user presence.');
		return;
	}
	throw new Error(`pulse trust supports: pulse trust status | pulse trust install --confirm "${TRUST_INSTALL_CONFIRMATION}"`);
}

async function main() {
  if (command === 'mcp') {
    // Stdio MCP server: nothing may touch stdout before the JSON-RPC handshake.
    await runMcpServer();
    return;
  }

  if (command === 'codex-mcp') {
    // The plugin MCP resolves one immutable workspace binding before importing
    // the stdio server, so no user/global Pulse endpoint can shadow routing.
    await runCodexMcpServer();
    return;
  }

  if (command === 'claude-mcp') {
    await runClaudeMcpServer();
    return;
  }

  if (command === 'cursor-mcp') {
    await runCursorMcpServer();
    return;
  }

  if (command === 'codex-hook') {
    await recoverBindingAuthority();
    await runCodexHookCLI(args[1]);
    return;
  }

  if (command === 'claude-hook') {
    await recoverBindingAuthority();
    await runClaudeHookCLI(args[1]);
    return;
  }

  if (command === 'cursor-hook') {
    await recoverBindingAuthority();
    await runCursorHookCLI(args[1]);
    return;
  }

  if (command === '--why' || command === 'why') {
    console.log('Because repeating yourself to machines is a terrible way to live.');
    return;
  }

  if (command === '--help' || command === '-h' || command === 'help') {
    usage();
    return;
  }

  if (command === 'install-plan') {
    const target = args[1];
    if (target === 'claude-code') {
      printInstallPlan(target, { json: args.includes('--json') });
      return;
    }
    if (target !== undefined && target !== '--json' && target !== 'codex') {
      throw new Error('pulse install-plan supports host-neutral Personal or the legacy claude-code preview plan');
    }
    printPersonalInstallPlan(currentPersonalInstallPlan(), { json: args.includes('--json') });
    return;
  }

  if (command === 'install' || command === 'repair') {
    const executed = await executePersonalInstallCommand({
      argv: args.slice(1),
      buildDependencies: personalInstallDependencies,
      buildPlan: currentPersonalInstallPlan,
      dataDir: DATA_DIR,
      mode: command,
      openHome: () => runHome([]),
    });
    if (executed.exitCode !== 0) process.exitCode = executed.exitCode;
    return;
  }

  if (command === 'demo') {
    if (args.includes('--clean')) {
      demoCleanup();
      return;
    }
    await runStatefulDemo();
    return;
  }

  if (command === 'doctor') {
    await runDoctor(args.slice(1));
    return;
  }

  if (command === 'trust') {
	await trustCommand();
	return;
  }

  if (command === 'binding') {
	const subcommand = args[1];
	if (subcommand === 'create-personal' || subcommand === 'create-team') {
		await createBinding(subcommand);
		return;
	}
	if (subcommand !== 'resolve' && subcommand !== 'status') {
	  throw new Error('pulse binding supports resolve, status, create-personal, or create-team');
	}
	try {
	  await recoverBindingAuthority();
	  const binding = resolveWorkspaceBinding({
		cwd: getArg('--cwd') ?? process.cwd(),
	  });
	  if (args.includes('--json')) {
		console.log(JSON.stringify(binding, null, 2));
	  } else {
		console.log(`[pulse] binding ${binding.binding_id}: ${binding.mode} (${binding.receipt_id})`);
			console.log(`[pulse] workspace ${binding.workspace.workspace_id}; resolver epoch ${binding.resolver_epoch}; store_topology_fallback=false`);
	  }
	} catch (error) {
	  if (error instanceof BindingError) {
		throw new Error(`${error.code}: ${error.message}`);
	  }
	  throw error;
	}
	return;
  }

  if (command === 'supervisor') {
	const subcommand = args[1];
	if (!['start', 'status', 'stop'].includes(subcommand)) {
	  throw new Error('pulse supervisor supports start, status, or stop');
	}
	try {
	  await recoverBindingAuthority();
	  const binding = resolveWorkspaceBinding({ cwd: getArg('--cwd') ?? process.cwd() });
	  const runtime = vaultRuntimeFromBinding(binding);
	  let result;
		if (subcommand === 'start') {
			const daemonPath = process.env.PULSE_GO_BIN || join(DATA_DIR, 'bin', 'pulse-product-daemon');
			result = await startVaultRuntime(runtime, { daemonPath });
		} else if (subcommand === 'stop') {
			result = await stopVaultRuntimeAndWait(runtime);
	  } else {
		result = inspectVaultRuntime(runtime);
	  }
	  if (args.includes('--json')) {
		console.log(JSON.stringify(result, null, 2));
	  } else {
			console.log(`[pulse] ${runtime.kind} vault: ${result.status}; store=${runtime.store_id}; store_topology_fallback=false`);
	  }
	} catch (error) {
	  if (error instanceof BindingError || error instanceof SupervisorError) {
		throw new Error(`${error.code}: ${error.message}`);
	  }
	  throw error;
	}
	return;
  }

  if (command === 'team') {
	if (args[1] === 'login') await loginTeam();
	else if (args[1] === 'status') await teamStatus();
	else if (args[1] === 'owner') await teamOwnerCommand();
	else throw new Error('pulse team supports: pulse team login | pulse team status | pulse team owner');
	return;
  }

  if (command === 'init') {
    const target = args[1];
    if (target !== 'claude-code') {
      throw new Error('v1 supports only: pulse init claude-code');
    }
    if (args.includes('--dry-run')) {
      printInstallPlan(target, { dryRun: true });
      return;
    }
    await connectClaudeCode();
    return;
  }

  if (command === 'connect') {
	    const target = args[1];
	    if (target === 'claude-code') {
	      await connectClaudeCode();
	      return;
	    }
    if (target === 'codex') {
      await connectCodex();
      return;
    }
    if (target === 'cursor') {
      await connectCursor();
      return;
    }
    if (target === 'chatgpt' || target === 'claude-chat') {
      connectRemoteHost(target);
      return;
    }
    throw new Error('v1 supports: pulse connect codex | claude-code | cursor | chatgpt | claude-chat');
  }

  if (command === 'connect-smoke') {
    runConnectorSmoke(args.slice(1));
    return;
  }

  if (command === 'disconnect') {
    const target = args[1];
		if (target === 'codex') {
			await disconnectCodex();
      return;
    }
		if (target === 'cursor') {
			await disconnectCursor();
			return;
		}
    if (target !== 'claude-code') {
      throw new Error('v1 supports: pulse disconnect codex | claude-code | cursor');
    }
		await disconnectClaudeCode();
    return;
  }

  if (command === 'stop') {
    stopPreviewDaemon();
    return;
  }

  if (command === 'remove') {
    const target = args[1];
    if (target !== 'claude-code') {
      throw new Error('v1 supports only: pulse remove claude-code');
    }
		await disconnectClaudeCode();
    stopPreviewDaemon();
    console.log('[pulse] Local memory was not wiped.');
    console.log('[pulse] To wipe memory, run: pulse wipe --confirm "wipe pulse memory"');
    return;
  }

  if (command === 'hook') {
    await runHook(args[1]);
    return;
  }

  if (command === 'migrate') {
    await runMigrate(args[1], args.slice(2));
    return;
  }

  if (command === 'home') {
    await runHome(args.slice(1));
    return;
  }

  if (command === 'viewer') {
    await runViewer(args.slice(1));
    return;
  }

  if (command === 'daemon') {
    const sep = args.indexOf('--');
    daemon(sep >= 0 ? args.slice(sep + 1) : args.slice(1));
    return;
  }

  if (command === 'status') {
    console.log(JSON.stringify(await pulseFetch('/memory/status', { method: 'GET' }), null, 2));
    return;
  }

  if (command === 'export') {
    console.log(JSON.stringify(await pulseFetch('/memory/export', { method: 'GET' }), null, 2));
    return;
  }

  if (command === 'import') {
    const file = getArg('--file');
    if (!file) throw new Error('pulse import requires --file <path>');
    const payload = JSON.parse(readFileSync(resolve(file), 'utf8'));
    console.log(JSON.stringify(await pulseFetch('/memory/import', { body: payload }), null, 2));
    return;
  }

  if (command === 'delete') {
    requireInteractiveDestructiveCLI('delete');
    const id = getArg('--id');
    if (!id) throw new Error('pulse delete requires --id <pulse:id>');
    const result = await pulseFetch('/memory/delete', { body: { id } });
    if (result?.status === 'updated' && result?.reason_code === 'user_deleted' && result?.object_id === id && result?.receipt_id) {
      console.log(`[pulse] deleted ${id}; receipt ${result.receipt_id}`);
    } else if (result?.ok === true && result?.deleted_id === id && Object.keys(result).length === 2) {
      console.log(`[pulse] deleted ${id} (Local Preview)`);
    } else {
      throw new Error('Pulse delete returned no truthful deletion receipt');
    }
    return;
  }

  if (command === 'wipe') {
    requireInteractiveDestructiveCLI('wipe');
    if (getArg('--confirm') !== 'wipe pulse memory') {
      throw new Error('pulse wipe requires --confirm "wipe pulse memory"');
    }
    await pulseFetch('/memory/wipe', { body: { confirm: 'wipe pulse memory' } });
    console.log('[pulse] wiped host-extracted memory');
    return;
  }

  if (command === 'consolidate') {
    // Explicit, opt-in near-duplicate capsule fold (invalidate-not-delete).
    // Default is a dry run — pass --apply to actually mark near-duplicates
    // merged. Nothing is ever deleted; merged rows stay in the store.
    const apply = args.includes('--apply');
    const thresholdRaw = getArg('--threshold');
    const body = { dry_run: !apply };
    if (thresholdRaw !== undefined) {
      const threshold = Number.parseFloat(thresholdRaw);
      if (!Number.isFinite(threshold)) {
        throw new Error('pulse consolidate --threshold expects a number (e.g. 0.90)');
      }
      body.threshold = threshold;
    }
    const scope = getArg('--scope');
    if (scope !== undefined) body.scope = scope;
    const result = await pulseFetch('/memory/consolidate', { body });
    console.log(JSON.stringify(result, null, 2));
    if (!apply) {
      console.log('[pulse] dry run — re-run with --apply to fold near-duplicates');
    }
    return;
  }

  usage();
  throw new Error(`unknown command: ${command}`);
}

main().catch((err) => {
  console.error(`[pulse] ${err instanceof Error ? err.message : String(err)}`);
  process.exit(1);
});
