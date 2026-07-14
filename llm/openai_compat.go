package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/peiblow/avm/agent"
)

type OpenAICompatModel struct {
	baseURL   string
	apiKey    string
	model     string
	cfg       agent.AgentCfg
	http      *http.Client
	normalize toolCallParser
}

func NewOpenAICompatModel(baseURL, apiKey, model string, cfg agent.AgentCfg) *OpenAICompatModel {
	return &OpenAICompatModel{
		baseURL:   baseURL,
		apiKey:    apiKey,
		model:     model,
		cfg:       cfg,
		normalize: normalizerFor(model),
		http:      &http.Client{Timeout: clientTimeout()},
	}
}

func (m *OpenAICompatModel) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolsSpec, choice agent.ToolChoice) (agent.Completion, error) {
	reqBody := chatRequest{
		Model:       m.model,
		Messages:    toWireMessages(msgs),
		Temperature: m.cfg.Temperature,
		MaxTokens:   m.cfg.MaxTokens,
		Tools:       toWireTools(tools),
	}
	if choice == agent.ChoiceRequired && len(tools) > 0 {
		reqBody.ToolChoice = "required"
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return agent.Completion{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return agent.Completion{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.apiKey)

	resp, err := m.http.Do(req)
	if err != nil {
		return agent.Completion{}, fmt.Errorf("call provider: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return agent.Completion{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if m.normalize != nil {
			if calls := m.normalize(failedGeneration(body)); len(calls) > 0 {
				return agent.Completion{ToolCalls: calls}, nil
			}
		}
		return agent.Completion{}, fmt.Errorf("provider returned %d: %s", resp.StatusCode, string(body))
	}

	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return agent.Completion{}, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return agent.Completion{}, fmt.Errorf("provider returned no choices")
	}

	msg := out.Choices[0].Message
	calls := fromWireToolCalls(msg.ToolCalls)
	content := msg.Content
	if m.normalize != nil {
		content = stripThinking(content)
		if len(calls) == 0 {
			if embedded := m.normalize(content); len(embedded) > 0 {
				calls = embedded
				content = ""
			}
		}
	}
	return agent.Completion{Text: content, ToolCalls: calls}, nil
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Tools       []chatTool    `json:"tools,omitempty"`
	ToolChoice  any           `json:"tool_choice,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolCallFunc `json:"function"`
}

type chatToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatToolFunc `json:"function"`
}

type chatToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func toWireMessages(msgs []agent.Message) []chatMessage {
	out := make([]chatMessage, len(msgs))
	for i, m := range msgs {
		cm := chatMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: chatToolCallFunc{
					Name:      tc.Name,
					Arguments: string(tc.Input),
				},
			})
		}
		out[i] = cm
	}
	return out
}

type providerError struct {
	Error struct {
		Code             string `json:"code"`
		FailedGeneration string `json:"failed_generation"`
	} `json:"error"`
}

func failedGeneration(body []byte) string {
	var e providerError
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	if e.Error.Code != "tool_use_failed" {
		return ""
	}
	return e.Error.FailedGeneration
}

func fromWireToolCalls(tcs []chatToolCall) []agent.ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]agent.ToolCall, len(tcs))
	for i, tc := range tcs {
		out[i] = agent.ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		}
	}
	return out
}

func toWireTools(tools []agent.ToolsSpec) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatTool, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out[i] = chatTool{
			Type: "function",
			Function: chatToolFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return out
}
