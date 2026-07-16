package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/peiblow/avm/database"
	goredis "github.com/redis/go-redis/v9"
)

// Run with: SYNX_IT=1 go test ./ingress -run Reclaim -v   (redis up on :6379)
func reclaimTestSetup(t *testing.T) (*database.RedisClient, *RedisSource, string, string) {
	t.Helper()
	if os.Getenv("SYNX_IT") == "" {
		t.Skip("integration test; set SYNX_IT=1 with redis up")
	}

	client, err := database.NewRedisClient()
	if err != nil {
		t.Fatalf("redis: %v", err)
	}

	ctx := context.Background()
	stream := fmt.Sprintf("synx:test:reclaim:%d", time.Now().UnixNano())
	group := "agent_awake"

	src, err := NewRedisSource(ctx, client, stream, group, "live")
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	ev := AgentEvent{EventID: "ev-1", AgentHash: "0xdeadbeef", ContextID: "ctx-1", Payload: json.RawMessage(`{"text":"hi"}`)}
	raw, _ := json.Marshal(ev)
	if _, err := client.XAdd(ctx, stream, raw); err != nil {
		t.Fatalf("xadd: %v", err)
	}

	// Simulate a consumer that read the entry and then died before acking:
	// the entry now sits in "dead"'s PEL, unacked.
	_, err = client.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group: group, Consumer: "dead", Streams: []string{stream, ">"}, Count: 1, Block: time.Second,
	}).Result()
	if err != nil {
		t.Fatalf("dead read: %v", err)
	}

	t.Cleanup(func() { _ = client.Del(ctx, stream, stream+":dead") })
	return client, src, stream, group
}

func TestReclaimBringsBackStalePending(t *testing.T) {
	client, src, _, _ := reclaimTestSetup(t)
	ctx := context.Background()

	// minIdle=0 → reclaim regardless of idle; maxDeliveries high → reprocess.
	ds, err := src.Reclaim(ctx, 0, 5, 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("expected 1 reclaimed delivery, got %d", len(ds))
	}
	if ds[0].Event.AgentHash != "0xdeadbeef" || ds[0].Event.EventID != "ev-1" {
		t.Fatalf("event not decoded: %+v", ds[0].Event)
	}
	if ds[0].Deliveries < 1 {
		t.Fatalf("expected delivery count >= 1, got %d", ds[0].Deliveries)
	}
	_ = client
}

func TestReclaimDeadLettersPoison(t *testing.T) {
	client, src, stream, group := reclaimTestSetup(t)
	ctx := context.Background()

	// maxDeliveries=2: each Reclaim XClaims (bumps retry). After the retry count
	// passes 2, the entry is poison and must go to the dead stream, not come back.
	const max = 2
	for i := 0; i < 6; i++ {
		if _, err := src.Reclaim(ctx, 0, max, 10); err != nil {
			t.Fatalf("reclaim %d: %v", i, err)
		}
	}

	deadLen, err := client.XLen(ctx, stream+":dead").Result()
	if err != nil {
		t.Fatalf("xlen dead: %v", err)
	}
	if deadLen != 1 {
		t.Fatalf("expected 1 dead-lettered entry, got %d", deadLen)
	}

	pending, err := client.XPending(ctx, stream, group).Result()
	if err != nil {
		t.Fatalf("xpending: %v", err)
	}
	if pending.Count != 0 {
		t.Fatalf("expected empty PEL after poison dead-letter, got %d", pending.Count)
	}
}
