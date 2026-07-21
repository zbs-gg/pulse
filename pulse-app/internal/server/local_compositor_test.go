package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

type compositorReaderStub struct {
	storeID  string
	items    []BoundContextItem
	err      error
	calls    int
	requests []BoundContextRequest
}

func (s *compositorReaderStub) StoreID() string { return s.storeID }
func (s *compositorReaderStub) ReadBoundContext(_ context.Context, request BoundContextRequest) ([]BoundContextItem, error) {
	s.calls++
	s.requests = append(s.requests, request)
	return append([]BoundContextItem(nil), s.items...), s.err
}

func validContextLease() ContextLease {
	return ContextLease{
		BindingID: "binding_zbs", ResolverEpoch: 7,
		ExpiresAt: time.Date(2026, 7, 14, 12, 5, 0, 0, time.UTC),
	}
}

func newTestCompositor(t *testing.T, desk, commons *compositorReaderStub) *LocalCompositor {
	t.Helper()
	compositor, err := NewLocalCompositor(LocalCompositorConfig{
		BindingID: "binding_zbs", ResolverEpoch: 7,
		DeskStoreID: "store_desk_dima", CommonsStoreID: "store_commons_zbs",
		Desk: desk, Commons: commons,
		Now: func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("new compositor: %v", err)
	}
	return compositor
}

func TestLocalCompositorKeepsDeskAndCommonsResultsPartitioned(t *testing.T) {
	desk := &compositorReaderStub{storeID: "store_desk_dima", items: []BoundContextItem{{ObjectID: "desk_1", StoreID: "store_desk_dima", Text: "private Dima context"}}}
	commons := &compositorReaderStub{storeID: "store_commons_zbs", items: []BoundContextItem{{ObjectID: "commons_1", StoreID: "store_commons_zbs", Text: "shared practice"}}}
	result, err := newTestCompositor(t, desk, commons).Query(context.Background(), BoundContextRequest{
		DeskQuery: "private resume", CommonsQuery: "shared project resume", Lease: validContextLease(),
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(result.Desk) != 1 || len(result.Commons) != 1 || result.Fallback {
		t.Fatalf("unexpected partitioned result: %#v", result)
	}
	if result.Desk[0].StoreID == result.Commons[0].StoreID {
		t.Fatal("desk and commons provenance collapsed")
	}
	if desk.requests[0].CommonsQuery != "" || desk.requests[0].DeskQuery != "private resume" ||
		commons.requests[0].DeskQuery != "" || commons.requests[0].CommonsQuery != "shared project resume" {
		t.Fatal("local Desk query crossed into Commons or vice versa")
	}
}

func TestLocalCompositorOptionalCommonsOutageNeverFallsBack(t *testing.T) {
	desk := &compositorReaderStub{storeID: "store_desk_dima", items: []BoundContextItem{{ObjectID: "desk_1", StoreID: "store_desk_dima"}}}
	commons := &compositorReaderStub{storeID: "store_commons_zbs", err: errors.New("outage")}
	result, err := newTestCompositor(t, desk, commons).Query(context.Background(), BoundContextRequest{
		DeskQuery: "resume", CommonsQuery: "project resume", Lease: validContextLease(), Mandatory: false,
	})
	if err != nil {
		t.Fatalf("optional query: %v", err)
	}
	if result.Fallback || len(result.Desk) != 1 || len(result.Commons) != 0 ||
		len(result.DegradedReasons) != 1 || result.DegradedReasons[0] != "commons_unavailable" {
		t.Fatalf("unexpected degraded result: %#v", result)
	}

	_, err = newTestCompositor(t, desk, commons).Query(context.Background(), BoundContextRequest{
		DeskQuery: "mandatory", CommonsQuery: "mandatory practice", Lease: validContextLease(), Mandatory: true,
	})
	if !errors.Is(err, ErrMandatoryContextUnavailable) {
		t.Fatalf("mandatory outage error = %v", err)
	}
}

func TestLocalCompositorFailsBeforeReadsForStaleBindingLease(t *testing.T) {
	desk := &compositorReaderStub{storeID: "store_desk_dima"}
	commons := &compositorReaderStub{storeID: "store_commons_zbs"}
	lease := validContextLease()
	lease.ResolverEpoch = 6
	_, err := newTestCompositor(t, desk, commons).Query(context.Background(), BoundContextRequest{Lease: lease})
	if !errors.Is(err, ErrContextLeaseStale) {
		t.Fatalf("stale lease error = %v", err)
	}
	if desk.calls != 0 || commons.calls != 0 {
		t.Fatal("stale lease queried a vault")
	}
}

func TestLocalCompositorRejectsReaderStoreIdentityMismatch(t *testing.T) {
	_, err := NewLocalCompositor(LocalCompositorConfig{
		BindingID: "binding_zbs", ResolverEpoch: 7,
		DeskStoreID: "store_desk_dima", CommonsStoreID: "store_commons_zbs",
		Desk:    &compositorReaderStub{storeID: "store_desk_nik"},
		Commons: &compositorReaderStub{storeID: "store_commons_zbs"},
	})
	if err == nil {
		t.Fatal("expected fixed desk identity mismatch")
	}
}
