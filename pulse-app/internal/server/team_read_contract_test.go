package server

import (
	"strings"
	"testing"
)

func TestDecodeTeamReadContractsAppliesExactDefaults(t *testing.T) {
	recall, err := decodeTeamRecallRequest([]byte(`{
		"schema":"pulse.team.recall.v1",
		"query":"scoped retrieval",
		"active_context":{"project_id":"project-pulse"},
		"privacy_ceiling":"sensitive"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if recall.Schema != TeamRecallSchema || recall.Query != "scoped retrieval" ||
		recall.ActiveContext.ProjectID != "project-pulse" || recall.PrivacyCeiling != "sensitive" ||
		recall.Retention != "" || recall.Limit != 5 {
		t.Fatalf("recall request = %+v", recall)
	}

	contextRequest, err := decodeTeamContextRequest([]byte(`{
		"schema":"pulse.team.context.v1",
		"query":"current plan",
		"active_context":{},
		"privacy_ceiling":"normal"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if contextRequest.Schema != TeamContextSchema || contextRequest.Limit != 10 ||
		contextRequest.IncludeTrace || contextRequest.GraphMode != "anchored" {
		t.Fatalf("context request = %+v", contextRequest)
	}

	resume, err := decodeTeamResumeRequest([]byte(`{
		"schema":"pulse.team.resume.v1",
		"active_context":{"session_id":"session-pulse"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if resume.Schema != TeamResumeSchema || resume.ActiveContext.SessionID != "session-pulse" ||
		resume.ThreadID != "" || resume.Limit != 20 {
		t.Fatalf("resume request = %+v", resume)
	}
}

func TestDecodeTeamReadContractsRejectsAuthoritySpoofingAndOpenShapes(t *testing.T) {
	validRecall := `{
		"schema":"pulse.team.recall.v1",
		"query":"scoped retrieval",
		"active_context":{},
		"privacy_ceiling":"normal"
	}`
	for _, fragment := range []string{
		`,"principal_id":"principal-spoofed"}`,
		`,"team_id":"team-spoofed"}`,
		`,"owner_id":"owner-spoofed"}`,
		`,"unexpected":true}`,
	} {
		body := strings.TrimSuffix(validRecall, "}") + fragment
		if _, err := decodeTeamRecallRequest([]byte(body)); err == nil {
			t.Fatalf("authority/open field accepted: %s", fragment)
		}
	}
	if _, err := decodeTeamRecallRequest([]byte(`{
		"schema":"pulse.team.recall.v1",
		"query":null,
		"active_context":{},
		"privacy_ceiling":"normal"
	}`)); err == nil {
		t.Fatal("null query accepted")
	}
	if _, err := decodeTeamContextRequest([]byte(`{
		"schema":"pulse.team.context.v1",
		"query":"context",
		"active_context":{"project_id":"../private"},
		"privacy_ceiling":"normal"
	}`)); err == nil {
		t.Fatal("unsafe active context accepted")
	}
	if _, err := decodeTeamResumeRequest([]byte(`{
		"schema":"pulse.team.resume.v1",
		"active_context":{}
	}`)); err == nil {
		t.Fatal("resume without thread, project, or session accepted")
	}
}
