package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
			body := bodyForStep(res, step.Function, input)
			out, err = t.executeAction(ctx, step.Action, input, body)
			if err != nil {
				return nil, fmt.Errorf("action %s: %w", step.Action.Type, err)
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

func bodyForStep(res *smcp.Result, fn string, input json.RawMessage) []byte {
	for _, s := range res.Steps {
		if s.Function == fn && len(s.Events) > 0 {
			return eventBody(s.Events)
		}
	}
	return input
}

func eventBody(events []smcp.StepEvent) []byte {
	data := events[len(events)-1].Data
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) == nil && len(obj) == 1 {
		for _, v := range obj {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return []byte(s)
			}
			return v
		}
	}
	return data
}

func (t *contractTool) executeAction(ctx context.Context, action *ToolAction, input json.RawMessage, body []byte) (json.RawMessage, error) {
	switch action.Type {
	case "http", "":
		return t.executeHTTP(ctx, action, input, body)
	case "filesystem":
		return executeFilesystem(action, input, body)
	case "shell":
		return executeShell(ctx, action, input, body)
	default:
		return nil, fmt.Errorf("unknown action type: %s", action.Type)
	}
}

func (t *contractTool) executeHTTP(ctx context.Context, action *ToolAction, input json.RawMessage, body []byte) (json.RawMessage, error) {
	url, err := resolveTemplate(action.Url, input)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, action.Method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	return json.RawMessage(respBody), nil
}

func executeFilesystem(action *ToolAction, input json.RawMessage, body []byte) (json.RawMessage, error) {
	path, err := resolveTemplate(action.Path, input)
	if err != nil {
		return nil, err
	}

	switch action.Operation {
	case "read":
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return readDir(path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"path": path, "content": string(data)})
	case "write":
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"path": path, "bytes": len(body)})
	default:
		return nil, fmt.Errorf("unknown filesystem operation: %s", action.Operation)
	}
}

func readDir(root string) (json.RawMessage, error) {
	type fileEntry struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}

	var files []fileEntry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, fileEntry{Path: rel, Content: string(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]any{"dir": root, "files": files})
}

func executeShell(ctx context.Context, action *ToolAction, input json.RawMessage, body []byte) (json.RawMessage, error) {
	command, err := resolveTemplate(action.Command, input)
	if err != nil {
		return nil, err
	}

	args := make([]string, len(action.Args))
	for i, a := range action.Args {
		v, err := resolveTemplate(a, input)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = bytes.NewReader(body)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("shell %s: %w", command, err)
	}
	return json.Marshal(map[string]string{"stdout": string(out)})
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

var getEnvRe = regexp.MustCompile(`getEnv\(([^)]+)\)`)
var fieldRe = regexp.MustCompile(`\{([^}]+)\}`)

func resolveTemplate(raw string, input json.RawMessage) (string, error) {
	var missing string
	out := getEnvRe.ReplaceAllStringFunc(raw, func(m string) string {
		name := strings.Trim(strings.TrimSpace(getEnvRe.FindStringSubmatch(m)[1]), `"'`)
		val := os.Getenv(name)
		if val == "" {
			missing = name
		}
		return val
	})
	if missing != "" {
		return "", fmt.Errorf("env %s not set", missing)
	}

	if strings.Contains(out, "{") {
		var fields map[string]any
		_ = json.Unmarshal(input, &fields)
		out = fieldRe.ReplaceAllStringFunc(out, func(m string) string {
			key := strings.TrimSpace(m[1 : len(m)-1])
			if v, ok := fields[key]; ok {
				return fmt.Sprint(v)
			}
			return m
		})
	}

	return out, nil
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
