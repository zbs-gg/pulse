package model

import (
	"errors"
	"testing"
)

func TestRegistryRejectsInvalidBaseURL(t *testing.T) {
	_, err := NewRegistry(RegistryConfig{
		DefaultAlias: "x",
		Models: map[string]ModelConfig{
			"x": {
				Provider: ProviderOpenAICompat,
				Model:    "any",
				BaseURL:  "not-a-url",
			},
		},
	})
	if !errors.Is(err, ErrUnsafeBaseURL) {
		t.Fatalf("expected ErrUnsafeBaseURL, got %v", err)
	}
}

func TestRegistryRejectsAnthropicProviderOnForeignHost(t *testing.T) {
	_, err := NewRegistry(RegistryConfig{
		DefaultAlias: "x",
		Models: map[string]ModelConfig{
			"x": {
				Provider: ProviderAnthropic,
				Model:    "claude-opus-4-6",
				BaseURL:  "https://api.openai.com/v1",
			},
		},
	})
	if !errors.Is(err, ErrUnsafeBaseURL) {
		t.Fatalf("expected ErrUnsafeBaseURL, got %v", err)
	}
}

func TestResolveIgnoresOpenAIEnvForLocalProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-should-not-be-used")
	reg, err := NewRegistry(RegistryConfig{
		DefaultAlias: "local/coder",
		Models: map[string]ModelConfig{
			"local/coder": {
				Provider:  ProviderOpenAICompat,
				Model:     "local-model",
				Tier:      "local",
				BaseURL:   "http://127.0.0.1:1234/v1",
				APIKeyEnv: "LOCAL_OAI_API_KEY",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	resolved, err := reg.Resolve("local/coder")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.APIKey != "" {
		t.Fatalf("expected empty APIKey, got %q", resolved.APIKey)
	}
}

func TestResolveAnthropicDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant_test")
	reg, err := NewRegistry(DefaultRegistryConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	resolved, err := reg.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Provider != ProviderAnthropic {
		t.Fatalf("provider = %q", resolved.Provider)
	}
	if resolved.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("base URL = %q", resolved.BaseURL)
	}
	if resolved.APIKey != "ant_test" {
		t.Fatalf("api key not resolved from ANTHROPIC_API_KEY: %q", resolved.APIKey)
	}
}

func TestResolveKimiAlias(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi_test")
	reg, err := NewRegistry(DefaultRegistryConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	resolved, err := reg.Resolve("kimi/default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Provider != ProviderOpenAICompat {
		t.Fatalf("provider = %q", resolved.Provider)
	}
	if resolved.BaseURL != "https://api.moonshot.ai/v1" {
		t.Fatalf("base URL = %q", resolved.BaseURL)
	}
	if resolved.APIKey != "kimi_test" {
		t.Fatalf("api key not resolved from KIMI_API_KEY: %q", resolved.APIKey)
	}
}

func TestResolveGLMAlias(t *testing.T) {
	t.Setenv("GLM_API_KEY", "glm_test")
	reg, err := NewRegistry(DefaultRegistryConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	resolved, err := reg.Resolve("glm/default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.BaseURL != "https://open.bigmodel.cn/api/paas/v4" {
		t.Fatalf("base URL = %q", resolved.BaseURL)
	}
	if resolved.APIKey != "glm_test" {
		t.Fatalf("api key not resolved from GLM_API_KEY: %q", resolved.APIKey)
	}
}

func TestResolveOpenAIAlias(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-real-test")
	reg, err := NewRegistry(DefaultRegistryConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	resolved, err := reg.Resolve("openai/default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base URL = %q", resolved.BaseURL)
	}
	if resolved.Model != "gpt-5.4" {
		t.Fatalf("model = %q (Pro models intentionally absent from default registry)", resolved.Model)
	}
	if resolved.APIKey != "sk-real-test" {
		t.Fatalf("api key not resolved from OPENAI_API_KEY: %q", resolved.APIKey)
	}
}
