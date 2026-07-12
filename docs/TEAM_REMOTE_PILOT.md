# Pulse Team Remote Pilot

## Status

This repository contains the security foundation for a synthetic-data team-remote pilot. It is not a production deployment and it is not yet the shared workspace for Nikita, Dima, or other real team members.

The current increment proves identity separation, scoped storage, revocation, contribution-aware deletion, metadata-only audit, and fail-closed remote behavior with synthetic principals and content. Real client onboarding, a hosted pilot repository, a human review UI, Krisp ingestion, and production operations remain separate follow-up work.

## Three Modes That Must Stay Separate

| Mode | Intended use | Data boundary |
|---|---|---|
| Local Preview | One person's local development and memory | Local `~/.pulse`; no team sharing |
| Team remote foundation | Synthetic verification of multi-principal policy | Dedicated team database; no local fallback |
| Real pilot deployment | A separately approved hosted pilot | New deployment, real IdP, named members, explicit connector consent |

Starting team mode never upgrades or adopts an existing local database. A remote outage never falls back to local storage.

## Interfaces

### Team members and agents

Agents use the versioned `pulse_team_*` MCP tools through one canonical HTTPS MCP resource. OAuth grants only a coarse capability; current team membership, client binding, project grants, object scope, and revocation are checked by Pulse on every request.

Legacy local tools are not advertised in team mode. Team responses carry `fallback=false`.

### Human Owner

Membership changes, client registration, grants, revocation, shared deletion, activation, and broader audit are not MCP tools. They require a separate browser step-up and a one-time approval bound to the exact store, team, action, and target. Possession of a shell, loopback access, or the daemon IPC key is not human approval.

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
- negative smoke rejects legacy auth, fallback, route, tool, migration, assertion, and approval settings.

Activation is explicit. Passing migrations or starting an empty daemon cannot activate a store automatically.

The internal bootstrap sequence is deliberately restart-bound:

1. start the loopback Go daemon in `PULSE_TEAM_OWNER_ADMIN_ONLY=1` against a fresh dedicated data directory;
2. prepare the immutable store/team/Owner intent, obtain recent browser step-up, and execute the one-time approved bootstrap;
3. pin the returned store and team IDs, then restart the Owner daemon so it acquires the v39 writer lease;
4. execute the one-time approved synthetic activation with the acceptance-gate digest;
5. stop the admin-only process and start normal `team-remote`, which refuses an inactive store and mounts MCP plus Owner routes on one listener and one writer lease.

The prebootstrap process cannot activate or perform post-bootstrap mutations. An admin-only process cannot bypass an active writer lease.

## What the Real Pilot Still Needs

1. Create a separate private pilot repository or deployment configuration; do not put deployment secrets in this public source tree.
2. Select and configure the external Authorization Server, HTTPS resource URI, team/store identity, signing-key rotation, backups, and operational owner.
3. Enroll Nikita and Dima as named humans, then register each agent installation as a distinct client binding.
4. Define project scopes and grant only the projects each participant needs.
5. Run the synthetic acceptance suite in the target deployment before any real content is enabled.
6. Connect Krisp only after meeting selection, consent, retention, and structured-extraction rules are approved.
7. Add a human review surface and real two-member dogfood before making availability or production claims.

## Explicitly Deferred

- automatic ingestion of every call or chat;
- raw transcript storage;
- team-wide wipe;
- direct writes or promotion to team scope;
- a hosted Viewer or review UI;
- production SLOs, incident response, billing, or compliance claims;
- a completed two-person pilot.
