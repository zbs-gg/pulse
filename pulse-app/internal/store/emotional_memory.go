package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const emotionCauseQuestion = "Что именно сейчас вызвало эту эмоцию?"

var emotionAliases = map[string]string{
	"joy": "joy", "радость": "joy", "радостно": "joy",
	"sadness": "sadness", "грусть": "sadness", "печаль": "sadness", "грустно": "sadness",
	"anger": "anger", "злость": "anger", "гнев": "anger", "раздражение": "anger",
	"fear": "fear", "страх": "fear", "тревога": "fear", "тревожно": "fear",
	"trust": "trust", "доверие": "trust", "спокойствие": "trust",
	"disgust": "disgust", "отвращение": "disgust", "неприятие": "disgust",
	"anticipation": "anticipation", "ожидание": "anticipation", "предвкушение": "anticipation",
	"surprise": "surprise", "удивление": "surprise", "неожиданность": "surprise",
	"shame": "shame", "стыд": "shame", "стыдно": "shame",
	"guilt": "guilt", "вина": "guilt", "виноват": "guilt", "виновата": "guilt",
}

type EmotionContextItem struct {
	EventID          int64   `json:"event_id"`
	Emotion          string  `json:"emotion"`
	ObservedLabel    string  `json:"observed_label,omitempty"`
	Intensity        float64 `json:"intensity"`
	Confidence       float64 `json:"confidence"`
	Influence        float64 `json:"influence"`
	Derivation       string  `json:"derivation"`
	OccurredAt       string  `json:"occurred_at"`
	Trigger          string  `json:"trigger,omitempty"`
	TriggerConfirmed bool    `json:"trigger_confirmed"`
}

type CurrentEmotionContext struct {
	AsOf     string               `json:"as_of"`
	Items    []EmotionContextItem `json:"items"`
	Dominant *EmotionContextItem  `json:"dominant,omitempty"`
}

type EmotionHistoryLabel struct {
	Emotion          string  `json:"emotion"`
	Intensity        float64 `json:"intensity"`
	CurrentInfluence float64 `json:"current_influence"`
}

type EmotionHistoryItem struct {
	EventID           int64                 `json:"event_id"`
	Title             string                `json:"title"`
	Summary           string                `json:"summary"`
	OccurredAt        string                `json:"occurred_at"`
	Labels            []EmotionHistoryLabel `json:"labels"`
	PrimaryEmotion    string                `json:"primary_emotion"`
	PrimaryIntensity  float64               `json:"primary_intensity"`
	Derivation        string                `json:"derivation"`
	Confidence        float64               `json:"confidence"`
	ObservedLabel     string                `json:"observed_label,omitempty"`
	Trigger           string                `json:"trigger,omitempty"`
	TriggerDerivation string                `json:"trigger_derivation,omitempty"`
	TriggerConfidence float64               `json:"trigger_confidence"`
	TriggerConfirmed  bool                  `json:"trigger_confirmed"`
	Question          *EmotionQuestion      `json:"question,omitempty"`
}

type EmotionPattern struct {
	Emotion   string `json:"emotion"`
	Count     int    `json:"count"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	Label     string `json:"label"`
}

type EmotionEdit struct {
	Emotions      map[string]float64
	Derivation    string
	Confidence    float64
	ObservedLabel string
	Trigger       *SemanticEmotionTrigger
}

type emotionOverridePayload struct {
	Emotions      map[string]float64      `json:"emotions"`
	Derivation    string                  `json:"derivation"`
	Confidence    float64                 `json:"confidence"`
	ObservedLabel string                  `json:"observed_label"`
	Trigger       *SemanticEmotionTrigger `json:"trigger,omitempty"`
}

func normalizedEmotionDerivation(value, fallback string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "explicit" || value == "inferred" || value == "user_confirmed" {
		return value
	}
	return fallback
}

func canonicalEmotionLabel(value string) string {
	return emotionAliases[strings.ToLower(strings.TrimSpace(value))]
}

func dominantEmotionLabel(values map[string]float64) string {
	label, highest := "", -1.0
	for key, value := range values {
		canonical := canonicalEmotionLabel(key)
		if canonical == "" {
			canonical = key
		}
		if value > highest || (value == highest && canonical < label) {
			label, highest = canonical, value
		}
	}
	return label
}

func maxEmotionIntensity(values map[string]float64) float64 {
	max := 0.0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func semanticEventDigest(event SemanticEvent) string {
	refs := append([]string(nil), event.EntityRefs...)
	sort.Strings(refs)
	identity := struct {
		Title, Summary, Sentiment, PrivacyTier, Domain string
		EntityRefs                                     []string
		EmotionalWeight, Confidence                    float64
		Emotions                                       map[string]float64
		EmotionDerivation                              string
		EmotionConfidence                              float64
		ObservedLabel                                  string
		Trigger                                        *SemanticEmotionTrigger
	}{
		Title: strings.TrimSpace(event.Title), Summary: strings.TrimSpace(event.Summary),
		Sentiment: strings.TrimSpace(event.Sentiment), PrivacyTier: event.PrivacyTier,
		Domain: normalizeDomain(event.Domain), EntityRefs: refs,
		EmotionalWeight: event.EmotionalWeight, Confidence: event.Confidence,
		Emotions: event.Emotions, EmotionDerivation: normalizedEmotionDerivation(event.EmotionDerivation, "inferred"),
		EmotionConfidence: event.EmotionConfidence, ObservedLabel: strings.TrimSpace(event.ObservedLabel),
		Trigger: event.Trigger,
	}
	raw, _ := json.Marshal(identity)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func emotionQuestionID(event SemanticEvent) string {
	digest := sha256.Sum256([]byte("pulse:emotion-question:v1:" + semanticEventDigest(event)))
	return "emotion_question:" + hex.EncodeToString(digest[:16])
}

func ensureEmotionQuestionTx(
	tx *sql.Tx, eventID int64, event SemanticEvent, askedAt string, delivered bool,
) (*EmotionQuestion, error) {
	if len(event.Emotions) == 0 || maxEmotionIntensity(event.Emotions) < 0.6 ||
		(event.Trigger != nil && strings.TrimSpace(event.Trigger.Summary) != "") {
		return nil, nil
	}
	var emotionExists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM event_emotions WHERE event_id=?`, eventID).Scan(&emotionExists); err != nil {
		return nil, err
	}
	if emotionExists == 0 {
		return nil, nil
	}
	questionID := emotionQuestionID(event)
	asked, err := time.Parse(time.RFC3339, strings.TrimSpace(askedAt))
	if err != nil {
		return nil, err
	}
	expires := asked.Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var initialDelivery any
	if delivered {
		initialDelivery = asked.UTC().Format(time.RFC3339)
	}
	ledgerResult, err := tx.Exec(`
		INSERT INTO emotion_question_delivery(question_id, asked_at, expires_at, delivered_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(question_id) DO NOTHING`,
		questionID, asked.UTC().Format(time.RFC3339), expires, initialDelivery)
	if err != nil {
		return nil, err
	}
	ledgerCreated, _ := ledgerResult.RowsAffected()
	var rememberedDelivery sql.NullString
	if err := tx.QueryRow(`SELECT delivered_at FROM emotion_question_delivery WHERE question_id=?`, questionID).
		Scan(&rememberedDelivery); err != nil {
		return nil, err
	}
	var deliveredAt any
	if rememberedDelivery.Valid {
		deliveredAt = rememberedDelivery.String
	}
	_, err = tx.Exec(`
		INSERT INTO emotion_questions(question_id, event_id, question_text, asked_at, expires_at, delivered_at, state)
		VALUES (?, ?, ?, ?, ?, ?, 'open')
		ON CONFLICT(question_id) DO NOTHING`,
		questionID, eventID, emotionCauseQuestion, asked.UTC().Format(time.RFC3339), expires, deliveredAt)
	if err != nil {
		return nil, err
	}
	var state string
	if err := tx.QueryRow(`SELECT state, expires_at FROM emotion_questions WHERE question_id=?`, questionID).
		Scan(&state, &expires); err != nil {
		return nil, err
	}
	if state != "open" {
		return nil, nil
	}
	if rememberedDelivery.Valid && ledgerCreated == 0 {
		return nil, nil
	}
	return &EmotionQuestion{QuestionID: questionID, EventID: eventID, Question: emotionCauseQuestion, ExpiresAt: expires}, nil
}

func applyEmotionOverrideTx(tx *sql.Tx, eventID int64, emotionKey string) error {
	var action, payloadJSON string
	err := tx.QueryRow(`SELECT action, payload_json FROM emotion_overrides WHERE emotion_key=?`, emotionKey).
		Scan(&action, &payloadJSON)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if action == "delete" {
		if _, err := tx.Exec(`DELETE FROM emotion_questions WHERE event_id=?`, eventID); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM event_emotions WHERE event_id=?`, eventID)
		return err
	}
	var payload emotionOverridePayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return err
	}
	return updateEventEmotionTx(tx, eventID, emotionKey, payload)
}

func updateEventEmotionTx(tx *sql.Tx, eventID int64, emotionKey string, payload emotionOverridePayload) error {
	value := func(key string) float64 { return payload.Emotions[key] }
	triggerSummary, triggerDerivation := "", ""
	triggerConfidence, triggerConfirmed := 0.0, 0
	if payload.Trigger != nil {
		triggerSummary = strings.TrimSpace(payload.Trigger.Summary)
		triggerDerivation = normalizedEmotionDerivation(payload.Trigger.Derivation, payload.Derivation)
		triggerConfidence = payload.Trigger.Confidence
		if payload.Trigger.Confirmed || triggerDerivation == "explicit" || triggerDerivation == "user_confirmed" {
			triggerConfirmed = 1
		}
	}
	_, err := tx.Exec(`
		UPDATE event_emotions
		   SET joy=?, sadness=?, anger=?, fear=?, trust=?, disgust=?, anticipation=?, surprise=?, shame=?, guilt=?,
		       derivation=?, confidence=?, observed_label=?, trigger_summary=?, trigger_derivation=?,
		       trigger_confidence=?, trigger_confirmed=?, emotion_key=?, updated_at=?
		 WHERE event_id=?`,
		value("joy"), value("sadness"), value("anger"), value("fear"), value("trust"), value("disgust"),
		value("anticipation"), value("surprise"), value("shame"), value("guilt"),
		payload.Derivation, payload.Confidence, payload.ObservedLabel,
		triggerSummary, triggerDerivation, triggerConfidence, triggerConfirmed,
		emotionKey, time.Now().UTC().Format(time.RFC3339), eventID)
	return err
}

func validateEmotionEdit(edit EmotionEdit) (emotionOverridePayload, error) {
	if len(edit.Emotions) == 0 || len(edit.Emotions) > 10 {
		return emotionOverridePayload{}, fmt.Errorf("at least one emotion is required")
	}
	values := make(map[string]float64, len(edit.Emotions))
	for label, intensity := range edit.Emotions {
		canonical := canonicalEmotionLabel(label)
		if canonical == "" || intensity < 0 || intensity > 1 {
			return emotionOverridePayload{}, fmt.Errorf("emotion is unsupported or outside 0..1")
		}
		if intensity > values[canonical] {
			values[canonical] = intensity
		}
	}
	derivation := normalizedEmotionDerivation(edit.Derivation, "")
	if derivation == "" || edit.Confidence < 0 || edit.Confidence > 1 {
		return emotionOverridePayload{}, fmt.Errorf("emotion source or confidence is invalid")
	}
	if err := validateSemanticText("observed_label", edit.ObservedLabel, 120, false); err != nil {
		return emotionOverridePayload{}, err
	}
	if edit.Trigger != nil {
		if err := validateSemanticTrigger("trigger", *edit.Trigger); err != nil {
			return emotionOverridePayload{}, err
		}
	}
	return emotionOverridePayload{
		Emotions: values, Derivation: derivation, Confidence: edit.Confidence,
		ObservedLabel: strings.TrimSpace(edit.ObservedLabel), Trigger: edit.Trigger,
	}, nil
}

func (s *Store) EditEventEmotion(eventID int64, edit EmotionEdit, now time.Time) error {
	payload, err := validateEmotionEdit(edit)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var emotionKey string
	if err := tx.QueryRow(`SELECT emotion_key FROM event_emotions WHERE event_id=?`, eventID).Scan(&emotionKey); err != nil {
		return err
	}
	if emotionKey == "" {
		emotionKey = fmt.Sprintf("legacy-event:%d", eventID)
	}
	raw, _ := json.Marshal(payload)
	if _, err := tx.Exec(`
		INSERT INTO emotion_overrides(emotion_key, action, payload_json, updated_at)
		VALUES (?, 'update', ?, ?)
		ON CONFLICT(emotion_key) DO UPDATE SET action='update', payload_json=excluded.payload_json, updated_at=excluded.updated_at`,
		emotionKey, string(raw), now.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if err := updateEventEmotionTx(tx, eventID, emotionKey, payload); err != nil {
		return err
	}
	if payload.Trigger != nil && payload.Trigger.Summary != "" {
		_, _ = tx.Exec(`DELETE FROM emotion_questions WHERE event_id=?`, eventID)
	}
	return tx.Commit()
}

func (s *Store) ConfirmEventEmotion(eventID int64, now time.Time) error {
	edit, err := s.emotionEditFromEvent(eventID)
	if err != nil {
		return err
	}
	edit.Derivation = "user_confirmed"
	if edit.Trigger != nil && edit.Trigger.Summary != "" {
		edit.Trigger.Derivation = "user_confirmed"
		edit.Trigger.Confirmed = true
	}
	return s.EditEventEmotion(eventID, edit, now)
}

func (s *Store) emotionEditFromEvent(eventID int64) (EmotionEdit, error) {
	var values [10]float64
	var edit EmotionEdit
	var triggerSummary, triggerDerivation string
	var triggerConfidence float64
	var triggerConfirmed int
	args := []any{}
	for index := range values {
		args = append(args, &values[index])
	}
	args = append(args, &edit.Derivation, &edit.Confidence, &edit.ObservedLabel,
		&triggerSummary, &triggerDerivation, &triggerConfidence, &triggerConfirmed)
	err := s.db.QueryRow(`
		SELECT joy, sadness, anger, fear, trust, disgust, anticipation, surprise, shame, guilt,
		       derivation, confidence, observed_label, trigger_summary, trigger_derivation,
		       trigger_confidence, trigger_confirmed
		  FROM event_emotions WHERE event_id=?`, eventID).Scan(args...)
	if err != nil {
		return EmotionEdit{}, err
	}
	keys := []string{"joy", "sadness", "anger", "fear", "trust", "disgust", "anticipation", "surprise", "shame", "guilt"}
	edit.Emotions = map[string]float64{}
	for index, value := range values {
		if value > 0 {
			edit.Emotions[keys[index]] = value
		}
	}
	if triggerSummary != "" {
		edit.Trigger = &SemanticEmotionTrigger{Summary: triggerSummary, Derivation: triggerDerivation,
			Confidence: triggerConfidence, Confirmed: triggerConfirmed == 1}
	}
	return edit, nil
}

func (s *Store) DeleteEventEmotion(eventID int64, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var emotionKey string
	if err := tx.QueryRow(`SELECT emotion_key FROM event_emotions WHERE event_id=?`, eventID).Scan(&emotionKey); err != nil {
		return err
	}
	if emotionKey == "" {
		emotionKey = fmt.Sprintf("legacy-event:%d", eventID)
	}
	if _, err := tx.Exec(`
		INSERT INTO emotion_overrides(emotion_key, action, payload_json, updated_at)
		VALUES (?, 'delete', '{}', ?)
		ON CONFLICT(emotion_key) DO UPDATE SET action='delete', payload_json='{}', updated_at=excluded.updated_at`,
		emotionKey, now.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM emotion_questions WHERE event_id=?`, eventID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM event_emotions WHERE event_id=?`, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EmotionHistory(limit int, now time.Time) ([]EmotionHistoryItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT e.id, e.title, COALESCE(e.description,''), e.ts,
		       em.joy, em.sadness, em.anger, em.fear, em.trust, em.disgust,
		       em.anticipation, em.surprise, em.shame, em.guilt,
		       em.derivation, em.confidence, em.observed_label,
		       em.trigger_summary, em.trigger_derivation, em.trigger_confidence, em.trigger_confirmed,
		       COALESCE(q.question_id,''), COALESCE(q.question_text,''), COALESCE(q.expires_at,'')
		  FROM events e JOIN event_emotions em ON em.event_id=e.id
		  LEFT JOIN emotion_questions q ON q.event_id=e.id AND q.state='open'
		 ORDER BY e.ts DESC, e.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{"joy", "sadness", "anger", "fear", "trust", "disgust", "anticipation", "surprise", "shame", "guilt"}
	out := []EmotionHistoryItem{}
	for rows.Next() {
		var item EmotionHistoryItem
		var values [10]float64
		var confirmed int
		var questionID, questionText, questionExpires string
		args := []any{&item.EventID, &item.Title, &item.Summary, &item.OccurredAt}
		for index := range values {
			args = append(args, &values[index])
		}
		args = append(args, &item.Derivation, &item.Confidence, &item.ObservedLabel,
			&item.Trigger, &item.TriggerDerivation, &item.TriggerConfidence, &confirmed,
			&questionID, &questionText, &questionExpires)
		if err := rows.Scan(args...); err != nil {
			return nil, err
		}
		item.TriggerConfirmed = confirmed == 1
		when, _ := time.Parse(time.RFC3339, item.OccurredAt)
		age := math.Max(0, now.UTC().Sub(when).Hours())
		for index, intensity := range values {
			if intensity <= 0 {
				continue
			}
			influence := 0.0
			if age <= 7*24 {
				influence = intensity * item.Confidence * math.Pow(2, -age/24)
			}
			item.Labels = append(item.Labels, EmotionHistoryLabel{Emotion: keys[index], Intensity: intensity, CurrentInfluence: influence})
			if item.PrimaryEmotion == "" || intensity > item.PrimaryIntensity {
				item.PrimaryEmotion = keys[index]
				item.PrimaryIntensity = intensity
			}
		}
		if questionID != "" {
			item.Question = &EmotionQuestion{QuestionID: questionID, EventID: item.EventID, Question: questionText, ExpiresAt: questionExpires}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ConfirmedEmotionPatterns() ([]EmotionPattern, error) {
	rows, err := s.db.Query(`
		SELECT e.ts, em.joy, em.sadness, em.anger, em.fear, em.trust, em.disgust,
		       em.anticipation, em.surprise, em.shame, em.guilt
		  FROM events e JOIN event_emotions em ON em.event_id=e.id
		 WHERE em.derivation IN ('explicit','user_confirmed')
		 ORDER BY e.ts ASC, e.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type span struct {
		count       int
		first, last time.Time
	}
	keys := []string{"joy", "sadness", "anger", "fear", "trust", "disgust", "anticipation", "surprise", "shame", "guilt"}
	spans := map[string]span{}
	for rows.Next() {
		var occurred string
		var values [10]float64
		args := []any{&occurred}
		for index := range values {
			args = append(args, &values[index])
		}
		if err := rows.Scan(args...); err != nil {
			return nil, err
		}
		when, err := time.Parse(time.RFC3339, occurred)
		if err != nil {
			continue
		}
		for index, value := range values {
			if value <= 0 {
				continue
			}
			current := spans[keys[index]]
			current.count++
			if current.first.IsZero() || when.Before(current.first) {
				current.first = when
			}
			if current.last.IsZero() || when.After(current.last) {
				current.last = when
			}
			spans[keys[index]] = current
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []EmotionPattern{}
	for emotion, item := range spans {
		if item.count < 3 || item.last.Sub(item.first) < 14*24*time.Hour {
			continue
		}
		out = append(out, EmotionPattern{
			Emotion: emotion, Count: item.count,
			FirstSeen: item.first.UTC().Format(time.RFC3339), LastSeen: item.last.UTC().Format(time.RFC3339),
			Label: "Repeated observation, not a personality trait",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Emotion < out[j].Emotion })
	return out, nil
}

func answerEmotionQuestionTx(tx *sql.Tx, answer SemanticEmotionAnswer, answeredAt string) (int64, error) {
	var eventID int64
	var state, expiresAt string
	if err := tx.QueryRow(`
		SELECT event_id, state, expires_at FROM emotion_questions WHERE question_id=?`,
		strings.TrimSpace(answer.QuestionID)).Scan(&eventID, &state, &expiresAt); err != nil {
		return 0, err
	}
	if state != "open" {
		return 0, fmt.Errorf("emotion question is not open")
	}
	now, err := time.Parse(time.RFC3339, strings.TrimSpace(answeredAt))
	if err != nil {
		return 0, err
	}
	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now.After(expires) {
		_, _ = tx.Exec(`UPDATE emotion_questions SET state='expired' WHERE question_id=?`, answer.QuestionID)
		return 0, fmt.Errorf("emotion question has expired")
	}
	trigger := answer.Trigger
	res, err := tx.Exec(`
		INSERT INTO events(title, description, emotional_weight, scorer_version, ts,
		                   belief_class, confidence_floor, provenance, domain)
		VALUES ('Confirmed emotion cause', ?, 0, 'emotion-answer', ?,
		        'operational', ?, 'interactive_memory', 'real')`,
		strings.TrimSpace(trigger.Summary), now.UTC().Format(time.RFC3339), trigger.Confidence)
	if err != nil {
		return 0, err
	}
	answerEventID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	derivation := normalizedEmotionDerivation(trigger.Derivation, "user_confirmed")
	if _, err := tx.Exec(`
		UPDATE event_emotions
		   SET trigger_summary=?, trigger_derivation=?, trigger_confidence=?,
		       trigger_confirmed=1, updated_at=?
		 WHERE event_id=?`, strings.TrimSpace(trigger.Summary), derivation,
		trigger.Confidence, now.UTC().Format(time.RFC3339), eventID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO event_chains(parent_id, child_id, strength, kind)
		VALUES (?, ?, ?, 'causal')`, answerEventID, eventID, trigger.Confidence); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		UPDATE emotion_questions
		   SET state='answered', answered_at=?, answer_event_id=?
		 WHERE question_id=? AND state='open'`, now.UTC().Format(time.RFC3339),
		answerEventID, answer.QuestionID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		UPDATE emotion_question_delivery
		   SET answered_at=COALESCE(answered_at, ?), delivered_at=COALESCE(delivered_at, ?)
		 WHERE question_id=?`, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), answer.QuestionID); err != nil {
		return 0, err
	}
	return answerEventID, nil
}

func (s *Store) CurrentEmotionContext(now time.Time) (CurrentEmotionContext, error) {
	now = now.UTC()
	if _, err := s.db.Exec(`UPDATE emotion_questions SET state='expired' WHERE state='open' AND expires_at < ?`, now.Format(time.RFC3339)); err != nil {
		return CurrentEmotionContext{}, err
	}
	rows, err := s.db.Query(`
		SELECT e.id, e.ts, em.joy, em.sadness, em.anger, em.fear, em.trust,
		       em.disgust, em.anticipation, em.surprise, em.shame, em.guilt,
		       em.confidence, em.derivation, em.observed_label,
		       em.trigger_summary, em.trigger_confirmed
		  FROM events e JOIN event_emotions em ON em.event_id=e.id
		 WHERE e.ts >= ? ORDER BY e.ts DESC, e.id DESC`,
		now.Add(-7*24*time.Hour).Format(time.RFC3339))
	if err != nil {
		return CurrentEmotionContext{}, err
	}
	defer rows.Close()
	keys := []string{"joy", "sadness", "anger", "fear", "trust", "disgust", "anticipation", "surprise", "shame", "guilt"}
	out := CurrentEmotionContext{AsOf: now.Format(time.RFC3339), Items: []EmotionContextItem{}}
	for rows.Next() {
		var eventID int64
		var occurred, derivation, observed, trigger string
		var values [10]float64
		var confidence float64
		var confirmed int
		args := []any{&eventID, &occurred}
		for index := range values {
			args = append(args, &values[index])
		}
		args = append(args, &confidence, &derivation, &observed, &trigger, &confirmed)
		if err := rows.Scan(args...); err != nil {
			return CurrentEmotionContext{}, err
		}
		when, err := time.Parse(time.RFC3339, occurred)
		if err != nil {
			continue
		}
		ageHours := math.Max(0, now.Sub(when).Hours())
		for index, intensity := range values {
			influence := intensity * confidence * math.Pow(2, -ageHours/24)
			if influence < 0.25 {
				continue
			}
			item := EmotionContextItem{
				EventID: eventID, Emotion: keys[index], ObservedLabel: observed,
				Intensity: intensity, Confidence: confidence, Influence: influence,
				Derivation: derivation, OccurredAt: occurred, Trigger: trigger,
				TriggerConfirmed: confirmed == 1,
			}
			out.Items = append(out.Items, item)
		}
	}
	if err := rows.Err(); err != nil {
		return CurrentEmotionContext{}, err
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Influence > out.Items[j].Influence })
	if len(out.Items) > 0 && out.Items[0].Influence >= 0.5 {
		item := out.Items[0]
		out.Dominant = &item
	}
	return out, nil
}

func (s *Store) PendingEmotionQuestion(now time.Time) (*EmotionQuestion, error) {
	now = now.UTC()
	if _, err := s.db.Exec(`UPDATE emotion_questions SET state='expired' WHERE state='open' AND expires_at < ?`, now.Format(time.RFC3339)); err != nil {
		return nil, err
	}
	var question EmotionQuestion
	err := s.db.QueryRow(`
		SELECT question_id, event_id, question_text, expires_at
		  FROM emotion_questions WHERE state='open'
		 ORDER BY asked_at ASC, question_id ASC LIMIT 1`).Scan(
		&question.QuestionID, &question.EventID, &question.Question, &question.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &question, nil
}

// TakePendingEmotionQuestion returns one question to an ordinary response and
// marks it delivered in the same transaction. An unanswered question never
// creates another model turn and is never offered twice.
func (s *Store) TakePendingEmotionQuestion(now time.Time) (*EmotionQuestion, error) {
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE emotion_questions SET state='expired' WHERE state='open' AND expires_at < ?`, now.Format(time.RFC3339)); err != nil {
		return nil, err
	}
	var question EmotionQuestion
	err = tx.QueryRow(`
		SELECT question_id, event_id, question_text, expires_at
		  FROM emotion_questions
		 WHERE state='open' AND delivered_at IS NULL
		 ORDER BY asked_at ASC, question_id ASC LIMIT 1`).Scan(
		&question.QuestionID, &question.EventID, &question.Question, &question.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.Exec(`
		UPDATE emotion_questions SET delivered_at=?
		 WHERE question_id=? AND state='open' AND delivered_at IS NULL`,
		now.Format(time.RFC3339), question.QuestionID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, fmt.Errorf("emotion question delivery changed concurrently")
	}
	if _, err := tx.Exec(`
		UPDATE emotion_question_delivery SET delivered_at=COALESCE(delivered_at, ?)
		 WHERE question_id=?`, now.Format(time.RFC3339), question.QuestionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &question, nil
}

// SemanticWriteOutcome translates a committed private semantic object into the
// event-level receipt returned to MCP clients. It contains no raw content.
func (s *Store) SemanticWriteOutcome(
	receipt MemoryWriteReceipt, now time.Time,
) ([]int64, []SemanticEventWriteResult, *EmotionQuestion, error) {
	if receipt.ObjectID == "" || receipt.CandidateID == "" ||
		(receipt.Status != MemoryWriteCreated && receipt.Status != MemoryWriteDeduplicated) {
		return nil, nil, nil, nil
	}
	var payload string
	if err := s.db.QueryRow(`
		SELECT payload_json FROM memory_tray_candidates
		 WHERE candidate_id=? AND candidate_kind='semantic_delta'`,
		receipt.CandidateID).Scan(&payload); err == sql.ErrNoRows {
		return nil, nil, nil, nil
	} else if err != nil {
		return nil, nil, nil, err
	}
	var candidate PrivateMemoryCandidate
	if err := json.Unmarshal([]byte(payload), &candidate); err != nil || candidate.SemanticDelta == nil {
		return nil, nil, nil, fmt.Errorf("stored semantic outcome is invalid")
	}
	rows, err := s.db.Query(`
		SELECT CAST(row_ref AS INTEGER)
		  FROM private_semantic_projection_rows
		 WHERE object_id=? AND row_kind='event'
		 ORDER BY CAST(row_ref AS INTEGER)`, receipt.ObjectID)
	if err != nil {
		return nil, nil, nil, err
	}
	var allEventIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		allEventIDs = append(allEventIDs, id)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	if len(candidate.SemanticDelta.Events) == 0 {
		return nil, nil, nil, nil
	}
	if len(allEventIDs) < len(candidate.SemanticDelta.Events) {
		return nil, nil, nil, fmt.Errorf("semantic outcome event projection is incomplete")
	}
	// Answer-only deltas can add a projected cause event after ordinary events;
	// the ordinary event rows are always projected first.
	eventIDs := allEventIDs[:len(candidate.SemanticDelta.Events)]
	outcome := "created"
	if receipt.Status == MemoryWriteDeduplicated {
		outcome = "deduplicated"
	}
	results := make([]SemanticEventWriteResult, 0, len(eventIDs))
	for index, id := range eventIDs {
		results = append(results, SemanticEventWriteResult{
			ClientID: candidate.SemanticDelta.Events[index].ClientID,
			ID:       id, Result: outcome,
		})
	}
	question, err := s.takeEmotionQuestionForEvents(eventIDs, now)
	if err != nil {
		return nil, nil, nil, err
	}
	if question != nil {
		for index, id := range eventIDs {
			if id == question.EventID {
				question.EventClientID = candidate.SemanticDelta.Events[index].ClientID
				break
			}
		}
	}
	return eventIDs, results, question, nil
}

func (s *Store) takeEmotionQuestionForEvents(eventIDs []int64, now time.Time) (*EmotionQuestion, error) {
	for _, eventID := range eventIDs {
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		var question EmotionQuestion
		err = tx.QueryRow(`
			SELECT question_id, event_id, question_text, expires_at
			  FROM emotion_questions
			 WHERE event_id=? AND state='open' AND delivered_at IS NULL AND expires_at>=?`,
			eventID, now.UTC().Format(time.RFC3339)).Scan(
			&question.QuestionID, &question.EventID, &question.Question, &question.ExpiresAt)
		if err == sql.ErrNoRows {
			_ = tx.Rollback()
			continue
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE emotion_questions SET delivered_at=? WHERE question_id=? AND delivered_at IS NULL`,
			now.UTC().Format(time.RFC3339), question.QuestionID); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err := tx.Exec(`
			UPDATE emotion_question_delivery SET delivered_at=COALESCE(delivered_at, ?)
			 WHERE question_id=?`, now.UTC().Format(time.RFC3339), question.QuestionID); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &question, nil
	}
	return nil, nil
}
