package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testGitMemoryBinding    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testGitMemoryRepository = "repository_git_memory_test"
	testGitMemoryProject    = "project_0123456789abcdef0123456789abcdef"
)

func openGitMemoryDeskStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "desk.db")
	vault, err := OpenVault(path, StoreKindDesk, "store_desk_git_memory_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(testGitMemoryBinding, 0, 0); err != nil {
		_ = vault.Close()
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(testGitMemoryBinding, testGitMemoryRepository); err != nil {
		_ = vault.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return vault, path
}

func gitMemorySourceRegistration(locator, digest string, bytes int64) ProjectSourceRegistration {
	return ProjectSourceRegistration{
		Schema: "pulse.project_source.register.v1", PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		SourceKind: "repository_text", Locator: locator, VersionDigest: digest,
		ByteCount: bytes, ObservedAt: "2026-07-16T08:00:00Z",
	}
}

func TestProjectSourceRegistrationIsVersionedMetadataOnly(t *testing.T) {
	vault, path := openGitMemoryDeskStore(t)
	sentinel := "RAW_SOURCE_SENTINEL_NEVER_PERSISTED"
	first, err := vault.RegisterProjectSource(gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120), time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := vault.RegisterProjectSource(gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120), time.Date(2026, 7, 16, 8, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := vault.RegisterProjectSource(gitMemorySourceRegistration("notes/team.md", strings.Repeat("c", 64), 180), time.Date(2026, 7, 16, 8, 2, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceID == "" || first.VersionID == "" || first.Status != "registered" {
		t.Fatalf("first registration = %#v", first)
	}
	if unchanged.SourceID != first.SourceID || unchanged.VersionID != first.VersionID || unchanged.Status != "unchanged" {
		t.Fatalf("unchanged registration = %#v; first = %#v", unchanged, first)
	}
	if changed.SourceID != first.SourceID || changed.VersionID == first.VersionID || changed.Status != "changed" {
		t.Fatalf("changed registration = %#v; first = %#v", changed, first)
	}
	status, err := vault.ProjectSourceStatus(ProjectSourceStatusRequest{
		Schema: "pulse.project_source.status.v1", PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		SourceID: first.SourceID,
	})
	if err != nil || status.CurrentVersionDigest != strings.Repeat("c", 64) || status.VersionCount != 2 {
		t.Fatalf("source status = %#v, err=%v", status, err)
	}
	if _, err := vault.DB().Exec(`INSERT INTO git_memory_audit(audit_id, action, outcome, reason_code, created_at) VALUES ('audit_sentinel_probe', 'probe', 'rejected', ?, '2026-07-16T08:03:00Z')`, sentinel); err == nil {
		t.Fatal("audit accepted an arbitrary raw reason")
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		body, err := os.ReadFile(candidate)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(body), sentinel) {
			t.Fatalf("raw sentinel persisted in %s", candidate)
		}
	}
}

func TestProjectSourceRejectsUntrustedOrUnsafeMetadata(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	for name, mutate := range map[string]func(*ProjectSourceRegistration){
		"wrong binding":     func(req *ProjectSourceRegistration) { req.BindingDigest = strings.Repeat("f", 64) },
		"wrong repository":  func(req *ProjectSourceRegistration) { req.RepositoryID = "repository_other" },
		"absolute locator":  func(req *ProjectSourceRegistration) { req.Locator = "/Users/private/team.md" },
		"traversal locator": func(req *ProjectSourceRegistration) { req.Locator = "../team.md" },
		"unsupported type":  func(req *ProjectSourceRegistration) { req.SourceKind = "binary" },
		"oversized":         func(req *ProjectSourceRegistration) { req.ByteCount = projectSourceMaxBytes + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			req := gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120)
			mutate(&req)
			if _, err := vault.RegisterProjectSource(req, time.Now()); err == nil {
				t.Fatal("unsafe source metadata was accepted")
			}
		})
	}
}

func TestMigration048AppliesOnlyToPersonalAndDesk(t *testing.T) {
	desk, _ := openGitMemoryDeskStore(t)
	var disposition string
	if err := desk.DB().QueryRow(`
		SELECT disposition FROM schema_migration_applicability WHERE version=48`,
	).Scan(&disposition); err != nil || disposition != "applied" {
		t.Fatalf("desk migration 048 disposition=%q err=%v", disposition, err)
	}
	preview, err := Open(filepath.Join(t.TempDir(), "preview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	if err := preview.DB().QueryRow(`
		SELECT disposition FROM schema_migration_applicability WHERE version=48`,
	).Scan(&disposition); err != nil || disposition != "skipped" {
		t.Fatalf("preview migration 048 disposition=%q err=%v", disposition, err)
	}
	var tableCount int
	if err := preview.DB().QueryRow(`
		SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='git_memory_sources'`,
	).Scan(&tableCount); err != nil || tableCount != 0 {
		t.Fatalf("preview git_memory_sources=%d err=%v", tableCount, err)
	}
}
