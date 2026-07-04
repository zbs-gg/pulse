package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Scope is the typed boundary that keeps cross-harness recall deliberate.
// Personal facts must not bleed into a repo- or agent-scoped query just
// because they share one local store.
type Scope struct {
	Type       string // personal | project | repo | agent | session
	ID         string
	Visibility string // private | shared
}

// normalized returns the scope with empty fields filled by the safe defaults
// (personal / "" / private) so callers can pass a zero Scope.
func (sc Scope) normalized() Scope {
	out := sc
	out.Type = strings.TrimSpace(out.Type)
	out.Visibility = strings.TrimSpace(out.Visibility)
	out.ID = strings.TrimSpace(out.ID)
	if out.Type == "" {
		out.Type = "personal"
	}
	if out.Visibility == "" {
		out.Visibility = "private"
	}
	return out
}

// Assertion is a first-class, bitemporal claim about an entity.
// valid_from/valid_to is event-time (when the claim is true in the world);
// system_from/system_to is transaction-time (when Pulse believed it). The
// split lets us tell "the world changed" (supersede) from "we recorded it
// wrong" (retract).
type Assertion struct {
	ID               int64
	ClaimKey         string // derived from subject+predicate when empty
	SubjectEntityID  *int64
	Predicate        string
	ObjectText       string
	ObjectEntityID   *int64
	Qualifiers       map[string]any
	Confidence       float64
	ValidFrom        string
	ValidTo          string
	SystemFrom       string
	SystemTo         string
	Status           string
	SupersededBy     *int64
	SourceEventIDs   []int64
	ExtractorVersion string
	Scope            Scope
	CreatedAt        string

	// MentionCount is the corroboration counter: how many times this exact active
	// claim (same claim_key+scope+object) has been re-confirmed. Defaults to 1 on
	// insert (migration 030). Pure metadata surfaced on read — NOT read by
	// scoreEventsV3 / v3boosts / state_fit.
	MentionCount int64
	// LastMentionedAt is the RFC3339 timestamp of the most recent corroboration.
	// Empty (NULL) until the claim is re-confirmed at least once.
	LastMentionedAt string

	// CtxVec is the embedding of "<subject> <predicate> <object>", persisted in
	// ctx_vec; claim resolution compares claims by meaning, not just key. Optional.
	CtxVec []float32

	// ChangeCue: the source statement signalled a change (now/changed/switched…).
	// Required for supersession — without it a same-slot claim is kept as a
	// sibling (multi-valued protection). Transient (a resolution input).
	ChangeCue bool

	// Subject is the human-readable subject used to build ClaimKey when
	// ClaimKey is empty. Not stored directly; the entity ref carries identity.
	Subject string
}

// MakeClaimKey normalizes subject+predicate into the stable key that two
// assertions about the same thing share, so a later value can supersede an
// earlier one. Cyrillic-aware and punctuation-insensitive, matching the
// entity-key normalization used elsewhere in the store.
func MakeClaimKey(subject, predicate string) string {
	return normalizeClaimComponent(subject) + "|" + normalizeClaimComponent(predicate)
}

func normalizeClaimComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 'а' && r <= 'я':
			b.WriteRune(r)
		case r == 'ё':
			b.WriteRune('е')
		}
	}
	return b.String()
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// SaveAssertion inserts a new active assertion and returns its id. It does not
// supersede anything — use SupersedeAssertion for "the world changed".
func (s *Store) SaveAssertion(a Assertion) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := insertAssertion(tx, a)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// SupersedeAssertion records "the world changed": it closes the valid interval
// of the current active assertion(s) for the same claim_key+scope, marks them
// superseded, then inserts the new active assertion and links them. The prior
// rows are never deleted — history stays queryable as-of.
func (s *Store) SupersedeAssertion(a Assertion) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	newID, err := supersedeAssertionTx(tx, a)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newID, nil
}

// supersedeAssertionTx is SupersedeAssertion's body without its own
// transaction, so callers (e.g. SaveSemanticDelta) can record a claim ATOMICALLY
// in the same transaction as the producing event.
func supersedeAssertionTx(tx *sql.Tx, a Assertion) (int64, error) {
	a = withDerivedKey(a)
	scope := a.Scope.normalized()
	now := a.SystemFrom
	if strings.TrimSpace(now) == "" {
		now = nowRFC3339()
	}
	validTo := a.ValidFrom
	if strings.TrimSpace(validTo) == "" {
		validTo = now
	}
	newID, err := insertAssertion(tx, a)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		UPDATE assertions
		   SET status = 'superseded',
		       valid_to = COALESCE(valid_to, ?),
		       superseded_by = ?
		 WHERE claim_key = ? AND scope_type = ? AND scope_id = ?
		   AND status = 'active' AND id != ?`,
		validTo, newID, a.ClaimKey, scope.Type, scope.ID, newID); err != nil {
		return 0, fmt.Errorf("supersede prior assertions: %w", err)
	}
	return newID, nil
}

// mentionTime returns the timestamp to stamp on a corroboration bump: the
// incoming assertion's SystemFrom when set, else now.
func mentionTime(a Assertion) string {
	if t := strings.TrimSpace(a.SystemFrom); t != "" {
		return t
	}
	return nowRFC3339()
}

// bumpMention is THE corroboration write: an existing claim was re-confirmed,
// so its mention_count grows and last_mentioned_at moves. Shared by the
// exact-key upsert path and the paraphrase resolver; runs on *sql.DB or *sql.Tx.
func bumpMention(ex interface {
	Exec(query string, args ...any) (sql.Result, error)
}, id int64, at string) error {
	if _, err := ex.Exec(`
		UPDATE assertions
		   SET mention_count = mention_count + 1,
		       last_mentioned_at = ?
		 WHERE id = ?`, at, id); err != nil {
		return fmt.Errorf("bump mention_count: %w", err)
	}
	return nil
}

// UpsertAssertion is the embedder-free claim-write path: it matches on the exact
// claim_key+scope only. When the active claim already carries the same object it
// is a corroboration — mention_count is bumped and inserted=false (no new row,
// no supersede). A different object supersedes the prior active claim(s). No
// active claim => a fresh insert (inserted=true).
//
// This is additive corroboration scaffolding. It is deliberately NOT wired into
// SaveSemanticDelta, which uses the precision-first embedding resolver
// (ResolveClaim) for paraphrase-tolerant matching. mention_count is metadata
// surfaced on read and is never routed into scoreEventsV3 / v3boosts / state_fit.
func (s *Store) UpsertAssertion(a Assertion) (int64, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	id, inserted, err := upsertAssertionTx(tx, a)
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return id, inserted, nil
}

// upsertAssertionTx resolves an incoming claim against the current active claim
// for its exact claim_key+scope, inside the caller's transaction. Object
// comparison mirrors the resolver's duplicate guard (normalizeObject), so case
// and punctuation do not defeat corroboration.
func upsertAssertionTx(tx *sql.Tx, a Assertion) (int64, bool, error) {
	a = withDerivedKey(a)
	scope := a.Scope.normalized()
	var currentID int64
	var currentObject string
	err := tx.QueryRow(`
		SELECT id, object_text
		  FROM assertions
		 WHERE claim_key = ? AND scope_type = ? AND scope_id = ?
		   AND status = 'active' AND system_to IS NULL AND valid_to IS NULL
		 ORDER BY id DESC
		 LIMIT 1`, a.ClaimKey, scope.Type, scope.ID).Scan(&currentID, &currentObject)
	if err == sql.ErrNoRows {
		id, err := insertAssertion(tx, a)
		return id, err == nil, err
	}
	if err != nil {
		return 0, false, fmt.Errorf("lookup current assertion: %w", err)
	}
	if normalizeObject(currentObject) == normalizeObject(a.ObjectText) {
		// Corroboration: the same claim was re-confirmed. Bump the mention count;
		// do NOT insert a new row and do NOT supersede. inserted stays false so
		// callers that count inserts/supersedes are unaffected.
		if err := bumpMention(tx, currentID, mentionTime(a)); err != nil {
			return 0, false, err
		}
		return currentID, false, nil
	}
	// The world changed: supersede the prior active claim(s) and insert the new
	// one. The fresh row carries the default mention_count of 1 (a change is not a
	// corroboration).
	newID, err := supersedeAssertionTx(tx, a)
	return newID, err == nil, err
}

// SupersededPairsForEvents returns (staleEventID, currentEventID) pairs where a
// superseded assertion's source event and its replacement's source event are
// BOTH among ids. The retrieval demotion overlay uses this to move a stale fact
// below its own correction. Read-only; never mutates.
func (s *Store) SupersededPairsForEvents(ids []int64) ([][2]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT a.source_event_ids, c.source_event_ids
		  FROM assertions a
		  JOIN assertions c ON a.superseded_by = c.id
		 WHERE a.status = 'superseded'
		 ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	in := make(map[int64]bool, len(ids))
	for _, id := range ids {
		in[id] = true
	}
	var out [][2]int64
	for rows.Next() {
		var staleJSON, curJSON sql.NullString
		if err := rows.Scan(&staleJSON, &curJSON); err != nil {
			return nil, err
		}
		stale := firstEventID(staleJSON.String)
		cur := firstEventID(curJSON.String)
		if stale != 0 && cur != 0 && stale != cur && in[stale] && in[cur] {
			out = append(out, [2]int64{stale, cur})
		}
	}
	return out, rows.Err()
}

func firstEventID(jsonArr string) int64 {
	if strings.TrimSpace(jsonArr) == "" {
		return 0
	}
	var arr []int64
	if err := json.Unmarshal([]byte(jsonArr), &arr); err != nil || len(arr) == 0 {
		return 0
	}
	return arr[0]
}

// RetractAssertion records "we recorded it wrong": it closes the system
// (belief) interval and marks the row retracted. The valid (world) interval is
// left untouched — we no longer believe it, but we don't claim the world
// changed.
func (s *Store) RetractAssertion(id int64, now string) error {
	if strings.TrimSpace(now) == "" {
		now = nowRFC3339()
	}
	_, err := s.db.Exec(`
		UPDATE assertions
		   SET status = 'retracted', system_to = ?
		 WHERE id = ? AND system_to IS NULL`, now, id)
	return err
}

// CurrentAssertions returns what Pulse currently believes is true for a
// claim_key within a scope: active, currently believed (system_to IS NULL) and
// currently true (valid_to IS NULL).
func (s *Store) CurrentAssertions(claimKey string, scope Scope) ([]Assertion, error) {
	scope = scope.normalized()
	rows, err := s.db.Query(`
		SELECT id, claim_key, subject_entity_id, predicate, object_text, object_entity_id,
		       qualifiers, confidence, valid_from, valid_to, system_from, system_to,
		       status, superseded_by, source_event_ids, extractor_version,
		       scope_type, scope_id, visibility, created_at, ctx_vec,
		       mention_count, last_mentioned_at
		  FROM assertions
		 WHERE claim_key = ? AND scope_type = ? AND scope_id = ?
		   AND status = 'active' AND system_to IS NULL AND valid_to IS NULL
		 ORDER BY id DESC`, claimKey, scope.Type, scope.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssertions(rows)
}

// ActiveAssertionsInScope returns all currently-believed assertions in a scope
// (across claim_keys), with ctx_vec — used by cross-key resolution to find a
// fact phrased with a different subject. Capped for safety.
func (s *Store) ActiveAssertionsInScope(scope Scope, limit int) ([]Assertion, error) {
	scope = scope.normalized()
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`
		SELECT id, claim_key, subject_entity_id, predicate, object_text, object_entity_id,
		       qualifiers, confidence, valid_from, valid_to, system_from, system_to,
		       status, superseded_by, source_event_ids, extractor_version,
		       scope_type, scope_id, visibility, created_at, ctx_vec,
		       mention_count, last_mentioned_at
		  FROM assertions
		 WHERE scope_type = ? AND scope_id = ?
		   AND status = 'active' AND system_to IS NULL AND valid_to IS NULL
		 ORDER BY id DESC LIMIT ?`, scope.Type, scope.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssertions(rows)
}

func insertAssertion(tx *sql.Tx, a Assertion) (int64, error) {
	a = withDerivedKey(a)
	if a.ClaimKey == "|" || a.ClaimKey == "" {
		return 0, fmt.Errorf("assertion needs a subject/predicate to form a claim_key")
	}
	if strings.TrimSpace(a.Predicate) == "" {
		return 0, fmt.Errorf("assertion.predicate is required")
	}
	if strings.TrimSpace(a.ObjectText) == "" {
		return 0, fmt.Errorf("assertion.object_text is required")
	}
	scope := a.Scope.normalized()
	now := a.SystemFrom
	if strings.TrimSpace(now) == "" {
		now = nowRFC3339()
	}
	created := a.CreatedAt
	if strings.TrimSpace(created) == "" {
		created = now
	}
	confidence := a.Confidence
	if confidence == 0 {
		confidence = 1.0
	}
	status := strings.TrimSpace(a.Status)
	if status == "" {
		status = "active"
	}

	var qualifiers any
	if len(a.Qualifiers) > 0 {
		raw, err := json.Marshal(a.Qualifiers)
		if err != nil {
			return 0, fmt.Errorf("marshal qualifiers: %w", err)
		}
		qualifiers = string(raw)
	}
	var sourceEvents any
	if len(a.SourceEventIDs) > 0 {
		raw, err := json.Marshal(a.SourceEventIDs)
		if err != nil {
			return 0, fmt.Errorf("marshal source_event_ids: %w", err)
		}
		sourceEvents = string(raw)
	}

	var ctxVec any
	if len(a.CtxVec) > 0 {
		raw, err := json.Marshal(a.CtxVec)
		if err != nil {
			return 0, fmt.Errorf("marshal ctx_vec: %w", err)
		}
		ctxVec = string(raw)
	}

	res, err := tx.Exec(`
		INSERT INTO assertions
		  (claim_key, subject_entity_id, predicate, object_text, object_entity_id,
		   qualifiers, confidence, valid_from, valid_to, system_from, system_to,
		   status, superseded_by, source_event_ids, extractor_version,
		   scope_type, scope_id, visibility, created_at, object_norm, ctx_vec)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ClaimKey, a.SubjectEntityID, a.Predicate, a.ObjectText, a.ObjectEntityID,
		qualifiers, confidence, nullableString(a.ValidFrom), nullableString(a.ValidTo), now,
		status, sourceEvents, nullableString(a.ExtractorVersion),
		scope.Type, scope.ID, scope.Visibility, created, normalizeObject(a.ObjectText), ctxVec)
	if err != nil {
		return 0, fmt.Errorf("insert assertion: %w", err)
	}
	return res.LastInsertId()
}

func withDerivedKey(a Assertion) Assertion {
	if strings.TrimSpace(a.ClaimKey) == "" {
		a.ClaimKey = MakeClaimKey(a.Subject, a.Predicate)
	}
	return a
}

func scanAssertions(rows *sql.Rows) ([]Assertion, error) {
	var out []Assertion
	for rows.Next() {
		var a Assertion
		var subj, objEnt, supBy sql.NullInt64
		var qualifiers, validFrom, validTo, systemTo, sourceEvents, extractor, ctxVec, lastMentioned sql.NullString
		if err := rows.Scan(
			&a.ID, &a.ClaimKey, &subj, &a.Predicate, &a.ObjectText, &objEnt,
			&qualifiers, &a.Confidence, &validFrom, &validTo, &a.SystemFrom, &systemTo,
			&a.Status, &supBy, &sourceEvents, &extractor,
			&a.Scope.Type, &a.Scope.ID, &a.Scope.Visibility, &a.CreatedAt, &ctxVec,
			&a.MentionCount, &lastMentioned,
		); err != nil {
			return nil, err
		}
		a.LastMentionedAt = lastMentioned.String
		if ctxVec.Valid && ctxVec.String != "" {
			_ = json.Unmarshal([]byte(ctxVec.String), &a.CtxVec)
		}
		if subj.Valid {
			v := subj.Int64
			a.SubjectEntityID = &v
		}
		if objEnt.Valid {
			v := objEnt.Int64
			a.ObjectEntityID = &v
		}
		if supBy.Valid {
			v := supBy.Int64
			a.SupersededBy = &v
		}
		a.ValidFrom = validFrom.String
		a.ValidTo = validTo.String
		a.SystemTo = systemTo.String
		a.ExtractorVersion = extractor.String
		if qualifiers.Valid && qualifiers.String != "" {
			_ = json.Unmarshal([]byte(qualifiers.String), &a.Qualifiers)
		}
		if sourceEvents.Valid && sourceEvents.String != "" {
			_ = json.Unmarshal([]byte(sourceEvents.String), &a.SourceEventIDs)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
