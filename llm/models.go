package llm

import (
	"fmt"
	"os"

	"github.com/peiblow/avm/agent"
)

type Provider struct {
	Family  string
	BaseURL string
	EnvKey  string
}

var Providers = map[string]Provider{
	"groq":       {Family: "openai-compat", BaseURL: "https://api.groq.com/openai/v1", EnvKey: "GROQ_API_KEY"},
	"openai":     {Family: "openai-compat", BaseURL: "https://api.openai.com/v1", EnvKey: "OPENAI_API_KEY"},
	"openrouter": {Family: "openai-compat", BaseURL: "https://openrouter.ai/api/v1", EnvKey: "OPENROUTER_API_KEY"},
	"anthropic":  {Family: "anthropic", BaseURL: "https://api.anthropic.com", EnvKey: "ANTHROPIC_API_KEY"},
}

func NewModel(provider, name string, cfg agent.AgentCfg) (agent.Model, error) {
	p, ok := Providers[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	apiKey := os.Getenv(p.EnvKey)
	if apiKey == "" {
		return nil, fmt.Errorf("missing API key: set %s", p.EnvKey)
	}

	switch p.Family {
	case "openai-compat":
		return NewOpenAICompatModel(p.BaseURL, apiKey, name, cfg), nil
	case "anthropic":
		return NewAnthropicModel(p.BaseURL, apiKey, name, cfg), nil
	default:
		return nil, fmt.Errorf("unsupported family: %s", p.Family)
	}
}
