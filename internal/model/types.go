package model

// Message is one text-only chat message.
type Message struct {
	Role    string
	Content string
}

// ChatRequest is the normalized request used by Pulse internals.
type ChatRequest struct {
	Alias            string
	ProviderOverride string
	System           string
	Messages         []Message
	MaxTokens        int
	Temperature      *float64
}

// ChatResponse is the normalized provider response.
type ChatResponse struct {
	Text         string
	Model        string
	Provider     string
	InputTokens  int
	OutputTokens int
	StopReason   string
}

// Capabilities describes provider/model features known at config time.
type Capabilities struct {
	Streaming bool
	JSONMode  bool
	Tools     bool
	Vision    bool
}

// ModelConfig is one registry entry.
type ModelConfig struct {
	Provider         string       `json:"provider"`
	Model            string       `json:"model"`
	Tier             string       `json:"tier,omitempty"`
	DOOnly           bool         `json:"do_only,omitempty"`
	MaxTokensDefault int          `json:"max_tokens_default,omitempty"`
	BaseURL          string       `json:"base_url,omitempty"`
	BaseURLEnv       string       `json:"base_url_env,omitempty"`
	APIKeyEnv        string       `json:"api_key_env,omitempty"`
	Capabilities     Capabilities `json:"capabilities,omitempty"`
}

// ResolvedModel is a model entry after env/default resolution.
type ResolvedModel struct {
	Alias            string
	Provider         string
	Model            string
	Tier             string
	DOOnly           bool
	MaxTokensDefault int
	BaseURL          string
	APIKey           string
	Capabilities     Capabilities
}

// RegistryConfig is the top-level model registry.
type RegistryConfig struct {
	DefaultAlias     string                 `json:"default_alias"`
	EnforceDOOnlyPro bool                   `json:"enforce_do_only_pro"`
	Models           map[string]ModelConfig `json:"models"`
}
