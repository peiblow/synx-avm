package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/peiblow/avm/agent"
	"github.com/peiblow/avm/smcp"
)

const maxDepth = 5

type contractTool struct {
	spec     agent.ToolsSpec
	gateName string
	steps    []ToolStep
	bridge   *smcp.Bridge
	reg      Registry
	http     *http.Client
}

func newContractTool(def ToolDef, bridge *smcp.Bridge, reg Registry) *contractTool {
	return &contractTool{
		spec: agent.ToolsSpec{
			Name:        sanitizeName(def.Name),
			Description: def.Description,
			Parameters:  buildInputSchema(def.Steps),
		},
		gateName: def.Name,
		steps:    def.Steps,
		bridge:   bridge,
		reg:      reg,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *contractTool) Spec() agent.ToolsSpec { return t.spec }

func (t *contractTool) Run(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	res, err := t.bridge.Call(ctx, t.gateName, input)
	if err != nil {
		return nil, err
	}

	if res.Decision != "APPROVED" {
		return res.Raw, nil
	}

	outputs := make([]json.RawMessage, 0, len(t.steps))
	for _, step := range t.steps {
		var out json.RawMessage
		var err error
		switch {
		case step.Action != nil:
			out, err = t.executeAction(ctx, step.Action, input)
			if err != nil {
				return nil, fmt.Errorf("action %s %s: %w", step.Action.Method, step.Action.Url, err)
			}
		case step.Delegate != "":
			out, err = t.delegate(ctx, step.Delegate, input)
			if err != nil {
				return nil, fmt.Errorf("delegate %s: %w", step.Delegate, err)
			}
		default:
			continue
		}
		outputs = append(outputs, out)
	}

	return combine(res.Raw, outputs), nil
}

func (t *contractTool) delegate(ctx context.Context, target string, input json.RawMessage) (json.RawMessage, error) {
	depth := depthFrom(ctx)
	if depth >= maxDepth {
		return nil, fmt.Errorf("max delegation depth %d exceeded", maxDepth)
	}

	agt, err := t.reg.GetAgent(ctx, target)
	if err != nil {
		return nil, err
	}

	msgs, err := agt.Run(withDepth(ctx, depth+1), []agent.Message{
		{Role: "user", Content: string(input)},
	})
	if err != nil {
		return nil, err
	}

	final := ""
	if len(msgs) > 0 {
		final = msgs[len(msgs)-1].Content
	}
	out, _ := json.Marshal(struct {
		Agent  string `json:"agent"`
		Output string `json:"output"`
	}{Agent: target, Output: final})
	return out, nil
}

func (t *contractTool) executeAction(ctx context.Context, action *ToolAction, input json.RawMessage) (json.RawMessage, error) {
	url, err := resolveURL(action.Url)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, action.Method, url, bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return json.RawMessage(body), nil
}

func combine(decision json.RawMessage, outputs []json.RawMessage) json.RawMessage {
	out, err := json.Marshal(struct {
		Decision json.RawMessage   `json:"decision"`
		Actions  []json.RawMessage `json:"actions,omitempty"`
	}{Decision: decision, Actions: outputs})
	if err != nil {
		return decision
	}
	return out
}

type depthKey struct{}

func depthFrom(ctx context.Context) int {
	d, _ := ctx.Value(depthKey{}).(int)
	return d
}

func withDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, depthKey{}, d)
}

var nameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeName(name string) string {
	return nameRe.ReplaceAllString(name, "_")
}

var getEnvRe = regexp.MustCompile(`^getEnv\(([^)]+)\)$`)

func resolveURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	m := getEnvRe.FindStringSubmatch(raw)
	if m == nil {
		return raw, nil
	}

	name := strings.Trim(strings.TrimSpace(m[1]), `"'`)
	val := os.Getenv(name)
	if val == "" {
		return "", fmt.Errorf("env %s not set", name)
	}
	return val, nil
}

func buildInputSchema(steps []ToolStep) json.RawMessage {
	props := map[string]any{}
	required := []string{}
	seen := map[string]bool{}

	for _, s := range steps {
		for _, in := range s.Input {
			if seen[in.Name] {
				continue
			}
			seen[in.Name] = true
			props[in.Name] = map[string]string{"type": jsonType(in.Type)}
			required = append(required, in.Name)
		}
	}

	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}

	b, _ := json.Marshal(schema)
	return b
}

func jsonType(t string) string {
	switch t {
	case "UInt", "Int":
		return "integer"
	default:
		return "string"
	}
}
