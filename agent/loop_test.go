package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type scriptedModel struct {
	steps []stepFn
	calls int
	mu    sync.Mutex
}

type stepFn func() (Completion, error)

func (m *scriptedModel) Complete(ctx context.Context, msgs []Message, tools []ToolsSpec, choice ToolChoice) (Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls >= len(m.steps) {
		return Completion{}, fmt.Errorf("scriptedModel: no step %d", m.calls)
	}
	fn := m.steps[m.calls]
	m.calls++
	return fn()
}

type recordingTool struct {
	name  string
	runs  int
	deny  bool
	mu    sync.Mutex
}

func (t *recordingTool) Spec() ToolsSpec { return ToolsSpec{Name: t.name} }

func (t *recordingTool) Run(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	t.mu.Lock()
	t.runs++
	t.mu.Unlock()
	if t.deny {
		return json.RawMessage(`{"decision":"REJECTED"}`), ErrDenied
	}
	return json.RawMessage(`{"ok":true}`), nil
}

type memCheckpoint struct {
	mu    sync.Mutex
	saved []Message
	count int
}

func (c *memCheckpoint) Save(ctx context.Context, msgs []Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saved = append([]Message(nil), msgs...)
	c.count++
	return nil
}

func call(name string) Completion {
	return Completion{ToolCalls: []ToolCall{{ID: name + "-1", Name: name, Input: json.RawMessage(`{}`)}}}
}

func newAgent(model Model, tools map[string]Tool, cfg AgentCfg) *AgentInfo {
	return NewAgent(model, tools, cfg, "1.0.0", "system prompt")
}

func TestBackoffRetriesTransientThenSucceeds(t *testing.T) {
	tool := &recordingTool{name: "act"}
	model := &scriptedModel{steps: []stepFn{
		func() (Completion, error) { return Completion{}, Transient(errors.New("429")) },
		func() (Completion, error) { return Completion{}, Transient(errors.New("429")) },
		func() (Completion, error) { return call("act"), nil },
		func() (Completion, error) { return Completion{Text: "done"}, nil },
	}}
	agt := newAgent(model, map[string]Tool{"act": tool}, AgentCfg{MaxSteps: 5})

	msgs, err := agt.Run(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected success after transient retries, got %v", err)
	}
	if model.calls != 4 {
		t.Fatalf("expected 4 model calls (2 transient + 2 real), got %d", model.calls)
	}
	if tool.runs != 1 {
		t.Fatalf("expected tool to run once, got %d", tool.runs)
	}
	if len(msgs) == 0 {
		t.Fatal("expected a transcript")
	}
}

func TestFatalErrorIsNotRetried(t *testing.T) {
	model := &scriptedModel{steps: []stepFn{
		func() (Completion, error) { return Completion{}, errors.New("400 bad request") },
	}}
	agt := newAgent(model, map[string]Tool{}, AgentCfg{MaxSteps: 5})

	_, err := agt.Run(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected fatal error to propagate")
	}
	if IsTransient(err) {
		t.Fatal("error should not be transient")
	}
	if model.calls != 1 {
		t.Fatalf("expected exactly 1 model call (no retry), got %d", model.calls)
	}
}

func TestCheckpointCapturesProgressWithoutSystem(t *testing.T) {
	tool := &recordingTool{name: "act"}
	model := &scriptedModel{steps: []stepFn{
		func() (Completion, error) { return call("act"), nil },
		func() (Completion, error) { return Completion{Text: "done"}, nil },
	}}
	agt := newAgent(model, map[string]Tool{"act": tool}, AgentCfg{MaxSteps: 5})
	cp := &memCheckpoint{}

	_, err := agt.Run(context.Background(), []Message{{Role: "user", Content: "hi"}}, cp)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if cp.count == 0 {
		t.Fatal("expected at least one checkpoint save")
	}
	for _, m := range cp.saved {
		if m.Role == "system" {
			t.Fatal("checkpoint must not contain the system prompt")
		}
	}
	var sawTool bool
	for _, m := range cp.saved {
		if m.Role == "tool" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatal("checkpoint should contain the tool result after execution")
	}
}

func TestResumeDoesNotRerunCompletedTools(t *testing.T) {
	tool := &recordingTool{name: "act"}
	model := &scriptedModel{steps: []stepFn{
		func() (Completion, error) { return call("act"), nil },
		func() (Completion, error) { return Completion{}, Transient(errors.New("429")) },
		func() (Completion, error) { return Completion{}, Transient(errors.New("429")) },
		func() (Completion, error) { return Completion{}, Transient(errors.New("429")) },
		func() (Completion, error) { return Completion{}, Transient(errors.New("429")) },
		func() (Completion, error) { return Completion{}, Transient(errors.New("429")) },
	}}
	agt := newAgent(model, map[string]Tool{"act": tool}, AgentCfg{MaxSteps: 5})
	cp := &memCheckpoint{}

	_, err := agt.Run(context.Background(), []Message{{Role: "user", Content: "hi"}}, cp)
	if err == nil || !IsTransient(err) {
		t.Fatalf("expected transient failure on second step, got %v", err)
	}
	if tool.runs != 1 {
		t.Fatalf("attempt 1 should have run the tool once, got %d", tool.runs)
	}

	resume := append([]Message(nil), cp.saved...)
	model2 := &scriptedModel{steps: []stepFn{
		func() (Completion, error) { return Completion{Text: "done"}, nil },
	}}
	agt2 := newAgent(model2, map[string]Tool{"act": tool}, AgentCfg{MaxSteps: 5})

	_, err = agt2.Run(context.Background(), resume, cp)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if tool.runs != 1 {
		t.Fatalf("resume must not re-run the completed tool: runs=%d", tool.runs)
	}
}

func TestOnDenyHaltStopsLoop(t *testing.T) {
	tool := &recordingTool{name: "act", deny: true}
	model := &scriptedModel{steps: []stepFn{
		func() (Completion, error) { return call("act"), nil },
		func() (Completion, error) { return call("act"), nil },
		func() (Completion, error) { return call("act"), nil },
	}}
	agt := newAgent(model, map[string]Tool{"act": tool}, AgentCfg{MaxSteps: 5, OnDeny: "halt"})

	_, err := agt.Run(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("halt should not error: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("halt should stop after first denied step, got %d model calls", model.calls)
	}
}
