export type RuntimeMode = 'local-stdio' | 'development-http' | 'team-remote';

export interface TeamRemoteStaticConfig {
  args: readonly string[];
  daemonBaseURL: string;
  engineMode: 'auto' | 'daemon' | 'standalone';
  env: NodeJS.ProcessEnv;
  host: string;
  nodeVersion: string;
  publicBaseURL: string;
  authIssuer: string;
}

export function resolveRuntimeMode(
  value: string | undefined,
  httpRequested: boolean,
): RuntimeMode {
  if (value === undefined || value.trim() === '') {
    return httpRequested ? 'development-http' : 'local-stdio';
  }
  if (value === 'local-stdio') {
    if (httpRequested) {
      throw new Error('PULSE_RUNTIME_MODE=local-stdio cannot use --http');
    }
    return value;
  }
  if (value === 'development-http' || value === 'team-remote') {
    return value;
  }
  throw new Error(
    `invalid PULSE_RUNTIME_MODE: ${value} (use local-stdio, development-http, or team-remote)`,
  );
}

export function assertTeamRemoteStaticConfig(config: TeamRemoteStaticConfig): void {
  const {
    args,
    daemonBaseURL,
    engineMode,
    env,
    host,
    nodeVersion,
    publicBaseURL,
    authIssuer,
  } = config;

  const nodeMajor = Number.parseInt(nodeVersion.split('.')[0] ?? '', 10);
  if (!Number.isInteger(nodeMajor) || nodeMajor < 22) {
    throw new Error(`team-remote requires Node 22 or newer (found ${nodeVersion})`);
  }
  if (engineMode !== 'daemon') {
    throw new Error('team-remote requires PULSE_MCP_MODE=daemon; auto and standalone are forbidden');
  }
  requireHTTPS('PULSE_REMOTE_PUBLIC_BASE_URL', publicBaseURL, true);
  requireHTTPS('PULSE_REMOTE_AUTH_ISSUER', authIssuer, false);
  requireLoopbackURL('PULSE_BASE_URL', daemonBaseURL);

  if (!isNumericLoopbackHost(host)) {
    throw new Error(
      'team-remote preflight may bind only to a numeric loopback before public activation',
    );
  }
  if (env.PULSE_REMOTE_BEARER?.trim()) {
    throw new Error('team-remote refuses static PULSE_REMOTE_BEARER authentication');
  }
  if (enabled(env.PULSE_REMOTE_OAUTH_DEV)) {
    throw new Error('team-remote refuses PULSE_REMOTE_OAUTH_DEV');
  }
  if (enabled(env.PULSE_REMOTE_AUTH_PROXY_MODE) || enabled(env.PULSE_REMOTE_TRUST_AUTH_HEADER)) {
    throw new Error('team-remote refuses trusted proxy bearer passthrough');
  }
  if (
    enabled(env.PULSE_REMOTE_ALLOW_UNAUTHENTICATED) ||
    enabled(env.PULSE_ALLOW_AUTHLESS_PUBLIC) ||
    args.includes('--allow-unauthenticated')
  ) {
    throw new Error('team-remote refuses unauthenticated HTTP shortcuts');
  }
  if (
    args.includes('--sse') ||
    (env.PULSE_MCP_TRANSPORT !== undefined && env.PULSE_MCP_TRANSPORT !== 'streamable-http')
  ) {
    throw new Error('team-remote supports Streamable HTTP only; legacy SSE is forbidden');
  }
  if (enabled(env.PULSE_TEAM_REMOTE_ACTIVATED)) {
    throw new Error('team-remote public activation is unavailable until the synthetic gate lands');
  }
}

function enabled(value: string | undefined): boolean {
  if (value === undefined) {
    return false;
  }
  const normalized = value.trim().toLowerCase();
  return normalized !== '' && normalized !== '0' && normalized !== 'false';
}

function requireHTTPS(name: string, value: string, rootOnly: boolean): void {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error(`team-remote requires ${name} to be an HTTPS URL`);
  }
  if (
    value === '' || value !== value.trim() || url.protocol !== 'https:' ||
    url.username !== '' || url.password !== '' || url.search !== '' || url.hash !== '' ||
    (rootOnly && value !== url.origin)
  ) {
    throw new Error(`team-remote requires ${name} to be an HTTPS URL without credentials`);
  }
}

function requireLoopbackURL(name: string, value: string): void {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error(`team-remote requires ${name} to be a numeric loopback HTTP(S) URL`);
  }
  if (
    !['http:', 'https:'].includes(url.protocol) || !isNumericLoopbackHost(url.hostname) ||
    url.username !== '' || url.password !== '' || url.search !== '' || url.hash !== '' ||
    value !== url.origin
  ) {
    throw new Error(`team-remote requires ${name} to be a numeric loopback HTTP(S) URL`);
  }
}

function isNumericLoopbackHost(host: string): boolean {
  const normalized = host.toLowerCase();
  return normalized === '127.0.0.1' ||
    normalized === '::1' ||
    normalized === '[::1]';
}
