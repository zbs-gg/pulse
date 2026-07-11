package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type teamObjectWriteFixture struct {
	store     *Store
	bootstrap BootstrapResult
	actor     mutationAuthorizationActor
	lease     TeamWriterLease
	permit    TeamMutationPermit
	request   TeamObjectWriteRequest
}

func newTeamObjectWriteFixture(t *testing.T) teamObjectWriteFixture {
	t.Helper()
	return newTeamObjectWriteFixtureAt(t, filepath.Join(t.TempDir(), "team.db"))
}

func newTeamObjectWriteFixtureAt(t *testing.T, path string) teamObjectWriteFixture {
	t.Helper()
	return newTeamObjectWriteFixtureWithOptions(t, path, reviewTeamOptions(testBootstrapRoot()))
}

func newTeamObjectWriteFixtureWithOptions(t *testing.T, path string, options TeamOpenOptions) teamObjectWriteFixture {
	t.Helper()
	root := testBootstrapRoot()
	s, err := OpenTeam(path, options)
	if err != nil {
		t.Fatalf("OpenTeam: %v", err)
	}
	bootstrap, err := s.BootstrapTeam(context.Background(), BootstrapTeamRequest{
		TeamName: "Synthetic Object Spine", PresentedRoot: root,
	})
	if err != nil {
		s.Close()
		t.Fatalf("BootstrapTeam: %v", err)
	}
	actor := addMutationAuthorizationActor(t, s, bootstrap, "object-writer", "member")
	permit, err := s.AuthorizeTeamMutation(context.Background(), mutationWriteRequest(bootstrap, actor))
	if err != nil {
		s.Close()
		t.Fatalf("AuthorizeTeamMutation: %v", err)
	}
	lease := acquireReadyWriter(t, s)
	body := []byte("synthetic object body only visible to the domain extension")
	digest := sha256.Sum256(body)
	return teamObjectWriteFixture{
		store: s, bootstrap: bootstrap, actor: actor, lease: lease, permit: permit,
		request: TeamObjectWriteRequest{
			Permit:    permit,
			Writer:    TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token},
			RequestID: "request-object-spine-0001", OAuthClientKey: actor.clientKey,
			IdempotencyKey: "idempotency-object-spine-0001", Body: body,
			BodyDigest:      fmt.Sprintf("%x", digest),
			Policy:          TeamObjectPolicy{PrivacyTier: "normal", Retention: "long_term"},
			ProjectionKinds: []string{"semantic_graph", "embedding"},
		},
	}
}

func cloneTeamObjectWriteRequest(request TeamObjectWriteRequest) TeamObjectWriteRequest {
	request.Body = append([]byte(nil), request.Body...)
	request.ProjectionKinds = append([]string(nil), request.ProjectionKinds...)
	return request
}

func requireSameTeamObjectWriteIDs(t *testing.T, got, want TeamObjectWriteResult) {
	t.Helper()
	if got.ObjectID != want.ObjectID || got.AuditEventID != want.AuditEventID ||
		got.Status != want.Status || got.ProjectionState != want.ProjectionState ||
		got.FullyProjected != want.FullyProjected || !reflect.DeepEqual(got.ProjectionJobs, want.ProjectionJobs) {
		t.Fatalf("result does not replay exact IDs/state:\n got  %+v\n want %+v", got, want)
	}
}

func TestStoreTeamObjectCommitsCanonicalSpineAndReplaysExactIDs(t *testing.T) {
	ctx := context.Background()
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()

	result, err := f.store.StoreTeamObject(ctx, f.request)
	if err != nil {
		t.Fatalf("StoreTeamObject: %v", err)
	}
	if result.ObjectID == "" || result.AuditEventID == "" || result.Replayed ||
		result.Status != TeamObjectStatusStored || result.ProjectionState != TeamProjectionStatePending || result.FullyProjected {
		t.Fatalf("unsafe or incomplete write result: %+v", result)
	}
	if len(result.ProjectionJobs) != 2 {
		t.Fatalf("projection jobs = %+v", result.ProjectionJobs)
	}
	if !sort.SliceIsSorted(result.ProjectionJobs, func(i, j int) bool {
		return result.ProjectionJobs[i].Kind < result.ProjectionJobs[j].Kind
	}) {
		t.Fatalf("projection jobs are not stable: %+v", result.ProjectionJobs)
	}
	for _, job := range result.ProjectionJobs {
		if job.JobID == "" || job.State != TeamProjectionStatePending {
			t.Fatalf("projection job is not pending: %+v", job)
		}
	}

	target := f.permit.EffectiveTarget()
	var objectKind, scopeType, scopeID, ownerID, authorID, privacy, retention, lifecycle string
	var generation int64
	if err := f.store.DB().QueryRowContext(ctx, `
		SELECT object_kind, scope_type, scope_id, COALESCE(owner_principal_id, ''),
		       author_principal_id, privacy_tier, retention, lifecycle, generation
		  FROM team_object_registry WHERE object_id = ?`, result.ObjectID).Scan(
		&objectKind, &scopeType, &scopeID, &ownerID, &authorID,
		&privacy, &retention, &lifecycle, &generation,
	); err != nil {
		t.Fatal(err)
	}
	if objectKind != f.permit.ObjectKind() || scopeType != string(target.Type) || scopeID != target.ID ||
		ownerID != target.OwnerPrincipalID || authorID != f.actor.binding.AgentPrincipalID ||
		privacy != "normal" || retention != "long_term" || lifecycle != "active" || generation != 1 {
		t.Fatalf("canonical root mismatch: kind=%q scope=%q/%q owner=%q author=%q policy=%q/%q lifecycle=%q generation=%d",
			objectKind, scopeType, scopeID, ownerID, authorID, privacy, retention, lifecycle, generation)
	}

	var state, storedObjectID, storedAuditID, bodyDigest, storedKey string
	if err := f.store.DB().QueryRowContext(ctx, `
		SELECT state, object_id, audit_event_id, body_digest, idempotency_key_hash
		  FROM team_idempotency_records
		 WHERE team_id = ? AND principal_id = ? AND client_key = ? AND action = ?`,
		f.bootstrap.TeamID, f.actor.binding.AgentPrincipalID, f.actor.clientKey, teamObjectWriteAction,
	).Scan(&state, &storedObjectID, &storedAuditID, &bodyDigest, &storedKey); err != nil {
		t.Fatal(err)
	}
	wantKeyDigest := sha256.Sum256([]byte("pulse-team-idempotency-key-v1\x00" + f.request.IdempotencyKey))
	if state != "stored" || storedObjectID != result.ObjectID || storedAuditID != result.AuditEventID ||
		!lowerHexDigest(bodyDigest) || bodyDigest == f.request.BodyDigest ||
		storedKey != fmt.Sprintf("%x", wantKeyDigest) || storedKey == f.request.IdempotencyKey {
		t.Fatalf("unsafe idempotency result: state=%q object=%q audit=%q digest=%q key=%q",
			state, storedObjectID, storedAuditID, bodyDigest, storedKey)
	}

	replay, err := f.store.StoreTeamObject(ctx, f.request)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("replay was not identified: %+v", replay)
	}
	replay.Replayed = result.Replayed
	requireSameTeamObjectWriteIDs(t, replay, result)

	var roots, audits, jobs, idempotency int
	for query, destination := range map[string]*int{
		`SELECT count(*) FROM team_object_registry WHERE object_id = '` + result.ObjectID + `'`:      &roots,
		`SELECT count(*) FROM team_audit_events WHERE event_id = '` + result.AuditEventID + `'`:      &audits,
		`SELECT count(*) FROM team_projection_jobs WHERE root_object_id = '` + result.ObjectID + `'`: &jobs,
		`SELECT count(*) FROM team_idempotency_records WHERE object_id = '` + result.ObjectID + `'`:  &idempotency,
	} {
		if err := f.store.DB().QueryRow(query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if roots != 1 || audits != 1 || jobs != 2 || idempotency != 1 {
		t.Fatalf("replay duplicated rows: root=%d audit=%d jobs=%d idempotency=%d", roots, audits, jobs, idempotency)
	}
}

func TestStoreTeamObjectConcurrentRetryAndRestartReturnOriginalIDs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "team.db")
	f := newTeamObjectWriteFixtureAt(t, path)

	const callers = 16
	results := make([]TeamObjectWriteResult, callers)
	errorsSeen := make([]error, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := range callers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsSeen[index] = f.store.StoreTeamObject(ctx, cloneTeamObjectWriteRequest(f.request))
		}(i)
	}
	close(start)
	wait.Wait()

	first := results[0]
	originals := 0
	for i := range callers {
		if errorsSeen[i] != nil {
			t.Fatalf("concurrent caller %d: %v", i, errorsSeen[i])
		}
		if !results[i].Replayed {
			originals++
		}
		copyResult := results[i]
		copyResult.Replayed = first.Replayed
		requireSameTeamObjectWriteIDs(t, copyResult, first)
	}
	if originals != 1 {
		t.Fatalf("original commits = %d, want 1", originals)
	}

	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenTeam(path, reviewTeamOptions(testBootstrapRoot()))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	restarted, err := reopened.StoreTeamObject(ctx, f.request)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if !restarted.Replayed {
		t.Fatalf("restart did not replay: %+v", restarted)
	}
	restarted.Replayed = first.Replayed
	requireSameTeamObjectWriteIDs(t, restarted, first)

	conflict := cloneTeamObjectWriteRequest(f.request)
	conflict.Body = []byte("different body under the same key")
	conflictingDigest := sha256.Sum256(conflict.Body)
	conflict.BodyDigest = fmt.Sprintf("%x", conflictingDigest)
	if _, err := reopened.StoreTeamObject(ctx, conflict); !errors.Is(err, ErrTeamIdempotencyConflict) {
		t.Fatalf("different body error = %v", err)
	}
}

func TestStoreTeamObjectIdempotencyBindsTheFullCanonicalOperation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, teamObjectWriteFixture, *TeamObjectWriteRequest)
	}{
		{name: "object kind", mutate: func(t *testing.T, f teamObjectWriteFixture, request *TeamObjectWriteRequest) {
			authorization := mutationWriteRequest(f.bootstrap, f.actor)
			authorization.ObjectKind = "decision"
			permit, err := f.store.AuthorizeTeamMutation(context.Background(), authorization)
			if err != nil {
				t.Fatal(err)
			}
			request.Permit = permit
		}},
		{name: "effective scope", mutate: func(t *testing.T, f teamObjectWriteFixture, request *TeamObjectWriteRequest) {
			authorization := mutationWriteRequest(f.bootstrap, f.actor)
			authorization.RequestedScope = &teamauth.CanonicalScope{Type: teamauth.ScopeAgent, ID: "agent-scope-alternate"}
			permit, err := f.store.AuthorizeTeamMutation(context.Background(), authorization)
			if err != nil {
				t.Fatal(err)
			}
			request.Permit = permit
		}},
		{name: "privacy policy", mutate: func(_ *testing.T, _ teamObjectWriteFixture, request *TeamObjectWriteRequest) {
			request.Policy.PrivacyTier = "sensitive"
		}},
		{name: "retention policy", mutate: func(_ *testing.T, _ teamObjectWriteFixture, request *TeamObjectWriteRequest) {
			request.Policy.Retention = "project"
		}},
		{name: "expiry policy", mutate: func(_ *testing.T, _ teamObjectWriteFixture, request *TeamObjectWriteRequest) {
			expires := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
			request.Policy.ExpiresAt = &expires
		}},
		{name: "projection set", mutate: func(_ *testing.T, _ teamObjectWriteFixture, request *TeamObjectWriteRequest) {
			request.ProjectionKinds = []string{"embedding"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newTeamObjectWriteFixture(t)
			defer f.store.Close()
			if _, err := f.store.StoreTeamObject(context.Background(), f.request); err != nil {
				t.Fatalf("initial write: %v", err)
			}
			changed := cloneTeamObjectWriteRequest(f.request)
			test.mutate(t, f, &changed)
			if _, err := f.store.StoreTeamObject(context.Background(), changed); !errors.Is(err, ErrTeamIdempotencyConflict) {
				t.Fatalf("changed canonical operation error = %v", err)
			}
		})
	}
}

func TestCanonicalTeamObjectOperationDigestIncludesEveryAuthorityAndPolicyField(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	base, err := f.store.normalizeTeamObjectWrite(f.request)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest := canonicalTeamObjectOperationDigest(base)
	mutations := map[string]func(*normalizedTeamObjectWrite){
		"store":               func(write *normalizedTeamObjectWrite) { write.attribution.StoreID = "store-alternate" },
		"team attribution":    func(write *normalizedTeamObjectWrite) { write.attribution.TeamID = "team-attribution-alternate" },
		"actor":               func(write *normalizedTeamObjectWrite) { write.attribution.ActorPrincipalID = "principal-alternate" },
		"human":               func(write *normalizedTeamObjectWrite) { write.attribution.HumanPrincipalID = "human-alternate" },
		"principal kind":      func(write *normalizedTeamObjectWrite) { write.attribution.PrincipalKind = teamauth.PrincipalService },
		"client":              func(write *normalizedTeamObjectWrite) { write.clientKey = strings.Repeat("b", 64) },
		"object kind":         func(write *normalizedTeamObjectWrite) { write.objectKind = "decision" },
		"scope team":          func(write *normalizedTeamObjectWrite) { write.target.TeamID = "team-scope-alternate" },
		"scope type":          func(write *normalizedTeamObjectWrite) { write.target.Type = teamauth.ScopeAgent },
		"scope id":            func(write *normalizedTeamObjectWrite) { write.target.ID = "scope-alternate" },
		"scope owner":         func(write *normalizedTeamObjectWrite) { write.target.OwnerPrincipalID = "owner-alternate" },
		"scope lifecycle":     func(write *normalizedTeamObjectWrite) { write.target.Lifecycle = teamauth.LifecycleState("deleted") },
		"scope generation":    func(write *normalizedTeamObjectWrite) { write.target.Generation++ },
		"scope privacy":       func(write *normalizedTeamObjectWrite) { write.target.PrivacyTier = "sensitive" },
		"scope retention":     func(write *normalizedTeamObjectWrite) { write.target.Retention = "project" },
		"privacy policy":      func(write *normalizedTeamObjectWrite) { write.privacyTier = "sensitive" },
		"retention policy":    func(write *normalizedTeamObjectWrite) { write.retention = "project" },
		"expiry policy":       func(write *normalizedTeamObjectWrite) { write.expiryDigestValue = "at:2026-07-11T10:00:00Z" },
		"raw body digest":     func(write *normalizedTeamObjectWrite) { write.bodyDigest = strings.Repeat("c", 64) },
		"projection ordering": func(write *normalizedTeamObjectWrite) { write.projectionKinds = []string{"embedding", "facts"} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.projectionKinds = append([]string(nil), base.projectionKinds...)
			mutate(&changed)
			if got := canonicalTeamObjectOperationDigest(changed); got == baseDigest {
				t.Fatalf("digest did not bind %s", name)
			}
		})
	}
}

func TestStoreTeamObjectRollsBackEveryTableBoundary(t *testing.T) {
	tests := []struct {
		name      string
		createSQL string
	}{
		{name: "idempotency reserve", createSQL: `
			CREATE TRIGGER u5_forced_failure BEFORE INSERT ON team_idempotency_records
			BEGIN SELECT RAISE(ABORT, 'forced idempotency reserve failure'); END`},
		{name: "root registry", createSQL: `
			CREATE TRIGGER u5_forced_failure BEFORE INSERT ON team_object_registry
			BEGIN SELECT RAISE(ABORT, 'forced root failure'); END`},
		{name: "domain audit", createSQL: `
			CREATE TRIGGER u5_forced_failure BEFORE INSERT ON team_audit_events
			BEGIN SELECT RAISE(ABORT, 'forced audit failure'); END`},
		{name: "audit ordering", createSQL: `
			CREATE TRIGGER u5_forced_failure BEFORE INSERT ON team_audit_event_order
			BEGIN SELECT RAISE(ABORT, 'forced audit order failure'); END`},
		{name: "projection intent", createSQL: `
			CREATE TRIGGER u5_forced_failure BEFORE INSERT ON team_projection_jobs
			BEGIN SELECT RAISE(ABORT, 'forced projection failure'); END`},
		{name: "idempotency finalize", createSQL: `
			CREATE TRIGGER u5_forced_failure BEFORE UPDATE ON team_idempotency_records
			WHEN NEW.state = 'stored'
			BEGIN SELECT RAISE(ABORT, 'forced idempotency finalize failure'); END`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newTeamObjectWriteFixture(t)
			defer f.store.Close()
			before := teamObjectTableCounts(t, f.store)
			if _, err := f.store.DB().Exec(test.createSQL); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.StoreTeamObject(context.Background(), f.request); !errors.Is(err, ErrTeamObjectCommitFailed) {
				t.Fatalf("forced failure error = %v", err)
			}
			after := teamObjectTableCounts(t, f.store)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("partial commit after %s: before=%v after=%v", test.name, before, after)
			}
			if _, err := f.store.DB().Exec(`DROP TRIGGER u5_forced_failure`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.StoreTeamObject(context.Background(), f.request); err != nil {
				t.Fatalf("retry after rollback: %v", err)
			}
		})
	}
}

func TestStoreTeamObjectDomainExtensionSharesTheAtomicTransaction(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	if _, err := f.store.DB().Exec(`CREATE TABLE u5_domain_rows(object_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	before := teamObjectTableCounts(t, f.store)
	want := errors.New("synthetic domain write failure")
	_, err := f.store.storeTeamObjectWithExtension(context.Background(), f.request,
		func(_ context.Context, write *teamObjectWriteTransaction) error {
			if write.ObjectID == "" {
				t.Fatal("extension did not receive the canonical root identity")
			}
			if _, err := write.ExecContext(context.Background(),
				`INSERT INTO u5_domain_rows(object_id) VALUES (?)`, write.ObjectID); err != nil {
				t.Fatalf("domain insert: %v", err)
			}
			if err := write.MapStorage(context.Background(), "capsule", "capsule_synthetic"); err != nil {
				t.Fatalf("storage map: %v", err)
			}
			return want
		})
	if err != ErrTeamObjectCommitFailed || errors.Is(err, want) || strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("extension failure = %v", err)
	}
	after := teamObjectTableCounts(t, f.store)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("extension failure partially committed: before=%v after=%v", before, after)
	}
	var domainRows int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM u5_domain_rows`).Scan(&domainRows); err != nil {
		t.Fatal(err)
	}
	if domainRows != 0 {
		t.Fatalf("extension failure committed %d domain rows", domainRows)
	}
	var mappings int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM team_object_storage_map`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 {
		t.Fatalf("extension failure committed %d storage mappings", mappings)
	}
}

func TestTeamObjectDomainExtensionCannotControlTheTransaction(t *testing.T) {
	txType := reflect.TypeOf((*sql.Tx)(nil))
	writeType := reflect.TypeOf(teamObjectWriteTransaction{})
	for index := range writeType.NumField() {
		if writeType.Field(index).Type == txType {
			t.Fatalf("extension facade exposes raw *sql.Tx in field %q", writeType.Field(index).Name)
		}
	}
	for _, method := range []string{"Commit", "Rollback"} {
		if _, ok := reflect.TypeOf(&teamObjectWriteTransaction{}).MethodByName(method); ok {
			t.Fatalf("extension facade exposes %s", method)
		}
	}

	for _, statement := range []string{
		"COMMIT",
		"ROLLBACK",
		"SAVEPOINT attacker",
		"UPDATE team_writer_leases SET expires_at = '2099-01-01T00:00:00Z'",
		"UPDATE main.team_writer_leases SET expires_at = '2099-01-01T00:00:00Z'",
		"INSERT INTO team_object_registry(object_id) VALUES ('x'); COMMIT",
	} {
		t.Run(statement, func(t *testing.T) {
			f := newTeamObjectWriteFixture(t)
			defer f.store.Close()
			before := teamObjectTableCounts(t, f.store)
			var extensionErr error
			_, err := f.store.storeTeamObjectWithExtension(context.Background(), f.request,
				func(ctx context.Context, write *teamObjectWriteTransaction) error {
					_, extensionErr = write.ExecContext(ctx, statement)
					return extensionErr
				})
			if !errors.Is(extensionErr, errTeamObjectExtensionStatementInvalid) {
				t.Fatalf("statement was not rejected inside facade: %v", extensionErr)
			}
			if err != ErrTeamObjectCommitFailed || err.Error() != ErrTeamObjectCommitFailed.Error() {
				t.Fatalf("unsafe boundary error = %v", err)
			}
			if after := teamObjectTableCounts(t, f.store); !reflect.DeepEqual(after, before) {
				t.Fatalf("transaction control partially committed: before=%v after=%v", before, after)
			}
		})
	}
}

func TestStoreTeamObjectRechecksWriterLeaseAfterDomainExtension(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	options := reviewTeamOptions(testBootstrapRoot())
	options.Clock = func() time.Time { return now }
	f := newTeamObjectWriteFixtureWithOptions(t, filepath.Join(t.TempDir(), "team.db"), options)
	defer f.store.Close()
	if _, err := f.store.DB().Exec(`CREATE TABLE u5_domain_rows(object_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	before := teamObjectTableCounts(t, f.store)
	_, err := f.store.storeTeamObjectWithExtension(context.Background(), f.request,
		func(ctx context.Context, write *teamObjectWriteTransaction) error {
			if _, err := write.ExecContext(ctx, `INSERT INTO u5_domain_rows(object_id) VALUES (?)`, write.ObjectID); err != nil {
				return err
			}
			now = f.lease.ExpiresAt.Add(time.Nanosecond)
			return nil
		})
	if !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("expired lease after extension error = %v", err)
	}
	if after := teamObjectTableCounts(t, f.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("late lease failure partially committed: before=%v after=%v", before, after)
	}
	var domainRows int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM u5_domain_rows`).Scan(&domainRows); err != nil {
		t.Fatal(err)
	}
	if domainRows != 0 {
		t.Fatalf("late lease failure committed %d domain rows", domainRows)
	}
}

func TestStoreTeamObjectRejectsUnboundOrUnsafeInputsBeforeWriting(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	tests := []struct {
		name   string
		mutate func(*TeamObjectWriteRequest)
		want   error
	}{
		{name: "wrong oauth key", mutate: func(request *TeamObjectWriteRequest) { request.OAuthClientKey = strings.Repeat("a", 64) }, want: ErrTeamObjectInvalid},
		{name: "non hex oauth key", mutate: func(request *TeamObjectWriteRequest) { request.OAuthClientKey = strings.Repeat("z", 64) }, want: ErrTeamObjectInvalid},
		{name: "wrong writer", mutate: func(request *TeamObjectWriteRequest) { request.Writer.WriterID = "other-daemon" }, want: ErrTeamWriterLeaseMismatch},
		{name: "wrong writer token", mutate: func(request *TeamObjectWriteRequest) { request.Writer.Token = "writer_lease_wrong" }, want: ErrTeamWriterLeaseMismatch},
		{name: "unsafe request id", mutate: func(request *TeamObjectWriteRequest) { request.RequestID = "../../secret" }, want: ErrTeamObjectInvalid},
		{name: "empty idempotency key", mutate: func(request *TeamObjectWriteRequest) { request.IdempotencyKey = "" }, want: ErrTeamObjectInvalid},
		{name: "control in idempotency key", mutate: func(request *TeamObjectWriteRequest) { request.IdempotencyKey = "idempotency\nsecret" }, want: ErrTeamObjectInvalid},
		{name: "body digest mismatch", mutate: func(request *TeamObjectWriteRequest) { request.Body = []byte("tampered") }, want: ErrTeamObjectInvalid},
		{name: "uppercase digest", mutate: func(request *TeamObjectWriteRequest) { request.BodyDigest = strings.ToUpper(request.BodyDigest) }, want: ErrTeamObjectInvalid},
		{name: "unsafe projection", mutate: func(request *TeamObjectWriteRequest) { request.ProjectionKinds = []string{"embedding/prompt"} }, want: ErrTeamObjectInvalid},
		{name: "duplicate projection", mutate: func(request *TeamObjectWriteRequest) { request.ProjectionKinds = []string{"embedding", "embedding"} }, want: ErrTeamObjectInvalid},
		{name: "ready projection injection", mutate: func(request *TeamObjectWriteRequest) { request.ProjectionKinds = []string{"ready"} }, want: ErrTeamObjectInvalid},
		{name: "invalid privacy", mutate: func(request *TeamObjectWriteRequest) { request.Policy.PrivacyTier = "public" }, want: ErrTeamObjectInvalid},
		{name: "invalid retention", mutate: func(request *TeamObjectWriteRequest) { request.Policy.Retention = "forever" }, want: ErrTeamObjectInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneTeamObjectWriteRequest(f.request)
			request.IdempotencyKey += "-" + strings.ReplaceAll(test.name, " ", "-")
			test.mutate(&request)
			before := teamObjectTableCounts(t, f.store)
			if _, err := f.store.StoreTeamObject(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if after := teamObjectTableCounts(t, f.store); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid input wrote rows: before=%v after=%v", before, after)
			}
		})
	}
}

func TestStoreTeamObjectSessionPolicyUsesInjectedClockAndCapsExpiryAt24Hours(t *testing.T) {
	baseNow := time.Date(2026, 7, 10, 13, 14, 15, 123456789, time.UTC)
	for _, test := range []struct {
		name       string
		expiresAt  func(time.Time) *time.Time
		wantExpiry func(time.Time) time.Time
		wantErr    error
	}{
		{name: "missing defaults to 24 hours", wantExpiry: func(now time.Time) time.Time { return now.Add(24 * time.Hour) }},
		{name: "shorter explicit expiry", expiresAt: func(now time.Time) *time.Time {
			expires := now.Add(time.Hour)
			return &expires
		}, wantExpiry: func(now time.Time) time.Time { return now.Add(time.Hour) }},
		{name: "exactly 24 hours", expiresAt: func(now time.Time) *time.Time {
			expires := now.Add(24 * time.Hour)
			return &expires
		}, wantExpiry: func(now time.Time) time.Time { return now.Add(24 * time.Hour) }},
		{name: "more than 24 hours", expiresAt: func(now time.Time) *time.Time {
			expires := now.Add(24*time.Hour + time.Nanosecond)
			return &expires
		}, wantErr: ErrTeamObjectInvalid},
		{name: "already expired", expiresAt: func(now time.Time) *time.Time {
			expires := now
			return &expires
		}, wantErr: ErrTeamObjectInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := baseNow
			options := reviewTeamOptions(testBootstrapRoot())
			options.Clock = func() time.Time { return now }
			f := newTeamObjectWriteFixtureWithOptions(t, filepath.Join(t.TempDir(), "team.db"), options)
			defer f.store.Close()
			authorization := mutationWriteRequest(f.bootstrap, f.actor)
			authorization.RequestedScope = &teamauth.CanonicalScope{
				Type: teamauth.ScopeSession, ID: "session-object-spine",
			}
			authorization.Context.SessionID = "session-object-spine"
			permit, err := f.store.AuthorizeTeamMutation(context.Background(), authorization)
			if err != nil {
				t.Fatal(err)
			}
			f.request.Permit = permit
			f.request.Policy.Retention = "session"
			if test.expiresAt != nil {
				f.request.Policy.ExpiresAt = test.expiresAt(now)
			}
			result, err := f.store.StoreTeamObject(context.Background(), f.request)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("session write: %v", err)
			}
			var storedExpiry string
			if err := f.store.DB().QueryRow(`SELECT expires_at FROM team_object_registry WHERE object_id = ?`, result.ObjectID).Scan(&storedExpiry); err != nil {
				t.Fatal(err)
			}
			parsed, err := time.Parse(time.RFC3339Nano, storedExpiry)
			if err != nil || !parsed.Equal(test.wantExpiry(baseNow)) {
				t.Fatalf("expiry = %q (%v), want %s", storedExpiry, err, test.wantExpiry(baseNow).Format(time.RFC3339Nano))
			}
			if test.expiresAt == nil {
				now = now.Add(time.Second)
				replayed, err := f.store.StoreTeamObject(context.Background(), f.request)
				if err != nil {
					t.Fatalf("default-expiry replay after clock advance: %v", err)
				}
				if !replayed.Replayed || replayed.ObjectID != result.ObjectID || replayed.AuditEventID != result.AuditEventID {
					t.Fatalf("default-expiry retry did not replay original: first=%+v replay=%+v", result, replayed)
				}
			}
		})
	}
}

func TestStoreTeamObjectReplayConcealsExpiredOrTombstonedRoot(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *teamObjectWriteFixture, TeamObjectWriteResult, *time.Time)
	}{
		{name: "expired", mutate: func(_ *testing.T, _ *teamObjectWriteFixture, _ TeamObjectWriteResult, now *time.Time) {
			*now = now.Add(20 * time.Second)
		}},
		{name: "tombstoned", mutate: func(t *testing.T, f *teamObjectWriteFixture, result TeamObjectWriteResult, _ *time.Time) {
			if _, err := f.store.DB().Exec(`
				UPDATE team_object_registry
				   SET lifecycle = 'tombstoned', generation = generation + 1
				 WHERE object_id = ?`, result.ObjectID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
			options := reviewTeamOptions(testBootstrapRoot())
			options.Clock = func() time.Time { return now }
			f := newTeamObjectWriteFixtureWithOptions(t, filepath.Join(t.TempDir(), "team.db"), options)
			defer f.store.Close()
			expires := now.Add(10 * time.Second)
			f.request.Policy.ExpiresAt = &expires
			result, err := f.store.StoreTeamObject(context.Background(), f.request)
			if err != nil {
				t.Fatalf("initial write: %v", err)
			}
			test.mutate(t, &f, result, &now)
			if _, err := f.store.StoreTeamObject(context.Background(), f.request); !errors.Is(err, ErrConcealedNotFound) {
				t.Fatalf("unavailable root replay error = %v", err)
			}
			var state string
			if err := f.store.DB().QueryRow(`SELECT state FROM team_idempotency_records WHERE object_id = ?`, result.ObjectID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "stored" {
				t.Fatalf("idempotency state changed to %q", state)
			}
		})
	}
}

func teamObjectTableCounts(t *testing.T, s *Store) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{
		"team_object_registry", "team_idempotency_records", "team_audit_events",
		"team_audit_event_order", "team_projection_jobs",
	} {
		var count int
		if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}
