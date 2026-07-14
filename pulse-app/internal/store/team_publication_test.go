package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type airlockAdversarialFixture struct {
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
	Accept  bool     `json:"accept"`
}

func committedDeskObject(t *testing.T, desk *Store) MemoryWriteReceipt {
	t.Helper()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	prepared, err := desk.FinalizeTurn(trayFinalizeRequest("Publish only this reviewed team memory."), now, time.Second)
	if err != nil {
		t.Fatalf("prepare desk memory: %v", err)
	}
	committed, err := desk.CommitMemoryTrayCandidate(
		prepared.Receipts[0].CandidateID, prepared.Receipts[0].CandidateVersion, now.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("commit desk memory: %v", err)
	}
	return committed
}

func publicationFixture(t *testing.T, desk *Store) TeamPublicationPrepareRequest {
	t.Helper()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	desk.clock = func() time.Time { return now }
	object := committedDeskObject(t, desk)
	clientKey := strings.Repeat("c", 64)
	envelope := []byte(fmt.Sprintf(
		`{"action":"team.commons.publish","client_key":%q,"content":"Use the reviewed team rule.","deployment_id":"deployment_zbs","metadata":{"kind":"decision","tags":["pulse","team"]},"policy_epoch":7,"publication_key":"airlock_prepare_01","schema":"pulse.team.airlock_envelope.v1","source_timestamp":"2026-07-14T12:00:00.000Z","store_id":"store_commons_zbs","target_id":"team_zbs","target_kind":"commons","team_id":"team_zbs","writer_id":"writer_zbs","writer_principal_id":"principal_nik"}`,
		clientKey,
	))
	digest := sha256.Sum256(envelope)
	return TeamPublicationPrepareRequest{
		SourceObjectID: object.ObjectID, SourceContentDigest: object.ContentDigest,
		DeploymentID: "deployment_zbs", RemoteStoreID: "store_commons_zbs",
		TeamID: "team_zbs", PolicyEpoch: 7, WriterPrincipalID: "principal_nik",
		ClientKey: clientKey, WriterID: "writer_zbs", CanonicalEnvelope: envelope,
		EnvelopeDigest: fmt.Sprintf("%x", digest), IdempotencyKey: "airlock_prepare_01",
		ExpiresAt: time.Date(2026, 7, 14, 12, 5, 0, 0, time.UTC),
	}
}

func TestMigration042CreatesDisclosureIntentsOnlyInDeskStores(t *testing.T) {
	desk, _ := openDeskTrayStore(t)
	personal, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_test")
	if err != nil {
		t.Fatal(err)
	}
	defer personal.Close()
	commons, err := OpenTeam(filepath.Join(t.TempDir(), "commons.db"), reviewTeamOptions(testBootstrapRoot()))
	if err != nil {
		t.Fatal(err)
	}
	defer commons.Close()

	for name, fixture := range map[string]struct {
		store       *Store
		disposition string
		table       int
	}{
		"desk": {desk, "applied", 1}, "personal": {personal, "skipped", 0}, "commons": {commons, "skipped", 0},
	} {
		t.Run(name, func(t *testing.T) {
			var disposition string
			if err := fixture.store.DB().QueryRow(`
				SELECT disposition FROM schema_migration_applicability WHERE version=42`,
			).Scan(&disposition); err != nil {
				t.Fatalf("migration disposition: %v", err)
			}
			if disposition != fixture.disposition {
				t.Fatalf("disposition=%q want=%q", disposition, fixture.disposition)
			}
			var tables int
			if err := fixture.store.DB().QueryRow(`
				SELECT count(*) FROM sqlite_master WHERE type='table' AND name='team_publication_intents'`,
			).Scan(&tables); err != nil || tables != fixture.table {
				t.Fatalf("publication table=%d want=%d err=%v", tables, fixture.table, err)
			}
		})
	}
}

func TestMigration043CreatesPublicationAuthorityOnlyInCommonsStores(t *testing.T) {
	desk, _ := openDeskTrayStore(t)
	personal, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_publication")
	if err != nil {
		t.Fatal(err)
	}
	defer personal.Close()
	commons, err := OpenTeam(filepath.Join(t.TempDir(), "commons.db"), reviewTeamOptions(testBootstrapRoot()))
	if err != nil {
		t.Fatal(err)
	}
	defer commons.Close()

	for name, fixture := range map[string]struct {
		store       *Store
		disposition string
		tables      int
	}{
		"desk": {desk, "skipped", 0}, "personal": {personal, "skipped", 0}, "commons": {commons, "applied", 3},
	} {
		t.Run(name, func(t *testing.T) {
			var disposition string
			if err := fixture.store.DB().QueryRow(`
				SELECT disposition FROM schema_migration_applicability WHERE version=43`,
			).Scan(&disposition); err != nil {
				t.Fatal(err)
			}
			if disposition != fixture.disposition {
				t.Fatalf("disposition=%q want=%q", disposition, fixture.disposition)
			}
			var tables int
			if err := fixture.store.DB().QueryRow(`
				SELECT count(*) FROM sqlite_master
				 WHERE type='table' AND name IN (
				     'team_publication_approvals','team_publication_receipts',
				     'team_publication_receipt_payloads')`,
			).Scan(&tables); err != nil || tables != fixture.tables {
				t.Fatalf("publication tables=%d want=%d err=%v", tables, fixture.tables, err)
			}
		})
	}
}

func TestDeskPublicationIntentPayloadIsPurgedBySourceDeleteAndFullWipe(t *testing.T) {
	t.Run("per-object delete keeps only content-free evidence", func(t *testing.T) {
		desk, _ := openDeskTrayStore(t)
		defer desk.Close()
		request, now := futurePublicationFixture(t, desk)
		intent, err := desk.PrepareTeamPublication(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := desk.DeleteCommittedMemory(
			request.SourceObjectID, "delete-published-desk-object", now.Add(time.Second),
		); err != nil {
			t.Fatalf("delete published source: %v", err)
		}
		var state, envelopeJSON string
		var purgedAt any
		if err := desk.DB().QueryRow(`
			SELECT state, envelope_json, disclosure_purged_at
			  FROM team_publication_intents WHERE intent_id = ?`, intent.IntentID,
		).Scan(&state, &envelopeJSON, &purgedAt); err != nil {
			t.Fatal(err)
		}
		if state != TeamPublicationCanceled || envelopeJSON != `{}` || purgedAt == nil {
			t.Fatalf("post-delete intent state=%q envelope=%q purged_at=%v", state, envelopeJSON, purgedAt)
		}
		if strings.Contains(envelopeJSON, "Use the reviewed team rule") {
			t.Fatal("deleted Desk source retained disclosure content")
		}
	})

	t.Run("full wipe removes safe publication evidence before source rows", func(t *testing.T) {
		desk, _ := openDeskTrayStore(t)
		defer desk.Close()
		request, _ := futurePublicationFixture(t, desk)
		if _, err := desk.PrepareTeamPublication(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if err := desk.WipeProductMemory(); err != nil {
			t.Fatalf("wipe Desk with publication intent: %v", err)
		}
		for _, table := range []string{"team_publication_intents", "private_memory_objects", "memory_capsules"} {
			var rows int
			if err := desk.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("%s rows after wipe=%d err=%v", table, rows, err)
			}
		}
	})
}

func TestDeskPublicationTerminalIntentsPurgeWithoutIllegalSelfTransition(t *testing.T) {
	terminalStates := []string{
		TeamPublicationReconciled,
		TeamPublicationFailed,
		TeamPublicationCanceled,
		TeamPublicationExpired,
	}
	for _, operation := range []string{"delete", "wipe"} {
		for _, terminalState := range terminalStates {
			t.Run(operation+"/"+terminalState, func(t *testing.T) {
				desk, _ := openDeskTrayStore(t)
				defer desk.Close()
				request, now := futurePublicationFixture(t, desk)
				intent, err := desk.PrepareTeamPublication(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				approvalID := moveDeskPublicationToTerminalState(t, desk, intent, request, terminalState, now)

				if operation == "delete" {
					if _, err := desk.DeleteCommittedMemory(
						request.SourceObjectID, "delete-terminal-publication", now.Add(10*time.Minute),
					); err != nil {
						t.Fatalf("delete source with %s publication: %v", terminalState, err)
					}
					var state, envelopeJSON string
					var purgedAt any
					if err := desk.DB().QueryRow(`
						SELECT state, envelope_json, disclosure_purged_at
						  FROM team_publication_intents WHERE intent_id=?`, intent.IntentID,
					).Scan(&state, &envelopeJSON, &purgedAt); err != nil {
						t.Fatal(err)
					}
					if state != terminalState || envelopeJSON != `{}` || purgedAt == nil {
						t.Fatalf("purged terminal intent state=%q envelope=%q purged_at=%v", state, envelopeJSON, purgedAt)
					}
					if terminalState == TeamPublicationFailed {
						if _, err := desk.MarkTeamPublicationInFlight(
							context.Background(), intent.IntentID, approvalID, now.Add(11*time.Minute),
						); !errors.Is(err, ErrTeamPublicationIdempotencyConflict) {
							t.Fatalf("stale retry resurrected purged failed intent: %v", err)
						}
					}
					return
				}

				if err := desk.WipeProductMemory(); err != nil {
					t.Fatalf("wipe source with %s publication: %v", terminalState, err)
				}
				var rows int
				if err := desk.DB().QueryRow(`SELECT count(*) FROM team_publication_intents`).Scan(&rows); err != nil || rows != 0 {
					t.Fatalf("publication intents after wipe=%d err=%v", rows, err)
				}
			})
		}
	}
}

func moveDeskPublicationToTerminalState(
	t *testing.T,
	desk *Store,
	intent TeamPublicationIntent,
	request TeamPublicationPrepareRequest,
	terminalState string,
	now time.Time,
) string {
	t.Helper()
	if terminalState == TeamPublicationCanceled {
		if _, err := desk.CancelTeamPublication(
			context.Background(), intent.IntentID, intent.EnvelopeDigest, now.Add(time.Second),
		); err != nil {
			t.Fatal(err)
		}
		return ""
	}
	if terminalState == TeamPublicationExpired {
		if _, err := desk.ExpireTeamPublication(
			context.Background(), intent.IntentID, intent.EnvelopeDigest, request.ExpiresAt,
		); err != nil {
			t.Fatal(err)
		}
		return ""
	}

	approvalID := "approval_terminal_purge"
	if _, err := desk.RecordTeamPublicationApprovalReceipt(context.Background(), TeamPublicationLocalApprovalReceipt{
		IntentID: intent.IntentID, ApprovalID: approvalID,
		EnvelopeDigest: intent.EnvelopeDigest, ApprovedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := desk.MarkTeamPublicationInFlight(
		context.Background(), intent.IntentID, approvalID, now.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if terminalState == TeamPublicationFailed {
		if _, err := desk.FailTeamPublicationRemoteAbsent(
			context.Background(), intent.IntentID, approvalID, intent.EnvelopeDigest, now.Add(3*time.Second),
		); err != nil {
			t.Fatal(err)
		}
		return approvalID
	}
	remote := TeamPublicationRemoteReceipt{
		IdempotencyKey: intent.IdempotencyKey, EnvelopeDigest: intent.EnvelopeDigest,
		ObjectID: "object_terminal_purge", ReceiptID: "publication_terminal_purge",
		AuditEventID: "audit_terminal_purge", RecordedAt: now.Add(3 * time.Second),
	}
	if _, err := desk.RecordTeamPublicationRemoteCommit(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	if _, err := desk.ReconcileTeamPublication(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	return approvalID
}

func TestDeskPublicationDeleteAndWipeFailClosedWhileRemoteResultIsAmbiguous(t *testing.T) {
	desk, _ := openDeskTrayStore(t)
	defer desk.Close()
	request, now := futurePublicationFixture(t, desk)
	intent, err := desk.PrepareTeamPublication(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	approvalID := "approval_delete_barrier"
	if _, err := desk.RecordTeamPublicationApprovalReceipt(context.Background(), TeamPublicationLocalApprovalReceipt{
		IntentID: intent.IntentID, ApprovalID: approvalID,
		EnvelopeDigest: intent.EnvelopeDigest, ApprovedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := desk.MarkTeamPublicationInFlight(
		context.Background(), intent.IntentID, approvalID, now.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := desk.DeleteCommittedMemory(
		request.SourceObjectID, "delete-ambiguous-publication", now.Add(3*time.Second),
	); err == nil {
		t.Fatal("per-object delete crossed an ambiguous publication")
	}
	if err := desk.WipeProductMemory(); err == nil {
		t.Fatal("full wipe crossed an ambiguous publication")
	}
	var lifecycle, envelopeJSON string
	if err := desk.DB().QueryRow(`SELECT lifecycle FROM private_memory_objects WHERE object_id = ?`, request.SourceObjectID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := desk.DB().QueryRow(`SELECT envelope_json FROM team_publication_intents WHERE intent_id = ?`, intent.IntentID).Scan(&envelopeJSON); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "active" || envelopeJSON == `{}` {
		t.Fatalf("failed-closed delete mutated source lifecycle=%q envelope=%q", lifecycle, envelopeJSON)
	}
}

func TestTeamPublicationEnvelopeMatchesTheMCPGoldenBytesAndDigest(t *testing.T) {
	clientKey := strings.Repeat("a", 64)
	canonical := []byte(fmt.Sprintf(
		`{"action":"team.commons.publish","client_key":%q,"content":"Use café.","deployment_id":"deployment_zbs","metadata":{"kind":"decision","tags":["pilot"]},"policy_epoch":7,"publication_key":"publication_airlock_01","schema":"pulse.team.airlock_envelope.v1","source_timestamp":"2026-07-14T12:00:00.000Z","store_id":"store_zbs","target_id":"team_zbs","target_kind":"commons","team_id":"team_zbs","writer_id":"writer_primary","writer_principal_id":"principal_nik"}`,
		clientKey,
	))
	request := TeamPublicationPrepareRequest{
		DeploymentID: "deployment_zbs", RemoteStoreID: "store_zbs", TeamID: "team_zbs",
		PolicyEpoch: 7, WriterPrincipalID: "principal_nik", ClientKey: clientKey, WriterID: "writer_primary",
		CanonicalEnvelope: canonical,
		EnvelopeDigest:    "251946d6d246694f10109a0c6de516686125b8795599b4e0b9ffb492d7bce54a",
		IdempotencyKey:    "publication_airlock_01",
	}

	_, actual, digest, err := normalizeTeamPublicationEnvelope(request)
	if err != nil {
		t.Fatalf("normalize golden envelope: %v", err)
	}
	if string(actual) != string(canonical) || digest != request.EnvelopeDigest {
		t.Fatalf("golden mismatch bytes=%s digest=%s", actual, digest)
	}
	private := strings.Replace(string(canonical), "Use café.", "Use memory_1234567890.", 1)
	privateDigest := sha256.Sum256([]byte(private))
	request.CanonicalEnvelope = []byte(private)
	request.EnvelopeDigest = fmt.Sprintf("%x", privateDigest)
	if _, _, _, err := normalizeTeamPublicationEnvelope(request); !errors.Is(err, ErrTeamPublicationInvalid) {
		t.Fatalf("private reference envelope err=%v", err)
	}
}

func TestPrepareTeamPublicationPersistsOneImmutableExactEnvelope(t *testing.T) {
	desk, _ := openDeskTrayStore(t)
	request := publicationFixture(t, desk)

	first, err := desk.PrepareTeamPublication(context.Background(), request)
	if err != nil {
		t.Fatalf("prepare publication: %v", err)
	}
	if first.State != TeamPublicationPrepared || first.Replayed || first.EnvelopeDigest != request.EnvelopeDigest {
		t.Fatalf("unexpected first result: %+v", first)
	}
	if strings.Contains(string(first.CanonicalEnvelope), request.SourceObjectID) ||
		strings.Contains(string(first.CanonicalEnvelope), request.SourceContentDigest) {
		t.Fatal("outbound envelope leaked a private Desk source reference")
	}
	replayed, err := desk.PrepareTeamPublication(context.Background(), request)
	if err != nil || !replayed.Replayed || replayed.IntentID != first.IntentID {
		t.Fatalf("idempotent replay: result=%+v err=%v", replayed, err)
	}

	changed := request
	changed.CanonicalEnvelope = []byte(strings.Replace(
		string(request.CanonicalEnvelope), "Use the reviewed team rule.", "Use the changed team rule.", 1,
	))
	changedDigest := sha256.Sum256(changed.CanonicalEnvelope)
	changed.EnvelopeDigest = fmt.Sprintf("%x", changedDigest)
	if _, err := desk.PrepareTeamPublication(context.Background(), changed); !errors.Is(err, ErrTeamPublicationIdempotencyConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
	if _, err := desk.DB().Exec(`UPDATE team_publication_intents SET envelope_digest=? WHERE intent_id=?`,
		"a"+first.EnvelopeDigest[1:], first.IntentID); err == nil {
		t.Fatal("immutable publication envelope was mutable")
	}
}

func TestPrepareTeamPublicationRejectsUncommittedOrMismatchedDeskMemory(t *testing.T) {
	desk, _ := openDeskTrayStore(t)
	request := publicationFixture(t, desk)

	badDigest := request
	badDigest.SourceContentDigest = "a" + request.SourceContentDigest[1:]
	if _, err := desk.PrepareTeamPublication(context.Background(), badDigest); !errors.Is(err, ErrTeamPublicationSourceMismatch) {
		t.Fatalf("source digest mismatch err=%v", err)
	}
	missing := request
	missing.SourceObjectID = "memory_missing"
	if _, err := desk.PrepareTeamPublication(context.Background(), missing); !errors.Is(err, ErrTeamPublicationSourceMismatch) {
		t.Fatalf("missing source err=%v", err)
	}
	personal, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_airlock")
	if err != nil {
		t.Fatal(err)
	}
	defer personal.Close()
	if _, err := personal.PrepareTeamPublication(context.Background(), request); !errors.Is(err, ErrTeamPublicationDeskRequired) {
		t.Fatalf("personal store publication err=%v", err)
	}
}

func TestTeamPublicationEnvelopeMatchesSharedGoTypeScriptAdversarialCorpus(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "airlock-adversarial.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []airlockAdversarialFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	clientKey := strings.Repeat("a", 64)
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			tags, err := json.Marshal(fixture.Tags)
			if err != nil {
				t.Fatal(err)
			}
			envelope := []byte(fmt.Sprintf(
				`{"action":"team.commons.publish","client_key":%q,"content":%q,"deployment_id":"deployment_zbs","metadata":{"kind":"decision","tags":%s},"policy_epoch":7,"publication_key":"publication_airlock_01","schema":"pulse.team.airlock_envelope.v1","source_timestamp":"2026-07-14T12:00:00.000Z","store_id":"store_zbs","target_id":"team_zbs","target_kind":"commons","team_id":"team_zbs","writer_id":"writer_primary","writer_principal_id":"principal_nik"}`,
				clientKey, fixture.Content, tags,
			))
			digest := sha256.Sum256(envelope)
			request := TeamPublicationPrepareRequest{
				DeploymentID: "deployment_zbs", RemoteStoreID: "store_zbs", TeamID: "team_zbs",
				PolicyEpoch: 7, WriterPrincipalID: "principal_nik", ClientKey: clientKey,
				WriterID: "writer_primary", CanonicalEnvelope: envelope,
				EnvelopeDigest: fmt.Sprintf("%x", digest), IdempotencyKey: "publication_airlock_01",
			}
			_, _, _, err = normalizeTeamPublicationEnvelope(request)
			if fixture.Accept && err != nil {
				t.Fatalf("accepted fixture error: %v", err)
			}
			if !fixture.Accept && !errors.Is(err, ErrTeamPublicationInvalid) {
				t.Fatalf("rejected fixture error: %v", err)
			}
		})
	}
}

func TestTeamPublicationIntentCannotBypassApprovalOrRewriteReceipts(t *testing.T) {
	desk, _ := openDeskTrayStore(t)
	intent, err := desk.PrepareTeamPublication(context.Background(), publicationFixture(t, desk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := desk.DB().Exec(`UPDATE team_publication_intents SET state='in_flight' WHERE intent_id=?`, intent.IntentID); err == nil {
		t.Fatal("prepared publication entered in_flight without approval")
	}
	if _, err := desk.DB().Exec(`
		UPDATE team_publication_intents
		   SET state='approved', approval_id='approval_exact', approval_digest=envelope_digest,
		       approved_at=created_at, updated_at=created_at
		 WHERE intent_id=?`, intent.IntentID); err != nil {
		t.Fatalf("exact approval transition: %v", err)
	}
	if _, err := desk.DB().Exec(`UPDATE team_publication_intents SET approval_id='approval_changed' WHERE intent_id=?`, intent.IntentID); err == nil {
		t.Fatal("approval binding was mutable")
	}
	if _, err := desk.DB().Exec(`UPDATE team_publication_intents SET state='in_flight' WHERE intent_id=?`, intent.IntentID); err != nil {
		t.Fatalf("approved in-flight transition: %v", err)
	}
	if _, err := desk.DB().Exec(`
		UPDATE team_publication_intents
		   SET state='remote_committed_local_pending', remote_object_id='object_remote',
		       remote_receipt_id='receipt_remote', remote_audit_event_id='audit_remote',
		       remote_content_digest=envelope_digest
		 WHERE intent_id=?`, intent.IntentID); err != nil {
		t.Fatalf("remote result transition: %v", err)
	}
	if _, err := desk.DB().Exec(`UPDATE team_publication_intents SET remote_receipt_id='receipt_changed' WHERE intent_id=?`, intent.IntentID); err == nil {
		t.Fatal("remote receipt binding was mutable")
	}
}

func TestTeamPublicationLocalSagaRecordsRemoteReceiptAndReconcilesWithoutRepublish(t *testing.T) {
	desk, _ := openDeskTrayStore(t)
	request := publicationFixture(t, desk)
	intent, err := desk.PrepareTeamPublication(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	approvedAt := intent.CreatedAt.Add(time.Second)
	approved, err := desk.RecordTeamPublicationApprovalReceipt(context.Background(), TeamPublicationLocalApprovalReceipt{
		IntentID: intent.IntentID, ApprovalID: "approval_remote_exact",
		EnvelopeDigest: intent.EnvelopeDigest, ApprovedAt: approvedAt,
	})
	if err != nil || approved.State != TeamPublicationApproved {
		t.Fatalf("record approval state=%+v err=%v", approved, err)
	}
	inFlight, err := desk.MarkTeamPublicationInFlight(context.Background(), intent.IntentID, "approval_remote_exact", approvedAt.Add(time.Second))
	if err != nil || inFlight.State != TeamPublicationInFlight {
		t.Fatalf("mark in flight state=%+v err=%v", inFlight, err)
	}
	remote := TeamPublicationRemoteReceipt{
		IdempotencyKey: intent.IdempotencyKey, EnvelopeDigest: intent.EnvelopeDigest,
		ObjectID: "object_remote_exact", ReceiptID: "publication_remote_exact",
		AuditEventID: "audit_remote_exact", RecordedAt: approvedAt.Add(2 * time.Second),
	}
	pending, err := desk.RecordTeamPublicationRemoteCommit(context.Background(), remote)
	if err != nil || pending.State != TeamPublicationRemoteCommittedLocalPending {
		t.Fatalf("record remote state=%+v err=%v", pending, err)
	}
	reconciled, err := desk.ReconcileTeamPublication(context.Background(), remote)
	if err != nil || reconciled.State != TeamPublicationReconciled || reconciled.RemoteReceiptID != remote.ReceiptID {
		t.Fatalf("reconcile state=%+v err=%v", reconciled, err)
	}
	replay, err := desk.ReconcileTeamPublication(context.Background(), remote)
	if err != nil || replay.IntentID != reconciled.IntentID || !replay.Replayed {
		t.Fatalf("reconcile replay state=%+v err=%v", replay, err)
	}
	changed := remote
	changed.ReceiptID = "publication_remote_changed"
	if _, err := desk.ReconcileTeamPublication(context.Background(), changed); !errors.Is(err, ErrTeamPublicationIdempotencyConflict) {
		t.Fatalf("changed remote receipt error=%v", err)
	}
}

func futurePublicationFixture(t *testing.T, desk *Store) (TeamPublicationPrepareRequest, time.Time) {
	t.Helper()
	request := publicationFixture(t, desk)
	now := time.Now().UTC().Truncate(time.Second)
	desk.clock = func() time.Time { return now }
	request.ExpiresAt = now.Add(5 * time.Minute)
	return request, now
}

func TestTeamPublicationCancelAndExpiryAreTerminalBeforeRemoteCommit(t *testing.T) {
	t.Run("cancel exact prepared envelope", func(t *testing.T) {
		desk, _ := openDeskTrayStore(t)
		request, now := futurePublicationFixture(t, desk)
		intent, err := desk.PrepareTeamPublication(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		canceled, err := desk.CancelTeamPublication(
			context.Background(), intent.IntentID, intent.EnvelopeDigest, now.Add(time.Second),
		)
		if err != nil || canceled.State != TeamPublicationCanceled || canceled.TerminalAt == nil {
			t.Fatalf("cancel state=%+v err=%v", canceled, err)
		}
		replay, err := desk.CancelTeamPublication(
			context.Background(), intent.IntentID, intent.EnvelopeDigest, now.Add(time.Second),
		)
		if err != nil || !replay.Replayed {
			t.Fatalf("cancel replay=%+v err=%v", replay, err)
		}
		if _, err := desk.CancelTeamPublication(
			context.Background(), intent.IntentID, "a"+intent.EnvelopeDigest[1:], now.Add(time.Second),
		); !errors.Is(err, ErrTeamPublicationIdempotencyConflict) {
			t.Fatalf("changed cancel digest error=%v", err)
		}
	})

	t.Run("expiry only after immutable deadline", func(t *testing.T) {
		desk, _ := openDeskTrayStore(t)
		request, now := futurePublicationFixture(t, desk)
		intent, err := desk.PrepareTeamPublication(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := desk.ExpireTeamPublication(
			context.Background(), intent.IntentID, intent.EnvelopeDigest, now.Add(time.Minute),
		); !errors.Is(err, ErrTeamPublicationIdempotencyConflict) {
			t.Fatalf("early expiry error=%v", err)
		}
		expired, err := desk.ExpireTeamPublication(
			context.Background(), intent.IntentID, intent.EnvelopeDigest, request.ExpiresAt,
		)
		if err != nil || expired.State != TeamPublicationExpired || expired.TerminalAt == nil {
			t.Fatalf("expiry state=%+v err=%v", expired, err)
		}
		if _, err := desk.DB().Exec(
			`UPDATE team_publication_intents SET expires_at=? WHERE intent_id=?`,
			request.ExpiresAt.Add(time.Hour).Format(time.RFC3339Nano), intent.IntentID,
		); err == nil {
			t.Fatal("immutable Airlock expiry was extended")
		}
	})
}

func TestTeamPublicationRetryReusesExactApprovedIntentOnlyAfterVerifiedAbsence(t *testing.T) {
	desk, _ := openDeskTrayStore(t)
	request, now := futurePublicationFixture(t, desk)
	intent, err := desk.PrepareTeamPublication(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	approvalID := "approval_remote_retry"
	approved, err := desk.RecordTeamPublicationApprovalReceipt(context.Background(), TeamPublicationLocalApprovalReceipt{
		IntentID: intent.IntentID, ApprovalID: approvalID,
		EnvelopeDigest: intent.EnvelopeDigest, ApprovedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	inFlight, err := desk.MarkTeamPublicationInFlight(
		context.Background(), intent.IntentID, approvalID, now.Add(2*time.Second),
	)
	if err != nil || inFlight.State != TeamPublicationInFlight {
		t.Fatalf("first in-flight=%+v err=%v", inFlight, err)
	}
	failed, err := desk.FailTeamPublicationRemoteAbsent(
		context.Background(), intent.IntentID, approvalID, intent.EnvelopeDigest, now.Add(3*time.Second),
	)
	if err != nil || failed.State != TeamPublicationFailed ||
		failed.FailureCode != TeamPublicationFailureRemoteAbsent || failed.TerminalAt == nil {
		t.Fatalf("failed state=%+v err=%v", failed, err)
	}
	retried, err := desk.MarkTeamPublicationInFlight(
		context.Background(), intent.IntentID, approvalID, now.Add(4*time.Second),
	)
	if err != nil || retried.State != TeamPublicationInFlight || retried.TerminalAt != nil ||
		retried.FailureCode != "" || retried.EnvelopeDigest != approved.EnvelopeDigest {
		t.Fatalf("retry state=%+v err=%v", retried, err)
	}

	remote := TeamPublicationRemoteReceipt{
		IdempotencyKey: intent.IdempotencyKey, EnvelopeDigest: intent.EnvelopeDigest,
		ObjectID: "object_remote_retry", ReceiptID: "publication_remote_retry",
		AuditEventID: "audit_remote_retry", RecordedAt: now.Add(5 * time.Second),
	}
	pending, err := desk.RecordTeamPublicationRemoteCommit(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := desk.FailTeamPublicationRemoteAbsent(
		context.Background(), intent.IntentID, approvalID, intent.EnvelopeDigest, now.Add(6*time.Second),
	); !errors.Is(err, ErrTeamPublicationIdempotencyConflict) {
		t.Fatalf("remote-committed failure error=%v pending=%+v", err, pending)
	}
	if _, err := desk.CancelTeamPublication(
		context.Background(), intent.IntentID, intent.EnvelopeDigest, now.Add(6*time.Second),
	); !errors.Is(err, ErrTeamPublicationIdempotencyConflict) {
		t.Fatalf("remote-committed cancel error=%v", err)
	}
}
