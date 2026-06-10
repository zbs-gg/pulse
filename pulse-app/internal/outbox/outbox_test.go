package outbox

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

func newTestOutbox(t *testing.T) *Outbox {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s.DB(), 30*time.Second)
}

func TestEnqueueDedupes(t *testing.T) {
	ob := newTestOutbox(t)
	msg := Message{ChatID: 1, Text: "hello", ReplyTo: ptrInt64(42)}
	id1, err := ob.Enqueue(msg)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ob.Enqueue(msg)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("expected dedupe to return same id, got %d and %d", id1, id2)
	}
}

func TestClaimMarksSending(t *testing.T) {
	ob := newTestOutbox(t)
	id, err := ob.Enqueue(Message{ChatID: 1, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := ob.Claim(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != id {
		t.Fatalf("expected to claim id %d, got %v", id, claimed)
	}
	// Second claim should return empty (already sending).
	again, err := ob.Claim(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("expected no pending, got %d", len(again))
	}
}

func TestAckSuccessMarksSent(t *testing.T) {
	ob := newTestOutbox(t)
	id, _ := ob.Enqueue(Message{ChatID: 1, Text: "hi"})
	ob.Claim(10)
	if err := ob.Ack(id, true, ""); err != nil {
		t.Fatal(err)
	}
	again, _ := ob.Claim(10)
	if len(again) != 0 {
		t.Errorf("expected sent message not re-claimed, got %d", len(again))
	}
}

func TestAckFailureSchedulesRetry(t *testing.T) {
	ob := newTestOutbox(t)
	id, _ := ob.Enqueue(Message{ChatID: 1, Text: "hi"})
	ob.Claim(10)
	if err := ob.Ack(id, false, "timeout"); err != nil {
		t.Fatal(err)
	}
	// Immediately after failure, next_retry is in the future; should not claim.
	again, _ := ob.Claim(10)
	if len(again) != 0 {
		t.Errorf("expected no claim before retry window, got %d", len(again))
	}
}

func TestReapRequeuesStaleSending(t *testing.T) {
	ob := newTestOutbox(t)
	ob.leaseDuration = 1 * time.Millisecond
	id, _ := ob.Enqueue(Message{ChatID: 1, Text: "hi"})
	ob.Claim(10)
	time.Sleep(5 * time.Millisecond)
	n, err := ob.Reap()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 reaped row, got %d", n)
	}
	again, _ := ob.Claim(10)
	if len(again) != 1 || again[0].ID != id {
		t.Errorf("expected reclaimed row, got %v", again)
	}
}

func TestDedupeKeyDistinguishesNilReplyTo(t *testing.T) {
	ob := newTestOutbox(t)
	msg1 := Message{ChatID: 1, Text: "42|hello"} // ReplyTo nil
	msg2 := Message{ChatID: 1, ReplyTo: ptrInt64(42), Text: "hello"}
	id1, err := ob.Enqueue(msg1)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ob.Enqueue(msg2)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Errorf("expected different ids for different messages, got both %d", id1)
	}
}

func TestAckReachesFailedAtMaxAttempts(t *testing.T) {
	ob := newTestOutbox(t)
	id, err := ob.Enqueue(Message{ChatID: 1, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	// Loop Ack MaxAttempts times; each failure increments attempts.
	for i := 0; i < MaxAttempts; i++ {
		// Move next_retry back so Claim can pick up.
		if _, err := ob.db.Exec("UPDATE outbox SET next_retry = NULL WHERE id = ?", id); err != nil {
			t.Fatal(err)
		}
		claimed, err := ob.Claim(10)
		if err != nil {
			t.Fatal(err)
		}
		if len(claimed) == 0 {
			t.Fatalf("iteration %d: expected to claim", i)
		}
		if err := ob.Ack(id, false, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	var attempts int
	if err := ob.db.QueryRow("SELECT status, attempts FROM outbox WHERE id = ?", id).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Errorf("expected status 'failed', got %q", status)
	}
	if attempts != MaxAttempts {
		t.Errorf("expected attempts=%d, got %d", MaxAttempts, attempts)
	}
}

func ptrInt64(i int64) *int64 { return &i }
