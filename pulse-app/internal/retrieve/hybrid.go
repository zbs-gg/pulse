package retrieve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/store"
)

var ErrPersonalMemoryEligibilityChanged = errors.New("personal memory eligibility changed during retrieval")

// Embedder is the minimal interface Engine needs from a vector embedder.
// Production callers pass a *embed.CohereClient; tests pass a fake.
type Embedder interface {
	Embed(ctx context.Context, texts []string, inputType embed.InputType) ([][]float32, error)
	Model() string
}

// Engine is Phase G hybrid retrieval. Loads events, facts, and chains from
// the store into in-memory caches at Init(); per-query it dispatches to
// factual/empathic/chain mode based on the router decision.
//
// Mirrors retrieval_v3.py:RetrievalV3 — empathic mode applies the full v3
// conditional boosts (emotion / state / anchor / date), each gated so a neutral
// signal collapses the score to the v2_pure base (cosine × recency). Go==Python
// parity is asserted by hybrid_parity_test.go.
// Expander is an optional pre-step that turns a free-text query into
// extra related search terms grounded in the user's graph vocabulary.
// When set, terms are appended to the BM25 query so lexical search can
// hit canonical entities the embedding rounds off. Nil = pure (B).
type Expander interface {
	Expand(ctx context.Context, query string) ([]string, error)
}

type Engine struct {
	store    *store.Store
	embedder Embedder
	expander Expander // optional; nil disables query expansion (B+)
	router   *Router

	// mu guards the in-memory indexes: Retrieve takes the read lock, Reload
	// the write lock, so interactive ingest can refresh indexes live.
	mu sync.RWMutex

	// Decay constants (mirrors Python defaults from retrieval_v3.py:444-446).
	decayLambda       float64
	decayLambdaAnchor float64

	// Event index (loaded once from DB).
	eventIDs    []int64
	eventVecs   [][]float32
	eventDays   []float64
	eventAnchor []bool
	eventTexts  []string
	// Plutchik-10 vector per event (10-dim slice; zero-vector when unset).
	eventEmo [][]float32
	// v3 state-boost fields (migration 021): lowercased sentiment label + parsed
	// biometric snapshot per event (nil bio = no biometric signal).
	eventSentLabel []string
	eventBio       []*bioSnapshot
	// Access-frequency salience (migration 029, Phase A): per-event recall count.
	// Loaded for data availability ONLY — the scorer never reads it in this phase,
	// so retrieval scores and Go==Python parity are untouched. Refreshed on Reload.
	eventAccess []int64
	// Event tags (migration 032, JSON array per event; nil = none). Additive
	// slice read ONLY by the state-tag affinity post-step
	// (state_tag_boost.go) — the frozen scorer never touches it.
	eventTags [][]string

	// Fact index.
	factIDs      []int64
	factEventIDs []int64
	factVecs     [][]float32

	// Chain edges (parent → children + child → parents).
	parentToChild map[int64][]int64
	childToParent map[int64][]int64

	embedModel    string
	referenceTime time.Time

	// assertionOverlay, when true, demotes a stale fact-event below its own
	// correction in the final ranked list (supersession-aware). Default false:
	// the list is untouched, so the frozen v3 path stays byte-identical.
	assertionOverlay bool

	// accessFreqEnabled gates the best-effort recall counter (migration 029).
	// Read once at New() from the PULSE_ACCESS_FREQ env var; OFF by default, so
	// no counter writes happen and behavior is unchanged. Instrumentation only —
	// the count never feeds the scorer in this phase.
	accessFreqEnabled bool

	// accessFreqBoostEnabled gates the Phase B access-frequency salience
	// re-rank (see access_boost.go). Read once at New() from the separate
	// PULSE_ACCESS_FREQ_BOOST env var; OFF by default ⇒ the post-scoring
	// step never runs and the final ranking is byte-identical to today.
	accessFreqBoostEnabled bool

	// stateTagBoostEnabled gates the state-tag affinity re-rank (see
	// state_tag_boost.go). Read once at New() from PULSE_STATE_TAG_BOOST;
	// default ON — the post-scoring step is provably neutral without a
	// user_state + state:* tag pair, and =off disables it entirely.
	stateTagBoostEnabled bool
}

// Config bundles the dependencies an Engine needs.
type Config struct {
	Store    *store.Store
	Embedder Embedder
	Expander Expander // optional; nil = no query expansion before BM25
	Router   *Router  // optional; if nil, Engine creates a default one
	// ReferenceTime — if set, days_ago is computed as
	// (ReferenceTime - event.ts) / 24h. Defaults to time.Now() at Init().
	ReferenceTime *time.Time
	// AssertionOverlay enables supersession-aware demotion of stale fact-events
	// in the final ranked list (post-scoring; never alters v3 scores/gating).
	AssertionOverlay bool
}

// New builds an Engine with sensible defaults. Call Init() to load indexes.
func New(cfg Config) *Engine {
	r := cfg.Router
	if r == nil {
		r = NewRouter()
	}
	model := "embed-v4.0"
	if cfg.Embedder != nil {
		model = cfg.Embedder.Model()
	}
	ref := time.Now()
	if cfg.ReferenceTime != nil {
		ref = *cfg.ReferenceTime
	}
	return &Engine{
		store:             cfg.Store,
		embedder:          cfg.Embedder,
		expander:          cfg.Expander,
		router:            r,
		decayLambda:       0.002,
		decayLambdaAnchor: 0.001,
		embedModel:        model,
		referenceTime:     ref,
		parentToChild:     make(map[int64][]int64),
		childToParent:     make(map[int64][]int64),
		assertionOverlay:  cfg.AssertionOverlay,
		accessFreqEnabled: accessFreqFlag(),

		accessFreqBoostEnabled: accessFreqBoostFlag(),
		stateTagBoostEnabled:   stateTagBoostFlag(),
	}
}

// accessFreqFlag reports whether the best-effort recall counter (migration 029)
// is enabled. OFF unless PULSE_ACCESS_FREQ is set to a truthy value ("1"/"true"/
// "yes"/"on"). Default OFF ⇒ no counter writes ⇒ no behavior change.
func accessFreqFlag() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PULSE_ACCESS_FREQ"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Init loads events + their embeddings + emotions + chains + facts + their
// embeddings into memory. Idempotent — call again after re-ingestion.
func (e *Engine) Init(ctx context.Context) error {
	if e.store == nil {
		return fmt.Errorf("retrieve: store is nil")
	}
	if err := e.loadEvents(ctx); err != nil {
		return fmt.Errorf("retrieve init events: %w", err)
	}
	if err := e.loadFacts(ctx); err != nil {
		return fmt.Errorf("retrieve init facts: %w", err)
	}
	if err := e.loadChains(ctx); err != nil {
		return fmt.Errorf("retrieve init chains: %w", err)
	}
	return nil
}

// Reload re-reads the in-memory indexes under the write lock so events
// ingested after startup become retrievable without a daemon restart.
func (e *Engine) Reload(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Init(ctx)
}

// EmbedderReady reports whether full retrieval can run (an embedder is wired).
func (e *Engine) EmbedderReady() bool {
	if e.embedder == nil {
		return false
	}
	if live, ok := e.embedder.(interface{ Ready() bool }); ok {
		return live.Ready()
	}
	return true
}

// EmbedderModel names the embedding model the index is built with.
func (e *Engine) EmbedderModel() string {
	return e.embedModel
}

// IndexEventDoc is one freshly ingested event to embed and index.
type IndexEventDoc struct {
	EventID int64
	Text    string
}

// EmbedAndPersistEvents embeds event documents and durably writes their
// vectors without reloading the in-memory index. Backfill callers use this to
// persist multiple bounded pages, then perform one deliberate Reload.
func (e *Engine) EmbedAndPersistEvents(ctx context.Context, docs []IndexEventDoc) (int, error) {
	if e.embedder == nil {
		return 0, fmt.Errorf("retrieve index: embedder is nil")
	}
	if len(docs) == 0 {
		return 0, nil
	}
	persisted := 0
	for start := 0; start < len(docs); start += embed.MaxBatchSize {
		end := min(start+embed.MaxBatchSize, len(docs))
		batch := docs[start:end]
		texts := make([]string, len(batch))
		for i, doc := range batch {
			texts[i] = doc.Text
		}
		vecs, err := e.embedder.Embed(ctx, texts, embed.TypeSearchDocument)
		if err != nil {
			return persisted, fmt.Errorf("retrieve index embed: %w", err)
		}
		if len(vecs) != len(batch) {
			return persisted, fmt.Errorf("retrieve index: embedder returned %d vectors for %d docs", len(vecs), len(batch))
		}
		for i, doc := range batch {
			raw, err := json.Marshal(vecs[i])
			if err != nil {
				return persisted, fmt.Errorf("retrieve index marshal vector: %w", err)
			}
			if _, err := e.store.DB().ExecContext(ctx, `
			INSERT INTO event_embeddings (event_id, model, dim, vector_json, text_source)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(event_id) DO UPDATE SET
			  model = excluded.model,
			  dim = excluded.dim,
			  vector_json = excluded.vector_json,
			  text_source = excluded.text_source`,
				doc.EventID, e.embedModel, len(vecs[i]), string(raw), doc.Text); err != nil {
				return persisted, fmt.Errorf("retrieve index upsert event %d: %w", doc.EventID, err)
			}
			persisted++
		}
	}
	return persisted, nil
}

// EmbedAndIndexEvents embeds freshly ingested events as search documents,
// writes them into event_embeddings, and reloads the in-memory index. This is
// what makes interactive ingest (/graph/delta) retrievable immediately.
func (e *Engine) EmbedAndIndexEvents(ctx context.Context, docs []IndexEventDoc) error {
	persisted, persistErr := e.EmbedAndPersistEvents(ctx, docs)
	if persisted > 0 {
		if reloadErr := e.Reload(ctx); reloadErr != nil && persistErr == nil {
			return reloadErr
		}
	}
	return persistErr
}

// EmbedAndRefreshEvents embeds a small interactive write and refreshes only
// those event rows in memory. Personal archives can contain tens of thousands
// of legacy vectors, so a full Reload on every new memory would make the write
// wait for the whole archive to be decoded again.
func (e *Engine) EmbedAndRefreshEvents(ctx context.Context, docs []IndexEventDoc) error {
	persisted, persistErr := e.EmbedAndPersistEvents(ctx, docs)
	if persisted > 0 {
		ids := make([]int64, 0, persisted)
		for _, doc := range docs[:persisted] {
			ids = append(ids, doc.EventID)
		}
		if refreshErr := e.RefreshEvents(ctx, ids); refreshErr != nil && persistErr == nil {
			return refreshErr
		}
	}
	return persistErr
}

type indexedEventRow struct {
	id        int64
	vec       []float32
	days      float64
	anchor    bool
	text      string
	emotion   []float32
	sentLabel string
	bio       *bioSnapshot
	access    int64
	tags      []string
}

// RefreshEvents replaces or inserts only the requested events in the global
// live index. Personal eligibility is applied per request, never while writing
// or loading the shared index.
func (e *Engine) RefreshEvents(ctx context.Context, eventIDs []int64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(eventIDs)), ",")
	q := `
SELECT
    e.id, e.ts, e.title, COALESCE(e.description, ''), ee.vector_json,
    COALESCE(em.joy, 0), COALESCE(em.sadness, 0), COALESCE(em.anger, 0),
    COALESCE(em.fear, 0), COALESCE(em.trust, 0), COALESCE(em.disgust, 0),
    COALESCE(em.anticipation, 0), COALESCE(em.surprise, 0),
    COALESCE(em.shame, 0), COALESCE(em.guilt, 0),
    COALESCE(e.user_flag, 0), COALESCE(e.sentiment_label, ''), COALESCE(e.biometric_json, ''),
    COALESCE(e.access_count, 0), COALESCE(e.tags, '')
FROM events e
JOIN event_embeddings ee ON ee.event_id=e.id
LEFT JOIN event_emotions em ON em.event_id=e.id
WHERE (ee.model=? OR (?='bge-m3' AND ee.model='bge-m3-mlx-fp16'))
  AND e.id IN (` + placeholders + `)
ORDER BY e.id`
	args := []any{e.embedModel, e.embedModel}
	for _, id := range eventIDs {
		args = append(args, id)
	}
	rows, err := e.store.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("retrieve refresh events: %w", err)
	}
	defer rows.Close()
	refreshed := make([]indexedEventRow, 0, len(eventIDs))
	for rows.Next() {
		var row indexedEventRow
		var ts, title, description, vectorJSON, bioJSON, tagsJSON string
		var userFlag int
		row.emotion = make([]float32, 10)
		if err := rows.Scan(
			&row.id, &ts, &title, &description, &vectorJSON,
			&row.emotion[0], &row.emotion[1], &row.emotion[2], &row.emotion[3], &row.emotion[4],
			&row.emotion[5], &row.emotion[6], &row.emotion[7], &row.emotion[8], &row.emotion[9],
			&userFlag, &row.sentLabel, &bioJSON, &row.access, &tagsJSON,
		); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(vectorJSON), &row.vec); err != nil {
			return fmt.Errorf("event %d: parse vector_json: %w", row.id, err)
		}
		row.days = computeDaysAgo(ts, e.referenceTime)
		row.anchor = userFlag == 1
		row.text = strings.ToLower(title + " " + description)
		row.sentLabel = strings.ToLower(row.sentLabel)
		row.bio = parseBioSnapshot(bioJSON)
		if tagsJSON != "" {
			_ = json.Unmarshal([]byte(tagsJSON), &row.tags)
		}
		refreshed = append(refreshed, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, row := range refreshed {
		index := sort.Search(len(e.eventIDs), func(i int) bool { return e.eventIDs[i] >= row.id })
		if index < len(e.eventIDs) && e.eventIDs[index] == row.id {
			e.eventVecs[index], e.eventDays[index], e.eventAnchor[index] = row.vec, row.days, row.anchor
			e.eventTexts[index], e.eventEmo[index], e.eventSentLabel[index] = row.text, row.emotion, row.sentLabel
			e.eventBio[index], e.eventAccess[index], e.eventTags[index] = row.bio, row.access, row.tags
			continue
		}
		e.eventIDs = insertIndexedValue(e.eventIDs, index, row.id)
		e.eventVecs = insertIndexedValue(e.eventVecs, index, row.vec)
		e.eventDays = insertIndexedValue(e.eventDays, index, row.days)
		e.eventAnchor = insertIndexedValue(e.eventAnchor, index, row.anchor)
		e.eventTexts = insertIndexedValue(e.eventTexts, index, row.text)
		e.eventEmo = insertIndexedValue(e.eventEmo, index, row.emotion)
		e.eventSentLabel = insertIndexedValue(e.eventSentLabel, index, row.sentLabel)
		e.eventBio = insertIndexedValue(e.eventBio, index, row.bio)
		e.eventAccess = insertIndexedValue(e.eventAccess, index, row.access)
		e.eventTags = insertIndexedValue(e.eventTags, index, row.tags)
	}
	return nil
}

func insertIndexedValue[T any](values []T, index int, value T) []T {
	values = append(values, value)
	copy(values[index+1:], values[index:len(values)-1])
	values[index] = value
	return values
}

// loadEvents pulls (id, ts, embedding, emotion_vec) per event.
// Joins events ⨝ event_embeddings ⨝ event_emotions. Events without embedding
// are skipped (can't be retrieved by cosine). Events without emotion get
// zero-vector (no emotion boost).
//
// days_ago is computed at load time as (referenceTime - ts) / 24h.
// user_flag (anchor), sentiment_label, and biometric_json come from migration
// 021 and feed the state / anchor / date boosts.
const personalEligibleEventsCTE = `
WITH eligible_objects AS (
    SELECT object_id
      FROM private_memory_objects
     WHERE lifecycle='active'
       AND (
           memory_scope='personal_global' OR
           (memory_scope='project' AND project_namespace_id=?)
       )
),
eligible_events(event_id) AS (
    SELECT capsule.event_id
      FROM eligible_objects object
      JOIN memory_capsules capsule ON capsule.id=object.object_id
     WHERE capsule.event_id IS NOT NULL
    UNION
    SELECT CAST(projection.row_ref AS INTEGER)
      FROM eligible_objects object
      JOIN private_semantic_projection_rows projection
        ON projection.object_id=object.object_id
       AND projection.row_kind='event'
)`

func (e *Engine) loadEvents(ctx context.Context) error {
	q := `
SELECT
    e.id,
    e.ts,
    e.title,
    COALESCE(e.description, ''),
    ee.vector_json,
    COALESCE(em.joy, 0), COALESCE(em.sadness, 0), COALESCE(em.anger, 0),
    COALESCE(em.fear, 0), COALESCE(em.trust, 0), COALESCE(em.disgust, 0),
    COALESCE(em.anticipation, 0), COALESCE(em.surprise, 0),
    COALESCE(em.shame, 0), COALESCE(em.guilt, 0),
    COALESCE(e.user_flag, 0), COALESCE(e.sentiment_label, ''), COALESCE(e.biometric_json, ''),
    COALESCE(e.access_count, 0), COALESCE(e.tags, '')
FROM events e
JOIN event_embeddings ee ON ee.event_id = e.id
LEFT JOIN event_emotions em ON em.event_id = e.id
WHERE ee.model = ? OR (?='bge-m3' AND ee.model='bge-m3-mlx-fp16')
ORDER BY e.id`
	args := []any{e.embedModel, e.embedModel}
	rows, err := e.store.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	e.eventIDs = e.eventIDs[:0]
	e.eventVecs = e.eventVecs[:0]
	e.eventDays = e.eventDays[:0]
	e.eventAnchor = e.eventAnchor[:0]
	e.eventTexts = e.eventTexts[:0]
	e.eventEmo = e.eventEmo[:0]
	e.eventSentLabel = e.eventSentLabel[:0]
	e.eventBio = e.eventBio[:0]
	e.eventAccess = e.eventAccess[:0]
	e.eventTags = e.eventTags[:0]

	for rows.Next() {
		var id int64
		var tsStr string
		var title string
		var description string
		var vecJSON string
		em := make([]float32, 10)
		var userFlag int
		var sentLabel, bioJSON string
		var accessCount int64
		var tagsJSON string
		if err := rows.Scan(&id, &tsStr, &title, &description, &vecJSON,
			&em[0], &em[1], &em[2], &em[3], &em[4],
			&em[5], &em[6], &em[7], &em[8], &em[9],
			&userFlag, &sentLabel, &bioJSON, &accessCount, &tagsJSON); err != nil {
			return err
		}
		var v []float32
		if err := json.Unmarshal([]byte(vecJSON), &v); err != nil {
			return fmt.Errorf("event %d: parse vector_json: %w", id, err)
		}
		days := computeDaysAgo(tsStr, e.referenceTime)
		e.eventIDs = append(e.eventIDs, id)
		e.eventVecs = append(e.eventVecs, v)
		e.eventDays = append(e.eventDays, days)
		e.eventAnchor = append(e.eventAnchor, userFlag == 1)
		e.eventTexts = append(e.eventTexts, strings.ToLower(title+" "+description))
		e.eventEmo = append(e.eventEmo, em)
		e.eventSentLabel = append(e.eventSentLabel, strings.ToLower(sentLabel))
		e.eventBio = append(e.eventBio, parseBioSnapshot(bioJSON))
		e.eventAccess = append(e.eventAccess, accessCount)
		// Tags are best-effort: unparsable/empty JSON means "no tags" (nil),
		// which keeps the state-tag post-step neutral for this event.
		var tags []string
		if tagsJSON != "" {
			_ = json.Unmarshal([]byte(tagsJSON), &tags)
		}
		e.eventTags = append(e.eventTags, tags)
	}
	return rows.Err()
}

// computeDaysAgo parses a timestamp (ISO8601 / RFC3339) and returns the
// distance to ref in fractional days. Falls back to 0 on parse failure.
func computeDaysAgo(tsStr string, ref time.Time) float64 {
	if tsStr == "" {
		return 0
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05Z", "2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, tsStr); err == nil {
			d := ref.Sub(t).Hours() / 24.0
			if d < 0 {
				return 0
			}
			return d
		}
	}
	return 0
}

func (e *Engine) loadFacts(ctx context.Context) error {
	q := `
SELECT f.id, f.event_id, fe.vector_json
FROM atomic_facts f
JOIN atomic_fact_embeddings fe ON fe.fact_id = f.id
WHERE fe.model = ?
ORDER BY f.id`
	args := []any{e.embedModel}
	rows, err := e.store.DB().QueryContext(ctx, q, args...)
	if err != nil {
		// Phase G migration may not be applied yet — treat empty as fine
		if isMissingTable(err) {
			return nil
		}
		return err
	}
	defer rows.Close()

	e.factIDs = e.factIDs[:0]
	e.factEventIDs = e.factEventIDs[:0]
	e.factVecs = e.factVecs[:0]

	for rows.Next() {
		var fid, eid int64
		var vecJSON string
		if err := rows.Scan(&fid, &eid, &vecJSON); err != nil {
			return err
		}
		var v []float32
		if err := json.Unmarshal([]byte(vecJSON), &v); err != nil {
			return fmt.Errorf("fact %d: parse vector_json: %w", fid, err)
		}
		e.factIDs = append(e.factIDs, fid)
		e.factEventIDs = append(e.factEventIDs, eid)
		e.factVecs = append(e.factVecs, v)
	}
	return rows.Err()
}

func (e *Engine) loadChains(ctx context.Context) error {
	q := `SELECT parent_id, child_id FROM event_chains`
	rows, err := e.store.DB().QueryContext(ctx, q)
	if err != nil {
		if isMissingTable(err) {
			return nil
		}
		return err
	}
	defer rows.Close()

	e.parentToChild = make(map[int64][]int64)
	e.childToParent = make(map[int64][]int64)
	for rows.Next() {
		var p, c int64
		if err := rows.Scan(&p, &c); err != nil {
			return err
		}
		e.parentToChild[p] = append(e.parentToChild[p], c)
		e.childToParent[c] = append(e.childToParent[c], p)
	}
	return rows.Err()
}

func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, marker := range []string{"no such table", "doesn't exist"} {
		if containsCI(s, marker) {
			return true
		}
	}
	return false
}

func containsCI(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a := s[i+j]
			b := sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// RetrieveRequest is the input to Retrieve().
type RetrieveRequest struct {
	Query     string
	Mode      QueryMode  // ModeAuto = router decides
	UserState *UserState // nullable
	TopK      int        // default 5 if zero
	// PersonalScope is derived server-side from a verified workspace binding.
	// Nil preserves Local Preview's historical unscoped retrieval.
	PersonalScope *store.PersonalMemoryScopeSnapshot
	// CapsuleFirst is used by automatic prompt memory. It avoids the global
	// archive BM25 expansion and instead requires lexical corroboration of the
	// already scope-fenced dense archive candidates when no capsule is ready.
	CapsuleFirst bool
	// GraphMode enables temporal entity-graph retrieval as an EXTRA RRF input
	// (graph-as-recall-injector). "" = OFF (default; behaviour unchanged).
	// "anchored" = entity-anchored events; "walk" = + typed relation walk.
	GraphMode string
}

// RetrieveResponse is the output: ranked event IDs + chosen mode + router trace.
type RetrieveResponse struct {
	EventIDs             []int64
	ModeUsed             QueryMode
	RouterDecision       RouteDecision
	EmotionRole          EmotionRoleDecision
	SurfaceabilityAction SurfaceabilityAction
	ScoreBreakdowns      map[int64]ScoreBreakdown
	CandidateEvidence    map[int64]CandidateEvidence
}

// CandidateEvidence lets the host compositor distinguish a short, curated
// memory capsule from a projected archive event without exposing any content.
type CandidateEvidence struct {
	Dense         bool `json:"dense"`
	Lexical       bool `json:"lexical"`
	DirectCapsule bool `json:"direct_capsule"`
}

func eligibleContains(eligible map[int64]struct{}, id int64) bool {
	if eligible == nil {
		return true
	}
	_, ok := eligible[id]
	return ok
}

func (e *Engine) personalEligibleEventSet(
	ctx context.Context,
	scope *store.PersonalMemoryScopeSnapshot,
) (map[int64]struct{}, error) {
	if scope == nil {
		return nil, nil
	}
	rows, err := e.store.DB().QueryContext(ctx, personalEligibleEventsCTE+`
SELECT event_id FROM eligible_events ORDER BY event_id`, scope.ProjectNamespaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	eligible := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		eligible[id] = struct{}{}
	}
	return eligible, rows.Err()
}

func (e *Engine) directCapsuleEventSet(ctx context.Context, ids []int64) map[int64]struct{} {
	if e.store == nil || len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := e.store.DB().QueryContext(ctx, `
SELECT DISTINCT event_id
  FROM memory_capsules
 WHERE status='active' AND event_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out[id] = struct{}{}
		}
	}
	return out
}

func (e *Engine) eligibleDirectCapsuleEventSet(
	ctx context.Context,
	scope *store.PersonalMemoryScopeSnapshot,
) (map[int64]struct{}, error) {
	out := make(map[int64]struct{})
	if e.store == nil {
		return out, nil
	}
	query := `
SELECT DISTINCT event_id
  FROM memory_capsules
 WHERE status='active' AND event_id IS NOT NULL`
	args := []any{}
	if scope != nil {
		query = `
SELECT DISTINCT capsule.event_id
  FROM memory_capsules capsule
  JOIN private_memory_objects object ON object.object_id=capsule.id
 WHERE capsule.status='active'
   AND capsule.event_id IS NOT NULL
   AND object.lifecycle='active'
   AND (
       object.memory_scope='personal_global' OR
       (object.memory_scope='project' AND object.project_namespace_id=?)
   )`
		args = append(args, scope.ProjectNamespaceID)
	}
	rows, err := e.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func preferCandidateIDs(preferred, fallback []int64, limit int) []int64 {
	if limit <= 0 {
		return nil
	}
	out := make([]int64, 0, limit)
	seen := make(map[int64]struct{}, limit)
	for _, candidates := range [][]int64{preferred, fallback} {
		for _, id := range candidates {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
			if len(out) == limit {
				return out
			}
		}
	}
	return out
}

func samePersonalScope(a, b *store.PersonalMemoryScopeSnapshot) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (e *Engine) currentPersonalScope(scope *store.PersonalMemoryScopeSnapshot) (*store.PersonalMemoryScopeSnapshot, error) {
	if scope == nil {
		return nil, nil
	}
	current, err := e.store.PersonalMemoryScopeSnapshotForBinding(scope.BindingDigest, scope.RepositoryID)
	if err != nil {
		return nil, err
	}
	return &current, nil
}

type ScoreBreakdown struct {
	Cosine               float64  `json:"cosine"`
	Recency              float64  `json:"recency"`
	ShadowThemeBoost     *float64 `json:"shadow_theme_boost,omitempty"`
	CuriositySignalBoost *float64 `json:"curiosity_signal_boost,omitempty"`
	ActiveTriggerBoost   *float64 `json:"active_trigger_boost,omitempty"`
	EmotionBoost         *float64 `json:"emotion_boost,omitempty"` // v3 boost_emo (1 + β·max(align,0))
	StateBoost           *float64 `json:"state_boost,omitempty"`   // v3 boost_state (1 + γ·state_fit)
	AnchorBoost          *float64 `json:"anchor_boost,omitempty"`  // v3 boost_anchor (1 + δ_a, anchors in top-N by base)
	DateBoost            *float64 `json:"date_boost,omitempty"`    // v3 boost_date (1 + δ_d·date_proximity)
	// AccessFreqBoost is NOT part of the frozen v3 score. It documents the
	// Phase B post-scoring re-rank multiplier (access_boost.go) and is set
	// only when PULSE_ACCESS_FREQ_BOOST is on and the multiplier ≠ 1.0.
	AccessFreqBoost *float64 `json:"access_freq_boost,omitempty"`
	// StateTagBoost is NOT part of the frozen v3 score. It documents the
	// state-tag affinity post-scoring re-rank multiplier (state_tag_boost.go)
	// and is set only when the step fired and the multiplier ≠ 1.0.
	StateTagBoost *float64 `json:"state_tag_boost,omitempty"`
}

// Retrieve dispatches the query to factual/empathic/chain mode.
//
// Empathic mode applies the full v3 conditional boosts (emotion / state /
// anchor / date) on the v2_pure base; chain mode expands via the chain graph;
// the hybrid layer fuses BM25 via RRF.
func (e *Engine) Retrieve(ctx context.Context, req RetrieveRequest) (*RetrieveResponse, error) {
	return e.retrieve(ctx, req)
}

func (e *Engine) retrieve(ctx context.Context, req RetrieveRequest) (*RetrieveResponse, error) {
	if e.embedder == nil {
		return nil, fmt.Errorf("retrieve: embedder is nil")
	}
	if req.Query == "" {
		return nil, fmt.Errorf("retrieve: empty query")
	}
	eligible, err := e.personalEligibleEventSet(ctx, req.PersonalScope)
	if err != nil {
		return nil, fmt.Errorf("retrieve personal eligibility: %w", err)
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}
	emotionRole := InferEmotionRole(req.Query, req.UserState)

	mode := req.Mode
	var decision RouteDecision
	if mode == "" || mode == ModeAuto {
		decision = e.router.Classify(ctx, req.Query, req.UserState)
		mode = decision.Mode
	} else {
		decision = RouteDecision{Mode: mode, Confidence: 1.0, Classifier: "forced"}
	}

	denseQuery := req.Query
	if req.CapsuleFirst {
		denseQuery = promptDenseQuery(req.Query)
	}
	qVec, err := e.embedQuery(ctx, denseQuery)
	if err != nil {
		return nil, fmt.Errorf("retrieve embed: %w", err)
	}
	directCapsuleEligible, err := e.eligibleDirectCapsuleEventSet(ctx, req.PersonalScope)
	if err != nil {
		return nil, fmt.Errorf("retrieve direct capsules: %w", err)
	}
	directCandidateLimit := topK * 4
	if directCandidateLimit < 16 {
		directCandidateLimit = 16
	}
	// Forty-eight candidates give the final ranker twice as much room as the
	// largest prompt result. A wider shortlist did not improve full-memory recall
	// and only raised latency, so keep the automatic path bounded here.
	if directCandidateLimit > 48 {
		directCandidateLimit = 48
	}
	var directLexicalIDs []int64
	if e.store != nil {
		directLexicalIDs, _ = BM25CapsuleSearchScoped(
			ctx, e.store.DB(), req.Query, directCandidateLimit, req.PersonalScope,
		)
	}

	e.mu.RLock()
	directCapsuleLimit := topK
	if directCapsuleLimit > 48 {
		directCapsuleLimit = 48
	}
	directDenseIDs := e.retrieveFactualEventsEligible(qVec, directCandidateLimit, directCapsuleEligible)
	directByID := make(map[int64]promptCapsuleCandidate, len(directDenseIDs)+len(directLexicalIDs))
	for _, id := range directDenseIDs {
		index := sort.Search(len(e.eventIDs), func(i int) bool { return e.eventIDs[i] >= id })
		if index < len(e.eventIDs) && e.eventIDs[index] == id {
			directByID[id] = promptCapsuleCandidate{
				id: id, text: e.eventTexts[index], cosine: float64(dotF32(qVec, e.eventVecs[index])), daysAgo: e.eventDays[index],
			}
		}
	}
	for rank, id := range directLexicalIDs {
		candidate, exists := directByID[id]
		if !exists {
			index := sort.Search(len(e.eventIDs), func(i int) bool { return e.eventIDs[i] >= id })
			if index >= len(e.eventIDs) || e.eventIDs[index] != id {
				continue
			}
			candidate = promptCapsuleCandidate{
				id: id, text: e.eventTexts[index], cosine: float64(dotF32(qVec, e.eventVecs[index])), daysAgo: e.eventDays[index],
			}
		}
		candidate.lexicalRank = rank + 1
		directByID[id] = candidate
	}
	directCandidates := make([]promptCapsuleCandidate, 0, len(directByID))
	promptSupportedDirect := make(map[int64]struct{}, len(directByID))
	presupposedEventTerms := promptPresupposedEventTerms(req.Query)
	for _, candidate := range directByID {
		if req.CapsuleFirst && !promptCapsuleSupportsPresupposedEvent(presupposedEventTerms, candidate.text) {
			continue
		}
		directCandidates = append(directCandidates, candidate)
		promptSupportedDirect[candidate.id] = struct{}{}
	}
	directCapsuleIDs := rankPromptCapsules(req.Query, directCandidates, directCapsuleLimit)
	directCapsuleReady := false
	for _, candidate := range directCandidates {
		if promptCapsuleAccepted(candidate) {
			directCapsuleReady = true
			break
		}
	}

	var cosineIDs []int64
	var breakdowns map[int64]ScoreBreakdown
	if req.CapsuleFirst && directCapsuleReady {
		// Automatic prompt memory never mixes archive rows into an accepted
		// capsule result. Reuse the already scope-fenced dense shortlist instead
		// of scanning the same event vectors a second time for an archive ranking
		// that will be discarded below.
		cosineIDs = directDenseIDs
	} else {
		switch mode {
		case ModeFactual:
			cosineIDs = e.retrieveFactualEligible(qVec, topK, eligible)
		case ModeChain:
			cosineIDs = e.retrieveChainEligible(qVec, req.Query, topK, req.UserState, eligible)
		default: // empathic + unknown
			cosineIDs, breakdowns = e.retrieveEmpathicScoredEligible(qVec, req.Query, topK, req.UserState, eligible)
		}
	}
	denseSet := make(map[int64]struct{}, len(cosineIDs))
	for _, id := range cosineIDs {
		denseSet[id] = struct{}{}
	}
	for _, id := range directDenseIDs {
		denseSet[id] = struct{}{}
	}

	// Hybrid: pull a parallel BM25 ranking over events_fts / facts_fts /
	// entities_fts and fuse with the cosine result via RRF (k=60). Lexical
	// catches what the embedding rounds off — esp. canonical terms and
	// entity names. If the FTS5 layer isn't available (no migration yet,
	// no DB) we fall through to pure cosine — never fail the request.
	//
	// (B+) Optional grounded query expansion: if an Expander is wired,
	// ask it for related canonical terms from the graph and append them
	// to the BM25 query string. So a query «project deadline» might become
	// «project deadline | milestone | release plan | sprint» and BM25
	// hits events that don't share the original word but match known
	// canon. Expansion failures are non-fatal — we keep the raw query.
	ids := cosineIDs
	// Factual mode: PURE cosine ranking (no BM25/RRF fusion, no empathic
	// surfaceability) — isolate the semantic ranker, like a dedicated fact store.
	// Empathic/chain keep the full hybrid fusion + surfaceability.
	lexicalSet := map[int64]struct{}{}
	for _, id := range directLexicalIDs {
		lexicalSet[id] = struct{}{}
	}
	// A relevant active capsule is the primary prompt-memory source and is never
	// mixed with archive rows. Non-prompt callers retain the global archive BM25
	// fallback when no capsule clears the host gate. Capsule-first callers use
	// the scope-fenced dense candidates plus cheap lexical corroboration below.
	if mode != ModeFactual && e.store != nil && !directCapsuleReady && !req.CapsuleFirst {
		bm25Query := req.Query
		// The legacy local expander snapshots the whole graph vocabulary at
		// helper startup and cannot accept a per-project eligibility fence.
		// Disable it for scoped Personal retrieval until its transport carries
		// the exact namespace/revision; letting foreign vocabulary rewrite the
		// query would violate pre-influence isolation even if BM25 were scoped.
		if e.expander != nil && req.PersonalScope == nil {
			if extras, err := e.expander.Expand(ctx, req.Query); err == nil && len(extras) > 0 {
				bm25Query = req.Query + " " + strings.Join(extras, " ")
			}
		}
		bm25IDs, err := BM25SearchScoped(ctx, e.store.DB(), bm25Query, topK*2, req.PersonalScope)
		for _, id := range bm25IDs {
			lexicalSet[id] = struct{}{}
		}
		lists := [][]int64{cosineIDs}
		if err == nil && len(bm25IDs) > 0 {
			lists = append(lists, bm25IDs)
		}
		// Graph-as-recall-injector: default-OFF extra RRF input (entity-anchored
		// / typed relation walk). Salience ranking is preserved — graph only adds
		// candidates the cosine/BM25 paths miss (multi-hop, entity-centric).
		if req.GraphMode != "" {
			if gids := e.retrieveGraphCandidatesScoped(ctx, req.Query, req.GraphMode, topK, req.PersonalScope); len(gids) > 0 {
				lists = append(lists, gids)
			}
		}
		if len(lists) > 1 {
			ids = RRFFuse(lists, topK, 60)
		}
	}
	// Curated capsules are the primary prompt-memory source. Put their bounded
	// semantic ranking before archive events; the host applies the final byte
	// budget and relevance threshold, then falls back to corroborated archive
	// rows only when no capsule clears it.
	ids = preferCandidateIDs(directCapsuleIDs, ids, topK)
	if req.CapsuleFirst && len(presupposedEventTerms) > 0 {
		filtered := make([]int64, 0, len(ids))
		for _, id := range ids {
			if _, direct := directCapsuleEligible[id]; direct {
				if _, supported := promptSupportedDirect[id]; !supported {
					continue
				}
			}
			filtered = append(filtered, id)
		}
		ids = filtered
	}
	// A rare literal name can be buried below generic question words in the
	// global BM25 window. Corroborate the already scope-fenced dense candidates
	// against their own FTS rows in every mode. Factual mode keeps its pure
	// cosine ranking; this adds evidence only and never widens eligibility.
	if e.store != nil {
		if corroborated, corroborationErr := FTSCorroborateCandidates(
			ctx, e.store.DB(), req.Query, cosineIDs,
		); corroborationErr == nil {
			for _, id := range corroborated {
				lexicalSet[id] = struct{}{}
			}
		}
	}
	var surfaceability SurfaceabilityAction
	if mode != ModeFactual {
		ids, surfaceability = e.applyFragileSurfaceabilityEligible(ids, topK, emotionRole, eligible)
	}
	// Legacy empathic surfaceability may reorder archive events, but automatic
	// prompt memory has already ranked active capsules for the actual question.
	// Reassert that bounded capsule order so a word such as "state" cannot make
	// generic emotional salience bury a concrete geographic answer.
	if req.CapsuleFirst {
		ids = preferCandidateIDs(directCapsuleIDs, ids, topK)
	}

	// Access-frequency salience re-rank (Phase B, default-off, post-scoring):
	// a mild, bounded reorder of the final id list by recall frequency (see
	// access_boost.go). Placed AFTER fusion + surfaceability so it sees the
	// complete final candidate list (graph-injected candidates included) and
	// the fragile-state safety substitution keeps operating on the untouched
	// frozen ranking; placed BEFORE assertion demotion so the supersession
	// invariant (stale never above its correction) is enforced last and can
	// never be undone by a frequently-recalled stale event. Pure reorder —
	// no v3 score/gating change, no id added or dropped.
	if e.accessFreqBoostEnabled {
		var mults map[int64]float64
		ids, mults = e.applyAccessFreqBoost(ids)
		for id, m := range mults {
			if bd, ok := breakdowns[id]; ok {
				bd.AccessFreqBoost = nonIdentityPtr(m)
				breakdowns[id] = bd
			}
		}
	}

	// State-tag affinity re-rank (default-on, post-scoring): events tagged
	// state:<flag> move up when the request's user_state declares that flag
	// active (see state_tag_boost.go). Placed AFTER fusion + surfaceability so
	// it sees the complete final candidate list, and AFTER the access-freq
	// nudge so the explicit user-state signal outranks the implicit frequency
	// one; placed BEFORE assertion demotion so the supersession invariant
	// (stale never above its correction) is enforced last. Pure reorder — no
	// v3 score/gating change, no id added or dropped; provably neutral
	// without user_state + tags.
	if e.stateTagBoostEnabled {
		var mults map[int64]float64
		ids, mults = e.applyStateTagBoost(ids, req.UserState, req.Query)
		for id, m := range mults {
			if bd, ok := breakdowns[id]; ok {
				bd.StateTagBoost = nonIdentityPtr(m)
				breakdowns[id] = bd
			}
		}
	}

	// Supersession-aware demotion (default-off, post-scoring): if both a stale
	// fact-event and its correction are present, the stale one is moved below
	// the correction. Pure reorder — no v3 score/gating change.
	if e.assertionOverlay {
		ids = e.applyAssertionDemotion(ids)
	}
	// Prompt-time recall needs one mode-independent relevance signal. The
	// query itself remains transient; expose only each returned event's cosine
	// so host adapters can omit weak matches instead of injecting noise.
	if breakdowns == nil {
		breakdowns = make(map[int64]ScoreBreakdown, len(ids))
	}
	for _, id := range ids {
		if _, exists := breakdowns[id]; exists {
			continue
		}
		index := sort.Search(len(e.eventIDs), func(i int) bool { return e.eventIDs[i] >= id })
		if index < len(e.eventIDs) && e.eventIDs[index] == id && index < len(e.eventVecs) {
			breakdowns[id] = ScoreBreakdown{Cosine: float64(dotF32(qVec, e.eventVecs[index]))}
		}
	}
	e.mu.RUnlock()

	// Access-frequency instrumentation (migration 029, Phase A). Best-effort and
	// OFF by default: only when PULSE_ACCESS_FREQ is enabled do we bump a bare
	// per-id counter on the events we just returned. Keyed by id ONLY — no
	// content/transcript touched, so this is not the raw-content write path. A
	// counter-write failure never fails the retrieval (error swallowed). Counts
	// take effect on the next Reload, and the scorer never reads them in this
	// phase, so scoring and Go==Python parity are untouched.
	if e.accessFreqEnabled && req.PersonalScope == nil && e.store != nil && len(ids) > 0 {
		_ = e.store.IncrementAccessCounts(ids, time.Now())
	}
	directSet := e.directCapsuleEventSet(ctx, ids)
	evidence := make(map[int64]CandidateEvidence, len(ids))
	for _, id := range ids {
		_, dense := denseSet[id]
		_, lexical := lexicalSet[id]
		_, direct := directSet[id]
		evidence[id] = CandidateEvidence{Dense: dense, Lexical: lexical, DirectCapsule: direct}
	}
	currentScope, err := e.currentPersonalScope(req.PersonalScope)
	if err != nil {
		return nil, fmt.Errorf("retrieve final personal scope fence: %w", err)
	}
	if !samePersonalScope(req.PersonalScope, currentScope) {
		return nil, ErrPersonalMemoryEligibilityChanged
	}

	return &RetrieveResponse{
		EventIDs:             ids,
		ModeUsed:             mode,
		RouterDecision:       decision,
		EmotionRole:          emotionRole,
		SurfaceabilityAction: surfaceability,
		ScoreBreakdowns:      breakdowns,
		CandidateEvidence:    evidence,
	}, nil
}

// applyAssertionDemotion moves any stale fact-event below its own correction
// when BOTH are present in ids (supersession-aware). Demote-only: it never
// drops, adds, or rescoring an id. With no superseded pairs in the store (every
// assertion-free corpus, including the v3 golden) it returns ids unchanged, so
// the frozen empathic path is unaffected unless an actual superseded fact and
// its replacement both surface.
func (e *Engine) applyAssertionDemotion(ids []int64) []int64 {
	if e.store == nil || len(ids) < 2 {
		return ids
	}
	pairs, err := e.store.SupersededPairsForEvents(ids)
	if err != nil || len(pairs) == 0 {
		return ids
	}
	return demoteSupersededBelowCurrent(ids, pairs)
}

// demoteSupersededBelowCurrent deterministically places every stale event
// immediately after the CURRENT leaf of its supersession chain (A→B→C ⇒ A,B sit
// after C, never above it), while preserving the relative order of all UNRELATED
// events. Pure and order-neutral: the result depends only on ids+pairs, not on
// the row order pairs arrive in, and events touched by no pair keep their order.
func demoteSupersededBelowCurrent(ids []int64, pairs [][2]int64) []int64 {
	succ := make(map[int64]int64, len(pairs)) // stale -> immediate current
	for _, p := range pairs {
		succ[p[0]] = p[1]
	}
	if len(succ) == 0 {
		return ids
	}
	pos := make(map[int64]int, len(ids))
	for i, id := range ids {
		pos[id] = i
	}
	terminal := func(x int64) int64 { // current leaf of x's chain (cycle-guarded)
		seen := map[int64]bool{}
		for {
			n, ok := succ[x]
			if !ok || seen[x] {
				return x
			}
			seen[x] = true
			x = n
		}
	}
	// A stale event is "violating" only if its current leaf is present AND ranked
	// above it. Already-below stales and unrelated events are left exactly in place
	// (order-neutral); only violating stales move — down to just after their leaf.
	violating := make(map[int64]bool)
	for s := range succ {
		sp, ok := pos[s]
		if !ok {
			continue
		}
		if lp, ok := pos[terminal(s)]; ok && sp < lp {
			violating[s] = true
		}
	}
	if len(violating) == 0 {
		return ids
	}
	insertAfter := make(map[int64][]int64) // leaf -> its violating stales, in original order
	reduced := make([]int64, 0, len(ids))
	for _, id := range ids {
		if violating[id] {
			leaf := terminal(id)
			insertAfter[leaf] = append(insertAfter[leaf], id)
			continue
		}
		reduced = append(reduced, id)
	}
	out := make([]int64, 0, len(ids))
	for _, id := range reduced {
		out = append(out, id)
		out = append(out, insertAfter[id]...)
	}
	return out
}

// EmbedText embeds a single string with the wired embedder. Public so the store
// can resolve claims by meaning. Returns nil,nil when no embedder is configured.
func (e *Engine) EmbedText(ctx context.Context, text string) ([]float32, error) {
	if e.embedder == nil {
		return nil, nil
	}
	return e.embedQuery(ctx, text)
}

func (e *Engine) embedQuery(ctx context.Context, q string) ([]float32, error) {
	vecs, err := e.embedder.Embed(ctx, []string{q}, embed.TypeSearchQuery)
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedder returned 0 vectors")
	}
	return vecs[0], nil
}

// retrieveEmpathic — full v3 ranking (delegates to retrieveEmpathicScored).
// Anchors get slower decay (decay_lambda_anchor = 0.001 vs 0.002).
func (e *Engine) retrieveEmpathic(qVec []float32, topK int) []int64 {
	ids, _ := e.retrieveEmpathicScoredEligible(qVec, "", topK, nil, nil)
	return ids
}

// retrieveEmpathicScored applies the full Pulse v3 ranking: v2_pure base
// (cosine × recency) × conditional boosts (emotion / state / anchor / date),
// each 1.0 when its signal is neutral so the score collapses to v2_pure exactly.
// Mirrors the multiplicative-boost combine of retrieval_v3.retrieve(). `query`
// feeds only temporal-keyword date inference; "" disables it.
func (e *Engine) retrieveEmpathicScored(qVec []float32, query string, topK int, state *UserState) ([]int64, map[int64]ScoreBreakdown) {
	return e.retrieveEmpathicScoredEligible(qVec, query, topK, state, nil)
}

func (e *Engine) retrieveEmpathicScoredEligible(qVec []float32, query string, topK int, state *UserState, eligible map[int64]struct{}) ([]int64, map[int64]ScoreBreakdown) {
	scores, breakdowns := e.scoreEventsV3Eligible(qVec, query, state, eligible)
	if scores == nil {
		return nil, nil
	}
	return topKEligibleIndicesToIDs(scores, e.eventIDs, topK, eligible), breakdowns
}

// scoreEventsV3 computes the full Pulse v3 score per event (cosine × recency ×
// conditional boosts) and the per-event breakdown map. Returns the raw score
// slice (parallel to e.eventIDs) so callers can take any top-N slice or feed
// chain expansion. Mirrors the boost-combine of retrieval_v3.retrieve() up to
// (but not including) the `order = argsort(-final)` step.
func (e *Engine) scoreEventsV3(qVec []float32, query string, state *UserState) ([]float64, map[int64]ScoreBreakdown) {
	return e.scoreEventsV3Eligible(qVec, query, state, nil)
}

func (e *Engine) scoreEventsV3Eligible(qVec []float32, query string, state *UserState, eligible map[int64]struct{}) ([]float64, map[int64]ScoreBreakdown) {
	n := len(e.eventIDs)
	if n == 0 {
		return nil, nil
	}
	// Pass 1 — v2_pure base (anchors decay slower).
	base := make([]float64, n)
	cosines := make([]float64, n)
	recencies := make([]float64, n)
	for i := 0; i < n; i++ {
		if !eligibleContains(eligible, e.eventIDs[i]) {
			base[i] = math.Inf(-1)
			continue
		}
		c := float64(dotF32(qVec, e.eventVecs[i]))
		lambda := e.decayLambda
		if e.eventAnchor[i] {
			lambda = e.decayLambdaAnchor
		}
		r := math.Exp(-lambda * e.eventDays[i])
		cosines[i], recencies[i], base[i] = c, r, c*r
	}
	// Boost contexts — each inactive ⇒ 1.0 (collapse to v2_pure).
	qe := prepareQueryEmotion(query, state)
	stateActive := state != nil && (state.IsBodyStressed() || state.IsBodyRestored())
	dateRef, dateActive := resolveDateRef(query, state)
	inTopN := anchorTopNSet(base, anchorTopN) // anchor boost gates on top-N by BASE

	// Pass 2 — apply conditional boosts.
	scores := make([]float64, n)
	breakdowns := make(map[int64]ScoreBreakdown, n)
	for i := 0; i < n; i++ {
		if !eligibleContains(eligible, e.eventIDs[i]) {
			scores[i] = math.Inf(-1)
			continue
		}
		// cartographerBoosts (shadow/curiosity/trigger) are a Pulse-side SUPERSET
		// not present in the Python v3 reference. They collapse to 1.0 unless the
		// UserState carries profile signals (shadow_themes / curiosity_signals /
		// active_triggers), so Go==Python v3 parity holds in any path without them
		// (the bench/golden config sets none — asserted by hybrid_parity_test.go).
		// They are NOT part of the frozen "1:1 v3" formula; the four boosts are.
		boosts := cartographerBoosts(state, e.eventTexts[i])
		emoBoost := qe.boost(e.eventEmo[i])
		stateBoost := 1.0
		if stateActive {
			stateBoost = 1.0 + gammaState*computeStateFit(e.eventBio[i], e.eventTexts[i], e.eventSentLabel[i], state)
		}
		anchorBoost := 1.0
		if e.eventAnchor[i] && inTopN[i] {
			anchorBoost = 1.0 + deltaAnchor
		}
		dateBoost := 1.0
		if dateActive {
			dateBoost = 1.0 + deltaDate*computeDateProximity(e.eventDays[i], dateRef)
		}
		scores[i] = base[i] * boosts.shadow * boosts.curiosity * boosts.trigger *
			emoBoost * stateBoost * anchorBoost * dateBoost
		breakdowns[e.eventIDs[i]] = ScoreBreakdown{
			Cosine:               cosines[i],
			Recency:              recencies[i],
			ShadowThemeBoost:     nonIdentityPtr(boosts.shadow),
			CuriositySignalBoost: nonIdentityPtr(boosts.curiosity),
			ActiveTriggerBoost:   nonIdentityPtr(boosts.trigger),
			EmotionBoost:         nonIdentityPtr(emoBoost),
			StateBoost:           nonIdentityPtr(stateBoost),
			AnchorBoost:          nonIdentityPtr(anchorBoost),
			DateBoost:            nonIdentityPtr(dateBoost),
		}
	}
	return scores, breakdowns
}

type profileBoosts struct {
	shadow    float64
	curiosity float64
	trigger   float64
}

func cartographerBoosts(state *UserState, eventText string) profileBoosts {
	boosts := profileBoosts{shadow: 1.0, curiosity: 1.0, trigger: 1.0}
	if state == nil {
		return boosts
	}
	if matchesAny(eventText, state.ShadowThemes) {
		boosts.shadow = 1.1
	}
	if matchesAny(eventText, state.CuriositySignals) {
		boosts.curiosity = 1.08
	}
	for _, trigger := range state.ActiveTriggers {
		if strings.TrimSpace(trigger.Stimulus) == "" {
			continue
		}
		if strings.Contains(eventText, strings.ToLower(trigger.Stimulus)) {
			boosts.trigger *= 1.0 + 0.15*clamp01(trigger.Intensity)
		}
	}
	return boosts
}

func matchesAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		needle = strings.ToLower(strings.TrimSpace(needle))
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func nonIdentityPtr(v float64) *float64 {
	if v == 1.0 {
		return nil
	}
	return &v
}

// retrieveFactual — cosine on fact embeddings → unique parent event_ids.
// retrieveFactualEvents ranks events by PLAIN cosine similarity to the query —
// no v3 emotion/state/anchor boosts, no recency decay. This is the right ranker
// for factual lookup ("what was the error code / email / occupation"): the
// answer-event should win on semantic match alone, exactly like a dedicated
// fact/vector store. Reuses e.eventVecs; does not touch scoreEventsV3 (the
// frozen v3 empathic path).
func (e *Engine) retrieveFactualEvents(qVec []float32, topK int) []int64 {
	return e.retrieveFactualEventsEligible(qVec, topK, nil)
}

func (e *Engine) retrieveFactualEventsEligible(qVec []float32, topK int, eligible map[int64]struct{}) []int64 {
	n := len(e.eventIDs)
	if n == 0 {
		return nil
	}
	scores := make([]float64, n)
	for i := 0; i < n; i++ {
		if !eligibleContains(eligible, e.eventIDs[i]) {
			scores[i] = math.Inf(-1)
			continue
		}
		scores[i] = float64(dotF32(qVec, e.eventVecs[i]))
	}
	return topKEligibleIndicesToIDs(scores, e.eventIDs, topK, eligible)
}

func (e *Engine) retrieveFactual(qVec []float32, topK int) []int64 {
	return e.retrieveFactualEligible(qVec, topK, nil)
}

func (e *Engine) retrieveFactualEligible(qVec []float32, topK int, eligible map[int64]struct{}) []int64 {
	if len(e.factIDs) == 0 {
		// No atomic_facts index (host-extracted mode never populates it) — do a
		// CLEAN semantic ranking over EVENT embeddings instead of falling back to
		// the empathic v3 ranker. Factual lookup wants plain cosine (what a fact
		// store does); the empathic boosts/recency bury the answer. This reuses
		// the already-loaded event vectors and never touches the frozen v3 path.
		return e.retrieveFactualEventsEligible(qVec, topK, eligible)
	}
	n := len(e.factIDs)
	scores := make([]float64, n)
	for i := 0; i < n; i++ {
		scores[i] = float64(dotF32(qVec, e.factVecs[i]))
	}
	order := argsortDesc(scores)
	seen := make(map[int64]bool, topK)
	out := make([]int64, 0, topK)
	for _, idx := range order {
		eid := e.factEventIDs[idx]
		if seen[eid] || !eligibleContains(eligible, eid) {
			continue
		}
		seen[eid] = true
		out = append(out, eid)
		if len(out) >= topK {
			break
		}
	}
	return out
}

// retrieveChain — chain expansion over the v3-scored candidate set.
//
// Faithful port of retrieval_v3.retrieve()'s chain branch (bench
// retrieval_v3.py:625-666). The candidate order is the FULL v3 ranking
// (cosine × recency × conditional boosts), not a simpler cosine×recency pass,
// so chain mode and empathic mode share the same scoring substrate.
//
// Algorithm:
//  1. Over-fetch max(top_k*3, 9) candidates by final v3 score (`wider`).
//  2. Among the first top_k candidates (best seeds first), pick the seed whose
//     connected component covers the most other candidates (`bestReach`).
//  3. If bestReach has ≥2 members, topologically expand from bestSeed (depth 4)
//     and intersect with bestReach, then fill chain-first, then original top_ids.
func (e *Engine) retrieveChain(qVec []float32, query string, topK int, state *UserState) []int64 {
	return e.retrieveChainEligible(qVec, query, topK, state, nil)
}

func (e *Engine) retrieveChainEligible(qVec []float32, query string, topK int, state *UserState, eligible map[int64]struct{}) []int64 {
	scores, _ := e.scoreEventsV3Eligible(qVec, query, state, eligible)
	if scores == nil {
		return nil
	}
	order := argsortDesc(scores)
	// topIds = order[:top_k]; wider = order[:max(top_k*3, 9)] (Python parity).
	topIDs := make([]int64, 0, topK)
	for _, idx := range order {
		if len(topIDs) >= topK {
			break
		}
		if !eligibleContains(eligible, e.eventIDs[idx]) {
			continue
		}
		topIDs = append(topIDs, e.eventIDs[idx])
	}
	if len(e.parentToChild)+len(e.childToParent) == 0 {
		return topIDs
	}

	widerN := topK * 3
	if widerN < 9 {
		widerN = 9
	}
	wider := make([]int64, 0, widerN)
	for _, idx := range order {
		if len(wider) >= widerN {
			break
		}
		if !eligibleContains(eligible, e.eventIDs[idx]) {
			continue
		}
		wider = append(wider, e.eventIDs[idx])
	}

	candSet := make(map[int64]bool, len(wider))
	for _, id := range wider {
		candSet[id] = true
	}

	// Pick the seed (from the best top_k candidates) whose connected component
	// includes the most other wider candidates.
	var bestSeed int64
	var bestReach map[int64]bool
	seedLimit := topK
	if seedLimit > len(wider) {
		seedLimit = len(wider)
	}
	for _, s := range wider[:seedLimit] {
		reach := e.connectedComponentEligible(s, candSet, eligible)
		if len(reach) > len(bestReach) {
			bestSeed, bestReach = s, reach
		}
	}

	if bestSeed != 0 && len(bestReach) >= 2 {
		expanded := e.expandChainFromSeedsEligible([]int64{bestSeed}, 4, eligible)
		// chain_ids = [eid for eid in expanded if eid in best_reach]
		result := make([]int64, 0, topK)
		seen := make(map[int64]bool, topK)
		for _, eid := range expanded {
			if len(result) >= topK {
				break
			}
			if bestReach[eid] && !seen[eid] {
				result = append(result, eid)
				seen[eid] = true
			}
		}
		// Fill remaining slots with the original top_ids (v3 order).
		for _, eid := range topIDs {
			if len(result) >= topK {
				break
			}
			if !seen[eid] {
				result = append(result, eid)
				seen[eid] = true
			}
		}
		return result
	}
	return topIDs
}

// connectedComponent does a BFS over chain edges (both directions) from `seed`
// and returns the subset of `candidates` reachable from it (including the seed
// itself when it is a candidate). Mirrors retrieval_v3.py:631-643.
func (e *Engine) connectedComponent(seed int64, candidates map[int64]bool) map[int64]bool {
	return e.connectedComponentEligible(seed, candidates, nil)
}

func (e *Engine) connectedComponentEligible(seed int64, candidates map[int64]bool, eligible map[int64]struct{}) map[int64]bool {
	visited := map[int64]bool{seed: true}
	frontier := []int64{seed}
	found := map[int64]bool{}
	if candidates[seed] {
		found[seed] = true
	}
	for len(frontier) > 0 {
		n := frontier[0]
		frontier = frontier[1:]
		// Python iterates c2p[n] + p2c[n] (parents first, then children).
		for _, nb := range append(append([]int64{}, e.childToParent[n]...), e.parentToChild[n]...) {
			if !visited[nb] && eligibleContains(eligible, nb) {
				visited[nb] = true
				frontier = append(frontier, nb)
				if candidates[nb] {
					found[nb] = true
				}
			}
		}
	}
	return found
}

// expandChainFromSeeds is a faithful port of retrieval_v3.expand_chain_from_seeds
// (bench retrieval_v3.py:230-267). BFS over chain edges (both directions) from
// the seeds up to `depth` steps, then a topological-ish ordering: roots (no
// visited parent) first, ties broken by days_ago descending (older first, since
// Python sorts on days_ago * -1). Only events present in the loaded index are
// considered (Python's `eid not in eid_map` guard).
func (e *Engine) expandChainFromSeeds(seeds []int64, depth int) []int64 {
	return e.expandChainFromSeedsEligible(seeds, depth, nil)
}

func (e *Engine) expandChainFromSeedsEligible(seeds []int64, depth int, eligible map[int64]struct{}) []int64 {
	exists := make(map[int64]bool, len(e.eventIDs))
	daysOf := make(map[int64]float64, len(e.eventIDs))
	for i, id := range e.eventIDs {
		if !eligibleContains(eligible, id) {
			continue
		}
		exists[id] = true
		daysOf[id] = e.eventDays[i]
	}

	visited := make(map[int64]bool)
	type qItem struct {
		id   int64
		dist int
	}
	frontier := make([]qItem, 0, len(seeds))
	for _, s := range seeds {
		frontier = append(frontier, qItem{s, 0})
	}
	for len(frontier) > 0 {
		it := frontier[0]
		frontier = frontier[1:]
		if visited[it.id] || !exists[it.id] {
			continue
		}
		visited[it.id] = true
		if it.dist >= depth {
			continue
		}
		// Python: c2p[eid] + p2c[eid] (parents first, then children).
		for _, nb := range append(append([]int64{}, e.childToParent[it.id]...), e.parentToChild[it.id]...) {
			if !visited[nb] {
				frontier = append(frontier, qItem{nb, it.dist + 1})
			}
		}
	}

	// _ancestor_depth: 1 + max ancestor-depth over visited predecessors; 0 if no
	// visited predecessor (root). Memoized, cycle-guarded.
	memo := make(map[int64]int, len(visited))
	var ancestorDepth func(eid int64) int
	ancestorDepth = func(eid int64) int {
		if d, ok := memo[eid]; ok {
			return d
		}
		memo[eid] = 0 // cycle guard
		best := -1
		for _, p := range e.childToParent[eid] {
			if visited[p] {
				if d := ancestorDepth(p); d > best {
					best = d
				}
			}
		}
		if best < 0 {
			memo[eid] = 0
			return 0
		}
		memo[eid] = 1 + best
		return memo[eid]
	}

	ordered := make([]int64, 0, len(visited))
	for id := range visited {
		ordered = append(ordered, id)
	}
	// Deterministic base order before the stable topo sort (map iteration is
	// random in Go; Python's set iteration is implementation-defined). Sorting
	// by ID first makes the final tiebreak reproducible without altering the
	// Python-defined sort keys (ancestor_depth, days_ago*-1).
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	sort.SliceStable(ordered, func(i, j int) bool {
		di, dj := ancestorDepth(ordered[i]), ancestorDepth(ordered[j])
		if di != dj {
			return di < dj
		}
		// Python key: days_ago * -1 ascending ⇒ larger days_ago first.
		return daysOf[ordered[i]] > daysOf[ordered[j]]
	})
	return ordered
}

func (e *Engine) applyFragileSurfaceability(ids []int64, topK int, role EmotionRoleDecision) ([]int64, SurfaceabilityAction) {
	return e.applyFragileSurfaceabilityEligible(ids, topK, role, nil)
}

func (e *Engine) applyFragileSurfaceabilityEligible(ids []int64, topK int, role EmotionRoleDecision, eligible map[int64]struct{}) ([]int64, SurfaceabilityAction) {
	if len(ids) == 0 || !role.Fragile || role.ExplicitPainIntent {
		return ids, ""
	}
	emotions := e.eventEmotionMap()
	if !allPainEventIDs(ids, emotions) {
		return ids, ""
	}
	repairID, ok := e.bestRepairAnchorIDEligible(ids, eligible)
	if !ok {
		return nil, SurfaceabilitySuppress
	}
	out := append([]int64(nil), ids...)
	if len(out) >= topK && len(out) > 0 {
		out[len(out)-1] = repairID
	} else {
		out = append(out, repairID)
	}
	return out, SurfaceabilityPairWithRepairAnchor
}

func (e *Engine) eventEmotionMap() map[int64][]float32 {
	out := make(map[int64][]float32, len(e.eventIDs))
	for i, id := range e.eventIDs {
		out[id] = e.eventEmo[i]
	}
	return out
}

func (e *Engine) bestRepairAnchorID(exclude []int64) (int64, bool) {
	return e.bestRepairAnchorIDEligible(exclude, nil)
}

func (e *Engine) bestRepairAnchorIDEligible(exclude []int64, eligible map[int64]struct{}) (int64, bool) {
	excluded := make(map[int64]bool, len(exclude))
	for _, id := range exclude {
		excluded[id] = true
	}
	var bestID int64
	var bestScore float32
	for i, id := range e.eventIDs {
		if excluded[id] || !eligibleContains(eligible, id) || !isRepairEmotionVector(e.eventEmo[i]) {
			continue
		}
		score := e.eventEmo[i][0] + e.eventEmo[i][4]
		if bestID == 0 || score > bestScore {
			bestID = id
			bestScore = score
		}
	}
	return bestID, bestID != 0
}

// ──────────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────────

func dotF32(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

type scoreIdx struct {
	score float64
	idx   int
}

func argsortDesc(scores []float64) []int {
	idx := make([]scoreIdx, len(scores))
	for i, s := range scores {
		idx[i] = scoreIdx{s, i}
	}
	sort.SliceStable(idx, func(i, j int) bool { return idx[i].score > idx[j].score })
	out := make([]int, len(scores))
	for i := range idx {
		out[i] = idx[i].idx
	}
	return out
}

func topKIndicesToIDs(scores []float64, ids []int64, k int) []int64 {
	order := argsortDesc(scores)
	if k > len(order) {
		k = len(order)
	}
	out := make([]int64, k)
	for i := 0; i < k; i++ {
		out[i] = ids[order[i]]
	}
	return out
}

func topKEligibleIndicesToIDs(scores []float64, ids []int64, k int, eligible map[int64]struct{}) []int64 {
	if eligible == nil {
		return topKIndicesToIDs(scores, ids, k)
	}
	order := argsortDesc(scores)
	out := make([]int64, 0, min(k, len(order)))
	for _, index := range order {
		if !eligibleContains(eligible, ids[index]) {
			continue
		}
		out = append(out, ids[index])
		if len(out) == k {
			break
		}
	}
	return out
}

// rowsErrSilencer is a placeholder for sql.Rows.Err pass-through if we add
// telemetry later. Not used yet but referenced via type so the package builds
// even with future hooks.
var _ = (*sql.Rows)(nil)
