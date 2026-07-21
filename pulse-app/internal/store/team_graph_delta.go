package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkkmnk/pulse/internal/teamauth"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

const (
	TeamGraphDeltaSchema       = "pulse.team.graph_delta.v1"
	maxTeamGraphDeltaBodyBytes = 256 << 10
)

var ErrTeamGraphDeltaInvalid = errors.New("team_graph_delta_invalid")

// TeamGraphDeltaWrite is the transport-independent team graph ingress
// contract. Identity, ownership, role, store, team, and generation come only
// from the sealed mutation permit and never from this caller-controlled body.
type TeamGraphDeltaWrite struct {
	Schema           string                 `json:"schema"`
	Source           CapsuleSource          `json:"source"`
	Nodes            []TeamGraphNode        `json:"nodes"`
	Edges            []TeamGraphEdge        `json:"edges"`
	Facts            []TeamGraphFact        `json:"facts"`
	Events           []TeamGraphEvent       `json:"events"`
	Continuity       *TeamGraphContinuity   `json:"continuity,omitempty"`
	RawInputIncluded bool                   `json:"raw_input_included"`
	ActiveContext    TeamGraphActiveContext `json:"active_context"`
	TargetScope      *TeamGraphTarget       `json:"target_scope,omitempty"`
	PrivacyTier      string                 `json:"privacy_tier"`
	Retention        string                 `json:"retention"`
	ExpiresAt        *string                `json:"expires_at,omitempty"`
	IdempotencyKey   string                 `json:"idempotency_key"`
}

type TeamGraphNode struct {
	ClientID        string   `json:"client_id"`
	Kind            string   `json:"kind"`
	CanonicalName   string   `json:"canonical_name"`
	Summary         *string  `json:"summary,omitempty"`
	Aliases         []string `json:"aliases,omitempty"`
	Salience        *float64 `json:"salience,omitempty"`
	EmotionalWeight *float64 `json:"emotional_weight,omitempty"`
	Domain          string   `json:"domain"`
}

type TeamGraphEdge struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Kind     string   `json:"kind"`
	Summary  *string  `json:"summary,omitempty"`
	Strength *float64 `json:"strength,omitempty"`
}

type TeamGraphFact struct {
	Node            string   `json:"node"`
	Text            string   `json:"text"`
	Predicate       *string  `json:"predicate,omitempty"`
	ObjectText      *string  `json:"object_text,omitempty"`
	ValidFrom       *string  `json:"valid_from,omitempty"`
	ChangeCue       *bool    `json:"change_cue,omitempty"`
	SourceEventRefs []string `json:"source_event_refs,omitempty"`
	Confidence      *float64 `json:"confidence"`
	Domain          string   `json:"domain"`
}

type TeamGraphBiometrics struct {
	HRV          *float64 `json:"hrv,omitempty"`
	SleepQuality *float64 `json:"sleep_quality,omitempty"`
	StressProxy  *float64 `json:"stress_proxy,omitempty"`
	HRTrend      *string  `json:"hr_trend,omitempty"`
	HRVTrend     *string  `json:"hrv_trend,omitempty"`
	Workout      *bool    `json:"workout,omitempty"`
}

type TeamGraphEvent struct {
	ClientID        string               `json:"client_id"`
	Title           string               `json:"title"`
	Summary         string               `json:"summary"`
	EntityRefs      []string             `json:"entity_refs,omitempty"`
	Sentiment       *string              `json:"sentiment,omitempty"`
	EmotionalWeight *float64             `json:"emotional_weight,omitempty"`
	Confidence      *float64             `json:"confidence"`
	Domain          string               `json:"domain"`
	OccurredAt      *string              `json:"occurred_at,omitempty"`
	Anchor          *bool                `json:"anchor,omitempty"`
	Biometrics      *TeamGraphBiometrics `json:"biometrics,omitempty"`
	Emotions        map[string]*float64  `json:"emotions,omitempty"`
}

type TeamGraphContinuity struct {
	ThreadID         string   `json:"thread_id"`
	SessionID        string   `json:"session_id"`
	Summary          string   `json:"summary"`
	Decisions        []string `json:"decisions,omitempty"`
	OpenLoops        []string `json:"open_loops,omitempty"`
	DoNotRepeat      []string `json:"do_not_repeat,omitempty"`
	EmotionalAnchors []string `json:"emotional_anchors,omitempty"`
	StateSignals     []string `json:"state_signals,omitempty"`
	ActiveThreads    []string `json:"active_threads,omitempty"`
	ReviewInsights   []string `json:"review_insights,omitempty"`
}

type TeamGraphActiveContext struct {
	ProjectID string `json:"project_id,omitempty"`
	RepoID    string `json:"repo_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type TeamGraphTarget struct {
	Type teamauth.ScopeType `json:"type"`
	ID   string             `json:"id,omitempty"`
}

type teamGraphCanonicalNode struct {
	ClientID        string   `json:"client_id"`
	Kind            string   `json:"kind"`
	CanonicalName   string   `json:"canonical_name"`
	Summary         *string  `json:"summary,omitempty"`
	Aliases         []string `json:"aliases"`
	Salience        float64  `json:"salience"`
	EmotionalWeight float64  `json:"emotional_weight"`
	Domain          string   `json:"domain"`
}

type teamGraphCanonicalEdge struct {
	From     string  `json:"from"`
	To       string  `json:"to"`
	Kind     string  `json:"kind"`
	Summary  *string `json:"summary,omitempty"`
	Strength float64 `json:"strength"`
}

type teamGraphCanonicalFact struct {
	Node            string    `json:"node"`
	Text            string    `json:"text"`
	Predicate       *string   `json:"predicate,omitempty"`
	ObjectText      *string   `json:"object_text,omitempty"`
	ValidFrom       *string   `json:"valid_from,omitempty"`
	ChangeCue       *bool     `json:"change_cue,omitempty"`
	SourceEventRefs *[]string `json:"source_event_refs,omitempty"`
	Confidence      float64   `json:"confidence"`
	Domain          string    `json:"domain"`
}

type teamGraphCanonicalBiometrics struct {
	HRV          *float64 `json:"hrv,omitempty"`
	SleepQuality *float64 `json:"sleep_quality,omitempty"`
	StressProxy  *float64 `json:"stress_proxy,omitempty"`
	HRTrend      *string  `json:"hr_trend,omitempty"`
	HRVTrend     *string  `json:"hrv_trend,omitempty"`
	Workout      *bool    `json:"workout,omitempty"`
}

type teamGraphCanonicalEmotions struct {
	Joy          *float64 `json:"joy,omitempty"`
	Sadness      *float64 `json:"sadness,omitempty"`
	Anger        *float64 `json:"anger,omitempty"`
	Fear         *float64 `json:"fear,omitempty"`
	Trust        *float64 `json:"trust,omitempty"`
	Disgust      *float64 `json:"disgust,omitempty"`
	Anticipation *float64 `json:"anticipation,omitempty"`
	Surprise     *float64 `json:"surprise,omitempty"`
	Shame        *float64 `json:"shame,omitempty"`
	Guilt        *float64 `json:"guilt,omitempty"`
}

type teamGraphCanonicalEvent struct {
	ClientID        string                        `json:"client_id"`
	Title           string                        `json:"title"`
	Summary         string                        `json:"summary"`
	EntityRefs      []string                      `json:"entity_refs"`
	Sentiment       *string                       `json:"sentiment,omitempty"`
	EmotionalWeight float64                       `json:"emotional_weight"`
	Confidence      float64                       `json:"confidence"`
	Domain          string                        `json:"domain"`
	OccurredAt      string                        `json:"occurred_at"`
	Anchor          bool                          `json:"anchor"`
	Biometrics      *teamGraphCanonicalBiometrics `json:"biometrics,omitempty"`
	Emotions        teamGraphCanonicalEmotions    `json:"emotions"`
}

type teamGraphCanonicalContinuity struct {
	ThreadID         string   `json:"thread_id"`
	SessionID        string   `json:"session_id"`
	Summary          string   `json:"summary"`
	Decisions        []string `json:"decisions"`
	OpenLoops        []string `json:"open_loops"`
	DoNotRepeat      []string `json:"do_not_repeat"`
	EmotionalAnchors []string `json:"emotional_anchors"`
	StateSignals     []string `json:"state_signals"`
	ActiveThreads    []string `json:"active_threads"`
	ReviewInsights   []string `json:"review_insights"`
}

type teamGraphCanonicalBody struct {
	Schema           string                        `json:"schema"`
	Source           CapsuleSource                 `json:"source"`
	Nodes            []teamGraphCanonicalNode      `json:"nodes"`
	Edges            []teamGraphCanonicalEdge      `json:"edges"`
	Facts            []teamGraphCanonicalFact      `json:"facts"`
	Events           []teamGraphCanonicalEvent     `json:"events"`
	Continuity       *teamGraphCanonicalContinuity `json:"continuity,omitempty"`
	RawInputIncluded bool                          `json:"raw_input_included"`
	ActiveContext    TeamGraphActiveContext        `json:"active_context"`
	TargetScope      *TeamGraphTarget              `json:"target_scope,omitempty"`
	PrivacyTier      string                        `json:"privacy_tier"`
	Retention        string                        `json:"retention"`
	ExpiresAt        *string                       `json:"expires_at,omitempty"`
}

type normalizedTeamGraphDeltaWrite struct {
	body              teamGraphCanonicalBody
	canonicalBody     []byte
	canonicalWire     []byte
	bodyDigest        string
	idempotencyKey    string
	expiresAt         *time.Time
	projectionKinds   []string
	policyDigest      string
	intentDescriptors []teamSemanticIntentDescriptor
}

type teamGraphDeltaStorageInput struct {
	Source        CapsuleSource
	CanonicalJSON []byte
	ContentDigest string
}

type teamSemanticIntentDescriptor struct {
	ProjectionKind     string
	SourceKind         string
	SourceOrdinal      int
	DerivativeObjectID string
	DerivativeKind     string
	SemanticKeyDigest  string
	PolicyDigest       string
	PayloadDigest      string
}

type teamSemanticProjectionIntent struct {
	IntentID           string
	ProjectionKind     string
	SourceKind         string
	SourceOrdinal      int
	DerivativeObjectID string
	DerivativeKind     string
	SemanticKeyDigest  string
	PolicyDigest       string
	PayloadDigest      string
}

// StoreTeamGraphDelta commits the canonical graph envelope, projection
// intents, root, audit, idempotency record, and conditional jobs in one
// writer-fenced transaction. It never writes the legacy local graph,
// assertion, or continuity tables.
func (s *Store) StoreTeamGraphDelta(
	ctx context.Context,
	permit TeamMutationPermit,
	writer TeamWriterLeaseIdentity,
	requestID, oauthClientKey string,
	write TeamGraphDeltaWrite,
) (TeamObjectWriteResult, error) {
	normalized, err := normalizeTeamGraphDeltaWrite(permit, write)
	if err != nil {
		return TeamObjectWriteResult{}, err
	}
	return s.storeTeamObjectWithExtension(ctx, TeamObjectWriteRequest{
		Permit: permit, Writer: writer, RequestID: requestID,
		OAuthClientKey: oauthClientKey, IdempotencyKey: normalized.idempotencyKey,
		Body: normalized.canonicalBody, BodyDigest: normalized.bodyDigest,
		Policy: TeamObjectPolicy{
			PrivacyTier: normalized.body.PrivacyTier,
			Retention:   normalized.body.Retention,
			ExpiresAt:   normalized.expiresAt,
		},
		ProjectionKinds: normalized.projectionKinds,
	}, func(ctx context.Context, transaction *teamObjectWriteTransaction) error {
		if err := transaction.InsertTeamGraphDeltaInput(ctx, teamGraphDeltaStorageInput{
			Source: normalized.body.Source, CanonicalJSON: normalized.canonicalBody,
			ContentDigest: normalized.bodyDigest,
		}); err != nil {
			return err
		}
		if err := transaction.MapStorage(ctx, "graph_delta_input", transaction.ObjectID); err != nil {
			return err
		}
		for _, descriptor := range normalized.intentDescriptors {
			intent := teamSemanticProjectionIntent{
				IntentID: teamGraphOpaqueDigestID("semantic_intent",
					"pulse-team-semantic-intent-v1", transaction.ObjectID,
					strconv.FormatInt(transaction.Scope.Generation, 10),
					descriptor.ProjectionKind, descriptor.SourceKind,
					strconv.Itoa(descriptor.SourceOrdinal), descriptor.DerivativeObjectID,
					descriptor.PayloadDigest),
				ProjectionKind: descriptor.ProjectionKind, SourceKind: descriptor.SourceKind,
				SourceOrdinal:      descriptor.SourceOrdinal,
				DerivativeObjectID: descriptor.DerivativeObjectID,
				DerivativeKind:     descriptor.DerivativeKind,
				SemanticKeyDigest:  descriptor.SemanticKeyDigest,
				PolicyDigest:       descriptor.PolicyDigest, PayloadDigest: descriptor.PayloadDigest,
			}
			if err := transaction.InsertTeamSemanticProjectionIntent(ctx, intent); err != nil {
				return err
			}
		}
		return nil
	})
}

func normalizeTeamGraphDeltaWrite(
	permit TeamMutationPermit,
	write TeamGraphDeltaWrite,
) (normalizedTeamGraphDeltaWrite, error) {
	return normalizeTeamGraphDeltaWriteWithIdempotencyHash(permit, write, "")
}

func normalizeTeamGraphDeltaWriteWithIdempotencyHash(
	permit TeamMutationPermit,
	write TeamGraphDeltaWrite,
	idempotencyKeyHash string,
) (normalizedTeamGraphDeltaWrite, error) {
	invalid := func() (normalizedTeamGraphDeltaWrite, error) {
		return normalizedTeamGraphDeltaWrite{}, ErrTeamGraphDeltaInvalid
	}
	if write.Schema != TeamGraphDeltaSchema || write.RawInputIncluded ||
		permit.Action() != teamauth.ActionWrite || permit.ObjectKind() != "graph_delta" ||
		permit.ExistingObjectID() != "" || write.Nodes == nil || write.Edges == nil ||
		write.Facts == nil || write.Events == nil ||
		len(write.Nodes) > 30 || len(write.Edges) > 50 ||
		len(write.Facts) > 50 || len(write.Events) > 20 ||
		(len(write.Nodes) == 0 && len(write.Edges) == 0 && len(write.Facts) == 0 &&
			len(write.Events) == 0 && write.Continuity == nil) {
		return invalid()
	}

	source, ok := canonicalTeamGraphSource(write.Source)
	if !ok {
		return invalid()
	}
	active, ok := canonicalTeamGraphActiveContext(write.ActiveContext)
	if !ok {
		return invalid()
	}
	target, ok := canonicalTeamGraphTarget(write.TargetScope)
	if !ok || !validTeamGraphPermitEnvelope(permit, active, target) ||
		!validTeamPrivacy(strings.TrimSpace(write.PrivacyTier)) ||
		!validTeamRetention(strings.TrimSpace(write.Retention)) {
		return invalid()
	}
	idempotencyKey, ok := canonicalTeamGraphOpaque(write.IdempotencyKey, 8, 255)
	if !ok || (idempotencyKeyHash != "" && !lowerHexDigest(idempotencyKeyHash)) {
		return invalid()
	}

	nodes := make([]teamGraphCanonicalNode, 0, len(write.Nodes))
	nodeRefs := make(map[string]struct{}, len(write.Nodes))
	nodeSemanticKeys := make(map[string]string, len(write.Nodes))
	semanticNodes := make(map[string]struct{}, len(write.Nodes))
	for _, input := range write.Nodes {
		clientID, ok := canonicalTeamGraphRef(input.ClientID)
		if !ok {
			return invalid()
		}
		if _, duplicate := nodeRefs[clientID]; duplicate {
			return invalid()
		}
		nodeRefs[clientID] = struct{}{}
		kind := strings.TrimSpace(input.Kind)
		name, nameOK := canonicalTeamGraphText(input.CanonicalName, 160)
		domain := strings.TrimSpace(input.Domain)
		if !validTeamGraphNodeKind(kind) || !nameOK || !validTeamGraphDomain(domain) {
			return invalid()
		}
		semanticName := teamGraphECMAScriptNFKCLower(name)
		semanticIdentity := domain + "\x00" + kind + "\x00" + semanticName
		if _, duplicate := semanticNodes[semanticIdentity]; duplicate {
			return invalid()
		}
		semanticNodes[semanticIdentity] = struct{}{}
		summary, ok := canonicalOptionalTeamGraphText(input.Summary, 1200)
		if !ok {
			return invalid()
		}
		aliases, ok := canonicalTeamGraphSet(input.Aliases, 20, func(value string) (string, bool) {
			return canonicalTeamGraphText(value, 160)
		})
		if !ok {
			return invalid()
		}
		salience, ok := canonicalOptionalTeamGraphScore(input.Salience)
		if !ok {
			return invalid()
		}
		emotionalWeight, ok := canonicalOptionalTeamGraphScore(input.EmotionalWeight)
		if !ok {
			return invalid()
		}
		nodes = append(nodes, teamGraphCanonicalNode{
			ClientID: clientID, Kind: kind, CanonicalName: name, Summary: summary,
			Aliases: aliases, Salience: salience, EmotionalWeight: emotionalWeight,
			Domain: domain,
		})
		nodeSemanticKeys[clientID] = teamGraphDigestParts(
			"pulse-team-graph-node-semantic-v1", domain, kind, semanticName,
		)
	}

	edges := make([]teamGraphCanonicalEdge, 0, len(write.Edges))
	edgeKeys := make(map[string]struct{}, len(write.Edges))
	for _, input := range write.Edges {
		from, fromOK := canonicalTeamGraphRef(input.From)
		to, toOK := canonicalTeamGraphRef(input.To)
		kind, kindOK := canonicalTeamGraphSlug(input.Kind)
		if !fromOK || !toOK || !kindOK {
			return invalid()
		}
		if _, exists := nodeRefs[from]; !exists {
			return invalid()
		}
		if _, exists := nodeRefs[to]; !exists {
			return invalid()
		}
		key := from + "\x00" + to + "\x00" + kind
		if _, duplicate := edgeKeys[key]; duplicate {
			return invalid()
		}
		edgeKeys[key] = struct{}{}
		summary, ok := canonicalOptionalTeamGraphText(input.Summary, 1200)
		if !ok {
			return invalid()
		}
		strength, ok := canonicalOptionalTeamGraphScore(input.Strength)
		if !ok {
			return invalid()
		}
		edges = append(edges, teamGraphCanonicalEdge{
			From: from, To: to, Kind: kind, Summary: summary, Strength: strength,
		})
	}

	events := make([]teamGraphCanonicalEvent, 0, len(write.Events))
	eventRefs := make(map[string]struct{}, len(write.Events))
	eventSemanticKeys := make(map[string]string, len(write.Events))
	for _, input := range write.Events {
		clientID, ok := canonicalTeamGraphRef(input.ClientID)
		if !ok {
			return invalid()
		}
		if _, duplicate := eventRefs[clientID]; duplicate {
			return invalid()
		}
		eventRefs[clientID] = struct{}{}
		title, titleOK := canonicalTeamGraphText(input.Title, 180)
		summary, summaryOK := canonicalTeamGraphText(input.Summary, 1200)
		entityRefs, refsOK := canonicalTeamGraphSet(input.EntityRefs, 20, canonicalTeamGraphRef)
		if !titleOK || !summaryOK || !refsOK {
			return invalid()
		}
		for _, ref := range entityRefs {
			if _, exists := nodeRefs[ref]; !exists {
				return invalid()
			}
		}
		sentiment, ok := canonicalOptionalTeamGraphText(input.Sentiment, 240)
		if !ok {
			return invalid()
		}
		emotionalWeight, ok := canonicalOptionalTeamGraphScore(input.EmotionalWeight)
		confidence, confidenceOK := canonicalTeamGraphNumber(input.Confidence, 0, 1)
		if !ok || !confidenceOK {
			return invalid()
		}
		domain := strings.TrimSpace(input.Domain)
		if !validTeamGraphDomain(domain) {
			return invalid()
		}
		occurredAt := source.Timestamp
		if input.OccurredAt != nil {
			occurredAt, _, ok = canonicalTeamMemoryOptionalTime(strings.TrimSpace(*input.OccurredAt))
			if !ok {
				return invalid()
			}
		}
		anchor := false
		if input.Anchor != nil {
			anchor = *input.Anchor
		}
		biometrics, ok := canonicalTeamGraphBiometrics(input.Biometrics)
		if !ok {
			return invalid()
		}
		emotions, ok := canonicalTeamGraphEmotions(input.Emotions)
		if !ok {
			return invalid()
		}
		event := teamGraphCanonicalEvent{
			ClientID: clientID, Title: title, Summary: summary, EntityRefs: entityRefs,
			Sentiment: sentiment, EmotionalWeight: emotionalWeight,
			Confidence: confidence, Domain: domain, OccurredAt: occurredAt,
			Anchor: anchor, Biometrics: biometrics, Emotions: emotions,
		}
		events = append(events, event)
		entitySemanticKeys := make([]string, 0, len(entityRefs))
		for _, ref := range entityRefs {
			entitySemanticKeys = append(entitySemanticKeys, nodeSemanticKeys[ref])
		}
		sort.Strings(entitySemanticKeys)
		semanticPayload, err := marshalTeamGraphCanonical(struct {
			Title           string                        `json:"title"`
			Summary         string                        `json:"summary"`
			Entities        []string                      `json:"entities"`
			Sentiment       *string                       `json:"sentiment,omitempty"`
			EmotionalWeight float64                       `json:"emotional_weight"`
			Confidence      float64                       `json:"confidence"`
			Domain          string                        `json:"domain"`
			OccurredAt      string                        `json:"occurred_at"`
			Anchor          bool                          `json:"anchor"`
			Biometrics      *teamGraphCanonicalBiometrics `json:"biometrics,omitempty"`
			Emotions        teamGraphCanonicalEmotions    `json:"emotions"`
		}{title, summary, entitySemanticKeys, sentiment, emotionalWeight, confidence, domain,
			occurredAt, anchor, biometrics, emotions})
		if err != nil {
			return invalid()
		}
		eventSemanticKeys[clientID] = teamGraphDigestParts(
			"pulse-team-graph-event-semantic-v1", string(semanticPayload),
		)
	}

	facts := make([]teamGraphCanonicalFact, 0, len(write.Facts))
	factKeys := make(map[string]struct{}, len(write.Facts))
	for _, input := range write.Facts {
		node, nodeOK := canonicalTeamGraphRef(input.Node)
		text, textOK := canonicalTeamGraphText(input.Text, 1200)
		if !nodeOK || !textOK {
			return invalid()
		}
		if _, exists := nodeRefs[node]; !exists {
			return invalid()
		}
		domain := strings.TrimSpace(input.Domain)
		confidence, confidenceOK := canonicalTeamGraphNumber(input.Confidence, 0, 1)
		if !validTeamGraphDomain(domain) || !confidenceOK {
			return invalid()
		}
		hasPredicate := input.Predicate != nil
		if hasPredicate != (input.ObjectText != nil) ||
			(!hasPredicate && (input.ValidFrom != nil || input.ChangeCue != nil || input.SourceEventRefs != nil)) {
			return invalid()
		}
		var predicate, objectText, validFrom *string
		var changeCue *bool
		var sourceEventRefs *[]string
		if hasPredicate {
			predicateValue, predicateOK := canonicalTeamGraphText(*input.Predicate, 120)
			objectValue, objectOK := canonicalTeamGraphText(*input.ObjectText, 400)
			if !predicateOK || !objectOK {
				return invalid()
			}
			predicate = &predicateValue
			objectText = &objectValue
			validValue := source.Timestamp
			if input.ValidFrom != nil {
				canonical, _, ok := canonicalTeamMemoryOptionalTime(strings.TrimSpace(*input.ValidFrom))
				if !ok {
					return invalid()
				}
				validValue = canonical
			}
			validFrom = &validValue
			changeValue := false
			if input.ChangeCue != nil {
				changeValue = *input.ChangeCue
			}
			changeCue = &changeValue
			refs, refsOK := canonicalTeamGraphSet(input.SourceEventRefs, 20, canonicalTeamGraphRef)
			if !refsOK {
				return invalid()
			}
			for _, ref := range refs {
				if _, exists := eventRefs[ref]; !exists {
					return invalid()
				}
			}
			sourceEventRefs = &refs
		}
		key := strings.Join([]string{
			node, domain, text, teamGraphOptionalString(predicate),
			teamGraphOptionalString(objectText), teamGraphOptionalString(validFrom),
		}, "\x00")
		if _, duplicate := factKeys[key]; duplicate {
			return invalid()
		}
		factKeys[key] = struct{}{}
		facts = append(facts, teamGraphCanonicalFact{
			Node: node, Text: text, Predicate: predicate, ObjectText: objectText,
			ValidFrom: validFrom, ChangeCue: changeCue, SourceEventRefs: sourceEventRefs,
			Confidence: confidence, Domain: domain,
		})
	}

	continuity, ok := canonicalTeamGraphContinuity(write.Continuity, active, target)
	if !ok {
		return invalid()
	}

	privacy := strings.TrimSpace(write.PrivacyTier)
	retention := strings.TrimSpace(write.Retention)
	var expiresAt *time.Time
	var canonicalExpiry *string
	if write.ExpiresAt != nil {
		canonical, parsed, ok := canonicalTeamMemoryOptionalTime(strings.TrimSpace(*write.ExpiresAt))
		if !ok {
			return invalid()
		}
		canonicalExpiry = &canonical
		expiresAt = &parsed
	}
	body := teamGraphCanonicalBody{
		Schema: TeamGraphDeltaSchema, Source: source, Nodes: nodes, Edges: edges,
		Facts: facts, Events: events, Continuity: continuity, RawInputIncluded: false,
		ActiveContext: active, TargetScope: target, PrivacyTier: privacy,
		Retention: retention, ExpiresAt: canonicalExpiry,
	}
	canonicalBody, err := marshalTeamGraphCanonical(body)
	if err != nil {
		return invalid()
	}
	canonicalWire := make([]byte, 0, len(canonicalBody)+len(idempotencyKey)+24)
	canonicalWire = append(canonicalWire, canonicalBody[:len(canonicalBody)-1]...)
	canonicalWire = append(canonicalWire, []byte(`,"idempotency_key":`)...)
	keyJSON, err := json.Marshal(idempotencyKey)
	if err != nil {
		return invalid()
	}
	canonicalWire = append(canonicalWire, keyJSON...)
	canonicalWire = append(canonicalWire, '}')
	if len(canonicalWire) > maxTeamGraphDeltaBodyBytes {
		return invalid()
	}
	digest := sha256.Sum256(canonicalBody)
	bodyDigest := hex.EncodeToString(digest[:])

	projectionKinds := expectedTeamGraphProjectionKinds(body)
	expiryPolicy := "none"
	if canonicalExpiry != nil {
		expiryPolicy = "at:" + *canonicalExpiry
	} else if permit.EffectiveTarget().Type == teamauth.ScopeSession || retention == "session" {
		// The object writer resolves default_24h against commit time. Two
		// distinct roots therefore receive distinct absolute expiry policies
		// and must not share derivatives. Bind the policy to sealed attribution
		// plus the already-domain-separated key hash, never the raw key.
		attribution := permit.Attribution()
		keyHashHex := idempotencyKeyHash
		if keyHashHex == "" {
			keyHash := sha256.Sum256([]byte("pulse-team-idempotency-key-v1\x00" + idempotencyKey))
			keyHashHex = hex.EncodeToString(keyHash[:])
		}
		expiryPolicy = "default_24h:" + teamGraphDigestParts(
			"pulse-team-default-expiry-policy-v1", attribution.ActorPrincipalID,
			attribution.HumanPrincipalID, attribution.OAuthClientKey,
			keyHashHex,
		)
	}
	policyDigest := teamGraphDigestParts(
		"pulse-team-root-policy-v1", privacy, retention, expiryPolicy,
	)
	descriptors, ok := buildTeamSemanticIntentDescriptors(
		permit, body, nodeSemanticKeys, eventSemanticKeys, policyDigest,
	)
	if !ok {
		return invalid()
	}
	return normalizedTeamGraphDeltaWrite{
		body: body, canonicalBody: canonicalBody, canonicalWire: canonicalWire,
		bodyDigest: bodyDigest, idempotencyKey: idempotencyKey,
		expiresAt: expiresAt, projectionKinds: projectionKinds,
		policyDigest: policyDigest, intentDescriptors: descriptors,
	}, nil
}

func buildTeamSemanticIntentDescriptors(
	permit TeamMutationPermit,
	body teamGraphCanonicalBody,
	nodeSemanticKeys, eventSemanticKeys map[string]string,
	policyDigest string,
) ([]teamSemanticIntentDescriptor, bool) {
	attribution := permit.Attribution()
	target := permit.EffectiveTarget()
	if attribution.StoreID == "" || attribution.TeamID == "" || target.ID == "" {
		return nil, false
	}
	descriptors := make([]teamSemanticIntentDescriptor, 0,
		2*(len(body.Nodes)+len(body.Edges)+len(body.Facts)+len(body.Events))+1)
	add := func(projectionKind, sourceKind string, ordinal int, derivativeKind,
		semanticKey, payloadDigest string) {
		derivativeID := teamGraphOpaqueDigestID("semantic_object",
			"pulse-team-semantic-derivative-v1", attribution.StoreID,
			attribution.TeamID, string(target.Type), target.ID, policyDigest,
			derivativeKind, semanticKey)
		descriptors = append(descriptors, teamSemanticIntentDescriptor{
			ProjectionKind: projectionKind, SourceKind: sourceKind,
			SourceOrdinal: ordinal, DerivativeObjectID: derivativeID,
			DerivativeKind: derivativeKind, SemanticKeyDigest: semanticKey,
			PolicyDigest: policyDigest, PayloadDigest: payloadDigest,
		})
	}
	for ordinal, node := range body.Nodes {
		payload, ok := teamGraphPayloadDigest(node)
		if !ok {
			return nil, false
		}
		semantic := nodeSemanticKeys[node.ClientID]
		add("graph", "node", ordinal, "graph_entity", semantic, payload)
		embeddingParts := []string{
			"pulse-team-embedding-semantic-v1", "node", semantic,
			teamGraphOptionalString(node.Summary), teamGraphFloatString(node.Salience),
			teamGraphFloatString(node.EmotionalWeight),
		}
		embeddingParts = append(embeddingParts, node.Aliases...)
		add("embedding", "node", ordinal, "embedding",
			teamGraphDigestParts(embeddingParts...), payload)
	}
	for ordinal, edge := range body.Edges {
		payload, ok := teamGraphPayloadDigest(edge)
		if !ok {
			return nil, false
		}
		semantic := teamGraphDigestParts("pulse-team-graph-edge-semantic-v1",
			nodeSemanticKeys[edge.From], nodeSemanticKeys[edge.To], edge.Kind)
		add("graph", "edge", ordinal, "graph_relation", semantic, payload)
		add("embedding", "edge", ordinal, "embedding",
			teamGraphDigestParts("pulse-team-embedding-semantic-v1", "edge",
				nodeSemanticKeys[edge.From], nodeSemanticKeys[edge.To], edge.Kind,
				teamGraphOptionalString(edge.Summary), teamGraphFloatString(edge.Strength)), payload)
	}
	for ordinal, fact := range body.Facts {
		payload, ok := teamGraphPayloadDigest(fact)
		if !ok {
			return nil, false
		}
		eventKeys := []string{}
		if fact.SourceEventRefs != nil {
			for _, ref := range *fact.SourceEventRefs {
				eventKeys = append(eventKeys, eventSemanticKeys[ref])
			}
		}
		sort.Strings(eventKeys)
		semanticParts := []string{
			"pulse-team-graph-fact-semantic-v1", nodeSemanticKeys[fact.Node],
			fact.Domain, fact.Text, teamGraphOptionalString(fact.Predicate),
			teamGraphOptionalString(fact.ObjectText), teamGraphOptionalString(fact.ValidFrom),
		}
		semanticParts = append(semanticParts, eventKeys...)
		semantic := teamGraphDigestParts(semanticParts...)
		add("graph", "fact", ordinal, "graph_fact", semantic, payload)
		changeCue := false
		if fact.ChangeCue != nil {
			changeCue = *fact.ChangeCue
		}
		add("embedding", "fact", ordinal, "embedding",
			teamGraphDigestParts("pulse-team-embedding-semantic-v1", "fact", semantic,
				strconv.FormatBool(changeCue), teamGraphFloatString(fact.Confidence)), payload)
		if fact.Predicate != nil {
			// Competing values for the same subject/predicate intentionally
			// converge on one derivative. Object, validity, change cue, and event
			// references remain in the payload/contribution evidence so a reducer
			// can recompute supersession and restore the prior survivor on delete.
			claimSemantic := teamGraphDigestParts(
				"pulse-team-claim-semantic-v1", nodeSemanticKeys[fact.Node],
				teamGraphECMAScriptNFKCLower(*fact.Predicate),
			)
			add("claim", "fact", ordinal, "assertion", claimSemantic, payload)
		}
	}
	for ordinal, event := range body.Events {
		payload, ok := teamGraphPayloadDigest(event)
		if !ok {
			return nil, false
		}
		semantic := eventSemanticKeys[event.ClientID]
		add("graph", "event", ordinal, "graph_event", semantic, payload)
		add("embedding", "event", ordinal, "embedding",
			teamGraphDigestParts("pulse-team-embedding-semantic-v1", "event", semantic), payload)
	}
	if body.Continuity != nil {
		payload, ok := teamGraphPayloadDigest(body.Continuity)
		if !ok {
			return nil, false
		}
		// Checkpoints for one thread/session converge on a recomputable
		// derivative; summary and lists stay content-bearing payload evidence.
		semantic := teamGraphDigestParts("pulse-team-continuity-semantic-v1",
			body.Continuity.ThreadID, body.Continuity.SessionID)
		add("continuity", "continuity", 0, "continuity_checkpoint", semantic, payload)
	}
	sort.Slice(descriptors, func(i, j int) bool {
		left, right := descriptors[i], descriptors[j]
		if left.ProjectionKind != right.ProjectionKind {
			return left.ProjectionKind < right.ProjectionKind
		}
		if left.SourceKind != right.SourceKind {
			return left.SourceKind < right.SourceKind
		}
		return left.SourceOrdinal < right.SourceOrdinal
	})
	return descriptors, true
}

func expectedTeamGraphProjectionKinds(body teamGraphCanonicalBody) []string {
	hasGraph := len(body.Nodes)+len(body.Edges)+len(body.Facts)+len(body.Events) > 0
	kinds := make([]string, 0, 4)
	for _, fact := range body.Facts {
		if fact.Predicate != nil {
			kinds = append(kinds, "claim")
			break
		}
	}
	if body.Continuity != nil {
		kinds = append(kinds, "continuity")
	}
	if hasGraph {
		kinds = append(kinds, "embedding", "graph")
	}
	sort.Strings(kinds)
	return kinds
}

func canonicalTeamGraphSource(source CapsuleSource) (CapsuleSource, bool) {
	host := strings.TrimSpace(source.Host)
	conversationScope := strings.TrimSpace(source.ConversationScope)
	timestamp, _, ok := canonicalTeamMemoryOptionalTime(strings.TrimSpace(source.Timestamp))
	if !ok || !validHost(host) || !validConversationScope(conversationScope) {
		return CapsuleSource{}, false
	}
	return CapsuleSource{Host: host, ConversationScope: conversationScope, Timestamp: timestamp}, true
}

func canonicalTeamGraphActiveContext(input TeamGraphActiveContext) (TeamGraphActiveContext, bool) {
	values := []*string{&input.ProjectID, &input.RepoID, &input.AgentID, &input.SessionID}
	for _, value := range values {
		if *value == "" {
			continue
		}
		canonical, ok := canonicalTeamGraphOpaque(*value, 1, 255)
		if !ok {
			return TeamGraphActiveContext{}, false
		}
		*value = canonical
	}
	return input, true
}

func canonicalTeamGraphTarget(input *TeamGraphTarget) (*TeamGraphTarget, bool) {
	if input == nil {
		return nil, true
	}
	target := *input
	target.Type = teamauth.ScopeType(strings.TrimSpace(string(target.Type)))
	switch target.Type {
	case teamauth.ScopePersonal:
		if target.ID != "" {
			return nil, false
		}
	case teamauth.ScopeProject, teamauth.ScopeRepo, teamauth.ScopeAgent, teamauth.ScopeSession:
		id, ok := canonicalTeamGraphOpaque(target.ID, 1, 255)
		if !ok {
			return nil, false
		}
		target.ID = id
	default:
		return nil, false
	}
	return &target, true
}

func validTeamGraphPermitEnvelope(
	permit TeamMutationPermit,
	active TeamGraphActiveContext,
	target *TeamGraphTarget,
) bool {
	context := permit.context
	if active.ProjectID != context.ProjectID || active.RepoID != context.RepoID ||
		active.AgentID != context.AgentID || active.SessionID != context.SessionID {
		return false
	}
	effective := permit.EffectiveTarget()
	attribution := permit.Attribution()
	if target == nil {
		return permit.requestedScope == nil && attribution.PrincipalKind == teamauth.PrincipalAgent &&
			attribution.HumanPrincipalID != "" && effective.Type == teamauth.ScopePersonal &&
			effective.ID == attribution.HumanPrincipalID &&
			effective.OwnerPrincipalID == attribution.HumanPrincipalID
	}
	if permit.requestedScope == nil || target.Type == teamauth.ScopeTeam ||
		target.Type != permit.requestedScope.Type || target.Type != effective.Type {
		return false
	}
	if target.Type == teamauth.ScopePersonal {
		return target.ID == "" && permit.requestedScope.ID == "" &&
			effective.ID == attribution.HumanPrincipalID &&
			effective.OwnerPrincipalID == attribution.HumanPrincipalID
	}
	return target.ID == permit.requestedScope.ID && target.ID == effective.ID
}

func canonicalTeamGraphContinuity(
	input *TeamGraphContinuity,
	active TeamGraphActiveContext,
	target *TeamGraphTarget,
) (*teamGraphCanonicalContinuity, bool) {
	if input == nil {
		return nil, true
	}
	threadID, threadOK := canonicalTeamGraphOpaque(input.ThreadID, 1, 96)
	sessionID, sessionOK := canonicalTeamGraphOpaque(input.SessionID, 1, 96)
	summary, summaryOK := canonicalTeamGraphText(input.Summary, 1200)
	if !threadOK || !sessionOK || !summaryOK || active.SessionID != sessionID ||
		(target != nil && target.Type == teamauth.ScopeSession && target.ID != sessionID) {
		return nil, false
	}
	fields := [][]string{
		input.Decisions, input.OpenLoops, input.DoNotRepeat, input.EmotionalAnchors,
		input.StateSignals, input.ActiveThreads, input.ReviewInsights,
	}
	canonical := make([][]string, len(fields))
	for index, values := range fields {
		if len(values) > 20 {
			return nil, false
		}
		canonical[index] = make([]string, 0, len(values))
		for _, value := range values {
			clean, ok := canonicalTeamGraphText(value, 1200)
			if !ok {
				return nil, false
			}
			canonical[index] = append(canonical[index], clean)
		}
	}
	return &teamGraphCanonicalContinuity{
		ThreadID: threadID, SessionID: sessionID, Summary: summary,
		Decisions: canonical[0], OpenLoops: canonical[1], DoNotRepeat: canonical[2],
		EmotionalAnchors: canonical[3], StateSignals: canonical[4],
		ActiveThreads: canonical[5], ReviewInsights: canonical[6],
	}, true
}

func canonicalTeamGraphBiometrics(input *TeamGraphBiometrics) (*teamGraphCanonicalBiometrics, bool) {
	if input == nil {
		return nil, true
	}
	result := &teamGraphCanonicalBiometrics{}
	if input.HRV != nil {
		value, ok := canonicalTeamGraphNumber(input.HRV, 0, 300)
		if !ok {
			return nil, false
		}
		result.HRV = &value
	}
	if input.SleepQuality != nil {
		value, ok := canonicalTeamGraphNumber(input.SleepQuality, 0, 1)
		if !ok {
			return nil, false
		}
		result.SleepQuality = &value
	}
	if input.StressProxy != nil {
		value, ok := canonicalTeamGraphNumber(input.StressProxy, 0, 1)
		if !ok {
			return nil, false
		}
		result.StressProxy = &value
	}
	if input.HRTrend != nil {
		value := strings.TrimSpace(*input.HRTrend)
		if value != "rising" && value != "stable" && value != "falling" {
			return nil, false
		}
		result.HRTrend = &value
	}
	if input.HRVTrend != nil {
		value := strings.TrimSpace(*input.HRVTrend)
		if value != "rising" && value != "stable" && value != "falling" {
			return nil, false
		}
		result.HRVTrend = &value
	}
	if input.Workout != nil {
		value := *input.Workout
		result.Workout = &value
	}
	return result, true
}

func canonicalTeamGraphEmotions(input map[string]*float64) (teamGraphCanonicalEmotions, bool) {
	var result teamGraphCanonicalEmotions
	destinations := map[string]**float64{
		"joy": &result.Joy, "sadness": &result.Sadness, "anger": &result.Anger,
		"fear": &result.Fear, "trust": &result.Trust, "disgust": &result.Disgust,
		"anticipation": &result.Anticipation, "surprise": &result.Surprise,
		"shame": &result.Shame, "guilt": &result.Guilt,
	}
	for name, value := range input {
		destination, exists := destinations[name]
		canonical, ok := canonicalTeamGraphNumber(value, 0, 1)
		if !exists || !ok {
			return teamGraphCanonicalEmotions{}, false
		}
		copyValue := canonical
		*destination = &copyValue
	}
	return result, true
}

func canonicalTeamGraphText(value string, maximum int) (string, bool) {
	clean := strings.TrimSpace(value)
	return clean, clean != "" && utf8.ValidString(clean) &&
		utf8.RuneCountInString(clean) <= maximum &&
		!strings.ContainsRune(clean, '\u2028') && !strings.ContainsRune(clean, '\u2029') &&
		!looksUnsafeTeamMemoryText(clean)
}

func canonicalOptionalTeamGraphText(value *string, maximum int) (*string, bool) {
	if value == nil {
		return nil, true
	}
	clean, ok := canonicalTeamGraphText(*value, maximum)
	if !ok {
		return nil, false
	}
	return &clean, true
}

func canonicalTeamGraphOpaque(value string, minimum, maximum int) (string, bool) {
	clean := strings.TrimSpace(value)
	return clean, validTeamOpaque(clean, minimum, maximum) && !looksUnsafeTeamMemoryText(clean)
}

func canonicalTeamGraphRef(value string) (string, bool) {
	clean, ok := canonicalTeamGraphOpaque(value, 2, 96)
	return clean, ok
}

func canonicalTeamGraphSlug(value string) (string, bool) {
	clean, ok := canonicalTeamGraphText(value, 64)
	return clean, ok && validTeamMemoryTag(clean)
}

func canonicalTeamGraphSet(
	input []string,
	maximum int,
	cleaner func(string) (string, bool),
) ([]string, bool) {
	if len(input) > maximum {
		return nil, false
	}
	result := make([]string, 0, len(input))
	for _, value := range input {
		clean, ok := cleaner(value)
		if !ok {
			return nil, false
		}
		result = append(result, clean)
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, false
		}
	}
	return result, true
}

func canonicalOptionalTeamGraphScore(value *float64) (float64, bool) {
	if value == nil {
		return 0, true
	}
	return canonicalTeamGraphNumber(value, 0, 1)
}

func validTeamGraphNumber(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func canonicalTeamGraphNumber(value *float64, minimum, maximum float64) (float64, bool) {
	if value == nil || !validTeamGraphNumber(*value, minimum, maximum) {
		return 0, false
	}
	if *value == 0 {
		return 0, true
	}
	return *value, true
}

func validTeamGraphNodeKind(value string) bool {
	switch value {
	case "person", "place", "project", "org", "product", "community",
		"skill", "concept", "thing", "event_series":
		return true
	default:
		return false
	}
}

func validTeamGraphDomain(value string) bool {
	switch value {
	case "real", "fiction_content", "fiction_meta", "meta_authorial":
		return true
	default:
		return false
	}
}

func validTeamGraphDeltaStorageInput(input teamGraphDeltaStorageInput) bool {
	canonical, ok := canonicalTeamGraphSource(input.Source)
	if !ok || canonical != input.Source || len(input.CanonicalJSON) < 2 ||
		len(input.CanonicalJSON) > maxTeamGraphDeltaBodyBytes || !json.Valid(input.CanonicalJSON) ||
		!lowerHexDigest(input.ContentDigest) {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(input.CanonicalJSON, &object); err != nil || object == nil {
		return false
	}
	digest := sha256.Sum256(input.CanonicalJSON)
	return hex.EncodeToString(digest[:]) == input.ContentDigest
}

func validTeamSemanticProjectionIntent(intent teamSemanticProjectionIntent) bool {
	if !validTeamOpaque(intent.IntentID, 1, 255) ||
		!validTeamOpaque(intent.DerivativeObjectID, 1, 255) ||
		intent.SourceOrdinal < 0 || intent.SourceOrdinal > 49 ||
		!lowerHexDigest(intent.SemanticKeyDigest) || !lowerHexDigest(intent.PolicyDigest) ||
		!lowerHexDigest(intent.PayloadDigest) {
		return false
	}
	switch intent.ProjectionKind {
	case "claim":
		return intent.SourceKind == "fact" && intent.DerivativeKind == "assertion"
	case "continuity":
		return intent.SourceKind == "continuity" && intent.SourceOrdinal == 0 &&
			intent.DerivativeKind == "continuity_checkpoint"
	case "embedding":
		return validTeamGraphProjectionSource(intent.SourceKind) && intent.DerivativeKind == "embedding"
	case "graph":
		switch intent.SourceKind {
		case "node":
			return intent.DerivativeKind == "graph_entity"
		case "edge":
			return intent.DerivativeKind == "graph_relation"
		case "fact":
			return intent.DerivativeKind == "graph_fact"
		case "event":
			return intent.DerivativeKind == "graph_event"
		default:
			return false
		}
	default:
		return false
	}
}

func validTeamGraphProjectionSource(value string) bool {
	return value == "node" || value == "edge" || value == "fact" || value == "event"
}

func teamGraphPayloadDigest(value any) (string, bool) {
	payload, err := marshalTeamGraphCanonical(value)
	if err != nil {
		return "", false
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), true
}

func marshalTeamGraphCanonical(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	bytes := output.Bytes()
	if len(bytes) == 0 || bytes[len(bytes)-1] != '\n' {
		return nil, errors.New("canonical json encoder did not terminate output")
	}
	return append([]byte(nil), bytes[:len(bytes)-1]...), nil
}

func teamGraphOpaqueDigestID(prefix string, parts ...string) string {
	return prefix + "_" + teamGraphDigestParts(parts...)
}

func teamGraphDigestParts(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		teamGraphWriteDigestPart(digest, part)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func teamGraphECMAScriptNFKCLower(value string) string {
	// ECMAScript toLowerCase uses Unicode Default Case Conversion, including
	// multi-rune mappings (İ -> i + combining dot) and contextual final sigma.
	// A Caser can be stateful, so create one per call instead of sharing it.
	return cases.Lower(language.Und, cases.HandleFinalSigma(true)).String(norm.NFKC.String(value))
}

func teamGraphWriteDigestPart(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}

func teamGraphOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func teamGraphFloatString(value float64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}
