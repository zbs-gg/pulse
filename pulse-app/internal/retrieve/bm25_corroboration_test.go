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
