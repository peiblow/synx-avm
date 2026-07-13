package llm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/peiblow/avm/agent"
)

type toolCallParser func(text string) []agent.ToolCall

func normalizerFor(model string) toolCallParser {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "qwen"):
		return parseHermesCalls
	case strings.Contains(m, "llama"):
		return parseLlamaCalls
	default:
		return nil
	}
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

func stripThinking(s string) string {
	return strings.TrimSpace(thinkRe.ReplaceAllString(s, ""))
}

func parseLlamaCalls(s string) []agent.ToolCall {
	var out []agent.ToolCall
	for {
		open := strings.Index(s, "<function=")
		if open < 0 {
			break
		}
		s = s[open+len("<function="):]
		end := strings.Index(s, "</function>")
		if end < 0 {
			break
		}
		inner := s[:end]
		s = s[end+len("</function>"):]

		brace := strings.Index(inner, "{")
		if brace < 0 {
			continue
		}
		name := strings.TrimRight(strings.TrimSpace(inner[:brace]), ">")
		args := strings.TrimSpace(inner[brace:])
		if name == "" || !json.Valid([]byte(args)) {
			continue
		}
		out = append(out, agent.ToolCall{ID: toolCallID(), Name: name, Input: json.RawMessage(args)})
	}
	return out
}

func parseHermesCalls(s string) []agent.ToolCall {
	var out []agent.ToolCall
	for {
		open := strings.Index(s, "<tool_call>")
		if open < 0 {
			break
		}
		s = s[open+len("<tool_call>"):]
		end := strings.Index(s, "</tool_call>")
		if end < 0 {
			break
		}
		inner := strings.TrimSpace(s[:end])
		s = s[end+len("</tool_call>"):]

		var parsed struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(inner), &parsed) != nil || parsed.Name == "" {
			continue
		}
		args := parsed.Arguments
		if len(args) == 0 || !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		out = append(out, agent.ToolCall{ID: toolCallID(), Name: parsed.Name, Input: args})
	}
	return out
}

func toolCallID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "call_" + hex.EncodeToString(b)
}
