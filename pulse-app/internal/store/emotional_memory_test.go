package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func emotionalDelta(at time.Time, clientID, title string, emotions map[string]float64) SemanticDelta {
	return SemanticDelta{
		Schema: SemanticDeltaSchema,
		Source: SemanticDeltaSource{
			Host: "codex", ConversationScope: "current_turn", Timestamp: at.UTC().Format(time.RFC3339),
		},
		Events: []SemanticEvent{{
			ClientID: clientID, Title: title, Summary: "A short private description of the moment.",
			Emotions: emotions, EmotionDerivation: "inferred", EmotionConfidence: 0.8,
			ObservedLabel: "strong feeling", Confidence: 0.9, PrivacyTier: "private",
		}},
	}
}

func TestEmotionalMemoryDecaysAndQuestionIsDeliveredOnce(t *testing.T) {
	vault, err := Open(filepath.Join(t.TempDir(), "emotion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	result, err := vault.SaveSemanticDelta(emotionalDelta(now, "event:joy", "Joyful launch", map[string]float64{"joy": 0.8}))
	if err != nil {
		t.Fatal(err)
	}
	if result.EmotionQuestion == nil || len(result.EventResults) != 1 || result.EventResults[0].Result != "created" {
		t.Fatalf("unexpected write result: %#v", result)
	}
	if question, err := vault.TakePendingEmotionQuestion(now.Add(time.Minute)); err != nil || question != nil {
		t.Fatalf("question was offered twice: question=%#v err=%v", question, err)
	}
	context, err := vault.CurrentEmotionContext(now.Add(24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(context.Items) != 1 || math.Abs(context.Items[0].Influence-0.32) > 0.0001 {
		t.Fatalf("24h influence=%#v, want 0.32", context.Items)
	}
	context, err = vault.CurrentEmotionContext(now.Add(8 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(context.Items) != 0 {
		t.Fatalf("older than seven days still affects current state: %#v", context.Items)
	}
}

func TestEmotionAnswerCreatesConfirmedCauseWithoutRawConversation(t *testing.T) {
	vault, err := Open(filepath.Join(t.TempDir(), "answer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	first, err := vault.SaveSemanticDelta(emotionalDelta(now, "event:fear", "A tense moment", map[string]float64{"fear": 0.9}))
	if err != nil {
		t.Fatal(err)
	}
	answer := SemanticDelta{
		Schema: SemanticDeltaSchema,
		Source: SemanticDeltaSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Add(time.Hour).Format(time.RFC3339)},
		EmotionAnswers: []SemanticEmotionAnswer{{
			QuestionID: first.EmotionQuestion.QuestionID,
			Trigger:    SemanticEmotionTrigger{Summary: "A public release decision felt irreversible.", Derivation: "user_confirmed", Confidence: 1, Confirmed: true},
		}},
	}
	if _, err := vault.SaveSemanticDelta(answer); err != nil {
		t.Fatal(err)
	}
	var trigger, derivation string
	var confirmed int
	if err := vault.DB().QueryRow(`
		SELECT trigger_summary, trigger_derivation, trigger_confirmed
		  FROM event_emotions WHERE event_id=?`, first.EventIDs[0]).Scan(&trigger, &derivation, &confirmed); err != nil {
		t.Fatal(err)
	}
	if trigger == "" || derivation != "user_confirmed" || confirmed != 1 {
		t.Fatalf("cause not confirmed: trigger=%q derivation=%q confirmed=%d", trigger, derivation, confirmed)
	}
	var links int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM event_chains WHERE child_id=? AND kind='causal'`, first.EventIDs[0]).Scan(&links); err != nil || links != 1 {
		t.Fatalf("causal link count=%d err=%v", links, err)
	}
}

func TestPersonalQuestionIsNotRedeliveredAfterProjectionRebuild(t *testing.T) {
	vault, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	first, err := vault.PrepareManualSemanticDelta(
		emotionalDelta(now, "event:personal-fear", "Private tense moment", map[string]float64{"fear": 0.9}),
		now, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, vault, first.Receipts[0], now, time.Second)
	committed, err := vault.CommitMemoryTrayCandidate(first.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, _, question, err := vault.SemanticWriteOutcome(committed, now.Add(time.Second))
	if err != nil || question == nil {
		t.Fatalf("first ordinary response did not receive the question: question=%#v err=%v", question, err)
	}

	secondDelta := emotionalDelta(now.Add(time.Minute), "event:unrelated", "Unrelated memory", map[string]float64{})
	second, err := vault.PrepareManualSemanticDelta(secondDelta, now.Add(time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, vault, second.Receipts[0], now.Add(time.Minute), time.Second)
	if _, err := vault.CommitMemoryTrayCandidate(second.Receipts[0].CandidateID, 1, now.Add(time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if repeated, err := vault.TakePendingEmotionQuestion(now.Add(2 * time.Minute)); err != nil || repeated != nil {
		t.Fatalf("projection rebuild offered the same question again: question=%#v err=%v", repeated, err)
	}
}

func TestDeletedPersonalEmotionDoesNotBreakLaterProjectionRebuild(t *testing.T) {
	vault, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	first, err := vault.PrepareManualSemanticDelta(
		emotionalDelta(now, "event:delete-fear", "Moment to correct", map[string]float64{"fear": 0.9}),
		now, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, vault, first.Receipts[0], now, time.Second)
	committed, err := vault.CommitMemoryTrayCandidate(first.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, _, question, err := vault.SemanticWriteOutcome(committed, now.Add(time.Second))
	if err != nil || question == nil {
		t.Fatalf("question=%#v err=%v", question, err)
	}
	answer := SemanticDelta{
		Schema: SemanticDeltaSchema,
		Source: SemanticDeltaSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Add(time.Minute).Format(time.RFC3339)},
		EmotionAnswers: []SemanticEmotionAnswer{{
			QuestionID: question.QuestionID,
			Trigger:    SemanticEmotionTrigger{Summary: "A specific private cause.", Derivation: "user_confirmed", Confidence: 1, Confirmed: true},
		}},
	}
	preparedAnswer, err := vault.PrepareManualSemanticDelta(answer, now.Add(time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, vault, preparedAnswer.Receipts[0], now.Add(time.Minute), time.Second)
	if _, err := vault.CommitMemoryTrayCandidate(preparedAnswer.Receipts[0].CandidateID, 1, now.Add(time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	var emotionalEventID int64
	if err := vault.DB().QueryRow(`SELECT id FROM events WHERE title='Moment to correct'`).Scan(&emotionalEventID); err != nil {
		t.Fatal(err)
	}
	if err := vault.DeleteEventEmotion(emotionalEventID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	thirdDelta := emotionalDelta(now.Add(3*time.Minute), "event:later", "Later unrelated memory", map[string]float64{})
	third, err := vault.PrepareManualSemanticDelta(thirdDelta, now.Add(3*time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, vault, third.Receipts[0], now.Add(3*time.Minute), time.Second)
	if _, err := vault.CommitMemoryTrayCandidate(third.Receipts[0].CandidateID, 1, now.Add(3*time.Minute+time.Second)); err != nil {
		t.Fatalf("later memory could not rebuild after emotion deletion: %v", err)
	}
	var emotionalRows int
	if err := vault.DB().QueryRow(`
		SELECT COUNT(*) FROM event_emotions em JOIN events e ON e.id=em.event_id
		 WHERE e.title='Moment to correct'`).Scan(&emotionalRows); err != nil {
		t.Fatal(err)
	}
	if emotionalRows != 0 {
		t.Fatalf("deleted emotion returned after rebuild: %d rows", emotionalRows)
	}
}

func TestMemoryHomeCanCorrectAndDeleteOnlyEmotion(t *testing.T) {
	vault, err := Open(filepath.Join(t.TempDir(), "edit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	created, err := vault.SaveSemanticDelta(emotionalDelta(now, "event:edit", "Correctable moment", map[string]float64{"sadness": 0.7}))
	if err != nil {
		t.Fatal(err)
	}
	eventID := created.EventIDs[0]
	if err := vault.EditEventEmotion(eventID, EmotionEdit{
		Emotions: map[string]float64{"anger": 0.75}, Derivation: "user_confirmed", Confidence: 1,
		ObservedLabel: "раздражение", Trigger: &SemanticEmotionTrigger{
			Summary: "The same manual step failed again.", Derivation: "user_confirmed", Confidence: 1, Confirmed: true,
		},
	}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	history, err := vault.EmotionHistory(10, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || len(history[0].Labels) != 1 || history[0].Labels[0].Emotion != "anger" ||
		history[0].PrimaryEmotion != "anger" || history[0].PrimaryIntensity != 0.75 || !history[0].TriggerConfirmed {
		t.Fatalf("edited history=%#v", history)
	}
	if err := vault.DeleteEventEmotion(eventID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	history, err = vault.EmotionHistory(10, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("emotion survived deletion: %#v", history)
	}
	var events int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE id=?`, eventID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("deleting emotion removed event: count=%d err=%v", events, err)
	}
}

func TestRepeatedObservationRequiresThreeConfirmedMomentsAcrossTwoWeeks(t *testing.T) {
	vault, err := Open(filepath.Join(t.TempDir(), "pattern.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	start := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	for index, days := range []int{0, 7, 15} {
		delta := emotionalDelta(start.Add(time.Duration(days)*24*time.Hour),
			"event:pattern:"+string(rune('a'+index)), "Confirmed trust moment "+string(rune('A'+index)), map[string]float64{"trust": 0.7})
		delta.Events[0].EmotionDerivation = "user_confirmed"
		if _, err := vault.SaveSemanticDelta(delta); err != nil {
			t.Fatal(err)
		}
	}
	patterns, err := vault.ConfirmedEmotionPatterns()
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 1 || patterns[0].Emotion != "trust" || patterns[0].Count != 3 {
		t.Fatalf("patterns=%#v", patterns)
	}
}
