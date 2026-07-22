import { runHistoricalIngestUnit } from './codex-subscription-runner.js';

const JOB_ID = /^job_[a-f0-9]{16,64}$/;
const DIGEST = /^[a-f0-9]{64}$/;
const LEASE = /^lease_[a-z0-9]{16,64}$/;
const UNIT_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;
const SOURCE_ALIAS = /^source_[a-f0-9]{16,64}$/;
const UNSAFE_EVIDENCE = /(?:\/Users\/|\/home\/|\\Users\\|api[_-]?key|access[_-]?token|authorization\s*:)/i;

export async function runHistoricalIngestWorker({
  jobID,
  request,
  qualification,
  runUnit = runHistoricalIngestUnit,
  maximumUnits = 10_000,
	signal,
}) {
  if (!JOB_ID.test(jobID ?? '') || typeof request !== 'function' ||
      !qualification?.live_model_qualified || !DIGEST.test(qualification.contract_digest ?? '') ||
      !Number.isSafeInteger(maximumUnits) || maximumUnits < 1) {
    throw new Error('historical_worker_contract_invalid');
  }
  let processed = 0;
  while (processed < maximumUnits) {
		if (signal?.aborted) throw signal.reason ?? new Error('historical_worker_interrupted');
    const response = await request('POST', `/memory/historical-ingest/jobs/${jobID}/lease`, {});
    if (response?.done) {
      const state = response.status?.state;
      if (!['manifest_ready', 'nothing_to_import'].includes(state)) throw new Error('historical_worker_terminal_invalid');
      return { state, accepted_units: response.status.accepted_units, processed_units: processed };
    }
    const current = assertWorkerLease(response, jobID, qualification.contract_digest);
    try {
      await runUnit({
        prompt: current.trusted_prompt,
        evidence: current.evidence,
        expectedJobID: jobID,
        expectedSnapshotDigest: current.unit.snapshot_digest,
        egressAuthorized: true,
        qualification,
			signal,
        acceptResult: async (manifest, receipt) => {
          await request('POST', `/memory/historical-ingest/jobs/${jobID}/submit`, {
            unit_id: current.unit.id,
            lease_token: current.lease_token,
            runner_contract_digest: current.runner_contract_digest,
            result: {
              schema_version: manifest.schema_version,
              work_unit_id: current.unit.id,
              evidence_digest: current.unit.evidence_digest,
              items: manifest.items,
              zero_material: manifest.items.length === 0,
            },
            usage: {
              input_tokens: receipt.usage.input_tokens,
              cached_input_tokens: receipt.usage.cached_input_tokens,
              output_tokens: receipt.usage.output_tokens,
              reasoning_tokens: receipt.usage.reasoning_output_tokens,
            },
          });
        },
      });
      processed += 1;
    } catch (error) {
      if (error?.code !== 'paused_quota') throw error;
      const status = await request('POST', `/memory/historical-ingest/jobs/${jobID}/quota`, {
        unit_id: current.unit.id,
        lease_token: current.lease_token,
      });
      return { state: 'paused_quota', accepted_units: status.accepted_units, processed_units: processed };
    }
  }
  throw new Error('historical_worker_unit_budget_exhausted');
}

export function assertWorkerLease(value, expectedJobID, expectedContractDigest) {
  if (!value || value.schema !== 'pulse.historical_ingest.worker_lease.v1' ||
      value.job_id !== expectedJobID || !LEASE.test(value.lease_token ?? '') ||
      !DIGEST.test(value.runner_contract_digest ?? '') || value.runner_contract_digest !== expectedContractDigest ||
      !DIGEST.test(value.source_snapshot_digest ?? '') || value.unit?.snapshot_digest !== value.source_snapshot_digest ||
      !Number.isSafeInteger(value.checkpoint_generation) || value.checkpoint_generation < 1 ||
      Number.isNaN(Date.parse(value.expires_at)) || typeof value.trusted_prompt !== 'string' || value.trusted_prompt.length < 1 || value.trusted_prompt.length > 32_768 ||
      typeof value.evidence !== 'string' || value.evidence.length > 4 * 1024 * 1024 || UNSAFE_EVIDENCE.test(value.evidence)) {
    throw new Error('historical_worker_lease_invalid');
  }
  const unit = value.unit;
  if (!unit || !UNIT_ID.test(unit.id ?? '') || !UNIT_ID.test(unit.root_id ?? '') ||
      !DIGEST.test(unit.snapshot_digest ?? '') || !DIGEST.test(unit.evidence_digest ?? '') ||
      !Number.isSafeInteger(unit.ordinal) || unit.ordinal < 0 || !Array.isArray(unit.source_aliases) ||
      unit.source_aliases.length < 1 || unit.source_aliases.some((alias) => !SOURCE_ALIAS.test(alias))) {
    throw new Error('historical_worker_unit_invalid');
  }
  return value;
}
