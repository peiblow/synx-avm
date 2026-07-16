package llm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/peiblow/avm/agent"
)

type toolCallParser func(text string, tools []agent.ToolsSpec) []agent.ToolCall

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

// singleParam returns the parameter name of a tool that takes exactly one
// argument, so a bare value in a malformed call can be mapped to it.
func singleParam(tools []agent.ToolsSpec, name string) string {
	for _, t := range tools {
		if t.Name != name {
			continue
		}
		var schema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(t.Parameters, &schema) != nil {
			return ""
		}
		if len(schema.Required) == 1 {
			return schema.Required[0]
		}
		if len(schema.Properties) == 1 {
			for k := range schema.Properties {
				return k
			}
		}
		return ""
	}
	return ""
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

func stripThinking(s string) string {
	return strings.TrimSpace(thinkRe.ReplaceAllString(s, ""))
}

var llamaHeadRe = regexp.MustCompile(`<function=([A-Za-z0-9_.-]+)`)

// parseLlamaCalls extracts tool calls from Llama's malformed pseudo-tool syntax
// without caring how it delimits the arguments. Every variant Groq's Llama emits
// is `<function=NAME` followed by an argument blob up to `</function>`; the blob
// is either a JSON object or a bare value. We locate each header, take the
// segment up to the closing tag (or the next header, or end of string), and pull
// the first balanced JSON object out of it — falling back to a bare value mapped
// onto the tool's single parameter. This is deliberately format-agnostic so new
// spacing/delimiter quirks don't need a new special case.
func parseLlamaCalls(s string, tools []agent.ToolsSpec) []agent.ToolCall {
	var out []agent.ToolCall
	heads := llamaHeadRe.FindAllStringSubmatchIndex(s, -1)
	for i, h := range heads {
		name := s[h[2]:h[3]]

		segStart := h[1]
		segEnd := len(s)
		if close := strings.Index(s[segStart:], "</function>"); close >= 0 {
			segEnd = segStart + close
		}
		if i+1 < len(heads) && heads[i+1][0] < segEnd {
			segEnd = heads[i+1][0]
		}

		args := llamaArgs(s[segStart:segEnd], tools, name)
		if args == nil {
			continue
		}
		out = append(out, agent.ToolCall{ID: toolCallID(), Name: name, Input: args})
	}
	return out
}

// llamaArgs turns an argument segment into JSON: a balanced JSON object if the
// segment carries one, otherwise the bare remainder mapped onto the tool's lone
// parameter, otherwise an empty object for a no-arg tool.
func llamaArgs(seg string, tools []agent.ToolsSpec, name string) json.RawMessage {
	if obj := firstJSONObject(seg); obj != "" {
		return json.RawMessage(obj)
	}
	rest := strings.TrimSpace(strings.TrimLeft(seg, "> \t\r\n"))
	if rest == "" {
		return json.RawMessage("{}")
	}
	param := singleParam(tools, name)
	if param == "" {
		return nil
	}
	wrapped, err := json.Marshal(map[string]string{param: rest})
	if err != nil {
		return nil
	}
	return wrapped
}

// firstJSONObject returns the first balanced, valid JSON object in s (respecting
// string literals), or "" if there is none.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				if cand := s[start : i+1]; json.Valid([]byte(cand)) {
					return cand
				}
				return ""
			}
		}
	}
	return ""
}

func parseHermesCalls(s string, _ []agent.ToolsSpec) []agent.ToolCall {
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
