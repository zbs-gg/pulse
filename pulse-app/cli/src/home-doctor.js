import { SUPPORTED_HOST_IDS } from './supported-hosts.js';

function supported(host) { return SUPPORTED_HOST_IDS.includes(host); }

function validReport(report, host) {
  return report?.target_host === host &&
    ['pulse.personal_live_readiness.v1', 'pulse.supported_host_live_readiness.v1']
      .includes(report?.personal_live_readiness?.schema);
}

export async function selectHomeDoctorReport({ requestedHost, enabledHosts = [], doctorForHost }) {
  if ((requestedHost !== undefined && !supported(requestedHost)) ||
      !Array.isArray(enabledHosts) || enabledHosts.some((host) => !supported(host)) ||
      typeof doctorForHost !== 'function') {
    throw new TypeError('pulse home --host must be claude-code, codex, cursor, or opencode.');
  }
  const hosts = requestedHost === undefined
    ? (enabledHosts.length > 0 ? [...new Set(enabledHosts)] : [...SUPPORTED_HOST_IDS])
    : [requestedHost];
  const inspected = [];
  for (const host of hosts) {
    try {
      const report = await doctorForHost(host);
      if (!validReport(report, host)) throw new TypeError('Pulse doctor returned an invalid Home readiness report.');
      if (requestedHost !== undefined || report.personal_live_readiness.outcome === 'ready') return report;
      inspected.push(report);
    } catch (error) {
      if (requestedHost !== undefined) throw error;
    }
  }
  if (inspected.length > 0) return inspected[0];
  throw new Error('Pulse could not inspect any supported host for Memory Home. Run pulse repair.');
}
