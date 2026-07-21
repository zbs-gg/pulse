package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBootstrapLegacyCatalogCannotBeBypassedByTableName(t *testing.T) {
	for _, table := range []string{
		"teams",
		"teamwork",
		"team_evil",
		"drafts",
		"gifts",
		"select",
		"order items",
		`table "quoted" name`,
	} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			root := testBootstrapRoot()
			s, err := OpenTeam(t.TempDir()+"/team.db", reviewTeamOptions(root))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			identifier := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
			if _, err := s.DB().Exec(`CREATE TABLE ` + identifier + ` (value TEXT)`); err != nil {
				t.Fatalf("create legacy table: %v", err)
			}
			if _, err := s.DB().Exec(`INSERT INTO ` + identifier + ` (value) VALUES ('legacy')`); err != nil {
				t.Fatalf("seed legacy table: %v", err)
			}
			if _, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
				TeamName: "Must Refuse", PresentedRoot: root,
			}); !errors.Is(err, ErrLegacyLocalData) {
				t.Fatalf("bootstrap with legacy table %q error = %v", table, err)
			}
		})
	}
}
