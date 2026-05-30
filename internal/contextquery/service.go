package contextquery

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/nkkmnk/pulse/internal/retrieve"
)

type Retrieval interface {
	Retrieve(context.Context, retrieve.RetrieveRequest) (*retrieve.RetrieveResponse, error)
}

type ServiceConfig struct {
	DB        *sql.DB
	Retrieval Retrieval
}

type Service struct {
	db        *sql.DB
	retrieval Retrieval
}

func New(cfg ServiceConfig) *Service {
	return &Service{db: cfg.DB, retrieval: cfg.Retrieval}
}

func (s *Service) Query(ctx context.Context, req ContextQueryRequest) (*ContextResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("context query: db is nil")
	}
	if s.retrieval == nil {
		return nil, fmt.Errorf("context query: retrieval is nil")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("context query: query is required")
	}
	scope := req.Scope
	if scope == "" {
		scope = "user"
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}

	ret, err := s.retrieval.Retrieve(ctx, retrieve.RetrieveRequest{
		Query:     query,
		Mode:      retrieve.QueryMode(req.Mode),
		TopK:      topK,
		UserState: req.UserState,
	})
	if err != nil {
		return nil, fmt.Errorf("context query retrieve: %w", err)
	}

	out := &ContextResult{
		SchemaVersion: SchemaVersion,
		Query:         query,
		ModeUsed:      string(ret.ModeUsed),
		Scope:         scope,
	}
	allowedDomains := normalizeDomains(req.DomainsAllowed)
	if req.IncludeTrace {
		out.Trace = &ContextTrace{
			Router: map[string]any{
				"mode":                    string(ret.RouterDecision.Mode),
				"confidence":              ret.RouterDecision.Confidence,
				"classifier":              ret.RouterDecision.Classifier,
				"reasoning":               ret.RouterDecision.Reasoning,
				"emotion_role":            string(ret.EmotionRole.Role),
				"emotion_role_confidence": ret.EmotionRole.Confidence,
				"emotion_role_reasoning":  ret.EmotionRole.Reasoning,
				"state_emotion":           ret.EmotionRole.StateEmotion,
			},
			Retrieval: map[string]any{
				"event_ids":             ret.EventIDs,
				"surfaceability_action": string(ret.SurfaceabilityAction),
			},
		}
	}

	if err := s.loadEvents(ctx, out, ret.EventIDs, allowedDomains); err != nil {
		return nil, err
	}
	if err := s.loadEntitiesAndFacts(ctx, out, ret.EventIDs, allowedDomains); err != nil {
		return nil, err
	}
	if err := s.loadAtomicFacts(ctx, out, ret.EventIDs); err != nil {
		return nil, err
	}
	if err := s.loadRelations(ctx, out); err != nil {
		return nil, err
	}
	if err := s.loadOpenQuestions(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) loadEvents(ctx context.Context, out *ContextResult, ids []int64, allowedDomains map[string]bool) error {
	for _, id := range ids {
		if !s.subjectInScope(ctx, "event", id, out.Scope) {
			continue
		}
		row := s.db.QueryRowContext(ctx, `
SELECT id, title, description, emotional_weight, COALESCE(belief_class,''), COALESCE(provenance,''), COALESCE(confidence_floor,0), COALESCE(domain,'real')
FROM events WHERE id=?`, id)
		var ev ContextEvent
		var emo float64
		if err := row.Scan(&ev.ID, &ev.Title, &ev.Summary, &emo, &ev.Kind, &ev.Provenance, &ev.Confidence, &ev.Domain); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return fmt.Errorf("load event %d: %w", id, err)
		}
		if !domainAllowed(ev.Domain, allowedDomains) {
			continue
		}
		ev.Score = emo
		ev.SourceScope = out.Scope
		ev.PrivacyFloor = "private"
		ev.EvidenceIDs = s.evidenceIDs(ctx, "event", ev.ID)
		out.Events = append(out.Events, ev)
		if emo >= 0.6 {
			out.EmotionalAnchors = append(out.EmotionalAnchors, ContextEmotionalAnchor{
				EventID:      ev.ID,
				Summary:      ev.Summary,
				Score:        emo,
				Confidence:   ev.Confidence,
				Provenance:   ev.Provenance,
				EvidenceIDs:  ev.EvidenceIDs,
				SourceScope:  out.Scope,
				PrivacyFloor: ev.PrivacyFloor,
			})
		}
	}
	return nil
}

// normalizeDomains turns the request's DomainsAllowed slice into a set.
// Returns nil to mean "no filter" (all domains pass) — keeps backwards
// compat for callers that don't yet send the field.
func normalizeDomains(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for _, d := range in {
		d = strings.TrimSpace(d)
		if d != "" {
			out[d] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// domainAllowed returns true when allowed is nil (no filter) OR the row's
// domain is in the set. Empty domain string is treated as "real" to be
// safe for legacy rows that pre-date migration 018.
func domainAllowed(domain string, allowed map[string]bool) bool {
	if allowed == nil {
		return true
	}
	if domain == "" {
		domain = "real"
	}
	return allowed[domain]
}

func (s *Service) loadEntitiesAndFacts(ctx context.Context, out *ContextResult, eventIDs []int64, allowedDomains map[string]bool) error {
	seen := map[int64]bool{}
	for _, eventID := range eventIDs {
		if !s.subjectInScope(ctx, "event", eventID, out.Scope) {
			continue
		}
		rows, err := s.db.QueryContext(ctx, `
SELECT e.id, e.kind, e.canonical_name, COALESCE(e.description_md,''), e.salience_score + e.emotional_weight, COALESCE(e.aliases,'[]')
FROM entities e
JOIN event_entities ee ON ee.entity_id=e.id
WHERE ee.event_id=?`, eventID)
		if err != nil {
			return fmt.Errorf("load entities for event %d: %w", eventID, err)
		}
		for rows.Next() {
			var ent ContextEntity
			var aliasesJSON string
			if err := rows.Scan(&ent.ID, &ent.Kind, &ent.CanonicalName, &ent.Summary, &ent.Score, &aliasesJSON); err != nil {
				rows.Close()
				return err
			}
			if ent.Kind == "safety_boundary" {
				out.Forbidden = append(out.Forbidden, ContextRedaction{
					SubjectKind: "entity",
					SubjectID:   ent.ID,
					Reason:      "safety boundary",
					Policy:      "never-default",
				})
				continue
			}
			if s.redactSensitiveEntity(ctx, out, ent.ID) {
				continue
			}
			if seen[ent.ID] {
				continue
			}
			seen[ent.ID] = true
			ent.Confidence = 1
			ent.Provenance = "graph"
			ent.SourceScope = out.Scope
			ent.PrivacyFloor = "private"
			ent.EvidenceIDs = s.evidenceIDs(ctx, "entity", ent.ID)
			out.Entities = append(out.Entities, ent)
			if err := s.loadFactsForEntity(ctx, out, ent.ID, allowedDomains); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func (s *Service) loadFactsForEntity(ctx context.Context, out *ContextResult, entityID int64, allowedDomains map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, text, confidence, COALESCE(belief_class,''), COALESCE(provenance,''), COALESCE(confidence_floor,0), COALESCE(domain,'real')
FROM facts
WHERE entity_id=?
  AND source_obs_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM observations o WHERE o.id=source_obs_id AND (o.scope=? OR o.scope='shared'))
ORDER BY confidence DESC, id ASC LIMIT 5`, entityID, out.Scope)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var fact ContextFact
		if err := rows.Scan(&fact.ID, &fact.Text, &fact.Confidence, &fact.Kind, &fact.Provenance, &fact.Score, &fact.Domain); err != nil {
			return err
		}
		if !domainAllowed(fact.Domain, allowedDomains) {
			continue
		}
		if fact.Kind == "hypothesis" || fact.Confidence < 0.5 {
			out.Uncertainty = append(out.Uncertainty, ContextUncertainty{
				SubjectKind: "fact",
				SubjectID:   fact.ID,
				Question:    fact.Text,
				Confidence:  fact.Confidence,
			})
			continue
		}
		fact.SourceScope = out.Scope
		fact.PrivacyFloor = "private"
		fact.EvidenceIDs = s.evidenceIDs(ctx, "fact", fact.ID)
		out.Facts = append(out.Facts, fact)
	}
	return rows.Err()
}

func (s *Service) loadAtomicFacts(ctx context.Context, out *ContextResult, eventIDs []int64) error {
	for _, eventID := range eventIDs {
		if !s.subjectInScope(ctx, "event", eventID, out.Scope) {
			continue
		}
		rows, err := s.db.QueryContext(ctx, `
SELECT id, text, confidence, COALESCE(extractor,''), confidence
FROM atomic_facts WHERE event_id=? ORDER BY confidence DESC, id ASC LIMIT 5`, eventID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var fact ContextFact
			if err := rows.Scan(&fact.ID, &fact.Text, &fact.Confidence, &fact.Provenance, &fact.Score); err != nil {
				rows.Close()
				return err
			}
			fact.Kind = "atomic_fact"
			fact.SourceScope = out.Scope
			fact.PrivacyFloor = "private"
			fact.EvidenceIDs = s.evidenceIDs(ctx, "event", eventID)
			out.Facts = append(out.Facts, fact)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func (s *Service) loadRelations(ctx context.Context, out *ContextResult) error {
	if len(out.Entities) == 0 {
		return nil
	}
	entityIDs := make(map[int64]bool, len(out.Entities))
	for _, ent := range out.Entities {
		entityIDs[ent.ID] = true
	}
	seen := map[int64]bool{}
	for entityID := range entityIDs {
		rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, from_entity_id, to_entity_id, strength, COALESCE(context,'')
FROM relations WHERE from_entity_id=? OR to_entity_id=? ORDER BY strength DESC, id ASC LIMIT 10`, entityID, entityID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var rel ContextRelation
			if err := rows.Scan(&rel.ID, &rel.Kind, &rel.FromEntityID, &rel.ToEntityID, &rel.Score, &rel.Summary); err != nil {
				rows.Close()
				return err
			}
			if !s.relationInScope(ctx, rel.ID, rel.FromEntityID, rel.ToEntityID, out.Scope) {
				continue
			}
			if seen[rel.ID] {
				continue
			}
			seen[rel.ID] = true
			rel.Confidence = 1
			rel.Provenance = "graph"
			rel.SourceScope = out.Scope
			rel.PrivacyFloor = "private"
			rel.EvidenceIDs = s.evidenceIDs(ctx, "relation", rel.ID)
			out.Relations = append(out.Relations, rel)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func (s *Service) loadOpenQuestions(ctx context.Context, out *ContextResult) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, subject_entity_id, question_text, state
FROM open_questions WHERE state='open' ORDER BY asked_at DESC LIMIT 5`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var q ContextImportanceQuestion
		var subject sql.NullInt64
		if err := rows.Scan(&q.ID, &subject, &q.QuestionText, &q.State); err != nil {
			return err
		}
		if !subject.Valid {
			continue
		}
		if !s.entityHasScopedEvidence(ctx, subject.Int64, out.Scope) {
			continue
		}
		q.SubjectEntityID = &subject.Int64
		out.ImportanceQuestions = append(out.ImportanceQuestions, q)
	}
	return rows.Err()
}

func (s *Service) redactSensitiveEntity(ctx context.Context, out *ContextResult, entityID int64) bool {
	var policy string
	err := s.db.QueryRowContext(ctx, `SELECT policy FROM sensitive_actors WHERE entity_id=?`, entityID).Scan(&policy)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		return false
	}
	out.Private = append(out.Private, ContextRedaction{
		SubjectKind: "entity",
		SubjectID:   entityID,
		Reason:      "sensitive actor",
		Policy:      policy,
	})
	return true
}

func (s *Service) entityHasScopedEvidence(ctx context.Context, entityID int64, scope string) bool {
	if scope == "" {
		return true
	}
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM event_entities ee
JOIN evidence ev ON ev.subject_kind='event' AND ev.subject_id=ee.event_id
JOIN observations o ON o.id = ev.observation_id
WHERE ee.entity_id=? AND (o.scope=? OR o.scope='shared')`, entityID, scope).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

func (s *Service) relationInScope(ctx context.Context, relationID, fromID, toID int64, scope string) bool {
	if !s.subjectInScope(ctx, "relation", relationID, scope) {
		return false
	}
	return !s.isSensitiveEntity(ctx, fromID) && !s.isSensitiveEntity(ctx, toID)
}

func (s *Service) isSensitiveEntity(ctx context.Context, entityID int64) bool {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sensitive_actors WHERE entity_id=?`, entityID).Scan(&count)
	return err == nil && count > 0
}

func (s *Service) subjectInScope(ctx context.Context, kind string, id int64, scope string) bool {
	if scope == "" {
		return true
	}
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM evidence ev
JOIN observations o ON o.id = ev.observation_id
WHERE ev.subject_kind=? AND ev.subject_id=? AND (o.scope=? OR o.scope='shared')`, kind, id, scope).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

func (s *Service) evidenceIDs(ctx context.Context, kind string, id int64) []int64 {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM evidence WHERE subject_kind=? AND subject_id=? ORDER BY id`, kind, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var evidenceID int64
		if rows.Scan(&evidenceID) == nil {
			ids = append(ids, evidenceID)
		}
	}
	return ids
}
