import { SUPPORTED_HOST_IDS } from './supported-hosts.js';

const SAFE_REASON = /^[a-z0-9][a-z0-9_]{0,127}$/;

function exactTargets(hosts, registry, context) {
  if (!Array.isArray(hosts) || !registry || typeof registry !== 'object' || Array.isArray(registry) ||
      !context || typeof context !== 'object' || Array.isArray(context)) {
    throw new TypeError('personal_host_inventory_invalid');
  }
  const seen = new Set();
  const targets = [];
  for (const host of hosts) {
    if (!host || typeof host !== 'object' || !SUPPORTED_HOST_IDS.includes(host.host) || seen.has(host.host) ||
        typeof host.detected !== 'boolean' || typeof host.compatible !== 'boolean' ||
        typeof host.activation_target !== 'boolean') {
      throw new TypeError('personal_host_inventory_invalid');
    }
    seen.add(host.host);
    if (!host.activation_target) continue;
    if (!host.compatible) throw new TypeError('personal_host_inventory_invalid');
    const adapter = registry[host.host];
    if (!adapter || typeof adapter.inspect !== 'function' || typeof adapter.activate !== 'function') {
      throw new TypeError('personal_host_registry_invalid');
    }
    targets.push({ host: host.host, detected: host.detected, compatible: host.compatible, adapter });
  }
  if (targets.length === 0) throw new TypeError('personal_host_inventory_invalid');
  return targets;
}

function stableReason(host, value, fallback) {
  const reason = value?.reason_code;
  if (typeof reason === 'string' && SAFE_REASON.test(reason)) return reason;
  return `${host.replaceAll('-', '_')}_${fallback}`;
}

function hostResult(target, status, { activated = false } = {}) {
  const installed = status?.installed === true || status?.ready === true;
  const mcpReady = status?.mcp_ready === true || status?.ready === true;
  const lifecycleReady = status?.lifecycle_ready === true;
  const verified = mcpReady && lifecycleReady;
  const milestones = Array.isArray(status?.milestones) &&
    status.milestones.every((milestone) => typeof milestone === 'string' && SAFE_REASON.test(milestone))
    ? Object.freeze([...new Set(status.milestones)].sort())
    : Object.freeze([]);
  const reloadRequired = mcpReady && !lifecycleReady;
  const fallback = verified ? 'verified' : reloadRequired ? 'lifecycle_required' : 'activation_required';
  const reasonCode = reloadRequired && !/_lifecycle_required$/.test(status?.reason_code ?? '')
    ? `${target.host.replaceAll('-', '_')}_lifecycle_required`
    : stableReason(target.host, status, fallback);
  return Object.freeze({
    host: target.host,
    detected: target.detected,
    compatible: target.compatible,
    installed,
    mcp_ready: mcpReady,
    activated: activated || installed,
    verified,
    lifecycle_ready: lifecycleReady,
    reload_required: reloadRequired,
    milestones,
    reason_code: reasonCode,
  });
}

function activationSummary(hosts) {
  const verified = hosts.filter((host) => host.verified).length;
  return Object.freeze({
    product_ready: verified > 0,
    parity: verified === hosts.length ? 'complete' : verified > 0 ? 'degraded' : 'blocked',
    hosts: Object.freeze(hosts),
  });
}

function priorHostResults(prior, targets) {
  if (prior === undefined) return new Map();
  if (!prior || typeof prior !== 'object' || !Array.isArray(prior.hosts) ||
      !['blocked', 'complete', 'degraded'].includes(prior.parity) || typeof prior.product_ready !== 'boolean') {
    throw new TypeError('personal_host_prior_invalid');
  }
  const targetIDs = new Set(targets.map((target) => target.host));
  const results = new Map();
  for (const host of prior.hosts) {
    if (!host || !targetIDs.has(host.host) || results.has(host.host) ||
        typeof host.detected !== 'boolean' || typeof host.compatible !== 'boolean' ||
        typeof host.installed !== 'boolean' || typeof host.mcp_ready !== 'boolean' ||
        typeof host.activated !== 'boolean' || typeof host.verified !== 'boolean' ||
        typeof host.lifecycle_ready !== 'boolean' || typeof host.reload_required !== 'boolean' ||
        !Array.isArray(host.milestones) || host.milestones.some((value) => !SAFE_REASON.test(value)) ||
        !SAFE_REASON.test(host.reason_code ?? '')) {
      throw new TypeError('personal_host_prior_invalid');
    }
    results.set(host.host, host);
  }
  return results;
}

export async function inspectDetectedPersonalHosts({ context, hosts, registry } = {}) {
  const targets = exactTargets(hosts, registry, context);
  const results = [];
  for (const target of targets) {
    try {
      results.push(hostResult(target, await target.adapter.inspect(context)));
    } catch (error) {
      results.push(hostResult(target, {
        reason_code: SAFE_REASON.test(error?.code ?? '')
          ? error.code
          : `${target.host.replaceAll('-', '_')}_inspection_failed`,
      }));
    }
  }
  return activationSummary(results);
}

export async function activateDetectedPersonalHosts({ context, hosts, registry, prior } = {}) {
  const targets = exactTargets(hosts, registry, context);
  const previous = priorHostResults(prior, targets);
  const attempts = new Map();
  for (const target of targets) {
    let before;
    try {
      const priorResult = previous.get(target.host);
      if (priorResult?.installed === true) {
        attempts.set(target.host, { activated: priorResult.activated });
        continue;
      }
      before = priorResult ? { ready: false } : await target.adapter.inspect(context);
      if (before?.ready === true) {
        attempts.set(target.host, { activated: true });
        continue;
      }
      await target.adapter.activate(context);
      attempts.set(target.host, { activated: true });
    } catch (error) {
      attempts.set(target.host, {
        activated: before?.ready === true,
        reason_code: SAFE_REASON.test(error?.code ?? '')
          ? error.code
          : `${target.host.replaceAll('-', '_')}_activation_failed`,
      });
    }
  }

  // Mutations are serialized above, then every target is re-inspected. A prior
  // verified result is only a skip hint; it is never final readiness evidence.
  const results = [];
  for (const target of targets) {
    const attempt = attempts.get(target.host) ?? {};
    try {
      const current = await target.adapter.inspect(context);
      results.push(hostResult(target, current?.ready === true ? current : {
        ...current,
        reason_code: attempt.reason_code ?? stableReason(target.host, current, 'activation_required'),
      }, { activated: attempt.activated === true }));
    } catch (error) {
      results.push(hostResult(target, {
        reason_code: attempt.reason_code ?? (SAFE_REASON.test(error?.code ?? '')
          ? error.code
          : `${target.host.replaceAll('-', '_')}_inspection_failed`),
      }, { activated: attempt.activated === true }));
    }
  }
  return activationSummary(results);
}
