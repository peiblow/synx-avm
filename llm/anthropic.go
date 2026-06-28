package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/peiblow/avm/agent"
)

type AnthropicModel struct {
	baseURL string
	apiKey  string
	model   string
	cfg     agent.AgentCfg
	http    *http.Client
}

func NewAnthropicModel(baseURL, apiKey, model string, cfg agent.AgentCfg) *AnthropicModel {
	return &AnthropicModel{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		cfg:     cfg,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (m *AnthropicModel) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolsSpec) (agent.Completion, error) {
	system, messages := toAnthropicMessages(msgs)

	maxTokens := m.cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = 1024
	}

	body := map[string]interface{}{
		"model":      m.model,
		"max_tokens": maxTokens,
		"messages":   messages,
	}
	if system != "" {
		body["system"] = []map[string]interface{}{{
			"type":          "text",
			"text":          system,
			"cache_control": map[string]string{"type": "ephemeral"},
		}}
	}
	if m.cfg.Temperature != 0 {
		body["temperature"] = m.cfg.Temperature
	}
	if at := toAnthropicTools(tools); len(at) > 0 {
		at[len(at)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
		body["tools"] = at
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return agent.Completion{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return agent.Completion{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", m.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := m.http.Do(req)
	if err != nil {
		return agent.Completion{}, fmt.Errorf("call provider: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return agent.Completion{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return agent.Completion{}, fmt.Errorf("provider returned %d: %s", resp.StatusCode, string(respBody))
	}

	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return agent.Completion{}, fmt.Errorf("decode response: %w", err)
	}

	var completion agent.Completion
	var textParts []string
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				textParts = append(textParts, b.Text)
			}
		case "tool_use":
			completion.ToolCalls = append(completion.ToolCalls, agent.ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Input,
			})
		}
	}
	completion.Text = strings.Join(textParts, "\n")

	return completion, nil
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

func toAnthropicMessages(msgs []agent.Message) (string, []anthropicMessage) {
	var system string
	var out []anthropicMessage
	var toolResults []anthropicBlock

	flush := func() {
		if len(toolResults) > 0 {
			out = append(out, anthropicMessage{Role: "user", Content: toolResults})
			toolResults = nil
		}
	}

	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			system = msg.Content
		case "tool":
			toolResults = append(toolResults, anthropicBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			})
		case "user":
			flush()
			out = append(out, anthropicMessage{Role: "user", Content: msg.Content})
		case "assistant":
			flush()
			var blocks []anthropicBlock
			if msg.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				input := tc.Input
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: input,
				})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		}
	}
	flush()

	return system, out
}

func toAnthropicTools(tools []agent.ToolsSpec) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(tools))
	for _, t := range tools {
		schema := t.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, map[string]interface{}{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": schema,
		})
	}
	return out
}
