package contextquery

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

// captureRetrieval records the RetrieveRequest it was called with so we can
// assert that the contextquery service forwards GraphMode onto the LIVE path.
type captureRetrieval struct{ got *retrieve.RetrieveRequest }

func (c *captureRetrieval) Retrieve(_ context.Context, req retrieve.RetrieveRequest) (*retrieve.RetrieveResponse, error) {
	*c.got = req
	return &retrieve.RetrieveResponse{EventIDs: nil, ModeUsed: retrieve.ModeEmpathic,
		RouterDecision: retrieve.RouteDecision{Mode: retrieve.ModeEmpathic}}, nil
}

func TestContextQuery_ForwardsGraphMode(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "cq.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	cases := []struct {
		name        string
		svcDefault  string
		reqOverride string
		want        string
	}{
		{"service default anchored", "anchored", "", "anchored"},
		{"per-request override walk", "anchored", "walk", "walk"},
		{"per-request off override", "anchored", "off", "off"},
		{"default off", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got retrieve.RetrieveRequest
			svc := New(ServiceConfig{DB: s.DB(), Retrieval: &captureRetrieval{got: &got}, GraphMode: tc.svcDefault})
			if _, err := svc.Query(context.Background(), ContextQueryRequest{
				Query: "what connects A and C", GraphMode: tc.reqOverride}); err != nil {
				t.Fatalf("Query: %v", err)
			}
			if got.GraphMode != tc.want {
				t.Fatalf("GraphMode forwarded = %q, want %q", got.GraphMode, tc.want)
			}
		})
	}
}
