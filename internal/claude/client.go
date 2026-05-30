package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL  = "https://api.anthropic.com/v1"
	anthropicVer    = "2023-06-01"
	defaultTimeout  = 120 * time.Second
	defaultMaxToken = 1024
	maxBodyBytes    = 10 << 20 // 10 MiB
)

// Message is one turn in the conversation.
type Message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// CompleteRequest describes the API call.
type CompleteRequest struct {
	Model     string
	System    string
	Messages  []Message
	MaxTokens int // 0 -> default 1024
}

// CompleteResponse is the parsed output.
type CompleteResponse struct {
	Text         string
	InputTokens  int
	OutputTokens int
	StopReason   string
	Model        string
}

// Client is a minimal Anthropic Messages API client.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New creates a Client using the production Anthropic base URL.
func New(apiKey string) *Client {
	return NewWithBaseURL(apiKey, defaultBaseURL)
}

// NewWithBaseURL creates a Client with a custom base URL (useful for tests).
// Trailing slashes on baseURL are normalized away.
func NewWithBaseURL(apiKey, baseURL string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

type apiReq struct {
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
}

type apiResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete calls the /messages endpoint and returns the assistant reply.
func (c *Client) Complete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error) {
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = defaultMaxToken
	}
	body, err := json.Marshal(apiReq{
		Model:     req.Model,
		System:    req.System,
		Messages:  req.Messages,
		MaxTokens: maxTok,
	})
	if err != nil {
		return nil, err
	}

	url := c.baseURL + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVer)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read body (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("claude api %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed apiResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal: %w (body=%s)", err, string(respBody))
	}
	var text string
	for _, blk := range parsed.Content {
		if blk.Type == "text" {
			text += blk.Text
		}
	}
	return &CompleteResponse{
		Text:         text,
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		StopReason:   parsed.StopReason,
		Model:        parsed.Model,
	}, nil
}
