package retrieve

import (
	"context"
	"reflect"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func TestFTSCorroborateCandidatesUsesDistinctiveWordsInsideDenseCandidates(t *testing.T) {
	vault, err := store.Open(t.TempDir() + "/corroboration.db")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	for _, row := range []struct {
		id    int64
		title string
	}{
		{1, "Apple FastVLM has a research-only license"},
		{2, "An unrelated commercial product decision"},
	} {
		if _, err := vault.DB().Exec(
			`INSERT INTO events(id,title,description,ts) VALUES(?,?,?,CURRENT_TIMESTAMP)`,
			row.id, row.title, row.title,
		); err != nil {
			t.Fatal(err)
		}
	}

	got, err := FTSCorroborateCandidates(
		context.Background(), vault.DB(),
		"Почему FastVLM не подошла для коммерческого продукта?", []int64{1, 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("corroborated=%v want=%v", got, want)
	}

	got, err = FTSCorroborateCandidates(
		context.Background(), vault.DB(), "Почему это не подошло?", []int64{1, 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("generic question words corroborated candidates: %v", got)
	}
}

func TestBM25CapsuleSearchRanksOnlyActiveCapsules(t *testing.T) {
	vault, err := store.Open(t.TempDir() + "/capsule-search.db")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	for _, row := range []struct {
		id    int64
		title string
	}{
		{1, "Caroline has support from friends family and mentors"},
		{2, "Caroline met friends family and mentors in the week before 2023-06-09"},
		{3, "Caroline met friends family and mentors in an archive transcript"},
	} {
		if _, err := vault.DB().Exec(
			`INSERT INTO events(id,title,description,ts) VALUES(?,?,?,CURRENT_TIMESTAMP)`,
			row.id, row.title, row.title,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := vault.DB().Exec(`
		INSERT INTO memory_capsules(
		  id,schema_version,source_host,conversation_scope,source_timestamp,kind,
		  redacted_summary,confidence,evidence_hint,privacy_tier,retention,tags,
		  created_at,status,event_id
		) VALUES
		  ('capsule_generic','pulse.memory_capsule.v1','codex','current_turn',CURRENT_TIMESTAMP,'fact',
		   'generic',1,'current_turn','normal','project','[]',CURRENT_TIMESTAMP,'active',1),
		  ('capsule_dated','pulse.memory_capsule.v1','codex','current_turn',CURRENT_TIMESTAMP,'fact',
		   'dated',1,'current_turn','normal','project','[]',CURRENT_TIMESTAMP,'active',2),
		  ('capsule_inactive','pulse.memory_capsule.v1','codex','current_turn',CURRENT_TIMESTAMP,'fact',
		   'inactive',1,'current_turn','normal','project','[]',CURRENT_TIMESTAMP,'merged',3)
	`); err != nil {
		t.Fatal(err)
	}
	got, err := BM25CapsuleSearchScoped(
		context.Background(), vault.DB(),
		"When did Caroline meet friends family mentors week before 2023-06-09?", 5, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("capsule ranking=%v want=[2 1]", got)
	}
}
