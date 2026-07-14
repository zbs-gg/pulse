# Pulse Team deployment assets

These files are a **default-off, synthetic-data-only** operating slice for the
Team remote security foundation and U7 read/Airlock path. They are not a
production deployment, do not enable real team reads, and cannot turn the
current loopback gateway into a public service. The files have not yet been
installed as a live external deployment, exercised against a real Auth0
tenant, or verified through fresh-machine packaged Codex onboarding.

The units deliberately stop when any reserved expansion gate exists. Enabling
external ingress, team reads, publication, or real content requires a later
reviewed implementation; an operator cannot approve those capabilities merely
by changing an environment variable or asking an agent to proceed.

## Host requirements

- a dedicated Linux host with systemd support for `LoadCredential=` and the
  `%d` credential-directory specifier;
- Node.js 22 or newer at `/usr/bin/node` (adjust the unit only after verifying
  the immutable binary path and digest);
- the pinned Pulse Go binary and prebuilt MCP `dist/` from the same release;
- Caddy 2 for the optional loopback TLS endpoint;
- `sqlite3` for the stopped-store integrity and restore rehearsal;
- one separately provisioned Team embedder: a dedicated Cohere credential file
  or an exact local Python/helper/model trio. The Team daemon deliberately
  ignores Personal `COHERE_API_KEY` and `~/.pulse/cohere-key.txt` discovery;
- outbound HTTPS from the gateway only to the pinned Auth0 issuer/JWKS origin;
- outbound HTTPS from the daemon to Cohere only when the dedicated Team Cohere
  path is selected. A local Team embedder needs no Cohere egress.

Run `make team-deploy-static-verify` on the development host first. It is an
executable, portable content check for the default-off units, loopback Caddy
shape, credential boundaries, and fail-closed environment values. It does not
execute systemd or Caddy. Run `systemd-analyze verify` and `caddy validate` on
the target Linux host before installing or enabling anything. That real-host
U10 validation remains outstanding; a successful parse still does not replace
the synthetic acceptance suite.

## Boundary and layout

Use two locked OS accounts and never reuse a Personal/Desk data directory:

| Component | OS account | Writable data | Secrets it may read |
|---|---|---|---|
| Go daemon | `pulse-team-daemon` | `/var/lib/pulse-team-daemon` | its generated IPC key, principal verification keyring, Team embedder credential, and labeled synthetic Airlock candidate |
| MCP gateway | `pulse-team-gateway` | `/var/lib/pulse-team-gateway` | a systemd credential copy of the IPC key, its assertion signing key, and the verification keyring |
| Caddy | distribution-managed Caddy account | none of the Pulse data roots | no Pulse secret |

Install an immutable release at `/opt/pulse-team/releases/<version-or-digest>`
and point `/opt/pulse-team/current` at it. The expected release layout is:

```text
/opt/pulse-team/current/pulse
/opt/pulse-team/current/mcp/dist/index.js
```

The gateway receives credentials in its private systemd credential directory.
It cannot traverse the daemon data directory. Do not put `PULSE_API_KEY`, an IPC
key, a private key, or an Auth0 client secret in an env file, unit, Caddyfile,
shell history, repository, or support bundle.

## Provision without activation

Run account and directory creation under a human-controlled root session:

```bash
useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin pulse-team-daemon
useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin pulse-team-gateway
install -d -m 0750 -o root -g root /etc/pulse-team/gates /etc/pulse-team/secrets
install -d -m 0700 -o pulse-team-daemon -g pulse-team-daemon /etc/pulse-team/synthetic
install -d -m 0700 -o pulse-team-daemon -g pulse-team-daemon /var/lib/pulse-team-daemon
install -d -m 0700 -o pulse-team-gateway -g pulse-team-gateway /var/lib/pulse-team-gateway
```

Copy the service units to `/etc/systemd/system/`. Split `env.example` into
`/etc/pulse-team/daemon.env` and `/etc/pulse-team/gateway.env`, then set each to
`0640 root:<service-group>`. Install the Ed25519 gateway assertion private key
and its public verification keyring at the paths named by the units. Generate
them through the reviewed activation tooling; do not paste key material into
this repository. Install the content-free installation enrollment registry as
a non-symlink mode-0600 file owned by the gateway service account. The Go daemon
creates its own 0600 IPC key in its private data directory on first start.

Install the selected Team Cohere key, if used, and the labeled synthetic
Airlock candidate as regular mode-0600 files owned by `pulse-team-daemon` at
the paths in `daemon.env`. Never place a Personal key or real content there.

## Exact Authorization Server contract

Before any start, verify the pinned OAuth/OIDC contract:

- `PULSE_REMOTE_AUTH_ISSUER` is the exact HTTPS Auth0 issuer, including its
  canonical trailing slash;
- the Auth0 API Identifier is exactly
  `PULSE_REMOTE_PUBLIC_BASE_URL + /mcp`;
- access tokens use RS256, have `typ=at+jwt`, contain `client_id`, `scope`,
  `jti`, `iat`, `exp`, and the exact audience, and live no longer than 900
  seconds;
- installed member authorization-code and refresh responses use
  `token_type=DPoP`; their access tokens contain `cnf.jkt` equal to the
  installation's P-256 public-key thumbprint;
- Pulse scopes, memberships, client bindings, and project grants are explicit;
  no wildcard scope or automatic member enrollment exists.

Auth0 client secrets belong to Auth0 clients, not to the Pulse resource server.
The installed member client is a public Authorization Code + PKCE client. Its
least-privilege deployment profile is:

```text
openid offline_access pulse:connect pulse:status pulse:read pulse:audit
```

The profile must contain every scope above and is rejected if it contains
`pulse:write`, `pulse:delete`, or `pulse:owner`; the deployment must not grant
unneeded additional scopes. Its installed Commons surface is limited to status, recall,
context query, resume, inspect, and own audit. Direct shared writes are not
registered or dispatched; Team publication goes through the separate browser
Airlock only.

### Airlock Owner client and Auth0 Action contract

The Airlock client is a separate public Authorization Code + PKCE client. The
gateway sends `prompt=login`, `max_age=0`, `scope=openid pulse:owner`, and:

```text
acr_values=https://pulse.zbs.gg/acr/airlock-human-presence/v1
```

The resulting ID token is accepted only when all of the following are true:

- `acr` is exactly
  `https://pulse.zbs.gg/acr/airlock-human-presence/v1`;
- `amr` is a unique string array containing `mfa`;
- nonce, `aud`, `azp` when present, `sub`, `auth_time`, `iat`, and `exp` bind
  the current Airlock flow and the same identity as the access token;
- the exact namespaced claim is present:

```json
{
  "https://pulse.zbs.gg/claims/airlock-human-presence/v1": {
    "schema": "pulse.airlock_human_presence.v1",
    "factor": "webauthn-platform",
    "verified_at": 1770000000
  }
}
```

`verified_at` is integer seconds for a platform-WebAuthn ceremony completed in
this authorization transaction. The gateway checks it and `auth_time` against
the flow start with a five-second clock-skew allowance; the browser session is
bounded to five minutes.

The Auth0 Action/policy must inspect authoritative current-transaction
authentication-method evidence and emit the ACR and custom claim only after it
has proved platform WebAuthn. It must deny the transaction when that evidence
is absent or when Auth0 cannot emit the exact claims. It must not translate
SMS, TOTP, email OTP, a remembered session, Action execution time, or passkey
enrollment into `webauthn-platform`. The code and tests define this contract,
but no live Auth0 Action has yet been verified; do not enable external or real
content gates until that verification succeeds.

## Team embedder boundary

Commons retrieval and projection readiness require exactly one Team-specific
embedding dependency:

- `PULSE_TEAM_COHERE_KEY_FILE` points to a root/service-owned, mode-0600
  regular file containing only the Team Cohere key; or
- all three of `PULSE_TEAM_LOCAL_EMBED_PYTHON`,
  `PULSE_TEAM_LOCAL_EMBED_HELPER`, and `PULSE_TEAM_LOCAL_EMBED_MODEL` point to
  separately provisioned absolute paths. Python must be executable; the helper
  must be a regular file; the model may be a file or directory.

Partial configuration fails startup. With no Team embedder, the projection
worker reports `embedding_dependency_not_configured` and operational readiness
stays degraded. Never use a Personal Cohere key or Personal MLX path for
Commons.

## Gates: files, not agent judgment

All services are inactive until a human creates this one positive sentinel:

```text
/etc/pulse-team/gates/enable-synthetic-runtime
```

The following names are reserved **tripwires**, not permission switches:

```text
/etc/pulse-team/gates/allow-external
/etc/pulse-team/gates/allow-team-read
/etc/pulse-team/gates/allow-publication
/etc/pulse-team/gates/allow-real-content
```

If any tripwire exists, both Pulse units refuse to start. A future real pilot
must replace each tripwire with an implemented, tested, human-approved security
contract. The synthetic Airlock described below is not a real-publication
exception. In this slice:

- `external` means non-loopback ingress; the supplied Caddyfile binds loopback;
- `team-read` means granting any real human/agent access to remembered content;
- `publication` means any real promotion/export/share path outside the single
  labeled synthetic Airlock candidate;
- `real-content` means anything other than labeled synthetic fixtures.

Only after reviewing the exact identifiers, empty dedicated store, key files,
and synthetic acceptance manifest should a human create the positive sentinel:

```bash
install -m 000 -o root -g root /dev/null /etc/pulse-team/gates/enable-synthetic-runtime
systemctl daemon-reload
```

Creating the sentinel does not enable the units. `systemctl enable` and
`systemctl start` remain separate human actions.

## Synthetic bootstrap, Airlock, and activation

Keep Caddy loopback-only. The restart-bound ceremony remains:

1. Complete the three Airlock OIDC variables in `gateway.env`, provision an
   Owner client that satisfies the exact Action contract above, and configure
   the daemon's five synthetic publication variables with one canonical,
   labeled synthetic candidate. Partial Airlock configuration fails closed.
2. Add a temporary root-owned systemd drop-in for the daemon with
   `Environment=PULSE_TEAM_OWNER_ADMIN_ONLY=1`; start the daemon and gateway.
3. Through the separate Owner browser step-up surface, bootstrap an empty,
   dedicated Team store using only synthetic identities and labeled synthetic
   content. Pin the returned store/team IDs in both env files.
4. Restart the admin-only daemon so it acquires the pinned store writer lease.
5. Execute the one-time synthetic activation approval bound to the exact
   acceptance-manifest digest.
6. Stop both services, remove the admin-only drop-in, run
   `systemctl daemon-reload`, then start the normal daemon and gateway.
7. Open `/airlock/team-publication`; verify the IdP performed platform WebAuthn,
   compare the exact canonical envelope, approve it, and retain the content-free
   receipt. This proves only the labeled synthetic path.

Do not improvise Owner HTTP calls with the IPC key. Shell access, loopback
access, and possession of a credential are not human approval. Do not create
real memberships, import chats/calls, or connect Krisp in this ceremony.

## Health and readiness

The public gateway health endpoint is intentionally shallow and content-free:

```bash
systemctl is-active pulse-team-daemon pulse-team-gateway
curl --fail --silent --show-error https://pulse-team.synthetic.invalid/health
journalctl -u pulse-team-daemon -u pulse-team-gateway --since '-10 min'
```

The curl command requires local DNS (or `/etc/hosts`) and trust in Caddy's local
CA. A 200 from `/health` proves only that the gateway process is serving. Before
calling the synthetic environment ready, use an approved OAuth client to call
the authenticated Team status/readiness surface and confirm `mode=team-remote`,
`fallback=false`, the exact store/team IDs, active synthetic boundary, current
schema/policy, writer/worker health, and no legacy tools. Run the installed
Codex status check separately and require `status=ready`,
`sender_constrained=true`,
`commons_agent_writes=false`, `airlock_only_publication=true`, and
`fallback=false`. `degraded`, `pending`, `failed`, `missing`, and `stale` are
non-ready and must make the CLI exit non-zero while preserving projection and
degradation reasons. Never print the IPC key to probe daemon health.

## Backup and restore rehearsal

Backups contain team content and inherit the strictest store classification.
Keep them encrypted and access-controlled outside the gateway account. For the
first rehearsal, use only a synthetic store.

1. Stop gateway, then daemon. Confirm both are inactive; never copy a live
   SQLite file while WAL writers may be active.
2. As `pulse-team-daemon`, run `sqlite3 /var/lib/pulse-team-daemon/pulse.db
   'PRAGMA wal_checkpoint(TRUNCATE); PRAGMA integrity_check;'` and require `ok`.
3. Copy `pulse.db` into a timestamped, root-owned encrypted backup location,
   record its SHA-256 digest and the release digest, schema fingerprint,
   expected store/team IDs, and backup time. Do not copy env or secret files.
4. Restore the copy into a new 0700 directory such as
   `/var/lib/pulse-team-restore-rehearsal`; never overwrite the live directory.
   Set ownership to `pulse-team-daemon`, run `PRAGMA integrity_check`, and match
   the recorded digest before opening it.
5. With the live services still stopped, start the same pinned binary against
   the rehearsal directory on a different loopback port using the daemon env
   and a systemd credential copy of the public verification keyring. Confirm
   startup/readiness and the exact synthetic store/team IDs, then stop it.
6. Delete the rehearsal directory through the approved operator procedure,
   restart daemon then gateway, and repeat authenticated readiness. Record the
   rehearsal result without content, credentials, local paths, or subjects.

A crash may leave the writer lease to expire. Do not force-clear it; wait for
the recorded lease TTL before opening the restored copy. A backup is not
accepted until this isolated restore rehearsal succeeds.

## Rollback and forward-fix

Before every release switch, take and rehearse a compatible stopped backup.
Stop gateway before daemon, then atomically repoint `/opt/pulse-team/current`.

Code rollback is allowed only when the prior binary explicitly supports the
current migration fingerprint, store kind, policy/schema versions, and writer
lease contract. Never run a pre-Team binary against a Team store, reverse SQL
manually, or restore an old database merely to match old code; that would lose
accepted writes and audit history.

If compatibility is uncertain or a migration has executed, keep ingress closed
and ship a new immutable forward-fix release. Re-run synthetic startup,
authenticated readiness, negative smoke, backup/restore, and outage recovery
before moving the symlink. The four tripwire gates remain absent throughout.

## Remove this synthetic slice

Stop and disable gateway, then daemon; remove the positive synthetic sentinel;
archive or destroy the synthetic database under the approved retention policy;
remove unit/env/Caddy copies and OS accounts only after credentials and backups
are accounted for. Removing these files does not authorize deletion of any
real Pulse store.
