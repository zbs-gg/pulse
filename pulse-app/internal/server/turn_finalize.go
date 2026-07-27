package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

const ReadinessLifecycleInputsSchema = "pulse.readiness_lifecycle_inputs.v1"

// TerminalMemoryReadinessFact is the content-free terminal write fact consumed
// by the future pure ReadinessSnapshot projection. The store remains the
// authority for these fields; this type is not a second onboarding state.
type TerminalMemoryReadinessFact = store.TerminalMemoryReadinessFact

// ContextDeliveryReadinessFact models only the acknowledgement and identity
// fields needed by readiness. Persistence and token accounting belong to the
// continuity-delivery ledger, not this projection.
type ContextDeliveryReadinessFact struct {
	ContextID       string   `json:"context_id"`
	Acknowledgement string   `json:"acknowledgement"`
	Purpose         string   `json:"purpose"`
	ObjectIDs       []string `json:"object_ids,omitempty"`
	EvidenceIDs     []string `json:"evidence_ids,omitempty"`
	PayloadDigest   string   `json:"payload_digest"`
	BindingDigest   string   `json:"binding_digest"`
	RepositoryID    string   `json:"repository_id"`
	Host            string   `json:"host"`
	SessionRef      string   `json:"session_ref"`
	CreatedAt       string   `json:"created_at"`
}

// ReadinessLifecycleInputs is recomputed from immutable facts. It is safe to
// hand to ReadinessSnapshot and must never be persisted as onboarding state.
type ReadinessLifecycleInputs struct {
	Schema         string                        `json:"schema"`
	State          string                        `json:"state"`
	TerminalMemory *TerminalMemoryReadinessFact  `json:"terminal_memory,omitempty"`
	OfferedToHost  *ContextDeliveryReadinessFact `json:"offered_to_host,omitempty"`
	HostObserved   *ContextDeliveryReadinessFact `json:"host_observed,omitempty"`
}

// ProjectReadinessLifecycleInputs proves the real first-memory chain: one
// active user memory reached a terminal save status, a fresh task was offered
// that exact object/evidence, and a later trusted lifecycle fact observed the
// same context offer. Invalid or cross-project facts are ignored fail-closed.
func ProjectReadinessLifecycleInputs(
	memories []TerminalMemoryReadinessFact,
	deliveries []ContextDeliveryReadinessFact,
) ReadinessLifecycleInputs {
	result := ReadinessLifecycleInputs{Schema: ReadinessLifecycleInputsSchema, State: "first_memory_pending"}
	terminal, terminalTime, ok := firstTerminalMemoryReadinessFact(memories)
	if !ok {
		return result
	}
	result.TerminalMemory = &terminal
	result.State = "context_offer_pending"

	offers := matchingContextFacts(deliveries, terminal, terminalTime, "offered_to_host", nil)
	if len(offers) == 0 {
		return result
	}
	result.OfferedToHost = &offers[0].fact
	result.State = "host_observation_pending"

	for _, offer := range offers {
		observations := matchingContextFacts(deliveries, terminal, offer.at, "host_observed", &offer.fact)
		if len(observations) == 0 {
			continue
		}
		result.OfferedToHost = &offer.fact
		result.HostObserved = &observations[0].fact
		result.State = "ready"
		return result
	}
	return result
}

func firstTerminalMemoryReadinessFact(facts []TerminalMemoryReadinessFact) (TerminalMemoryReadinessFact, time.Time, bool) {
	type candidate struct {
		fact TerminalMemoryReadinessFact
		at   time.Time
	}
	valid := make([]candidate, 0, len(facts))
	for _, fact := range facts {
		at, validTime := canonicalReadinessTime(fact.CreatedAt)
		terminalStatus := fact.Status == string(store.MemoryWriteCreated) ||
			fact.Status == string(store.MemoryWriteUpdated) || fact.Status == string(store.MemoryWriteDeduplicated)
		if !validTime || !terminalStatus || !fact.Active || !validReadinessScalar(fact.ReceiptID) ||
			(fact.PresentationReceiptID != "" && !validReadinessScalar(fact.PresentationReceiptID)) ||
			!validReadinessScalar(fact.ObjectID) ||
			!readinessDigest(fact.ContentDigest) || !validReadinessScalar(fact.MemoryKind) ||
			fact.MemoryKind == "system_event" || !validReadinessScalar(fact.ConversationScope) ||
			fact.ConversationScope == "install_event" || !readinessDigest(fact.BindingDigest) ||
			!validReadinessScalar(fact.RepositoryID) || !validReadinessScalar(fact.Host) ||
			fact.Host == "pulse-cli" || !validReadinessSessionRef(fact.SessionRef) ||
			!validReadinessIDs(fact.EvidenceIDs) {
			continue
		}
		copyFact := fact
		copyFact.EvidenceIDs = append([]string(nil), fact.EvidenceIDs...)
		valid = append(valid, candidate{fact: copyFact, at: at})
	}
	if len(valid) == 0 {
		return TerminalMemoryReadinessFact{}, time.Time{}, false
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].at.Equal(valid[j].at) {
			return valid[i].fact.ReceiptID < valid[j].fact.ReceiptID
		}
		return valid[i].at.Before(valid[j].at)
	})
	return valid[0].fact, valid[0].at, true
}

type contextReadinessCandidate struct {
	fact ContextDeliveryReadinessFact
	at   time.Time
}

func matchingContextFacts(
	facts []ContextDeliveryReadinessFact,
	terminal TerminalMemoryReadinessFact,
	after time.Time,
	acknowledgement string,
	offered *ContextDeliveryReadinessFact,
) []contextReadinessCandidate {
	valid := make([]contextReadinessCandidate, 0, len(facts))
	for _, fact := range facts {
		at, validTime := canonicalReadinessTime(fact.CreatedAt)
		if !validTime || !at.After(after) || fact.Acknowledgement != acknowledgement ||
			!validReadinessScalar(fact.ContextID) || !readinessDigest(fact.PayloadDigest) ||
			fact.BindingDigest != terminal.BindingDigest || fact.RepositoryID != terminal.RepositoryID ||
			!validReadinessScalar(fact.Host) || fact.Purpose != "session_start" || !validReadinessSessionRef(fact.SessionRef) ||
			fact.SessionRef == terminal.SessionRef ||
			!validReadinessIDs(fact.ObjectIDs) || !validReadinessIDs(fact.EvidenceIDs) ||
			!contextFactReferencesTerminal(fact, terminal) {
			continue
		}
		if offered != nil && (fact.ContextID != offered.ContextID || fact.PayloadDigest != offered.PayloadDigest ||
			fact.Purpose != offered.Purpose || fact.Host != offered.Host || fact.SessionRef != offered.SessionRef || !sameReadinessIDs(fact.ObjectIDs, offered.ObjectIDs) ||
			!sameReadinessIDs(fact.EvidenceIDs, offered.EvidenceIDs)) {
			continue
		}
		copyFact := fact
		copyFact.ObjectIDs = append([]string(nil), fact.ObjectIDs...)
		copyFact.EvidenceIDs = append([]string(nil), fact.EvidenceIDs...)
		valid = append(valid, contextReadinessCandidate{fact: copyFact, at: at})
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].at.Equal(valid[j].at) {
			return valid[i].fact.ContextID < valid[j].fact.ContextID
		}
		return valid[i].at.Before(valid[j].at)
	})
	return valid
}

func contextFactReferencesTerminal(fact ContextDeliveryReadinessFact, terminal TerminalMemoryReadinessFact) bool {
	if !readinessIDsContain(fact.ObjectIDs, terminal.ObjectID) {
		return false
	}
	if len(terminal.EvidenceIDs) == 0 {
		return true
	}
	for _, evidenceID := range terminal.EvidenceIDs {
		if readinessIDsContain(fact.EvidenceIDs, evidenceID) {
			return true
		}
	}
	return false
}

func readinessIDsContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sameReadinessIDs(left, right []string) bool {
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

func validReadinessIDs(values []string) bool {
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return false
		}
	}
	return true
}

func validReadinessScalar(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func canonicalReadinessTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Year() < 1970 || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, false
	}
	return parsed, true
}

func readinessDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validReadinessSessionRef(value string) bool {
	return len(value) == 72 && strings.HasPrefix(value, "session:") && readinessDigest(strings.TrimPrefix(value, "session:"))
}

func (s *Server) handleTurnFinalize(w http.ResponseWriter, r *http.Request) {
	var req store.TurnFinalizeRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	var authority *productBindingAuthority
	var result store.TurnFinalizeResult
	var err error
	if s.cfg.ProductBindingVerifier != nil {
		verified, ok := s.requireProductBindingAuthority(w, r)
		if !ok {
			return
		}
		if req.BindingDigest != verified.BindingDigest || req.PolicyEpoch != 0 ||
			req.ResolverEpoch != verified.ResolverEpoch {
			http.Error(w, "product binding authority mismatch", http.StatusForbidden)
			return
		}
		authority = &verified
		result, err = s.cfg.Store.FinalizeTurnForVerifiedBinding(
			req, time.Now().UTC(), s.cfg.TrayGracePeriod,
			verified.BindingDigest, verified.RepositoryID, verified.ResolverEpoch,
		)
	} else {
		result, err = s.cfg.Store.FinalizeTurn(req, time.Now().UTC(), s.cfg.TrayGracePeriod)
	}
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	result = s.commitTurnResultNowForAuthority(result, authority)
	writeJSON(w, result)
}

func (s *Server) handleTurnNoChange(w http.ResponseWriter, r *http.Request) {
	var req store.TurnNoChangeRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	var result store.TurnFinalizeResult
	var err error
	if s.cfg.ProductBindingVerifier != nil {
		verified, ok := s.requireProductBindingAuthority(w, r)
		if !ok {
			return
		}
		if req.BindingDigest != verified.BindingDigest || req.PolicyEpoch != 0 ||
			req.ResolverEpoch != verified.ResolverEpoch {
			http.Error(w, "product binding authority mismatch", http.StatusForbidden)
			return
		}
		result, err = s.cfg.Store.FinalizeTurnNoChangeForVerifiedBinding(
			req, time.Now().UTC(), verified.BindingDigest,
			verified.RepositoryID, verified.ResolverEpoch,
		)
	} else {
		result, err = s.cfg.Store.FinalizeTurnNoChange(req, time.Now().UTC())
	}
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleMemoryTrayList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	candidates, err := s.cfg.Store.ListMemoryTray(limit)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, map[string]any{"candidates": candidates})
}

func (s *Server) handleMemoryReceiptGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	receipt, err := s.cfg.Store.GetMemoryWriteReceipt(chi.URLParam(r, "id"))
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleMemoryTrayEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedVersion int                          `json:"expected_version"`
		Candidate       store.PrivateMemoryCandidate `json:"candidate"`
	}
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	receipt, err := s.cfg.Store.EditMemoryTrayCandidate(
		chi.URLParam(r, "id"), req.ExpectedVersion, req.Candidate,
		time.Now().UTC(), s.cfg.TrayGracePeriod,
	)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	receipt = s.commitReceiptNow(receipt)
	writeJSON(w, receipt)
}

func (s *Server) handleMemoryTrayCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	receipt, err := s.cfg.Store.CancelMemoryTrayCandidate(
		chi.URLParam(r, "id"), req.ExpectedVersion, time.Now().UTC(),
	)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleMemoryTrayCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	receipt, err := s.cfg.Store.CommitMemoryTrayCandidate(
		chi.URLParam(r, "id"), req.ExpectedVersion, time.Now().UTC(),
	)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	s.refreshProductRetrieval(receipt)
	writeJSON(w, receipt)
}

func (s *Server) handleMemoryCorrect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Candidate store.PrivateMemoryCandidate `json:"candidate"`
	}
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.PrepareMemoryCorrectionWithInvocation(
		chi.URLParam(r, "id"), req.Candidate, r.Header.Get("Idempotency-Key"),
		time.Now().UTC(), s.cfg.TrayGracePeriod,
	)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	result = s.commitTurnResultNow(result)
	writeJSON(w, result)
}

func decodeMemoryTrayBody(r *http.Request, target any) error {
	return decodeStrictJSONBody(
		r, target, 1<<20, errors.New("Memory Tray body is empty or too large"),
	)
}

func decodeStrictJSONBody(r *http.Request, target any, maxBytes int64, invalidBody error) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(raw) == 0 || int64(len(raw)) > maxBytes {
		return invalidBody
	}
	return decodeStrictJSON(raw, target)
}

func writeMemoryTrayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMemoryTrayUnavailable), errors.Is(err, store.ErrMemoryTrayRequired):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, store.ErrMemoryTrayGraceActive):
		http.Error(w, err.Error(), http.StatusTooEarly)
	case errors.Is(err, store.ErrMemoryTrayVersionConflict), errors.Is(err, store.ErrMemoryTrayTerminal),
		errors.Is(err, store.ErrMemoryCorrectionConflict), errors.Is(err, store.ErrTurnAlreadyFinalized),
		errors.Is(err, store.ErrTurnFinalizeConflict), errors.Is(err, store.ErrProductRuntimeMismatch),
		errors.Is(err, store.ErrMemoryTrayNotPresented):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "memory tray error: "+err.Error(), http.StatusBadRequest)
	}
}

// commitTurnResultNow materializes ordinary Personal memory before returning
// the tool response. A pending candidate remains a durable crash-recovery
// record, but it is not a user review state.
func (s *Server) commitTurnResultNow(result store.TurnFinalizeResult) store.TurnFinalizeResult {
	return s.commitTurnResultNowForAuthority(result, nil)
}

func (s *Server) commitTurnResultNowForAuthority(
	result store.TurnFinalizeResult,
	authority *productBindingAuthority,
) store.TurnFinalizeResult {
	for index, receipt := range result.Receipts {
		result.Receipts[index] = s.commitReceiptNowForAuthority(receipt, authority)
	}
	return result
}

func (s *Server) commitReceiptNow(receipt store.MemoryWriteReceipt) store.MemoryWriteReceipt {
	return s.commitReceiptNowForAuthority(receipt, nil)
}

func (s *Server) commitReceiptNowForAuthority(
	receipt store.MemoryWriteReceipt,
	authority *productBindingAuthority,
) store.MemoryWriteReceipt {
	if receipt.Status != store.MemoryWritePending {
		return receipt
	}
	var committed store.MemoryWriteReceipt
	var err error
	if authority != nil {
		committed, err = s.cfg.Store.CommitMemoryTrayCandidateForVerifiedBinding(
			receipt.CandidateID, receipt.CandidateVersion, time.Now().UTC(),
			authority.BindingDigest, authority.RepositoryID, authority.ResolverEpoch,
		)
	} else {
		committed, err = s.cfg.Store.CommitMemoryTrayCandidate(
			receipt.CandidateID, receipt.CandidateVersion, time.Now().UTC(),
		)
	}
	if err == nil {
		s.refreshProductRetrieval(committed)
		return committed
	}
	// The candidate itself is already durable. Retry transient materialization
	// in the background and return the honest pending receipt on failure.
	if authority == nil && !errors.Is(err, store.ErrMemoryTrayVersionConflict) &&
		!errors.Is(err, store.ErrMemoryTrayTerminal) {
		s.scheduleReceipt(receipt, 0)
	}
	return receipt
}

// recoverReceiptNow closes the startup race for rows left by a crash or an
// older runtime. New must not return while a fresh task could still observe
// an ambiguous pending write: recovery produces either canonical memory or a
// content-free terminal failure before the daemon reports ready.
func (s *Server) recoverReceiptNow(receipt store.MemoryWriteReceipt) error {
	if receipt.Status != store.MemoryWritePending {
		return nil
	}
	committed, err := s.cfg.Store.CommitMemoryTrayCandidate(
		receipt.CandidateID, receipt.CandidateVersion, time.Now().UTC(),
	)
	if err == nil {
		s.refreshProductRetrieval(committed)
		return nil
	}
	if errors.Is(err, store.ErrMemoryTrayVersionConflict) || errors.Is(err, store.ErrMemoryTrayTerminal) {
		return nil
	}
	if _, failErr := s.cfg.Store.FailMemoryTrayCandidate(
		receipt.CandidateID, receipt.CandidateVersion, "commit_failed", time.Now().UTC(),
	); failErr == nil ||
		errors.Is(failErr, store.ErrMemoryTrayVersionConflict) ||
		errors.Is(failErr, store.ErrMemoryTrayTerminal) {
		return nil
	} else {
		return fmt.Errorf("recover memory tray candidate %s: commit: %v; persist failure: %w", receipt.CandidateID, err, failErr)
	}
}

// scheduleTurnResult is kept for internal call sites that do not return the
// receipt body. Normal writes are still attempted synchronously.
func (s *Server) scheduleTurnResult(result store.TurnFinalizeResult) {
	_ = s.commitTurnResultNow(result)
}

func (s *Server) scheduleReceipt(receipt store.MemoryWriteReceipt, delay time.Duration) {
	key, ok := memoryTrayScheduleIdentity(receipt)
	if !ok {
		return
	}
	state, claimed := s.claimReceiptSchedule(key)
	if !claimed {
		return
	}
	s.scheduleReceiptAttempt(receipt, delay, 0, key, state)
}

// schedulePresentedMemory is the required bridge between the Home-only
// presentation service and the existing durable commit worker. U7 wires this
// callback into the authenticated Home route; presentation cannot be
// configured without a scheduler, so a late-rendered card cannot be stranded.
func (s *Server) schedulePresentedMemory(receipt store.MemoryPresentationReceipt, delay time.Duration) {
	writeReceipt := store.MemoryWriteReceipt{
		CandidateID: receipt.CandidateID, CandidateVersion: receipt.CandidateVersion,
	}
	key, ok := memoryTrayScheduleIdentity(writeReceipt)
	if !ok {
		return
	}
	deadline, err := time.Parse(time.RFC3339Nano, receipt.GraceExpiresAt)
	if err != nil {
		return
	}
	state, claimed := s.claimPresentedReceiptSchedule(key, deadline)
	if !claimed {
		return
	}
	s.scheduleReceiptAttempt(writeReceipt, delay, 0, key, state)
}

type memoryTrayScheduleKey struct {
	candidateID      string
	candidateVersion int
}

// memoryTrayScheduleState closes the only handoff race between an older
// speculative worker observing "not presented" and the Home presentation
// callback installing the authoritative deadline. The pointer is the worker
// generation; stale timers cannot delete a newer schedule for the same key.
type memoryTrayScheduleState struct {
	presentedDeadline time.Time
}

func memoryTrayScheduleIdentity(receipt store.MemoryWriteReceipt) (memoryTrayScheduleKey, bool) {
	key := memoryTrayScheduleKey{
		candidateID: receipt.CandidateID, candidateVersion: receipt.CandidateVersion,
	}
	return key, key.candidateID != "" && key.candidateVersion > 0
}

func (s *Server) claimReceiptSchedule(key memoryTrayScheduleKey) (*memoryTrayScheduleState, bool) {
	s.trayScheduleMu.Lock()
	defer s.trayScheduleMu.Unlock()
	if state, scheduled := s.traySchedules[key]; scheduled {
		return state, false
	}
	if s.traySchedules == nil {
		s.traySchedules = make(map[memoryTrayScheduleKey]*memoryTrayScheduleState)
	}
	state := &memoryTrayScheduleState{}
	s.traySchedules[key] = state
	return state, true
}

func (s *Server) claimPresentedReceiptSchedule(
	key memoryTrayScheduleKey,
	deadline time.Time,
) (*memoryTrayScheduleState, bool) {
	s.trayScheduleMu.Lock()
	defer s.trayScheduleMu.Unlock()
	if state, scheduled := s.traySchedules[key]; scheduled {
		state.presentedDeadline = deadline
		return state, false
	}
	if s.traySchedules == nil {
		s.traySchedules = make(map[memoryTrayScheduleKey]*memoryTrayScheduleState)
	}
	state := &memoryTrayScheduleState{presentedDeadline: deadline}
	s.traySchedules[key] = state
	return state, true

}

func (s *Server) finishReceiptSchedule(key memoryTrayScheduleKey, state *memoryTrayScheduleState) {
	s.trayScheduleMu.Lock()
	if s.traySchedules[key] == state {
		delete(s.traySchedules, key)
	}
	s.trayScheduleMu.Unlock()
}

func (s *Server) clearPresentedScheduleDeadline(key memoryTrayScheduleKey, state *memoryTrayScheduleState) {
	s.trayScheduleMu.Lock()
	if s.traySchedules[key] == state {
		state.presentedDeadline = time.Time{}
	}
	s.trayScheduleMu.Unlock()
}

func (s *Server) handoffPresentedScheduleAfterNotPresented(
	key memoryTrayScheduleKey,
	state *memoryTrayScheduleState,
	now time.Time,
) (time.Duration, bool) {
	s.trayScheduleMu.Lock()
	defer s.trayScheduleMu.Unlock()
	if s.traySchedules[key] != state {
		return 0, false
	}
	deadline := state.presentedDeadline
	if deadline.IsZero() {
		delete(s.traySchedules, key)
		return 0, false
	}
	state.presentedDeadline = time.Time{}
	delay := deadline.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}

func (s *Server) scheduleReceiptAttempt(
	receipt store.MemoryWriteReceipt,
	delay time.Duration,
	attempt int,
	key memoryTrayScheduleKey,
	state *memoryTrayScheduleState,
) {
	time.AfterFunc(delay, func() {
		committed, err := s.cfg.Store.CommitMemoryTrayCandidate(
			receipt.CandidateID, receipt.CandidateVersion, time.Now().UTC(),
		)
		if err == nil {
			s.finishReceiptSchedule(key, state)
			s.refreshProductRetrieval(committed)
			return
		}
		if errors.Is(err, store.ErrMemoryTrayVersionConflict) || errors.Is(err, store.ErrMemoryTrayTerminal) {
			s.finishReceiptSchedule(key, state)
			return
		}
		if errors.Is(err, store.ErrMemoryTrayNotPresented) {
			nextDelay, reschedule := s.handoffPresentedScheduleAfterNotPresented(key, state, time.Now())
			if reschedule {
				s.scheduleReceiptAttempt(receipt, nextDelay, 0, key, state)
			}
			return
		}
		if errors.Is(err, store.ErrMemoryTrayGraceActive) {
			s.clearPresentedScheduleDeadline(key, state)
			s.scheduleReceiptAttempt(receipt, 100*time.Millisecond, attempt, key, state)
			return
		}
		if attempt < 2 {
			s.scheduleReceiptAttempt(receipt, time.Duration(attempt+1)*250*time.Millisecond, attempt+1, key, state)
			return
		}
		if _, failErr := s.cfg.Store.FailMemoryTrayCandidate(
			receipt.CandidateID, receipt.CandidateVersion, "commit_failed", time.Now().UTC(),
		); failErr == nil || errors.Is(failErr, store.ErrMemoryTrayVersionConflict) || errors.Is(failErr, store.ErrMemoryTrayTerminal) {
			s.finishReceiptSchedule(key, state)
		} else {
			// A locked/unavailable DB cannot record its own failure. Keep retrying
			// until either the commit or the content-free failed receipt is durable.
			s.scheduleReceiptAttempt(receipt, time.Second, attempt, key, state)
		}
	})
}

func (s *Server) refreshProductRetrieval(receipt store.MemoryWriteReceipt) {
	s.refreshProductRetrievalAttempt(receipt, 0)
}

func (s *Server) refreshProductRetrievalAttempt(receipt store.MemoryWriteReceipt, attempt int) {
	if receipt.ObjectID == "" {
		return
	}
	if receipt.ReasonCode == "user_deleted" && s.cfg.Retrieval != nil && !s.cfg.Retrieval.EmbedderReady() {
		status := "complete"
		if err := s.cfg.Retrieval.Reload(context.Background()); err != nil {
			status = "failed"
			log.Printf("memory tray projection: post-delete reload failed: %v", err)
		}
		if err := s.cfg.Store.SetPrivateProjectionStatus(receipt.ObjectID, status, time.Now().UTC()); err != nil {
			log.Printf("memory tray projection: persist %s status failed: %v", status, err)
		}
		if status == "failed" {
			s.scheduleProjectionRetry(receipt, attempt)
		}
		return
	}
	if s.cfg.Retrieval == nil || !s.cfg.Retrieval.EmbedderReady() {
		// Keep the outbox pending. A later startup with a configured embedder
		// replays it, so memories captured in fallback mode never become
		// permanently dark.
		return
	}
	status := "complete"
	ctx := context.Background()
	for {
		docs, err := s.cfg.Store.UnindexedHostEventDocs(500)
		if err != nil {
			status = "failed"
			log.Printf("memory tray projection: list unindexed events failed: %v", err)
			break
		}
		if len(docs) == 0 {
			if err := s.cfg.Retrieval.Reload(ctx); err != nil {
				status = "failed"
				log.Printf("memory tray projection: reload failed: %v", err)
			}
			break
		}
		indexDocs := make([]retrieve.IndexEventDoc, len(docs))
		for index, doc := range docs {
			indexDocs[index] = retrieve.IndexEventDoc{EventID: doc.EventID, Text: doc.Text}
		}
		if err := s.cfg.Retrieval.EmbedAndIndexEvents(ctx, indexDocs); err != nil {
			status = "failed"
			log.Printf("memory tray projection: embed/index failed: %v", err)
			break
		}
		if len(docs) < 500 {
			break
		}
	}
	now := time.Now().UTC()
	if err := s.cfg.Store.SetPendingPrivateProjectionStatus(status, now); err != nil {
		log.Printf("memory tray projection: persist pending %s status failed: %v", status, err)
	}
	if receipt.ReasonCode == "user_deleted" && status == "failed" {
		if err := s.cfg.Store.SetPrivateProjectionStatus(receipt.ObjectID, status, now); err != nil {
			log.Printf("memory tray projection: persist delete %s status failed: %v", status, err)
		}
	}
	if status == "failed" {
		s.scheduleProjectionRetry(receipt, attempt)
	}
}

func (s *Server) scheduleProjectionRetry(receipt store.MemoryWriteReceipt, attempt int) {
	if attempt >= 5 {
		return
	}
	delay := time.Duration(1<<attempt) * 250 * time.Millisecond
	time.AfterFunc(delay, func() {
		s.refreshProductRetrievalAttempt(receipt, attempt+1)
	})
}

func (s *Server) recoverMemoryTray() error {
	if s.cfg.Store == nil || (s.cfg.Store.StoreKind() != store.StoreKindPersonal && s.cfg.Store.StoreKind() != store.StoreKindDesk) {
		return nil
	}
	after := ""
	for {
		candidates, err := s.cfg.Store.ListPendingMemoryTrayPage(after, 200)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			after = candidate.CandidateID
			graceExpiresAt := candidate.GraceExpiresAt
			if strings.TrimSpace(graceExpiresAt) == "" {
				graceExpiresAt, err = s.cfg.Store.EnsureMemoryTrayGraceDeadline(
					candidate.CandidateID, candidate.Version, time.Now().UTC(), s.cfg.TrayGracePeriod,
				)
				if err != nil {
					if errors.Is(err, store.ErrMemoryTrayVersionConflict) || errors.Is(err, store.ErrMemoryTrayTerminal) {
						continue
					}
					if _, failErr := s.cfg.Store.FailMemoryTrayCandidate(
						candidate.CandidateID, candidate.Version, "commit_failed", time.Now().UTC(),
					); failErr != nil {
						return failErr
					}
					continue
				}
			}
			// All pending rows, including rows from the old presentation/grace
			// runtime, are materialized before New returns. A fresh task cannot
			// race daemon startup and miss valid recovered memory.
			if err := s.recoverReceiptNow(candidate.LatestReceipt); err != nil {
				return err
			}
		}
		if len(candidates) < 200 {
			break
		}
	}
	afterObjectID := ""
	for {
		receipts, err := s.cfg.Store.ListPendingPrivateProjectionReceipts(afterObjectID, 200)
		if err != nil {
			return err
		}
		for _, receipt := range receipts {
			s.refreshProductRetrieval(receipt)
			afterObjectID = receipt.ObjectID
		}
		if len(receipts) < 200 {
			break
		}
	}
	return nil
}

func memoryTrayScheduleDelay(graceExpiresAt string, now time.Time) (time.Duration, bool, error) {
	if strings.TrimSpace(graceExpiresAt) == "" {
		return 0, false, nil
	}
	expires, err := time.Parse(time.RFC3339Nano, graceExpiresAt)
	if err != nil {
		return 0, false, err
	}
	delay := expires.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true, nil
}
