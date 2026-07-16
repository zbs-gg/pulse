package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func reviewTeamOptions(root teamauth.BootstrapRoot) TeamOpenOptions {
	return TeamOpenOptions{
		ExpectedBootstrapRoot: root,
		Clock: func() time.Time {
			return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
		},
	}
}

func TestBootstrapRootIsPinnedWhenTeamStoreOpens(t *testing.T) {
	ctx := context.Background()
	expected := testBootstrapRoot()
	s, err := OpenTeam(filepath.Join(t.TempDir(), "team.db"), reviewTeamOptions(expected))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	attacker := teamauth.BootstrapRoot{
		Issuer: "https://attacker.example", Subject: "self", AdminClientID: "self-client",
	}
	if _, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Attacker", PresentedRoot: attacker,
	}); !errors.Is(err, ErrBootstrapRootMismatch) {
		t.Fatalf("arbitrary self-root bootstrap error = %v", err)
	}
	if _, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Pinned", PresentedRoot: expected,
	}); err != nil {
		t.Fatalf("deployment-pinned root bootstrap: %v", err)
	}
}

func TestOpenTeamRejectsWhitespaceNormalizedBootstrapRoot(t *testing.T) {
	root := testBootstrapRoot()
	root.Subject = " " + root.Subject
	if _, err := OpenTeam(filepath.Join(t.TempDir(), "team.db"), reviewTeamOptions(root)); err == nil {
		t.Fatal("OpenTeam accepted bootstrap identity with surrounding whitespace")
	}
}

func TestExistingLatestLocalDatabaseCannotBecomeTeamStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pulse.db")
	local, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}

	root := testBootstrapRoot()
	if _, err := OpenTeam(path, reviewTeamOptions(root)); !errors.Is(err, ErrStoreIdentityMismatch) {
		t.Fatalf("retroactive team open error = %v", err)
	}
}

func TestExistingVersion32DatabaseCannotUseRetroactiveManifestAsBootstrapProof(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pulse-v32.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER PRIMARY KEY, applied TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:32] {
		if _, err := db.Exec(migration.SQL); err != nil {
			t.Fatalf("apply fixture migration %d: %v", migration.Version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_meta(version, applied) VALUES (?, 'legacy')`, migration.Version); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	root := testBootstrapRoot()
	team, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatalf("migrate legacy v32 to current: %v", err)
	}
	defer team.Close()
	if _, err := team.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Retroactive Hashes", PresentedRoot: root,
	}); !errors.Is(err, ErrTeamBootstrapCandidateRequired) {
		t.Fatalf("v32 retroactive-manifest bootstrap error = %v", err)
	}
	var manifestRows, candidates int
	if err := team.DB().QueryRow(`SELECT count(*) FROM schema_migration_manifest`).Scan(&manifestRows); err != nil {
		t.Fatal(err)
	}
	if err := team.DB().QueryRow(`SELECT count(*) FROM team_bootstrap_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if manifestRows != 46 || candidates != 0 {
		t.Fatalf("v32 adoption state: manifest=%d candidates=%d", manifestRows, candidates)
	}
}

func TestSchemaZeroDatabaseWithEmptyRogueTableIsNotBootstrapCandidate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rogue-schema-zero.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE rogue_empty_table (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	root := testBootstrapRoot()
	team, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	var candidates int
	if err := team.DB().QueryRow(`SELECT count(*) FROM team_bootstrap_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	_, bootstrapErr := team.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Rogue Empty", PresentedRoot: root,
	})
	if candidates != 0 || !errors.Is(bootstrapErr, ErrTeamBootstrapCandidateRequired) {
		t.Fatalf("rogue schema-zero eligibility: candidates=%d bootstrap_error=%v", candidates, bootstrapErr)
	}
}

func TestBootstrapAuditUsesIssuerScopedOAuthClientKey(t *testing.T) {
	ctx := context.Background()
	root := testBootstrapRoot()
	s, err := OpenTeam(filepath.Join(t.TempDir(), "team.db"), reviewTeamOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Audit Client", PresentedRoot: root,
	}); err != nil {
		t.Fatal(err)
	}
	var clientKey string
	if err := s.DB().QueryRow(`
		SELECT client_key FROM team_audit_events WHERE action = 'team.bootstrap'`).Scan(&clientKey); err != nil {
		t.Fatal(err)
	}
	want := teamauth.OAuthClientKey(root.Issuer, root.AdminClientID)
	if clientKey != want {
		t.Fatalf("bootstrap audit client key = %q, want issuer-scoped %q", clientKey, want)
	}
}

func TestFreshTeamBootstrapCandidateSurvivesRestartAndIsConsumed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "team.db")
	root := testBootstrapRoot()
	first, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Fresh", PresentedRoot: root,
	}); err != nil {
		t.Fatalf("bootstrap after restart: %v", err)
	}
	var candidates int
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM team_bootstrap_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("bootstrap candidate rows after consumption = %d", candidates)
	}
}

func TestConcurrentBootstrapConsumesCandidateExactlyOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "team.db")
	root := testBootstrapRoot()
	first, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []*Store{first, second} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			<-start
			_, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
				TeamName: "Concurrent", PresentedRoot: root,
			})
			results <- err
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, consumed := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrBootstrapConsumed):
			consumed++
		default:
			t.Fatalf("unexpected concurrent bootstrap error: %v", err)
		}
	}
	if successes != 1 || consumed != 1 {
		t.Fatalf("concurrent bootstrap: successes=%d consumed=%d", successes, consumed)
	}
}
