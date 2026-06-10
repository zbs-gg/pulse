// Package embed provides text embedding clients for Pulse retrieval.
//
// Phase G — Cohere embed-v4.0 client. Mirrors the reference Python prototype.
// Pulse's first non-Anthropic LLM HTTP client; pattern reusable for
// OpenAI/Voyage if needed.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

const (
	cohereDefaultBase  = "https://api.cohere.com"
	cohereDefaultModel = "embed-v4.0"
	cohereTimeout      = 60 * time.Second
	maxBatchSize       = 96 // Cohere v2 limit; we cap at 64 in practice
)

// CohereClient is a minimal Cohere /v2/embed client.
type CohereClient struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// NewCohere creates a Cohere embed client. baseURL/model fall back to
// production defaults when empty.
func NewCohere(apiKey, baseURL, model string) *CohereClient {
	if baseURL == "" {
		baseURL = cohereDefaultBase
	}
	if model == "" {
		model = cohereDefaultModel
	}
	return &CohereClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		http:    &http.Client{Timeout: cohereTimeout},
	}
}

// InputType maps to Cohere's input_type parameter — must match how the index
// was built. Use TypeSearchDocument when embedding events/facts at ingest;
// use TypeSearchQuery when embedding the query at retrieval time.
type InputType string

const (
	TypeSearchDocument InputType = "search_document"
	TypeSearchQuery    InputType = "search_query"
)

type embedReq struct {
	Texts          []string `json:"texts"`
	Model          string   `json:"model"`
	InputType      string   `json:"input_type"`
	EmbeddingTypes []string `json:"embedding_types"`
}

type embedResp struct {
	Embeddings struct {
		Float [][]float32 `json:"float"`
	} `json:"embeddings"`
}

type cohereError struct {
	Message string `json:"message"`
}

// Embed embeds a batch of texts. Returns one float32 vector per input,
// L2-normalized to unit length (mirrors the Python prototype).
//
// Hard caps: 8000 chars per text (truncated by caller if needed); 64-doc
// batches by default — split your input upstream.
func (c *CohereClient) Embed(ctx context.Context, texts []string,
	inputType InputType) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > maxBatchSize {
		return nil, fmt.Errorf("embed: batch size %d exceeds max %d; split upstream",
			len(texts), maxBatchSize)
	}
	body, err := json.Marshal(embedReq{
		Texts:          texts,
		Model:          c.model,
		InputType:      string(inputType),
		EmbeddingTypes: []string{"float"},
	})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL+"/v2/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20)) // 64 MiB cap
	if err != nil {
		return nil, fmt.Errorf("embed: read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var ce cohereError
		_ = json.Unmarshal(raw, &ce)
		msg := ce.Message
		if msg == "" {
			msg = string(raw[:min(len(raw), 500)])
		}
		return nil, fmt.Errorf("embed: status %d: %s", resp.StatusCode, msg)
	}
	var out embedResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embed: parse response: %w", err)
	}
	if len(out.Embeddings.Float) != len(texts) {
		return nil, fmt.Errorf("embed: got %d vectors for %d inputs",
			len(out.Embeddings.Float), len(texts))
	}
	for i := range out.Embeddings.Float {
		l2Normalize(out.Embeddings.Float[i])
	}
	return out.Embeddings.Float, nil
}

// EmbedBatched splits input into chunks of size <= maxBatchSize and
// concatenates results. Convenient for ingest pipelines.
func (c *CohereClient) EmbedBatched(ctx context.Context, texts []string,
	inputType InputType, batchSize int) ([][]float32, error) {
	if batchSize <= 0 || batchSize > maxBatchSize {
		batchSize = 64
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := c.Embed(ctx, texts[start:end], inputType)
		if err != nil {
			return nil, fmt.Errorf("embed batched [%d:%d]: %w", start, end, err)
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// Model returns the embedder name used (for storage in atomic_fact_embeddings.model).
func (c *CohereClient) Model() string {
	return c.model
}

// l2Normalize scales v to unit length in place. Zero vectors are left as-is.
func l2Normalize(v []float32) {
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	if sumSq == 0 {
		return
	}
	inv := float32(1.0 / math.Sqrt(sumSq))
	for i := range v {
		v[i] *= inv
	}
}

// CosineSim returns the cosine similarity between two L2-normalized vectors.
// (For unit vectors this is just the dot product, but we keep it explicit.)
func CosineSim(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, errors.New("embed: vector dimension mismatch")
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return float32(dot), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
