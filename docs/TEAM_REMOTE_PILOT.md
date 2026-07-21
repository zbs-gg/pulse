# Pulse Team Remote Pilot

## Status

This repository contains the security foundation and current U8 product path for a synthetic-data team-remote pilot. It is not a production deployment and it is not yet the shared workspace for Nikita, Dima, or other real team members.

The current worktree adds a sender-constrained, read-only Commons path to the installed Codex product; independent Desk and Commons continuity composition; safe OAuth refresh; an Owner-only publication Airlock; a separate action-bound Owner CLI for membership, installation binding, project, and grant administration; gateway/store surfaces for metadata-only audit and governed deletion; projection workers and readiness; and default-off deployment templates. Those paths have automated and synthetic coverage. A fresh-machine package install, protected enrollment-registry acceptance, a live Auth0 tenant, external HTTPS ingress, and a real two-person deployment have not been verified. No real team content is authorized by this document.

`npm run test:codex-team-packaging-contract` is deliberately narrower than a product E2E: it proves that the packed artifact contains the signed helper and MCP runtime, that both Codex and Claude composition use the exact signed Commons `project_id`, that the Codex package exposes the Desk plus read-only Commons registry, and that it carries the Airlock-only publication flag. It mocks binding authority, OAuth, remote transport, and the daemon. It is not evidence of a fresh-user install, a packaged Claude onboarding, or live Team continuity. `make verify` runs this contract.

The current local helper is Developer-ID signed and targets Apple Silicon macOS 13+, but it has not been notarized. Packaging therefore fails by default, and npm publication fails even with an override. An explicit `PULSE_ALLOW_UNNOTARIZED_INTERNAL_PREVIEW=1` is accepted only for an unquarantined internal checkout build and must not be redistributed. A notarized artifact is a blocker for ordinary colleague installation.

The release-only `make team-race-release` gate invokes the Team Go packages with `-race -count=1 -timeout 20m`; a default ten-minute timeout is not a passing substitute. `make release-verify` combines normal verification, that explicit race gate, and portable static validation of the deployment templates. It does not claim that systemd units or Caddy have been validated on Linux.

`npm publish` runs `make release-verify` from the repository root before package artifacts are prepared. A publish therefore cannot use the narrower packaging preparation step as a substitute for the packaging contract, Team race suite, or deployment-template verification; ordinary `npm test` and `npm pack` remain bounded development checks.

## Product Activation Base

The Codex-first Personal/Desk/Commons product is built on commit `34cb4b4` from `codex/team-remote-foundation`. Migrations 033-039, Team store identity, request-local principals, pre-retrieval authorization, atomic objects and receipts, projection/deletion fencing, Owner approval, activation, audit, and `fallback:false` behavior remain the authoritative security kernel.

Product activation adds versioned host lifecycle and binding contracts above that kernel. It does not reinterpret a Team store as Personal memory, adopt an existing local database, enable real content, or weaken the synthetic activation gate. Local Preview remains a separate one-person mode; Team remote remains a dedicated store and cannot construct or fall back to standalone storage.

The product branch begins at `codex/pulse-codex-team-memory`. New migrations start only after the frozen 001-039 chain and must declare store-kind applicability and binary floors before executing DDL.

## Three Modes That Must Stay Separate

| Mode | Intended use | Data boundary |
|---|---|---|
| Local Preview | One person's local development and memory | Local `~/.pulse`; no team sharing |
| Team remote foundation | Synthetic verification of multi-principal policy plus the U8 read/Airlock/Owner-operator product path | Dedicated team database; no local fallback |
| Real pilot deployment | A separately approved hosted pilot | New deployment, real IdP, named members, explicit connector consent |

Starting team mode never upgrades or adopts an existing local database. A remote outage never falls back to local storage.

## Interfaces

### Installed Codex member client

The installed Codex MCP composes the bound private Desk with one fixed Team Commons. The two stores are queried independently and are labeled separately in continuity output; a Commons outage is visible and never causes a local fallback or changes the Desk boundary.

Team onboarding is an explicit human binding action. `pulse binding create-team` requires the exact `--commons-project-id project_...` alongside team/store/resource identifiers and the confirmation phrase. That project ID is inside the root-anchored signed binding and is used for Commons status and resume authorization. The local Desk continues to use the canonical `workspace_id`; it is not substituted for the shared project grant. Binding replacement is a journaled, fsynced transition. On startup or the next create, the root anchor deterministically chooses completion or rollback under the same registry lock; malformed or unrelated journal state fails closed.

The installation authorization profile is exactly:

```text
openid offline_access pulse:connect pulse:status pulse:read pulse:audit
```

It rejects `pulse:write`, `pulse:delete`, and `pulse:owner`. The installed Codex MCP exposes only `pulse_team_status`, `pulse_team_recall`, `pulse_team_context_query`, `pulse_team_resume`, and `pulse_team_inspect` for Commons. The broader Team gateway contract also contains member-authorized audit and deletion-status operations, but the installed Codex client does not expose them. Direct `pulse_team_remember` and `pulse_team_graph_delta` publication is absent from the installed product registry and dispatch; delete is also absent from the installed Codex proxy.

Each installation has a P-256 key, a distinct enrollment, a DPoP-bound access token, and a protected credential reference. The gateway validates the access token, exact installation enrollment, proof target/method/token hash, replay state, current membership, client binding, project grant, and object scope on every request. Team responses carry `fallback=false`.

`pulse team status` reports `ready`, `degraded`, `pending`, `missing`, or `stale`. A failed projection cycle is retryable work and therefore reports `pending` while retaining the exact `projection_state=failed` and sorted degradation reasons. Only `ready` exits successfully; enrollment alone is not readiness.

### Human Owner

Membership changes, installation enrollment, grants, revocation, shared deletion, activation, broader audit, and publication approval are not ordinary member MCP tools. Publication is possible only through the browser Airlock: it shows the exact canonical envelope, requires fresh Owner authentication with platform WebAuthn, and binds approval to the exact store, team, publisher, request, and envelope digest. Possession of a shell, loopback access, token, or daemon IPC key is not human approval.

The separate `pulse team owner` operator surface covers login plus exact
member, installation-binding, project, and project-grant mutations. Its
root-owned profile is `/etc/pulse-team/team-owner-profile.json`; the closed
schema, file ownership, permissions, and complete command sequence are in
[`deploy/team/README.md`](../deploy/team/README.md). Each mutation displays the
exact action and target before opening a fresh platform-WebAuthn flow. Its ID
token nonce binds the canonical operation and a random challenge, and the
derived assertion ID is consumed durably once. The first Owner login only emits
an enrollment request: protected registry acceptance/status is still a manual,
deployment-specific gap and therefore blocks real colleague onboarding.

The Airlock starts Authorization Code + PKCE with `prompt=login`, `max_age=0`, `scope=openid pulse:owner`, and this exact ACR:

```text
https://pulse.zbs.gg/acr/airlock-human-presence/v1
```

The Authorization Server must return an ID token whose `acr` equals that value, whose unique `amr` includes `mfa`, and whose exact namespaced claim is:

```json
{
  "https://pulse.zbs.gg/claims/airlock-human-presence/v1": {
    "schema": "pulse.airlock_human_presence.v1",
    "factor": "webauthn-platform",
    "verified_at": 1770000000
  }
}
```

`verified_at` is an integer NumericDate for the platform-WebAuthn ceremony that occurred in this authorization transaction. It is not login time copied from a previous session, enrollment status, a generic MFA flag, or the time an Action happened to execute. The ID token must also bind the exact nonce, client, subject, `auth_time`, and expiry; the access token remains the `pulse:owner` capability authority for the exact `/mcp` audience.

An Auth0 Action/policy is conformant only when it verifies that platform WebAuthn actually completed in the current transaction before emitting the exact ACR and custom claim. If Auth0 cannot prove that fact or cannot emit the exact claims, it must deny the Airlock transaction. SMS, TOTP, email OTP, a remembered browser session, or merely having a passkey enrolled cannot be translated into `factor=webauthn-platform`. The repository tests this claim contract, but it has not yet been verified against a live Auth0 tenant and Action; that live check is an activation blocker.

### Internal daemon

The Go daemon listens only on loopback. Its IPC key authenticates the local channel between the public gateway and daemon; it is never sent to a configurable remote base and never establishes user identity.

### Krisp calls

Krisp is a prospective connector, not part of this foundation. The real pilot must decide which meetings may be imported, who consents, whether Krisp supplies a transcript or summary, and which project scope receives the structured result. Raw transcripts must not be silently captured into Pulse.

## Activation Gate

Real content stays blocked until all of these are demonstrated against a temporary database with synthetic identities:

- exact store and team identity, migration fingerprint, durability, and a single active writer;
- pinned issuer/resource and rotating principal-signing keys;
- current-request principal resolution and per-client binding;
- cross-principal read isolation with no influence on ranking, counts, trace, graph, or resume;
- next-request revocation on an already-open MCP session;
- idempotent writes and retries without duplicate audit or objects;
- generation-fenced deletion through database, queue, projection, and live-cache barriers;
- metadata-only audit with no bearer token, prompt, transcript, summary, secret, or local path;
- daemon outage returns `shared_memory_unavailable` with `fallback=false`;
- negative smoke rejects legacy auth, fallback, route, tool, and malformed-token settings. Migration/store drift, unscoped writers, complete assertion/approval replay matrices, and worker-readiness failures still require a real daemon/store acceptance gate and are not claimed by the current Node-only smoke.
- installed Codex lists only the five read/status/inspection Commons tools and receives a sender-constrained token with no write, delete, or Owner scope; its `pulse:audit` authorization is not surfaced as an installed tool in this increment;
- an Airlock approval succeeds only with the exact fresh platform-WebAuthn ACR/claim contract and the exact human-reviewed envelope;
- a Team-specific embedder is configured and its projection-worker heartbeat is ready; Personal `COHERE_API_KEY` discovery is never reused for Commons.

`make team-remote-daemon-store-acceptance` proves compiled Go, temporary
SQLite, workers, and the Node gateways. Its Owner slice also runs a synthetic
HTTP request through CLI-built canonical approval bytes, a real DPoP sender
proof, signed access and nonce-bound ID tokens, the production authorization
core, Go/SQLite, tamper denial, durable replay denial after daemon restart, and
the membership/binding/grant revocation cascade. It does not exercise external
TLS, a live IdP, protected registry installation, the native helper binary, or
a packaged fresh-machine CLI install. Those remain separate deployment gates.

Activation is explicit. Passing migrations or starting an empty daemon cannot activate a store automatically.

The internal bootstrap sequence is deliberately restart-bound:

1. start the loopback Go daemon in `PULSE_TEAM_OWNER_ADMIN_ONLY=1` against a fresh dedicated data directory;
2. prepare the immutable store/team/Owner intent, obtain recent browser step-up, and execute the one-time approved bootstrap;
3. pin the returned store and team IDs, then restart the Owner daemon so it acquires the v39 writer lease;
4. execute the one-time approved synthetic activation with the acceptance-gate digest;
5. stop the admin-only process and start normal `team-remote`, which refuses an inactive store and mounts MCP plus Owner routes on one listener and one writer lease.

The prebootstrap process cannot activate or perform post-bootstrap mutations. An admin-only process cannot bypass an active writer lease.

## What the Real Pilot Still Needs

The repository now includes [`deploy/team/`](../deploy/team/README.md) as a reviewed shape for two locked OS accounts, systemd credentials, loopback Caddy, health checks, and backup/restore and rollback rehearsals. These assets are default-off and contain negative tripwires for external access, real reads, real publication, and real content. `make team-deploy-static-verify` checks this shape without installing anything. Real systemd/Caddy execution and external deployment remain U10 work; these templates do not deploy or activate those capabilities.

1. Create a separate private pilot repository or deployment configuration based on the reviewed shape; do not put deployment secrets in this public source tree.
2. Configure and live-verify the external Authorization Server, exact HTTPS `/mcp` resource, DPoP issuance, Airlock platform-WebAuthn Action contract, team/store identity, signing-key rotation, backups, and operational owner.
3. Verify the packed fresh-machine Codex onboarding path, then enroll Nikita and Dima as named humans and approve each installation as a distinct sender-constrained client binding.
4. Define project scopes and grant only the projects each participant needs.
5. Run the synthetic acceptance suite in the target deployment before any real content is enabled.
6. Connect Krisp only after meeting selection, consent, retention, and structured-extraction rules are approved.
7. Exercise the Airlock UI with labeled synthetic candidates, then run real two-member dogfood under a separate explicit authorization before making availability or production claims.

## Explicitly Deferred

- automatic ingestion of every call or chat;
- raw transcript storage;
- team-wide wipe;
- direct agent writes or publication outside the exact human-approved Airlock envelope;
- a hosted memory Viewer beyond the bounded Airlock review surface;
- live Auth0/DPoP/Airlock interoperability and external deployment verification;
- a verified fresh-machine Codex package onboarding;
- production SLOs, incident response, billing, or compliance claims;
- a completed two-person pilot.
