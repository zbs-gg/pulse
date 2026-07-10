package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type mutableStoreClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableStoreClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableStoreClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func replayTeamOptions(clock *mutableStoreClock) TeamOpenOptions {
	options := reviewTeamOptions(testBootstrapRoot())
	options.Clock = clock.Now
	return options
}

func TestAssertionReplayConsumePersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "team.db")
	clock := &mutableStoreClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	root := testBootstrapRoot()
	s, err := OpenTeam(path, replayTeamOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{TeamName: "Replay", PresentedRoot: root}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeAssertionID(ctx, "kid-1", "jti-1", clock.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenTeam(path, replayTeamOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.ConsumeAssertionID(ctx, "kid-1", "jti-1", clock.Now().Add(5*time.Minute)); !errors.Is(err, ErrAssertionReplay) {
		t.Fatalf("replay after restart error = %v", err)
	}
}

func TestAssertionReplayConsumeIsAtomicAcrossConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "team.db")
	clock := &mutableStoreClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	root := testBootstrapRoot()
	first, err := OpenTeam(path, replayTeamOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.BootstrapTeam(ctx, BootstrapTeamRequest{TeamName: "Replay", PresentedRoot: root}); err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenTeam(path, replayTeamOptions(clock))
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
			results <- s.ConsumeAssertionID(ctx, "kid-race", "jti-race", clock.Now().Add(time.Minute))
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, replays := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAssertionReplay):
			replays++
		default:
			t.Fatalf("unexpected consume error: %v", err)
		}
	}
	if successes != 1 || replays != 1 {
		t.Fatalf("atomic results: successes=%d replays=%d", successes, replays)
	}
}

func TestAssertionReplayUsesStoreClockForExpiryAndPruning(t *testing.T) {
	ctx := context.Background()
	clock := &mutableStoreClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "team.db")
	s, err := OpenTeam(path, replayTeamOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := testBootstrapRoot()
	if _, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{TeamName: "Replay", PresentedRoot: root}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeAssertionID(ctx, "kid", "expired", clock.Now()); !errors.Is(err, ErrAssertionExpired) {
		t.Fatalf("expired assertion error = %v", err)
	}
	if err := s.ConsumeAssertionID(ctx, "kid", "prune-me", clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The caller has no cutoff parameter: pruning before the store clock
	// reaches expiry cannot erase the live replay record.
	removed, err := s.PruneExpiredAssertionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("future-prune bypass removed %d live records", removed)
	}
	if err := s.ConsumeAssertionID(ctx, "kid", "prune-me", clock.Now().Add(time.Hour)); !errors.Is(err, ErrAssertionReplay) {
		t.Fatalf("live replay record was bypassed: %v", err)
	}

	clock.Set(clock.Now().Add(2 * time.Hour))
	removed, err = s.PruneExpiredAssertionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("pruned = %d, want 1", removed)
	}
	if err := s.ConsumeAssertionID(ctx, "kid", "prune-me", clock.Now().Add(time.Hour)); err != nil {
		t.Fatalf("consume after store-time prune: %v", err)
	}
}
