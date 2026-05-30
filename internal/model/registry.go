package model

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	ProviderDOInference  = "doinference"
	ProviderOpenAICompat = "openaicompat"
	ProviderAnthropic    = "anthropic"

	TierPro = "pro"
)

// Registry resolves model aliases and enforces static policy.
type Registry struct {
	cfg RegistryConfig
}

func NewRegistry(cfg RegistryConfig) (*Registry, error) {
	if cfg.DefaultAlias == "" {
		return nil, fmt.Errorf("model registry: default_alias required")
	}
	if len(cfg.Models) == 0 {
		return nil, fmt.Errorf("model registry: at least one model required")
	}
	if _, ok := cfg.Models[cfg.DefaultAlias]; !ok {
		return nil, fmt.Errorf("model registry: default alias %q: %w", cfg.DefaultAlias, ErrAliasNotFound)
	}
	for alias, m := range cfg.Models {
		if alias == "" {
			return nil, fmt.Errorf("model registry: empty alias")
		}
		if m.Provider == "" || m.Model == "" {
			return nil, fmt.Errorf("model registry: %s provider and model required", alias)
		}
		if err := ValidatePolicy(alias, m); err != nil {
			return nil, err
		}
	}
	return &Registry{cfg: cfg}, nil
}

// DefaultRegistryConfig is the default model routing baseline.
//
// Production /msg path stays on Anthropic via subscription. Working
// channels — Anthropic, Kimi (Moonshot), GLM (Z.ai), OpenAI non-Pro
// (gpt-5.4) — are all routed through openaicompat or anthropic
// providers. Pro models (gpt-5.4-pro, gpt-5.5) are NOT in the default
// registry; they are expected to sit behind an out-of-band escalation gate.
func DefaultRegistryConfig() RegistryConfig {
	return RegistryConfig{
		DefaultAlias: "anthropic/opus",
		Models: map[string]ModelConfig{
			"anthropic/opus": {
				Provider:         ProviderAnthropic,
				Model:            "claude-opus-4-6",
				Tier:             "standard",
				MaxTokensDefault: 4096,
				APIKeyEnv:        "ANTHROPIC_API_KEY",
			},
			"anthropic/sonnet": {
				Provider:         ProviderAnthropic,
				Model:            "claude-sonnet-4-6",
				Tier:             "standard",
				MaxTokensDefault: 4096,
				APIKeyEnv:        "ANTHROPIC_API_KEY",
			},
			"openai/default": {
				Provider:         ProviderOpenAICompat,
				Model:            "gpt-5.4",
				Tier:             "standard",
				MaxTokensDefault: 4096,
				BaseURL:          "https://api.openai.com/v1",
				APIKeyEnv:        "OPENAI_API_KEY",
			},
			"kimi/default": {
				Provider:         ProviderOpenAICompat,
				Model:            "kimi-k2-0711-preview",
				Tier:             "standard",
				MaxTokensDefault: 8000,
				BaseURL:          "https://api.moonshot.ai/v1",
				BaseURLEnv:       "KIMI_BASE_URL",
				APIKeyEnv:        "KIMI_API_KEY",
			},
			"glm/default": {
				Provider:         ProviderOpenAICompat,
				Model:            "glm-5",
				Tier:             "standard",
				MaxTokensDefault: 8000,
				BaseURL:          "https://open.bigmodel.cn/api/paas/v4",
				BaseURLEnv:       "GLM_BASE_URL",
				APIKeyEnv:        "GLM_API_KEY",
			},
			"local/coder": {
				Provider:         ProviderOpenAICompat,
				Model:            "local-model",
				Tier:             "local",
				MaxTokensDefault: 4096,
				BaseURLEnv:       "LOCAL_OAI_BASE_URL",
				APIKeyEnv:        "LOCAL_OAI_API_KEY",
			},
		},
	}
}

func (r *Registry) DefaultAlias() string {
	return r.cfg.DefaultAlias
}

func (r *Registry) Resolve(alias string) (ResolvedModel, error) {
	if alias == "" {
		alias = r.cfg.DefaultAlias
	}
	m, ok := r.cfg.Models[alias]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("%s: %w", alias, ErrAliasNotFound)
	}
	baseURL := strings.TrimRight(m.BaseURL, "/")
	if baseURL == "" && m.BaseURLEnv != "" {
		baseURL = strings.TrimRight(os.Getenv(m.BaseURLEnv), "/")
	}
	if baseURL == "" && m.Provider == ProviderDOInference {
		baseURL = "https://inference.do-ai.run/v1"
	}
	if baseURL == "" && m.Provider == ProviderAnthropic {
		baseURL = "https://api.anthropic.com/v1"
	}
	if err := validateBaseURL(m.Provider, baseURL); err != nil {
		return ResolvedModel{}, fmt.Errorf("%s: %w", alias, err)
	}
	apiKey := ""
	if m.APIKeyEnv != "" {
		apiKey = os.Getenv(m.APIKeyEnv)
	}
	return ResolvedModel{
		Alias:            alias,
		Provider:         m.Provider,
		Model:            m.Model,
		Tier:             m.Tier,
		DOOnly:           m.DOOnly,
		MaxTokensDefault: m.MaxTokensDefault,
		BaseURL:          baseURL,
		APIKey:           apiKey,
		Capabilities:     m.Capabilities,
	}, nil
}

func validateBaseURL(provider, raw string) error {
	if raw == "" {
		return fmt.Errorf("empty base url: %w", ErrProviderUnavailable)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("parse %q: %w", raw, ErrUnsafeBaseURL)
	}
	host := strings.ToLower(u.Hostname())
	if provider == ProviderDOInference && host != "inference.do-ai.run" && !isLoopback(host) {
		return fmt.Errorf("DO provider must use inference.do-ai.run or loopback: %w", ErrUnsafeBaseURL)
	}
	if provider == ProviderAnthropic && host != "api.anthropic.com" && !isLoopback(host) {
		return fmt.Errorf("anthropic provider must use api.anthropic.com or loopback: %w", ErrUnsafeBaseURL)
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "[::1]"
}

// ValidatePolicy checks alias-level invariants. Pro-tier billing is not
// gated at the Go layer; spend control on expensive providers is expected
// to be enforced at the deployment/ops layer so the cost choice is approved
// deliberately rather than silently.
func ValidatePolicy(alias string, m ModelConfig) error {
	if m.BaseURL != "" {
		if err := validateBaseURL(m.Provider, m.BaseURL); err != nil {
			return fmt.Errorf("%s: %w", alias, err)
		}
	}
	return nil
}
