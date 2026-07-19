package server

import (
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func TestSupportedHostLifecycleReadinessRequiresOneSameHostSaveRecallChain(t *testing.T) {
	terminal := TerminalMemoryReadinessFact{
		ReceiptID: "receipt_memory_01", PresentationReceiptID: "presentation_01", ObjectID: "pulse:memory_01",
		Status: string(store.MemoryWriteCreated), ContentDigest: strings.Repeat("c", 64), MemoryKind: "decision",
		ConversationScope: "current_turn", BindingDigest: strings.Repeat("a", 64), RepositoryID: "repository-pulse",
		Host: "codex", SessionRef: "session:" + strings.Repeat("d", 64), CreatedAt: "2026-07-16T01:00:00Z", Active: true,
	}
	offer := ContextDeliveryReadinessFact{
		ContextID: "context_01", Acknowledgement: "offered_to_host", Purpose: "session_start",
		ObjectIDs: []string{terminal.ObjectID}, PayloadDigest: strings.Repeat("b", 64),
		BindingDigest: terminal.BindingDigest, RepositoryID: terminal.RepositoryID, Host: "codex",
		SessionRef: "session:" + strings.Repeat("e", 64), CreatedAt: "2026-07-16T01:01:00Z",
	}
	observed := offer
	observed.Acknowledgement = "host_observed"
	observed.CreatedAt = "2026-07-16T01:02:00Z"

	projection := projectSupportedHostLifecycleReadiness(
		[]TerminalMemoryReadinessFact{terminal}, []ContextDeliveryReadinessFact{offer, observed},
	)
	if projection.Schema != supportedHostLifecycleReadinessSchema || len(projection.Hosts) != 3 {
		t.Fatalf("projection=%#v", projection)
	}
	for _, host := range projection.Hosts {
		if host.Host == "codex" {
			if !host.LifecycleReady || host.State != "ready" || host.ObjectID != terminal.ObjectID ||
				host.ContextID != offer.ContextID || !slicesEqual(host.Milestones, []string{"write_receipt", "session_context", "prompt_context"}) {
				t.Fatalf("Codex same-host proof=%#v", host)
			}
		} else if host.LifecycleReady || host.State != "first_memory_pending" {
			t.Fatalf("unproven host became ready: %#v", host)
		}
	}

	crossHostOffer := offer
	crossHostOffer.Host = "cursor"
	crossHostObserved := observed
	crossHostObserved.Host = "cursor"
	crossHost := projectSupportedHostLifecycleReadiness(
		[]TerminalMemoryReadinessFact{terminal}, []ContextDeliveryReadinessFact{crossHostOffer, crossHostObserved},
	)
	for _, host := range crossHost.Hosts {
		if host.LifecycleReady {
			t.Fatalf("cross-host continuity alone proved a per-host capability floor: %#v", crossHost)
		}
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
