package registry

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/peiblow/avm/database"
	"github.com/peiblow/avm/smcp"
)

type fakeGate struct{ decision string }

func (g fakeGate) Call(ctx context.Context, gateName string, input json.RawMessage) (*smcp.Result, error) {
	data, _ := json.Marshal(map[string]string{"text": "implement the trigger"})
	return &smcp.Result{
		Decision: g.decision,
		Raw:      json.RawMessage(`{"decision":"` + g.decision + `"}`),
		Steps: []smcp.StepResult{{
			Function: "Handoff.toCoder",
			Status:   "approved",
			Events:   []smcp.StepEvent{{Type: "HandoffAllowed", Data: data}},
		}},
	}, nil
}

func TestExecuteDispatchEnqueuesToInbox(t *testing.T) {
	if os.Getenv("SYNX_IT") == "" {
		t.Skip("integration test; set SYNX_IT=1 with redis up")
	}

	rdb, err := database.NewRedisClient()
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	ctx := context.Background()
	target := "0xCODER_TEST"
	t.Setenv("CODER_HASH", target)

	tool := &contractTool{gateName: "handoff_to_coder", rdb: rdb}
	action := &ToolAction{Type: "dispatch", Agent: "getEnv(CODER_HASH)"}
	body := []byte(`{"text":"implement the trigger"}`)

	out, err := tool.executeDispatch(ctx, action, json.RawMessage(`{}`), body)
	if err != nil {
		t.Fatalf("executeDispatch: %v", err)
	}

	var res struct {
		DispatchedTo string `json:"dispatched_to"`
		StreamID     string `json:"stream_id"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if res.DispatchedTo != target {
		t.Fatalf("dispatched_to = %q, want %q", res.DispatchedTo, target)
	}

	msgs, err := rdb.Client.XRange(ctx, "synx:inbox", res.StreamID, res.StreamID).Result()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("XRange got %d msgs, err=%v", len(msgs), err)
	}

	var ev struct {
		AgentHash string          `json:"agent_hash"`
		Source    string          `json:"source"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(msgs[0].Values["data"].(string)), &ev); err != nil {
		t.Fatal(err)
	}

	if ev.AgentHash != target {
		t.Errorf("agent_hash = %q, want %q", ev.AgentHash, target)
	}
	if ev.Source != "handoff:handoff_to_coder" {
		t.Errorf("source = %q", ev.Source)
	}

	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Text != "implement the trigger" {
		t.Errorf("payload.text = %q (err=%v)", p.Text, err)
	}

	rdb.Client.XDel(ctx, "synx:inbox", res.StreamID)
}

func TestDispatchThroughToolRunGated(t *testing.T) {
	if os.Getenv("SYNX_IT") == "" {
		t.Skip("integration test; set SYNX_IT=1 with redis up")
	}

	rdb, err := database.NewRedisClient()
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	ctx := context.Background()
	target := "0xCODER_GATED"
	t.Setenv("CODER_HASH", target)

	tool := &contractTool{
		gateName: "handoff_to_coder",
		rdb:      rdb,
		bridge:   fakeGate{decision: "APPROVED"},
		steps: []ToolStep{{
			Function: "Handoff.toCoder",
			Action:   &ToolAction{Type: "dispatch", Agent: "getEnv(CODER_HASH)"},
		}},
	}

	out, err := tool.Run(ctx, json.RawMessage(`{"task":"implement the trigger"}`))
	if err != nil {
		t.Fatalf("tool.Run: %v", err)
	}

	var combined struct {
		Actions []struct {
			DispatchedTo string `json:"dispatched_to"`
			StreamID     string `json:"stream_id"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(out, &combined); err != nil {
		t.Fatal(err)
	}
	if len(combined.Actions) != 1 || combined.Actions[0].DispatchedTo != target {
		t.Fatalf("actions = %+v, want 1 dispatched to %s", combined.Actions, target)
	}
	streamID := combined.Actions[0].StreamID

	msgs, err := rdb.Client.XRange(ctx, "synx:inbox", streamID, streamID).Result()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("XRange got %d msgs, err=%v", len(msgs), err)
	}
	var ev struct {
		AgentHash string          `json:"agent_hash"`
		Payload   json.RawMessage `json:"payload"`
	}
	json.Unmarshal([]byte(msgs[0].Values["data"].(string)), &ev)
	if ev.AgentHash != target {
		t.Errorf("agent_hash = %q, want %q", ev.AgentHash, target)
	}
	var p struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(ev.Payload, &p); p.Text != "implement the trigger" {
		t.Errorf("payload.text = %q", p.Text)
	}

	rdb.Client.XDel(ctx, "synx:inbox", streamID)
}

func TestDispatchPropagatesCorrelation(t *testing.T) {
	if os.Getenv("SYNX_IT") == "" {
		t.Skip("integration test; set SYNX_IT=1 with redis up")
	}

	rdb, err := database.NewRedisClient()
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	corr := "corr-notion-card-42"
	ctx := WithCorrelation(context.Background(), corr)
	t.Setenv("CODER_HASH", "0xCODER_CORR")

	tool := &contractTool{gateName: "handoff_to_coder", rdb: rdb}
	out, err := tool.executeDispatch(ctx, &ToolAction{Type: "dispatch", Agent: "getEnv(CODER_HASH)"},
		json.RawMessage(`{}`), []byte(`{"text":"go"}`))
	if err != nil {
		t.Fatalf("executeDispatch: %v", err)
	}

	var res struct {
		StreamID string `json:"stream_id"`
	}
	json.Unmarshal(out, &res)

	msgs, err := rdb.Client.XRange(ctx, "synx:inbox", res.StreamID, res.StreamID).Result()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("XRange got %d msgs, err=%v", len(msgs), err)
	}
	var ev struct {
		CorrelationID string `json:"correlation_id"`
	}
	json.Unmarshal([]byte(msgs[0].Values["data"].(string)), &ev)
	if ev.CorrelationID != corr {
		t.Errorf("correlation_id = %q, want %q", ev.CorrelationID, corr)
	}

	rdb.Client.XDel(ctx, "synx:inbox", res.StreamID)
	rdb.Client.Del(ctx, "synx:seen:dispatch:"+corr+":handoff_to_coder:0xCODER_CORR")
}

func TestDispatchDedupesRepeatedHandoff(t *testing.T) {
	if os.Getenv("SYNX_IT") == "" {
		t.Skip("integration test; set SYNX_IT=1 with redis up")
	}

	rdb, err := database.NewRedisClient()
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	corr := "corr-dedup-" + randID()
	ctx := WithCorrelation(context.Background(), corr)
	target := "0xCODER_DEDUP"
	t.Setenv("CODER_HASH", target)

	tool := &contractTool{gateName: "handoff_to_coder", rdb: rdb}
	action := &ToolAction{Type: "dispatch", Agent: "getEnv(CODER_HASH)"}
	body := []byte(`{"text":"go"}`)

	before, _ := rdb.Client.XLen(ctx, "synx:inbox").Result()

	out1, err := tool.executeDispatch(ctx, action, json.RawMessage(`{}`), body)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	out2, err := tool.executeDispatch(ctx, action, json.RawMessage(`{}`), body)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}

	after, _ := rdb.Client.XLen(ctx, "synx:inbox").Result()
	if after-before != 1 {
		t.Fatalf("repeated handoff must enqueue once, inbox grew by %d", after-before)
	}

	var r1 struct {
		StreamID string `json:"stream_id"`
	}
	json.Unmarshal(out1, &r1)
	var r2 struct {
		Deduped string `json:"deduped"`
	}
	json.Unmarshal(out2, &r2)
	if r1.StreamID == "" {
		t.Errorf("first dispatch should return a stream_id, got %s", string(out1))
	}
	if r2.Deduped != "true" {
		t.Errorf("second dispatch should be deduped, got %s", string(out2))
	}

	rdb.Client.XDel(ctx, "synx:inbox", r1.StreamID)
	rdb.Client.Del(ctx, "synx:seen:dispatch:"+corr+":handoff_to_coder:"+target)
}

func TestDispatchDeniedByGateSkipsEnqueue(t *testing.T) {
	if os.Getenv("SYNX_IT") == "" {
		t.Skip("integration test; set SYNX_IT=1 with redis up")
	}

	rdb, err := database.NewRedisClient()
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	ctx := context.Background()
	t.Setenv("CODER_HASH", "0xCODER_DENIED")

	before, _ := rdb.Client.XLen(ctx, "synx:inbox").Result()

	tool := &contractTool{
		gateName: "handoff_to_coder",
		rdb:      rdb,
		bridge:   fakeGate{decision: "DENIED"},
		steps: []ToolStep{{
			Function: "Handoff.toCoder",
			Action:   &ToolAction{Type: "dispatch", Agent: "getEnv(CODER_HASH)"},
		}},
	}

	if _, err := tool.Run(ctx, json.RawMessage(`{"task":"x"}`)); err != nil {
		t.Fatalf("tool.Run: %v", err)
	}

	after, _ := rdb.Client.XLen(ctx, "synx:inbox").Result()
	if after != before {
		t.Fatalf("DENIED gate must not enqueue: inbox grew %d → %d", before, after)
	}
}
