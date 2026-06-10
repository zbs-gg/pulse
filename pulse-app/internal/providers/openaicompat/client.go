package openaicompat

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

const maxBodyBytes = 10 << 20

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

type chatReq struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Client) Chat(ctx context.Context, req model.ChatRequest, resolved model.ResolvedModel) (*model.ChatResponse, error) {
	messages := make([]chatMessage, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		messages = append(messages, chatMessage{Role: m.Role, Content: m.Content})
	}
	body, err := json.Marshal(chatReq{
		Model:       resolved.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	})
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(resolved.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if resolved.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+resolved.APIKey)
	}
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
		return nil, fmt.Errorf("openai-compatible %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed chatResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal openai-compatible response: %w", err)
	}
	text := ""
	stop := ""
	if len(parsed.Choices) > 0 {
		text = parsed.Choices[0].Message.Content
		stop = parsed.Choices[0].FinishReason
	}
	return &model.ChatResponse{
		Text:         text,
		Model:        parsed.Model,
		Provider:     model.ProviderOpenAICompat,
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
		StopReason:   stop,
	}, nil
}

func (c *Client) Health(ctx context.Context, resolved model.ResolvedModel) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(resolved.BaseURL, "/")+"/models", nil)
	if err != nil {
		return err
	}
	if resolved.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+resolved.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("openai-compatible health %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: false}
}
