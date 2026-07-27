#!/usr/bin/env node

import { existsSync, lstatSync, readFileSync, readdirSync } from 'node:fs';
import { basename, dirname, extname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const FORBIDDEN_DIRECTORIES = new Set(['docs', 'memories', 'plans', 'sessions']);
const FORBIDDEN_EXTENSIONS = new Set([
  '.db', '.env', '.jsonl', '.key', '.log', '.pem', '.sqlite', '.sqlite3', '.zip',
]);
const ALLOWED_PUBLIC_KEY = 'release/pulse-release-root.pem';
const SYNTHETIC_HOME_NAMES = new Set([
  'example', 'private', 'pulse', 'runner', 'test', 'tester', 'ubuntu',
]);

// This is the reviewed public npm file boundary. Adding any file to the
// package requires an explicit review and allowlist update.
const ALLOWED_PUBLIC_PACKAGE_PATHS = new Set(`
LICENSE
README.md
package.json
release/README.md
release/personal-preview-manifest.json
release/personal-preview-manifest.schema.json
release/personal-release-artifact-set.schema.json
release/release-snapshot.schema.json
release/pulse-release-root.pem
runtime/embedder-portable/runner.mjs
runtime/embedder-portable/target-runtime/package-lock.json
runtime/embedder-portable/target-runtime/package.json
runtime/embedder/ATTRIBUTION.md
runtime/embedder/LICENSES/Apache-2.0.txt
runtime/embedder/LICENSES/BGE-M3-MIT.txt
runtime/embedder/LICENSES/MLX-MIT.txt
runtime/embedder/README.md
runtime/embedder/fixture-contract.json
runtime/embedder/helper.py
runtime/embedder/pulse_embedder/__init__.py
runtime/embedder/pulse_embedder/protocol.py
runtime/embedder/pulse_embedder/xlm_roberta.py
runtime/embedder/quality_gate.py
runtime/embedder/source-manifest.json
runtime/windows-bootstrap/catalog.json
runtime/windows-bootstrap/win32-arm64/pulse-platform-adapter.exe
runtime/windows-bootstrap/win32-x64/pulse-platform-adapter.exe
scripts/build-embedder-runtime.mjs
scripts/build-npm-production-candidate.mjs
scripts/build-npm-production-inputs.mjs
scripts/build-public-soak-receipt.mjs
scripts/build-gold-promotion-receipt.mjs
scripts/generate-native-support-ledger.mjs
scripts/build-personal-catalog.mjs
scripts/build-personal-release.mjs
scripts/build-plugin-runtime.mjs
scripts/build-portable-embedder-runtime.mjs
scripts/build-portable-embedder-runtime.test.mjs
scripts/build-portable-model.mjs
scripts/build-presence-helper.mjs
scripts/build-product-hook-bundle.mjs
scripts/build-windows-bootstrap-adapter.mjs
scripts/claude-product-e2e.mjs
scripts/codex-product-e2e.mjs
scripts/codex-team-packaging-contract.mjs
scripts/connector-smoke.mjs
scripts/export-portable-model.py
scripts/generate-release-security-evidence.mjs
scripts/git-team-memory-e2e.mjs
scripts/native-universal-matrix.json
scripts/native-universal-matrix.mjs
scripts/native-universal-target.mjs
scripts/native-vendor-session-e2e.mjs
scripts/personal-consolidation-report-e2e.mjs
scripts/personal-native-packed-e2e.mjs
scripts/personal-preview-clean-room.mjs
scripts/personal-preview-interruption-e2e.mjs
scripts/personal-preview-multiharness-e2e.mjs
scripts/personal-preview-release-attestation.mjs
scripts/prepare-preview-vendor.mjs
scripts/publish-r2-release.mjs
scripts/publish-r2-snapshot-refresh.mjs
scripts/refresh-release-snapshot.mjs
scripts/product-release-fixture.mjs
scripts/public-package-audit.mjs
scripts/release-builder-core.mjs
scripts/release-builder-core.test.mjs
scripts/sign-release-manifest.mjs
scripts/target-release-fixture.mjs
scripts/target-release-fixture.test.mjs
scripts/verify-npm-stage-candidate.mjs
scripts/validate-native-evidence-set.mjs
scripts/verify-release-private-key.mjs
src/artifact-installer.js
src/binding-admin.js
src/capture-state.js
src/claude-hooks.js
src/claude-plugin-install.js
src/cli-idempotency.js
src/cli.js
src/codex-hooks.js
src/codex-install.js
src/codex-runtime.js
src/codex-subscription-runner.js
src/consolidation-report.js
src/cursor-hooks.js
src/cursor-install.js
src/demo-corpus.json
src/desktop-target.js
src/git-team-memory.js
src/historical-ingest-protocol.js
src/historical-ingest-schema.js
src/historical-ingest-worker.js
src/historical-ingest.js
src/home-doctor.js
src/host-adapter.js
src/install-journal.js
src/install-plan.js
src/local-supervisor.js
src/managed-embedder-download.js
src/managed-embedder-release.js
src/native-packed-fixture.js
src/personal-authority-profile.js
src/personal-host-adapters.js
src/personal-install-command.js
src/personal-install.js
src/personal-live-readiness.js
src/personal-principal.js
src/personal-runtime-installer.js
src/platform-services.js
src/product-binding-verifier.js
src/product-compositor.js
src/product-hook-entrypoint.bundle.js
src/product-hook-entrypoint.js
src/product-hook-worker.js
src/project-source.js
src/release-attestation.js
src/release-manifest.js
src/remote-auth-errors.js
src/remote-auth-network.js
src/remote-auth-oauth.js
src/remote-auth.js
src/supported-hosts.js
src/target-release-attestation.js
src/team-login.js
src/team-owner-client.js
src/team-owner-login.js
src/team-remote-client.js
src/team-status.js
src/trust-helper.js
src/unassigned-inbox.js
src/windows-bootstrap-adapter.js
src/workspace-binding.js
vendor/pulse-mcp-dist/airlock-browser-gateway.d.ts
vendor/pulse-mcp-dist/airlock-browser-gateway.js
vendor/pulse-mcp-dist/airlock-browser-gateway.js.map
vendor/pulse-mcp-dist/airlock-contracts.d.ts
vendor/pulse-mcp-dist/airlock-contracts.js
vendor/pulse-mcp-dist/airlock-contracts.js.map
vendor/pulse-mcp-dist/canonical-envelope.d.ts
vendor/pulse-mcp-dist/canonical-envelope.js
vendor/pulse-mcp-dist/canonical-envelope.js.map
vendor/pulse-mcp-dist/index.d.ts
vendor/pulse-mcp-dist/index.js
vendor/pulse-mcp-dist/index.js.map
vendor/pulse-mcp-dist/lifecycle-contracts.d.ts
vendor/pulse-mcp-dist/lifecycle-contracts.js
vendor/pulse-mcp-dist/lifecycle-contracts.js.map
vendor/pulse-mcp-dist/oauth-resource.d.ts
vendor/pulse-mcp-dist/oauth-resource.js
vendor/pulse-mcp-dist/oauth-resource.js.map
vendor/pulse-mcp-dist/owner-approval.d.ts
vendor/pulse-mcp-dist/owner-approval.js
vendor/pulse-mcp-dist/owner-approval.js.map
vendor/pulse-mcp-dist/principal-context.d.ts
vendor/pulse-mcp-dist/principal-context.js
vendor/pulse-mcp-dist/principal-context.js.map
vendor/pulse-mcp-dist/runtime-mode.d.ts
vendor/pulse-mcp-dist/runtime-mode.js
vendor/pulse-mcp-dist/runtime-mode.js.map
vendor/pulse-mcp-dist/sender-constrained-auth.d.ts
vendor/pulse-mcp-dist/sender-constrained-auth.js
vendor/pulse-mcp-dist/sender-constrained-auth.js.map
vendor/pulse-mcp-dist/standalone.d.ts
vendor/pulse-mcp-dist/standalone.js
vendor/pulse-mcp-dist/standalone.js.map
vendor/pulse-mcp-dist/team-contracts.d.ts
vendor/pulse-mcp-dist/team-contracts.js
vendor/pulse-mcp-dist/team-contracts.js.map
vendor/pulse-mcp-dist/validation.d.ts
vendor/pulse-mcp-dist/validation.js
vendor/pulse-mcp-dist/validation.js.map
vendor/pulse-mcp-dist/write-receipts.d.ts
vendor/pulse-mcp-dist/write-receipts.js
vendor/pulse-mcp-dist/write-receipts.js.map
vendor/pulse-presence-helper/gg.zbs.pulse.presence-helper
vendor/pulse-preview-source/README.md
vendor/pulse-preview-source/mcp/LICENSE
vendor/pulse-preview-source/mcp/README.md
vendor/pulse-preview-source/mcp/README_DEV_PREVIEW.md
vendor/pulse-preview-source/mcp/package-lock.json
vendor/pulse-preview-source/mcp/package.json
vendor/pulse-preview-source/mcp/scripts/claude-connector-smoke.mjs
vendor/pulse-preview-source/mcp/src/airlock-browser-gateway.ts
vendor/pulse-preview-source/mcp/src/airlock-contracts.ts
vendor/pulse-preview-source/mcp/src/canonical-envelope.ts
vendor/pulse-preview-source/mcp/src/index.ts
vendor/pulse-preview-source/mcp/src/lifecycle-contracts.ts
vendor/pulse-preview-source/mcp/src/oauth-resource.ts
vendor/pulse-preview-source/mcp/src/owner-approval.ts
vendor/pulse-preview-source/mcp/src/principal-context.ts
vendor/pulse-preview-source/mcp/src/runtime-mode.ts
vendor/pulse-preview-source/mcp/src/sender-constrained-auth.ts
vendor/pulse-preview-source/mcp/src/standalone.ts
vendor/pulse-preview-source/mcp/src/team-contracts.ts
vendor/pulse-preview-source/mcp/src/validation.ts
vendor/pulse-preview-source/mcp/src/write-receipts.ts
vendor/pulse-preview-source/mcp/tsconfig.json
vendor/pulse-preview-source/pulse-app/LICENSE
vendor/pulse-preview-source/pulse-app/cmd/pulse/main.go
vendor/pulse-preview-source/pulse-app/cmd/pulse/main_team_runtime.go
vendor/pulse-preview-source/pulse-app/go.mod
vendor/pulse-preview-source/pulse-app/go.sum
vendor/pulse-preview-source/pulse-app/internal/capture/canonical.go
vendor/pulse-preview-source/pulse-app/internal/capture/hash.go
vendor/pulse-preview-source/pulse-app/internal/capture/lifecycle.go
vendor/pulse-preview-source/pulse-app/internal/capture/types.go
vendor/pulse-preview-source/pulse-app/internal/claude/client.go
vendor/pulse-preview-source/pulse-app/internal/claude/router_adapter.go
vendor/pulse-preview-source/pulse-app/internal/config/config.go
vendor/pulse-preview-source/pulse-app/internal/config/vaults.go
vendor/pulse-preview-source/pulse-app/internal/consolidation/claude_mem_source.go
vendor/pulse-preview-source/pulse-app/internal/consolidation/pulse_source.go
vendor/pulse-preview-source/pulse-app/internal/consolidation/report.go
vendor/pulse-preview-source/pulse-app/internal/consolidation/sources.go
vendor/pulse-preview-source/pulse-app/internal/contextquery/service.go
vendor/pulse-preview-source/pulse-app/internal/contextquery/types.go
vendor/pulse-preview-source/pulse-app/internal/embed/cohere.go
vendor/pulse-preview-source/pulse-app/internal/embed/local.go
vendor/pulse-preview-source/pulse-app/internal/erase/erase.go
vendor/pulse-preview-source/pulse-app/internal/expand/local.go
vendor/pulse-preview-source/pulse-app/internal/health/fixture.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/apply.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/checkpoint.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/chunker.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/codex_job.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/codex_records.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/codex_source.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/manager.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/manifest.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/merge.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/retention.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/review.go
vendor/pulse-preview-source/pulse-app/internal/historicalingest/schema/historical_ingest_v1.schema.json
vendor/pulse-preview-source/pulse-app/internal/ingest/dedupe.go
vendor/pulse-preview-source/pulse-app/internal/ingest/handler.go
vendor/pulse-preview-source/pulse-app/internal/model/errors.go
vendor/pulse-preview-source/pulse-app/internal/model/loader.go
vendor/pulse-preview-source/pulse-app/internal/model/provider.go
vendor/pulse-preview-source/pulse-app/internal/model/registry.go
vendor/pulse-preview-source/pulse-app/internal/model/router.go
vendor/pulse-preview-source/pulse-app/internal/model/types.go
vendor/pulse-preview-source/pulse-app/internal/outbox/outbox.go
vendor/pulse-preview-source/pulse-app/internal/platform/lock.go
vendor/pulse-preview-source/pulse-app/internal/platform/platform.go
vendor/pulse-preview-source/pulse-app/internal/platform/private_posix.go
vendor/pulse-preview-source/pulse-app/internal/platform/private_windows.go
vendor/pulse-preview-source/pulse-app/internal/platform/process_darwin.go
vendor/pulse-preview-source/pulse-app/internal/platform/process_linux.go
vendor/pulse-preview-source/pulse-app/internal/platform/process_windows.go
vendor/pulse-preview-source/pulse-app/internal/platform/signals_posix.go
vendor/pulse-preview-source/pulse-app/internal/platform/signals_windows.go
vendor/pulse-preview-source/pulse-app/internal/prompt/builder.go
vendor/pulse-preview-source/pulse-app/internal/providers/anthropic/client.go
vendor/pulse-preview-source/pulse-app/internal/providers/doinference/client.go
vendor/pulse-preview-source/pulse-app/internal/providers/openaicompat/client.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/access_boost.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/bm25.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/emotion_role.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/graph_retrieve.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/hybrid.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/router.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/state.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/state_fit.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/state_tag_boost.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/team_context.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/team_engine.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/team_resume.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/team_types.go
vendor/pulse-preview-source/pulse-app/internal/retrieve/v3boosts.go
vendor/pulse-preview-source/pulse-app/internal/server/assets/anime.umd.min.js
vendor/pulse-preview-source/pulse-app/internal/server/assets/animejs-LICENSE.md
vendor/pulse-preview-source/pulse-app/internal/server/consolidation_report.go
vendor/pulse-preview-source/pulse-app/internal/server/continuity.go
vendor/pulse-preview-source/pulse-app/internal/server/continuity_delivery.go
vendor/pulse-preview-source/pulse-app/internal/server/git_team_memory_index.go
vendor/pulse-preview-source/pulse-app/internal/server/git_team_memory_review.go
vendor/pulse-preview-source/pulse-app/internal/server/graph_export.go
vendor/pulse-preview-source/pulse-app/internal/server/handlers.go
vendor/pulse-preview-source/pulse-app/internal/server/health.go
vendor/pulse-preview-source/pulse-app/internal/server/home_binding_verifier.go
vendor/pulse-preview-source/pulse-app/internal/server/home_routes.go
vendor/pulse-preview-source/pulse-app/internal/server/host_lifecycle_readiness.go
vendor/pulse-preview-source/pulse-app/internal/server/historical_ingest.go
vendor/pulse-preview-source/pulse-app/internal/server/historical_ingest_review.go
vendor/pulse-preview-source/pulse-app/internal/server/local_compositor.go
vendor/pulse-preview-source/pulse-app/internal/server/memory.go
vendor/pulse-preview-source/pulse-app/internal/server/memory_home.go
vendor/pulse-preview-source/pulse-app/internal/server/memory_presentation.go
vendor/pulse-preview-source/pulse-app/internal/server/personal_live_readiness.go
vendor/pulse-preview-source/pulse-app/internal/server/principal_context.go
vendor/pulse-preview-source/pulse-app/internal/server/product_binding_request.go
vendor/pulse-preview-source/pulse-app/internal/server/privileged_ui_security.go
vendor/pulse-preview-source/pulse-app/internal/server/project_source.go
vendor/pulse-preview-source/pulse-app/internal/server/security_event.go
vendor/pulse-preview-source/pulse-app/internal/server/semantic_delta.go
vendor/pulse-preview-source/pulse-app/internal/server/server.go
vendor/pulse-preview-source/pulse-app/internal/server/team_admin.go
vendor/pulse-preview-source/pulse-app/internal/server/team_deletion.go
vendor/pulse-preview-source/pulse-app/internal/server/team_errors.go
vendor/pulse-preview-source/pulse-app/internal/server/team_graph_delta.go
vendor/pulse-preview-source/pulse-app/internal/server/team_memory.go
vendor/pulse-preview-source/pulse-app/internal/server/team_owner_admin.go
vendor/pulse-preview-source/pulse-app/internal/server/team_owner_mutations.go
vendor/pulse-preview-source/pulse-app/internal/server/team_owner_queries.go
vendor/pulse-preview-source/pulse-app/internal/server/team_owner_shared_delete.go
vendor/pulse-preview-source/pulse-app/internal/server/team_owner_step_up.go
vendor/pulse-preview-source/pulse-app/internal/server/team_publication.go
vendor/pulse-preview-source/pulse-app/internal/server/team_read.go
vendor/pulse-preview-source/pulse-app/internal/server/team_read_contract.go
vendor/pulse-preview-source/pulse-app/internal/server/team_router.go
vendor/pulse-preview-source/pulse-app/internal/server/turn_finalize.go
vendor/pulse-preview-source/pulse-app/internal/server/viewer_session.go
vendor/pulse-preview-source/pulse-app/internal/store/assertions.go
vendor/pulse-preview-source/pulse-app/internal/store/capsule_dedup.go
vendor/pulse-preview-source/pulse-app/internal/store/claim_resolver.go
vendor/pulse-preview-source/pulse-app/internal/store/continuity.go
vendor/pulse-preview-source/pulse-app/internal/store/continuity_receipts.go
vendor/pulse-preview-source/pulse-app/internal/store/git_team_memory_approval.go
vendor/pulse-preview-source/pulse-app/internal/store/git_team_memory_index.go
vendor/pulse-preview-source/pulse-app/internal/store/git_team_memory_publication.go
vendor/pulse-preview-source/pulse-app/internal/store/git_team_memory_review.go
vendor/pulse-preview-source/pulse-app/internal/store/historical_ingest.go
vendor/pulse-preview-source/pulse-app/internal/store/material_graph.go
vendor/pulse-preview-source/pulse-app/internal/store/memory_capsule.go
vendor/pulse-preview-source/pulse-app/internal/store/memory_home.go
vendor/pulse-preview-source/pulse-app/internal/store/memory_home_delivery_query.go
vendor/pulse-preview-source/pulse-app/internal/store/memory_home_economy_projection.go
vendor/pulse-preview-source/pulse-app/internal/store/memory_tray.go
vendor/pulse-preview-source/pulse-app/internal/store/personal_scope.go
vendor/pulse-preview-source/pulse-app/internal/store/migrations/001_outbox.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/002_context.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/003_observations.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/004_extraction.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/005_graph.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/006_phase_1.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/007_phase_2.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/008_consolidation.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/009_safety.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/010_self_entity.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/011_embeddings.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/012_graph_snapshots.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/013_event_embeddings.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/014_belief_vocabulary.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/015_emotions_chains.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/016_entity_subkinds.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/017_atomic_facts.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/018_domain_marker.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/019_feed_signals.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/020_fts5_lexical.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/021_event_state_fields.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/022_memory_capsules.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/023_continuity.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/024_review_insights.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/025_assertions_scope.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/026_claim_resolution_meta.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/027_claim_decisions.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/028_procedures.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/029_access_frequency.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/030_assertions_mention_count.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/031_capsule_consolidation.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/032_capsule_events.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/033_team_identity.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/034_team_object_policy.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/035_team_memory.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/036_team_graph_delta.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/037_team_semantic_materializations.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/038_team_deletion.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/039_team_owner_activation.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/040_store_identity.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/041_memory_tray_receipts.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/042_team_publication_desk_intents.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/043_team_publication_commons_receipts.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/044_team_worker_heartbeats.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/045_memory_presentation_receipts.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/046_continuity_delivery_receipts.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/047_memory_tray_pending_home_index.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/048_git_team_memory_review.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/049_git_team_memory_hook_approval.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/050_git_team_memory_publication.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/051_git_team_memory_index.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/052_cursor_continuity_delivery.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/053_personal_memory_scope.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/054_personal_project_labels.sql
vendor/pulse-preview-source/pulse-app/internal/store/migrations/055_personal_historical_ingest.sql
vendor/pulse-preview-source/pulse-app/internal/store/procedures.go
vendor/pulse-preview-source/pulse-app/internal/store/product_memory_wipe.go
vendor/pulse-preview-source/pulse-app/internal/store/project_source.go
vendor/pulse-preview-source/pulse-app/internal/store/projection_jobs.go
vendor/pulse-preview-source/pulse-app/internal/store/projection_worker_health.go
vendor/pulse-preview-source/pulse-app/internal/store/schema.go
vendor/pulse-preview-source/pulse-app/internal/store/semantic_delta.go
vendor/pulse-preview-source/pulse-app/internal/store/store.go
vendor/pulse-preview-source/pulse-app/internal/store/team_assertion_guard.go
vendor/pulse-preview-source/pulse-app/internal/store/team_audit.go
vendor/pulse-preview-source/pulse-app/internal/store/team_authorization.go
vendor/pulse-preview-source/pulse-app/internal/store/team_deletion.go
vendor/pulse-preview-source/pulse-app/internal/store/team_embedding_projection_input.go
vendor/pulse-preview-source/pulse-app/internal/store/team_graph_delta.go
vendor/pulse-preview-source/pulse-app/internal/store/team_identity.go
vendor/pulse-preview-source/pulse-app/internal/store/team_memory_projection.go
vendor/pulse-preview-source/pulse-app/internal/store/team_metadata.go
vendor/pulse-preview-source/pulse-app/internal/store/team_objects.go
vendor/pulse-preview-source/pulse-app/internal/store/team_owner_admin.go
vendor/pulse-preview-source/pulse-app/internal/store/team_owner_approval.go
vendor/pulse-preview-source/pulse-app/internal/store/team_owner_queries.go
vendor/pulse-preview-source/pulse-app/internal/store/team_policy.go
vendor/pulse-preview-source/pulse-app/internal/store/team_publication.go
vendor/pulse-preview-source/pulse-app/internal/store/team_publication_approval.go
vendor/pulse-preview-source/pulse-app/internal/store/team_publication_remote.go
vendor/pulse-preview-source/pulse-app/internal/store/team_read_repository.go
vendor/pulse-preview-source/pulse-app/internal/store/team_semantic_embedding_projection.go
vendor/pulse-preview-source/pulse-app/internal/store/team_semantic_projection.go
vendor/pulse-preview-source/pulse-app/internal/store/team_semantic_readiness.go
vendor/pulse-preview-source/pulse-app/internal/store/team_semantic_structured_projection.go
vendor/pulse-preview-source/pulse-app/internal/teamauth/model.go
vendor/pulse-preview-source/pulse-app/internal/teamauth/policy.go
vendor/pulse-preview-source/pulse-app/internal/teamjobs/deletion.go
vendor/pulse-preview-source/pulse-app/internal/teamjobs/embedding_projection.go
vendor/pulse-preview-source/pulse-app/internal/teamjobs/projection.go
vendor/pulse-preview-source/pulse-app/internal/teamjobs/projection_materializer.go
vendor/pulse-preview-source/pulse-app/internal/teamread/repository.go
vendor/pulse-preview-source/pulse-app/internal/teamread/service.go
vendor/pulse-preview-source/pulse-app/internal/unassigned/inbox.go
vendor/pulse-preview-source/pulse-app/internal/userpresence/enhanced.go
vendor/pulse-preview-source/pulse-app/internal/userpresence/enhanced_platform.go
vendor/pulse-preview-source/pulse-app/internal/userpresence/enhanced_platform_darwin.go
vendor/pulse-preview-source/pulse-app/internal/userpresence/enhanced_platform_other.go
vendor/pulse-preview-source/pulse-app/internal/userpresence/presence.go
vendor/pulse-preview-source/pulse-app/internal/userpresence/presence_darwin.go
vendor/pulse-preview-source/pulse-app/internal/userpresence/presence_other.go
`.trim().split('\n'));

const PUBLIC_MCP_SCRIPT_NAMES = [
  'build',
  'start',
  'dev',
  'smoke:claude-connector',
  'prepack',
  'prepublishOnly',
];

const PLACEHOLDER_CREDENTIAL_MARKERS = [
  'dummy', 'example', 'fixture', 'legacy', 'placeholder', 'redacted', 'removed', 'sample', 'test', 'your',
];

const CONCRETE_CREDENTIAL_PATTERNS = [
  ['bearer authorization', /\bauthorization\s*[:=]\s*["']?bearer\s+(?!\$\{|<)[A-Za-z0-9._~+\/-]{12,}/i],
  ['GitHub token', /\bghp_[A-Za-z0-9]{20,}\b/],
  ['Slack bot token', /\bxoxb-[A-Za-z0-9-]{10,}\b/i],
  ['AWS access key', /\bAKIA[A-Z0-9]{16}\b/],
];

function packageRelativePath(root, path) {
  return relative(root, path).split(sep).join('/');
}

export function publicMcpPackageManifest(sourcePackageJSON) {
  const scripts = {};
  for (const name of PUBLIC_MCP_SCRIPT_NAMES) {
    const command = sourcePackageJSON?.scripts?.[name];
    if (typeof command !== 'string' || command.trim() === '') {
      throw new Error(`source MCP package is missing required public script: ${name}`);
    }
    scripts[name] = command;
  }
  return {
    ...sourcePackageJSON,
    files: [
      'dist',
      'src',
      'scripts/claude-connector-smoke.mjs',
      'README.md',
      'README_DEV_PREVIEW.md',
      'LICENSE',
    ],
    scripts,
    private: true,
  };
}

function assertSafePackagePath(relativePath, isDirectory) {
  const segments = relativePath.toLowerCase().split('/');
  if (
    basename(relativePath).toLowerCase() === 'agents.md'
    || segments.some((segment) => FORBIDDEN_DIRECTORIES.has(segment))
  ) {
    throw new Error(`forbidden package path: ${relativePath}`);
  }
  if (isDirectory) {
    return;
  }
  if (!ALLOWED_PUBLIC_PACKAGE_PATHS.has(relativePath)) {
    throw new Error(`unexpected public package path: ${relativePath}`);
  }
  const extension = extname(relativePath).toLowerCase();
  if (FORBIDDEN_EXTENSIONS.has(extension) && relativePath !== ALLOWED_PUBLIC_KEY) {
    throw new Error(`forbidden package path: ${relativePath}`);
  }
}

function assertNoPersonalHomePath(text, relativePath) {
  const patterns = [
    /\/Users\/([A-Za-z0-9._-]+)/g,
    /[A-Za-z]:\\Users\\([A-Za-z0-9._-]+)\\/g,
  ];
  for (const pattern of patterns) {
    for (const match of text.matchAll(pattern)) {
      if (!SYNTHETIC_HOME_NAMES.has(match[1].toLowerCase())) {
        throw new Error(`personal filesystem path in package file: ${relativePath}`);
      }
    }
  }
}

function looksLikePlaceholderCredential(value) {
  const normalized = value.toLowerCase();
  return PLACEHOLDER_CREDENTIAL_MARKERS.some((marker) => normalized.includes(marker));
}

function assertNoCredentialAssignment(text, relativePath) {
  const label = String.raw`(?:token|api[_ -]?key|password|private[_ -]?key)`;
  const quoted = new RegExp(String.raw`\b${label}\b\s*[:=]\s*(["'])([^"'\r\n]{8,})\1`, 'gi');
  for (const match of text.matchAll(quoted)) {
    if (!looksLikePlaceholderCredential(match[2])) {
      throw new Error(`release credential in package file: ${relativePath}`);
    }
  }
  const bare = new RegExp(String.raw`\b${label}\b\s*[:=]\s*([A-Za-z0-9_+/=-]{16,})`, 'gi');
  for (const match of text.matchAll(bare)) {
    const value = match[1];
    if (!looksLikePlaceholderCredential(value) && /[0-9_+/=-]/.test(value)) {
      throw new Error(`release credential in package file: ${relativePath}`);
    }
  }
}

function assertAdvertisedNodeScriptsExist(packageRoot, relativePath, packageJSON) {
  const packageDirectory = dirname(join(packageRoot, relativePath));
  const commands = Object.values(packageJSON?.scripts ?? {}).filter((command) => typeof command === 'string');
  for (const command of commands) {
    for (const match of command.matchAll(/(?:^|\s)node\s+(scripts\/[A-Za-z0-9._/-]+)/g)) {
      if (!existsSync(join(packageDirectory, match[1]))) {
        throw new Error(`advertised package script is missing: ${relativePath} -> ${match[1]}`);
      }
    }
  }
}

function assertNoPrivateContent(bytes, relativePath) {
  const text = bytes.toString('utf8');
  if (/-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/.test(text)) {
    throw new Error(`private key in package file: ${relativePath}`);
  }
  if (/\b(?:sk|rk|pk)_[A-Za-z0-9_-]{20,}\b|\bsk-[A-Za-z0-9_-]{20,}\b/.test(text)) {
    throw new Error(`secret token in package file: ${relativePath}`);
  }
  for (const [kind, pattern] of CONCRETE_CREDENTIAL_PATTERNS) {
    if (pattern.test(text)) {
      throw new Error(`${kind} in package file: ${relativePath}`);
    }
  }
  assertNoCredentialAssignment(text, relativePath);
  assertNoPersonalHomePath(text, relativePath);
  for (const match of text.matchAll(/[A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})/g)) {
    const domain = match[1].toLowerCase();
    if (!['example.com', 'example.org', 'example.test'].includes(domain)) {
      throw new Error(`email address in package file: ${relativePath}`);
    }
  }
  if (relativePath === ALLOWED_PUBLIC_KEY) {
    if (!text.startsWith('-----BEGIN PUBLIC KEY-----\n') || text.includes('PRIVATE KEY')) {
      throw new Error('release root must contain one public verification key');
    }
  }
}

export function auditPublicPackageRoot(packageRoot) {
  const root = resolve(packageRoot);
  const stack = [root];
  let files = 0;
  let bytes = 0;
  while (stack.length > 0) {
    const directory = stack.pop();
    for (const name of readdirSync(directory).sort()) {
      const path = join(directory, name);
      const relativePath = packageRelativePath(root, path);
      const info = lstatSync(path);
      if (info.isSymbolicLink()) {
        throw new Error(`forbidden package symlink: ${relativePath}`);
      }
      assertSafePackagePath(relativePath, info.isDirectory());
      if (info.isDirectory()) {
        stack.push(path);
        continue;
      }
      if (!info.isFile()) {
        throw new Error(`forbidden package entry: ${relativePath}`);
      }
      const content = readFileSync(path);
      assertNoPrivateContent(content, relativePath);
      if (basename(relativePath) === 'package.json') {
        let packageJSON;
        try {
          packageJSON = JSON.parse(content.toString('utf8'));
        } catch {
          throw new Error(`invalid package manifest in public package: ${relativePath}`);
        }
        assertAdvertisedNodeScriptsExist(root, relativePath, packageJSON);
      }
      files += 1;
      bytes += content.length;
    }
  }
  return { files, bytes, content_free: true };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const packageRoot = process.argv[2];
  if (!packageRoot) {
    throw new Error('usage: public-package-audit.mjs <unpacked-package-root>');
  }
  process.stdout.write(`${JSON.stringify(auditPublicPackageRoot(packageRoot))}\n`);
}
