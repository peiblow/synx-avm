package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizerForFamily(t *testing.T) {
	cases := map[string]bool{
		"llama-3.3-70b-versatile": true,
		"llama3.1:8b-16k":         true,
		"qwen3:8b":                true,
		"qwen2.5-coder:7b":        true,
		"gpt-4o":                  false,
		"openai/gpt-oss-120b":     false,
	}
	for model, wantParser := range cases {
		got := normalizerFor(model) != nil
		if got != wantParser {
			t.Errorf("normalizerFor(%q): hasParser=%v, want %v", model, got, wantParser)
		}
	}
}

func TestParseLlamaCalls(t *testing.T) {
	in := `<function=read_note {"path": "/Users/x/Synx/VVM.md"}</function>` + "\n"
	got := parseLlamaCalls(in)
	if len(got) != 1 {
		t.Fatalf("got %d calls, want 1", len(got))
	}
	if got[0].Name != "read_note" {
		t.Errorf("name=%q, want read_note", got[0].Name)
	}
	assertInput(t, got[0].Input, map[string]any{"path": "/Users/x/Synx/VVM.md"})
}

func TestParseLlamaCallsNameWithAngle(t *testing.T) {
	in := `<function=list_notes>{}</function>`
	got := parseLlamaCalls(in)
	if len(got) != 1 || got[0].Name != "list_notes" {
		t.Fatalf("got %+v, want single list_notes", got)
	}
}

func TestParseLlamaCallsMultiple(t *testing.T) {
	in := `<function=read_note {"path":"a"}</function> and <function=read_note {"path":"b"}</function>`
	got := parseLlamaCalls(in)
	if len(got) != 2 {
		t.Fatalf("got %d calls, want 2", len(got))
	}
}

func TestParseLlamaCallsSpecialChars(t *testing.T) {
	in := `<function=reply {"chat_id":"1","text":"a \"quote\" and\nnewline and } brace"}</function>`
	got := parseLlamaCalls(in)
	if len(got) != 1 || got[0].Name != "reply" {
		t.Fatalf("got %+v, want single reply", got)
	}
	assertInput(t, got[0].Input, map[string]any{
		"chat_id": "1",
		"text":    "a \"quote\" and\nnewline and } brace",
	})
}

func TestParseHermesCalls(t *testing.T) {
	in := `<tool_call>{"name": "read_note", "arguments": {"path": "x.md"}}</tool_call>`
	got := parseHermesCalls(in)
	if len(got) != 1 {
		t.Fatalf("got %d calls, want 1", len(got))
	}
	if got[0].Name != "read_note" {
		t.Errorf("name=%q, want read_note", got[0].Name)
	}
	assertInput(t, got[0].Input, map[string]any{"path": "x.md"})
}

func TestParseHermesCallsNoArgs(t *testing.T) {
	in := `<tool_call>{"name": "list_notes"}</tool_call>`
	got := parseHermesCalls(in)
	if len(got) != 1 || string(got[0].Input) != "{}" {
		t.Fatalf("got %+v, want list_notes with {}", got)
	}
}

func TestStripThinking(t *testing.T) {
	in := "<think>let me reason\nover lines</think>\nthe real answer"
	if out := stripThinking(in); out != "the real answer" {
		t.Errorf("stripThinking=%q, want %q", out, "the real answer")
	}
}

func assertInput(t *testing.T, raw json.RawMessage, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("input not valid json: %v (%s)", err, raw)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("input=%v, want %v", got, want)
	}
}
