package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (s *Store) MemoryMomentLocalBinding() string {
	binding, _, _ := s.productRuntimeAuthority()
	return binding
}

// FinalizeMemoryMoment admits the complete structured set in one transaction.
// The tray commits/replays individual candidates. The byte envelope is bounded,
// not the number of meaningful parts. Internal operation identity is separate
// from the real host turn retained as source_turn_ref.
func (s *Store) FinalizeMemoryMoment(req TurnFinalizeRequest, now time.Time, binding, repository string, epoch int64) (TurnFinalizeResult, error) {
	policy := int64(0)
	if repository == "" {
		binding, policy, epoch = s.productRuntimeAuthority()
	}
	if req.BindingDigest != binding || req.ResolverEpoch != epoch || req.PolicyEpoch != policy {
		return TurnFinalizeResult{}, ErrProductRuntimeMismatch
	}
	if req.Schema != TurnFinalizeRequestSchema || len(req.Candidates) == 0 {
		return TurnFinalizeResult{}, errors.New("memory moment requires a nonempty structured set")
	}
	if err := validateTrayEnvelope(req.Host, req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey, req.BindingDigest, req.PolicyEpoch, req.ResolverEpoch); err != nil {
		return TurnFinalizeResult{}, err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "moment-v1\x00%s\x00%s\x00%s\x00%s\x00", binding, req.Host, req.SessionID, req.TurnID)
	// Reject unsafe content before it can become persisted retry data.
	for i, candidate := range req.Candidates {
		prepared, err := prepareBoundPrivateCandidate(candidate, req.Host, req.SessionID)
		if err != nil {
			return TurnFinalizeResult{}, fmt.Errorf("moment item %d invalid; nothing accepted: %w", i, err)
		}
		fmt.Fprintf(hash, "%s\x00%s\x00", candidate.MemoryScope, prepared.digest)
	}
	req.momentID = "moment:" + hex.EncodeToString(hash.Sum(nil))
	req.momentRepositoryID = repository
	req.sourceTurnRef = opaqueTurnCorrelation("turn", req.TurnID)
	if existing, err := s.MemoryMomentStatus(req.momentID, binding); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TurnFinalizeResult{}, err
	}
	req.TurnID = req.momentID
	req.SourceEventKey = req.momentID
	req.IdempotencyKey = req.momentID
	return s.finalizeTurnForAuthority(req, now, 0, binding, policy, epoch)
}

func (s *Store) MemoryMomentStatus(id, binding string) (TurnFinalizeResult, error) {
	var host, session, turn, digest string
	if err := s.db.QueryRow(`SELECT host,session_id,turn_id,request_digest FROM turn_ledgers WHERE moment_id=? AND binding_digest=?`, id, binding).Scan(&host, &session, &turn, &digest); err != nil {
		return TurnFinalizeResult{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	defer tx.Rollback()
	result, _, err := loadExistingTurnTx(tx, host, session, turn, digest)
	result.MomentID = id
	return result, err
}

type MemoryMomentItem struct {
	ObjectID  string                 `json:"object_id"`
	Candidate PrivateMemoryCandidate `json:"candidate"`
}
type MemoryMomentPage struct {
	MomentID string             `json:"moment_id"`
	Items    []MemoryMomentItem `json:"items"`
	Next     int                `json:"next_cursor,omitempty"`
}

// Read current canonical content, never pending or deleted content. Filter
// namespaces before pagination; a mixed-scope moment cannot widen access.
func (s *Store) ReadMemoryMoment(id, binding, repository string, cursor int) (MemoryMomentPage, error) {
	page := MemoryMomentPage{MomentID: id, Items: []MemoryMomentItem{}}
	if !validTrayIdentifier(id) || cursor < 0 {
		return page, errors.New("invalid moment cursor")
	}
	namespace := s.currentPersonalMemoryScope(binding).ProjectNamespaceID
	if repository != "" {
		namespace = stableProjectNamespace(repository)
	}
	rows, err := s.db.Query(`SELECT object.object_id,candidate.payload_json
 FROM memory_write_receipts member
 JOIN turn_ledgers origin ON origin.ledger_id=member.ledger_id
 JOIN private_memory_objects object ON object.object_id=member.object_id
 JOIN memory_tray_candidates candidate ON candidate.candidate_id=object.created_from_candidate_id AND candidate.content_digest=object.content_digest
 JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
 JOIN turn_ledgers anchor ON anchor.moment_id=? AND origin.binding_digest=anchor.binding_digest
 AND origin.host=anchor.host AND origin.session_id=anchor.session_id AND origin.source_turn_ref=anchor.source_turn_ref
 WHERE object.lifecycle='active' AND origin.moment_id IS NOT NULL AND member.status IN ('created','updated','deduplicated') AND
 (object.memory_scope='personal_global' OR (ledger.binding_digest=? AND object.memory_scope='project' AND object.project_namespace_id=?))
 GROUP BY object.object_id ORDER BY MIN(member.rowid) LIMIT 21 OFFSET ?`, id, binding, namespace, cursor)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item MemoryMomentItem
		var payload string
		if err := rows.Scan(&item.ObjectID, &payload); err != nil {
			return page, err
		}
		if len(page.Items) == 20 {
			page.Next = cursor + 20
			break
		}
		if err := json.Unmarshal([]byte(payload), &item.Candidate); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

// MomentForEvent attaches a navigation reference only to currently eligible
// canonical memory. It never changes retrieval rank or loads adjacent content.
func (s *Store) MomentForEvent(eventID int64, binding, repository string) string {
	namespace := s.currentPersonalMemoryScope(binding).ProjectNamespaceID
	if repository != "" {
		namespace = stableProjectNamespace(repository)
	}
	var id string
	_ = s.db.QueryRow(`SELECT origin.moment_id FROM memory_write_receipts member
 JOIN turn_ledgers origin ON origin.ledger_id=member.ledger_id
 JOIN private_memory_objects object ON object.object_id=member.object_id
 JOIN memory_tray_candidates candidate ON candidate.candidate_id=object.created_from_candidate_id
 JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
 WHERE origin.moment_id IS NOT NULL AND object.lifecycle='active' AND
 (object.memory_scope='personal_global' OR (ledger.binding_digest=? AND object.memory_scope='project' AND object.project_namespace_id=?)) AND
 (EXISTS(SELECT 1 FROM memory_capsules c WHERE c.id=object.object_id AND c.event_id=?) OR
 EXISTS(SELECT 1 FROM private_semantic_projection_rows p WHERE p.object_id=object.object_id AND p.row_kind='event' AND p.row_ref=CAST(? AS TEXT)))
 ORDER BY member.rowid DESC LIMIT 1`, binding, namespace, eventID, eventID).Scan(&id)
	return id
}

// A signed admission authorizes completion of these exact durable candidates.
// Persist its repository identity, never a workspace path or caller-supplied
// authority. Recovery cannot use this grant to admit any new content.
func (s *Store) IsMemoryMomentCandidate(candidateID string) bool {
	var count int
	_ = s.db.QueryRow(`SELECT count(*) FROM memory_tray_candidates c JOIN turn_ledgers l ON l.ledger_id=c.ledger_id WHERE c.candidate_id=? AND l.moment_id IS NOT NULL`, candidateID).Scan(&count)
	return count == 1
}

func (s *Store) CommitRecoverableMemoryTrayCandidate(candidateID string, version int, now time.Time) (MemoryWriteReceipt, error) {
	var binding, repository string
	var epoch int64
	err := s.db.QueryRow(`SELECT l.binding_digest,l.moment_repository_id,l.resolver_epoch FROM memory_tray_candidates c JOIN turn_ledgers l ON l.ledger_id=c.ledger_id WHERE c.candidate_id=? AND l.moment_id IS NOT NULL`, candidateID).Scan(&binding, &repository, &epoch)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && repository == "") {
		return s.CommitMemoryTrayCandidate(candidateID, version, now)
	}
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	return s.CommitMemoryTrayCandidateForVerifiedBinding(candidateID, version, now, binding, repository, epoch)
}
