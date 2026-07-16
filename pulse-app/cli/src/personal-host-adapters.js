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
        typeof host.compatible !== 'boolean' || typeof host.activation_target !== 'boolean') {
      throw new TypeError('personal_host_inventory_invalid');
    }
    seen.add(host.host);
    if (!host.activation_target) continue;
    if (!host.compatible) throw new TypeError('personal_host_inventory_invalid');
    const adapter = registry[host.host];
    if (!adapter || typeof adapter.inspect !== 'function' || typeof adapter.activate !== 'function') {
      throw new TypeError('personal_host_registry_invalid');
    }
    targets.push({ host: host.host, adapter });
  }
  if (targets.length === 0) throw new TypeError('personal_host_inventory_invalid');
  return targets;
}

function stableReason(host, value, fallback) {
  const reason = value?.reason_code;
  if (typeof reason === 'string' && SAFE_REASON.test(reason)) return reason;
  return `${host.replaceAll('-', '_')}_${fallback}`;
}

function hostResult(host, status, { activated = false } = {}) {
  const verified = status?.ready === true;
  return Object.freeze({
    host,
    activated: activated || verified,
    verified,
    lifecycle_ready: status?.lifecycle_ready === true,
    reason_code: stableReason(host, status, verified ? 'verified' : 'activation_required'),
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
        typeof host.activated !== 'boolean' || typeof host.verified !== 'boolean' ||
        typeof host.lifecycle_ready !== 'boolean' || !SAFE_REASON.test(host.reason_code ?? '')) {
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
      results.push(hostResult(target.host, await target.adapter.inspect(context)));
    } catch (error) {
      results.push(hostResult(target.host, {
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
      if (priorResult?.verified === true) {
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
      results.push(hostResult(target.host, current?.ready === true ? current : {
        ...current,
        reason_code: attempt.reason_code ?? stableReason(target.host, current, 'activation_required'),
      }, { activated: attempt.activated === true }));
    } catch (error) {
      results.push(hostResult(target.host, {
        reason_code: attempt.reason_code ?? (SAFE_REASON.test(error?.code ?? '')
          ? error.code
          : `${target.host.replaceAll('-', '_')}_inspection_failed`),
      }, { activated: attempt.activated === true }));
    }
  }
  return activationSummary(results);
}
