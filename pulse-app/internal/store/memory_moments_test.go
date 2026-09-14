package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMemoryMomentAllPartsReplayAndSameTurn(t *testing.T) {
	for _, count := range []int{4, 21, 61} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, _ := openPersonalTrayStore(t)
			now := time.Now().UTC()
			req := trayFinalizeRequest("A complete meaningful moment.")
			req.Candidates = nil
			for i := 0; i < count; i++ {
				c := safeTrayCapsule(fmt.Sprintf("Meaningful part number %d of the same remembered moment.", i))
				req.Candidates = append(req.Candidates, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &c})
			}
			result, err := s.FinalizeMemoryMoment(req, now, "", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Receipts) != count || result.MomentID == "" {
				t.Fatalf("incomplete: %+v", result)
			}
			for _, receipt := range result.Receipts {
				if _, err := s.CommitMemoryTrayCandidate(receipt.CandidateID, receipt.CandidateVersion, now); err != nil {
					t.Fatal(err)
				}
			}
			replay, err := s.FinalizeMemoryMoment(req, now.Add(time.Second), "", "", 0)
			if err != nil || replay.LedgerID != result.LedgerID {
				t.Fatalf("replay: %v %+v", err, replay)
			}
			for _, receipt := range replay.Receipts {
				if receipt.ObjectID == "" || receipt.Status != MemoryWriteCreated {
					t.Fatal(receipt)
				}
			}
			var countRead int
			for cursor := 0; ; {
				page, err := s.ReadMemoryMoment(result.MomentID, req.BindingDigest, "", cursor)
				if err != nil {
					t.Fatal(err)
				}
				countRead += len(page.Items)
				if page.Next == 0 {
					break
				}
				cursor = page.Next
			}
			if countRead != count {
				t.Fatalf("read %d of %d", countRead, count)
			}
			req.Candidates = []PrivateMemoryCandidate{{Kind: PrivateMemoryCandidateCapsule, Capsule: ptr(safeTrayCapsule("An additional distinct feeling in the same host turn."))}}
			second, err := s.FinalizeMemoryMoment(req, now, "", "", 0)
			if err != nil || second.MomentID == result.MomentID {
				t.Fatalf("second operation: %v", err)
			}
		})
	}
}

func TestMemoryMomentInvalidPartAdmitsNothing(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	req := trayFinalizeRequest("Valid detail to preserve.")
	bad := safeTrayCapsule("Invalid part.")
	bad.RawInputIncluded = true
	req.Candidates = append(req.Candidates, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &bad})
	if _, err := s.FinalizeMemoryMoment(req, time.Now(), "", "", 0); err == nil {
		t.Fatal("accepted unsafe set")
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM turn_ledgers`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("persisted rejected payload: %d %v", n, err)
	}
}

func TestMemoryMomentReadHonorsScopeCorrectionDeletionAndLinkedWrites(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Now().UTC()
	req := trayFinalizeRequest("Original project detail.")
	req.Candidates[0].MemoryScope = "project"
	global := safeTrayCapsule("A personal detail explicitly shared across projects.")
	global.Items[0].Retention = "long_term"
	req.Candidates = append(req.Candidates, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, MemoryScope: "personal_global", Capsule: &global})
	result, err := s.FinalizeMemoryMoment(req, now, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var projectObject string
	for i, receipt := range result.Receipts {
		committed, err := s.CommitMemoryTrayCandidate(receipt.CandidateID, receipt.CandidateVersion, now)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			projectObject = committed.ObjectID
		}
	}
	foreign, err := s.ReadMemoryMoment(result.MomentID, strings.Repeat("b", 64), "repository_foreign", 0)
	if err != nil || len(foreign.Items) != 1 || foreign.Items[0].Candidate.MemoryScope != "personal_global" {
		t.Fatalf("scope leak: %+v %v", foreign, err)
	}
	corrected := safeTrayCapsule("Corrected project detail.")
	correction, err := s.PrepareMemoryCorrection(projectObject, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &corrected}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r := correction.Receipts[0]
	if _, err = s.CommitMemoryTrayCandidate(r.CandidateID, r.CandidateVersion, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadMemoryMoment(result.MomentID, req.BindingDigest, "", 0)
	if err != nil || len(page.Items) != 2 || page.Items[0].Candidate.Capsule.Items[0].RedactedSummary != "Corrected project detail." {
		t.Fatalf("stale correction: %+v %v", page, err)
	}
	if _, err = s.DeleteCommittedMemory(projectObject, "delete_moment_part", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	req.Candidates = []PrivateMemoryCandidate{{Kind: PrivateMemoryCandidateCapsule, Capsule: ptr(safeTrayCapsule("Another important part added in the same real host turn."))}}
	added, err := s.FinalizeMemoryMoment(req, now, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	r = added.Receipts[0]
	if _, err = s.CommitMemoryTrayCandidate(r.CandidateID, r.CandidateVersion, now); err != nil {
		t.Fatal(err)
	}
	page, err = s.ReadMemoryMoment(result.MomentID, req.BindingDigest, "", 0)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("linked write or deletion lost: %+v %v", page, err)
	}
	for _, item := range page.Items {
		if item.ObjectID == projectObject {
			t.Fatal("deleted memory reappeared")
		}
	}
}
