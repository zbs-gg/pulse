package model

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistryFromBytesEmptyFallsBackToDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant_test")
	reg, err := LoadRegistryFromBytes(nil)
	if err != nil {
		t.Fatalf("LoadRegistryFromBytes(nil): %v", err)
	}
	if reg.DefaultAlias() != "anthropic/opus" {
		t.Fatalf("default alias = %q", reg.DefaultAlias())
	}
}

func TestLoadRegistryFromBytesParsesValidJSON(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	data := []byte(`{
		"default_alias": "openai/main",
		"models": {
			"openai/main": {
				"provider": "openaicompat",
				"model": "gpt-5.4",
				"tier": "standard",
				"max_tokens_default": 8000,
				"base_url": "https://api.openai.com/v1",
				"api_key_env": "OPENAI_API_KEY"
			}
		}
	}`)
	reg, err := LoadRegistryFromBytes(data)
	if err != nil {
		t.Fatalf("LoadRegistryFromBytes: %v", err)
	}
	resolved, err := reg.Resolve("openai/main")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Model != "gpt-5.4" || resolved.MaxTokensDefault != 8000 {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base url = %q", resolved.BaseURL)
	}
	if resolved.APIKey != "sk-test" {
		t.Fatalf("api key = %q", resolved.APIKey)
	}
}

func TestLoadRegistryFromBytesRejectsAnthropicProviderOnForeignHost(t *testing.T) {
	data := []byte(`{
		"default_alias": "x",
		"models": {
			"x": {
				"provider": "anthropic",
				"model": "claude-opus-4-6",
				"base_url": "https://api.openai.com/v1"
			}
		}
	}`)
	_, err := LoadRegistryFromBytes(data)
	if err == nil {
		t.Fatal("expected error for anthropic provider on api.openai.com base_url")
	}
	if !errors.Is(err, ErrUnsafeBaseURL) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRegistryFromBytesRejectsUnknownFields(t *testing.T) {
	data := []byte(`{
		"default_alias": "x",
		"secret_token": "should-not-be-here",
		"models": {
			"x": {
				"provider": "openaicompat",
				"model": "local-model",
				"base_url": "http://127.0.0.1:8080/v1"
			}
		}
	}`)
	_, err := LoadRegistryFromBytes(data)
	if err == nil {
		t.Fatal("expected error for unknown top-level field")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unknown") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRegistryFromFileMissingReturnsDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant_test")
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	reg, err := LoadRegistryFromFile(path)
	if err != nil {
		t.Fatalf("LoadRegistryFromFile missing: %v", err)
	}
	if reg.DefaultAlias() != "anthropic/opus" {
		t.Fatalf("default alias = %q", reg.DefaultAlias())
	}
}

func TestLoadRegistryFromFileReadsJSON(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi_test")
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`{
		"default_alias": "kimi/file",
		"models": {
			"kimi/file": {
				"provider": "openaicompat",
				"model": "kimi-k2-0711-preview",
				"tier": "standard",
				"base_url": "https://api.moonshot.ai/v1",
				"api_key_env": "KIMI_API_KEY"
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write models.json: %v", err)
	}
	reg, err := LoadRegistryFromFile(path)
	if err != nil {
		t.Fatalf("LoadRegistryFromFile: %v", err)
	}
	if reg.DefaultAlias() != "kimi/file" {
		t.Fatalf("default alias = %q", reg.DefaultAlias())
	}
}

func TestLoadRegistryUsesEnvOverride(t *testing.T) {
	t.Setenv("GLM_API_KEY", "glm_test")
	dir := t.TempDir()
	envPath := filepath.Join(dir, "override.json")
	if err := os.WriteFile(envPath, []byte(`{
		"default_alias": "glm/env",
		"models": {
			"glm/env": {
				"provider": "openaicompat",
				"model": "glm-5",
				"tier": "standard",
				"base_url": "https://open.bigmodel.cn/api/paas/v4",
				"api_key_env": "GLM_API_KEY"
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write override.json: %v", err)
	}
	t.Setenv("PULSE_MODELS_PATH", envPath)
	reg, err := LoadRegistry(dir)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if reg.DefaultAlias() != "glm/env" {
		t.Fatalf("env override not applied: %q", reg.DefaultAlias())
	}
}
