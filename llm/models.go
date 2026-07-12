package llm

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/peiblow/avm/agent"
)

func clientTimeout() time.Duration {
	if v := os.Getenv("LLM_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 180 * time.Second
}

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
	"ollama":     {Family: "openai-compat", BaseURL: "http://localhost:11434/v1", EnvKey: ""},
}

func NewModel(provider, name string, cfg agent.AgentCfg) (agent.Model, error) {
	p, ok := Providers[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	var apiKey string
	if p.EnvKey != "" {
		apiKey = os.Getenv(p.EnvKey)
		if apiKey == "" {
			return nil, fmt.Errorf("missing API key: set %s", p.EnvKey)
		}
	}

	baseURL := p.BaseURL
	if override := os.Getenv(strings.ToUpper(provider) + "_BASE_URL"); override != "" {
		baseURL = override
	}

	switch p.Family {
	case "openai-compat":
		return NewOpenAICompatModel(baseURL, apiKey, name, cfg), nil
	case "anthropic":
		return NewAnthropicModel(baseURL, apiKey, name, cfg), nil
	default:
		return nil, fmt.Errorf("unsupported family: %s", p.Family)
	}
}
