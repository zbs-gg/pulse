package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/model"
)

const (
	maxBodyBytes     = 10 << 20
	anthropicVersion = "2023-06-01"
	defaultMaxTokens = 1024
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: newHTTPClient()}
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 120 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			if !sameHost(req.URL, via[0].URL) {
				return fmt.Errorf("cross-host redirect forbidden: %s -> %s", via[0].URL.Host, req.URL.Host)
			}
			return nil
		},
	}
}

func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(a.Hostname(), b.Hostname())
}

type messagesReq struct {
	Model       string        `json:"model"`
	System      string        `json:"system,omitempty"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature *float64      `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesResp struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *Client) Chat(ctx context.Context, req model.ChatRequest, resolved model.ResolvedModel) (*model.ChatResponse, error) {
	if resolved.APIKey == "" {
		return nil, fmt.Errorf("missing ANTHROPIC_API_KEY for anthropic provider: %w", model.ErrProviderUnavailable)
	}
	messages := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, chatMessage{Role: m.Role, Content: m.Content})
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = defaultMaxTokens
	}
	body, err := json.Marshal(messagesReq{
		Model:       resolved.Model,
		System:      strings.TrimSpace(req.System),
		Messages:    messages,
		MaxTokens:   maxTok,
		Temperature: req.Temperature,
	})
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(resolved.BaseURL, "/")+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("x-api-key", resolved.APIKey)
	hreq.Header.Set("anthropic-version", anthropicVersion)
	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed messagesResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic response: %w", err)
	}
	text := ""
	for _, b := range parsed.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	return &model.ChatResponse{
		Text:         text,
		Model:        parsed.Model,
		Provider:     model.ProviderAnthropic,
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		StopReason:   parsed.StopReason,
	}, nil
}

func (c *Client) Health(ctx context.Context, resolved model.ResolvedModel) error {
	if resolved.APIKey == "" {
		return fmt.Errorf("missing ANTHROPIC_API_KEY: %w", model.ErrProviderUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(resolved.BaseURL, "/")+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", resolved.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("anthropic health %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: false}
}
