package agent

import (
	"context"
	"encoding/json"
)

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type Completion struct {
	Text      string
	ToolCalls []ToolCall
}

type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

type ToolsSpec struct {
	Name        string
	Description string
	Func        string
	Actions     []interface{}
	Parameters  json.RawMessage
}

type AgentCfg struct {
	MaxSteps    int
	MaxTokens   int
	Temperature float64
	OnFinish    string
}

type ToolChoice int

const (
	ChoiceAuto ToolChoice = iota
	ChoiceRequired
)

type AgentInfo struct {
	Model        Model
	Tools        map[string]Tool
	Cfg          AgentCfg
	Version      string
	SystemPrompt string
}

func NewAgent(model Model, tools map[string]Tool, cfg AgentCfg, version, systemPrompt string) *AgentInfo {
	return &AgentInfo{
		Model:        model,
		Tools:        tools,
		Cfg:          cfg,
		Version:      version,
		SystemPrompt: systemPrompt,
	}
}

type Model interface {
	Complete(ctx context.Context, msgs []Message, tools []ToolsSpec, choice ToolChoice) (Completion, error)
}

type Tool interface {
	Spec() ToolsSpec
	Run(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

func assistantMessage(out Completion) Message {
	return Message{
		Role:      "assistant",
		Content:   out.Text,
		ToolCalls: out.ToolCalls,
	}
}

func toolResult(tool ToolCall, res json.RawMessage) Message {
	return Message{
		Role:       "tool",
		Content:    string(res),
		ToolCallID: tool.ID,
	}
}

func toolError(tool ToolCall, err string) Message {
	return Message{
		Role:       "tool",
		Content:    "error: " + err,
		ToolCallID: tool.ID,
	}
}

func specs(tools map[string]Tool) []ToolsSpec {
	specs := make([]ToolsSpec, 0, len(tools))
	for _, tool := range tools {
		specs = append(specs, tool.Spec())
	}
	return specs
}
