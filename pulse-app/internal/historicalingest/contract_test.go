package historicalingest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestManifestRoundTripsEveryMaterialKind(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	items := []MaterialItem{
		materialFixture(MaterialKindEvent, MaterialPayload{Title: "Pilot", Summary: "Started the pilot"}),
		materialFixture(MaterialKindDecision, MaterialPayload{Summary: "Use a local-first store"}),
		materialFixture(MaterialKindAssertion, MaterialPayload{SubjectID: "person_owner", Predicate: "leads", ObjectValue: "Pulse"}),
		materialFixture(MaterialKindPerson, MaterialPayload{EntityType: "person", Name: "Owner"}),
		materialFixture(MaterialKindProject, MaterialPayload{EntityType: "project", Name: "Pulse"}),
		materialFixture(MaterialKindRelation, MaterialPayload{SubjectID: "person_owner", Predicate: "works_on", ObjectID: "project_pulse"}),
		materialFixture(MaterialKindState, MaterialPayload{StateKind: "focused", Summary: "Focused on release"}),
		materialFixture(MaterialKindContinuity, MaterialPayload{Summary: "Finish the ingest pilot", ContinuityStatus: "open"}),
	}
	for index := range items {
		items[index].CandidateID = fmt.Sprintf("candidate_%016x", index+1)
	}
	items[0].ValidTime.To = &to

	manifest := Manifest{
		SchemaVersion:        SchemaVersionV1,
		JobID:                "job_0123456789abcdef",
		Revision:             1,
		SourceSnapshotDigest: strings.Repeat("a", 64),
		Items:                items,
	}

	encoded, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	decoded, err := DecodeManifest(encoded)
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(decoded.Items) != len(items) {
		t.Fatalf("decoded items = %d, want %d", len(decoded.Items), len(items))
	}
	for index, item := range decoded.Items {
		if item.Kind != items[index].Kind {
			t.Fatalf("item[%d] kind = %q, want %q", index, item.Kind, items[index].Kind)
		}
	}
}

func TestManifestValidationFailsClosed(t *testing.T) {
	t.Parallel()

	base := Manifest{
		SchemaVersion:        SchemaVersionV1,
		JobID:                "job_0123456789abcdef",
		Revision:             1,
		SourceSnapshotDigest: strings.Repeat("b", 64),
		Items:                []MaterialItem{materialFixture(MaterialKindDecision, MaterialPayload{Summary: "Keep exact receipts"})},
	}

	tests := map[string]func(*Manifest){
		"unknown kind":       func(value *Manifest) { value.Items[0].Kind = MaterialKind("unknown") },
		"missing provenance": func(value *Manifest) { value.Items[0].SourceRefs = nil },
		"invalid interval": func(value *Manifest) {
			before := value.Items[0].ValidTime.From.Add(-time.Second)
			value.Items[0].ValidTime.To = &before
		},
		"inferred state marked explicit": func(value *Manifest) {
			value.Items[0].Kind = MaterialKindState
			value.Items[0].Payload = MaterialPayload{StateKind: "angry", Summary: "Inferred anger"}
			value.Items[0].Derivation = DerivationInferred
			value.Items[0].EpistemicStatus = EpistemicExplicit
		},
		"scope widens by default": func(value *Manifest) {
			value.Items[0].Scope = Scope{Kind: ScopeProject}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneManifest(t, base)
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDecodeManifestRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		SchemaVersion:        SchemaVersionV1,
		JobID:                "job_0123456789abcdef",
		Revision:             1,
		SourceSnapshotDigest: strings.Repeat("c", 64),
		Items:                []MaterialItem{materialFixture(MaterialKindDecision, MaterialPayload{Summary: "Reject extras"})},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	encoded = bytes.Replace(encoded, []byte(`"revision":1`), []byte(`"revision":1,"unexpected":true`), 1)
	if _, err := DecodeManifest(encoded); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestEmbeddedSchemaIsClosedAndDigestStable(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(SchemaBytes(), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	if schema["$id"] != SchemaVersionV1 {
		t.Fatalf("schema id = %#v, want %q", schema["$id"], SchemaVersionV1)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("top-level schema must reject additional properties")
	}
	if len(SchemaDigest()) != 64 {
		t.Fatalf("schema digest length = %d, want 64", len(SchemaDigest()))
	}
}

func TestMaterialTextRejectsControlAndBidiCharacters(t *testing.T) {
	for _, value := range []string{"hidden\x00control", "right\u202eto-left", "line\nfeed"} {
		item := materialFixture(MaterialKindDecision, MaterialPayload{Summary: value})
		if err := item.Validate(); err == nil {
			t.Fatalf("unsafe material text %q was accepted", value)
		}
	}
}

func TestHistoricalApplyRuntimeProfile(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "apply.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(FULL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateApplyRuntime(db); err != nil {
		t.Fatalf("validate apply runtime: %v", err)
	}

	if _, err := db.Exec(`PRAGMA synchronous=NORMAL`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateApplyRuntime(db); err == nil || !strings.Contains(err.Error(), "synchronous") {
		t.Fatalf("runtime error = %v, want synchronous failure", err)
	}
}

func TestSupportedSQLiteVersions(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"3.44.6", "3.50.7", "3.51.3", "3.52.0"} {
		if !SupportedSQLiteVersion(version) {
			t.Errorf("version %s should be accepted", version)
		}
	}
	for _, version := range []string{"3.44.5", "3.50.6", "3.51.2", "invalid"} {
		if SupportedSQLiteVersion(version) {
			t.Errorf("version %s should be rejected", version)
		}
	}
}

func materialFixture(kind MaterialKind, payload MaterialPayload) MaterialItem {
	return MaterialItem{
		CandidateID:     "candidate_0123456789abcdef",
		Kind:            kind,
		Confidence:      0.9,
		Privacy:         PrivacyPrivate,
		EpistemicStatus: EpistemicExplicit,
		Derivation:      DerivationDirect,
		ValidTime:       ValidTime{From: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)},
		Scope:           Scope{Kind: ScopeProject, ProjectID: "project_pulse"},
		SourceRefs: []SourceRef{{
			Alias:         "source_0123456789abcdef",
			PrefixDigest:  strings.Repeat("d", 64),
			RecordLocator: "record:1",
		}},
		Payload: payload,
	}
}

func cloneManifest(t *testing.T, value Manifest) Manifest {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned Manifest
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}
