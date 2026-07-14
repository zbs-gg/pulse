# Pulse Codex + Team Day-0 Contract

This document fixes the shared domain contract used by the Go daemon, MCP gateway, Codex plugin, Claude adapter, Viewer, and future Airlock UI. Adapters may translate host-native envelopes, but they must not invent destinations, statuses, authority fields, or alternate memory stores.

## Authority boundary

- Pulse is the sole memory authority.
- The trusted host resolves a canonical workspace binding before model-visible memory is returned.
- A model may propose semantic content. It cannot select a Vault, Team, principal, role, audience, visibility, or destination.
- Agent-facing lifecycle inputs carrying `team_id`, `vault`, `scope`, `role`, `audience`, `visibility`, `principal`, or `workspace` are rejected with `authority_field_forbidden:<field>`.
- Raw prompts, transcripts, generic tool payloads, secrets, credentials, and local paths are not contract fields and are never persisted by this layer.

## Versioned schemas

| Schema | Purpose |
|---|---|
| `pulse.lifecycle_event.v1` | Normalized Codex and Claude lifecycle provenance |
| `pulse.binding.v1` | Host-owned immutable Personal or Team binding decision |
| `pulse.write_receipt.v1` | Truthful pending and terminal private-write result |
| `pulse.context_lease.v1` | Short-lived binding, membership, policy, and object-generation capability |
| `pulse.context.v1` | Structured separation of inert evidence and human-approved practices |

Schema changes require a new version. Unknown authority-bearing fields fail closed rather than being stripped.

## Lifecycle normalization

Native host events normalize to:

- `session_start`
- `turn_start`
- `tool_receipt`
- `pre_compact`
- `subagent_start`
- `subagent_stop`
- `turn_finalize`
- `session_resume`

Every normalized event preserves `host`, `session_id`, `turn_id`, canonical absolute `workspace`, `model`, `source`, and `stop_hook_active`. Codex and Claude provenance remains distinct. The stable idempotency key is the SHA-256 of schema, host, event, session, turn, workspace, and source separated by `0x1f`.

## Binding decision table

| Binding | Allowed reads | Automatic write | Team deployment |
|---|---|---|---|
| Personal | Personal only | Personal | Forbidden |
| Team | Current member Desk + fixed Commons | Desk only | Required and host-owned |

No binding may silently fall back to another Vault, Team, Safe Mode, or standalone storage.

## Write receipts

Statuses are `pending`, `created`, `updated`, `deduplicated`, `canceled`, `rejected`, and `failed`.

- Every result has a receipt ID and immutable destination (`personal` or `desk`).
- `pending` has no canonical object ID.
- `created`, `updated`, and `deduplicated` require an allowed object ID.
- All other statuses forbid an object ID.
- `actual_input_tokens` is legal only with a non-empty `provider_actual` measurement source. Estimated tokens remain separately labelled.

## State transitions

- `pending` may remain pending, be canceled, or commit privately after grace.
- Canceled candidates cannot commit; pending candidates cannot be retrieved, shared, dreamed over, or compiled as mandatory.
- A committed private Desk object may prepare an Airlock intent.
- Prepared Airlock intent may be approved, canceled, or expire. Approved intent may return to prepared after an edit, cancel before flight, or enter flight.
- Remote success with a missing local acknowledgement becomes `remote_committed_local_pending` and reconciles through the original idempotency key.
- Approval is valid only when the approved digest exactly matches the prepared canonical bytes.
- Mandatory context applies only when active and backed by at least one valid evidence ID.

## Context leases and revocation

A context lease binds the exact binding digest, policy epoch, membership generation, object generation, and expiry. Any mismatch or expiry invalidates future Pulse operations. Previously injected text cannot be erased; the lease constrains subsequent effects and forces a clean task restart when stale.

## Canonical envelopes

Go and TypeScript use the same Day-0 canonical JSON profile:

- UTF-8 JSON object root;
- NFC-normalized keys and strings;
- object keys sorted lexicographically;
- duplicate keys, normalization collisions, unknown top-level fields, control characters, trailing data, and unsafe numeric values rejected;
- numbers restricted to signed JavaScript-safe integers;
- digest formatted as `sha256:<lowercase hex>` over the exact canonical bytes.

The UI preview, approval digest, authorization decision, audit entry, and persisted interpretation must reference those same bytes. Reconstructing or reserializing an approved object through a different parser is forbidden.

## Provenance and injection grammar

Shared provenance closes only over Commons objects or explicit Airlock envelopes. Personal or Desk object IDs in a shared dependency graph fail with `private_lineage_forbidden`.

`pulse.context.v1` is structured JSON with separate `evidence` and `practices` arrays. Evidence is inert remembered content; delimiter-like text inside it stays a JSON string and cannot acquire system, tool, or instruction authority. Practices are human-approved declarative context and still cannot grant tools or widen host policy.

## Stable error families

Contract failures use stable machine-readable prefixes such as:

- `authority_field_forbidden`
- `invalid_session_id` / `invalid_turn_id`
- `binding_destination_mismatch`
- `object_id_required` / `object_id_forbidden`
- `provider_measurement_required`
- `context_lease_expired` / `context_lease_stale`
- `airlock_digest_mismatch`
- `mandatory_inactive` / `mandatory_evidence_required`
- `private_lineage_forbidden`
- `canonical_*`

Adapters and UI may add human explanation around these codes, but must not remap a failure to success or claim a write without its truthful receipt.
