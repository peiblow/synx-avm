package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/peiblow/avm/agent"
	"github.com/peiblow/avm/registry"
)

type Consumer struct {
	Source   EventSource
	registry registry.Registry
}

// reclaimer is the optional capability a source may expose to bring stale PEL
// entries back. Sources that don't implement it simply run without reclaim.
type reclaimer interface {
	Reclaim(ctx context.Context, minIdle time.Duration, maxDeliveries, batch int64) ([]Delivery, error)
}

func NewConsumer(source EventSource, registry registry.Registry) *Consumer {
	return &Consumer{
		Source:   source,
		registry: registry,
	}
}

func (c *Consumer) Start(ctx context.Context, mem agent.Memory, cfg Config) error {
	slog.Info("consumer started", "max_concurrent", cfg.MaxConcurrent)

	sem := make(chan struct{}, cfg.MaxConcurrent)
	var wg sync.WaitGroup

	if rc, ok := c.Source.(reclaimer); ok {
		go c.reclaimLoop(ctx, mem, cfg, rc, sem, &wg)
	}

	for {
		d, err := c.Source.Consume(ctx)
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			slog.Error("consuming event", "error", err)
			continue
		}
		c.dispatch(ctx, mem, d, sem, &wg)
	}
}

func (c *Consumer) reclaimLoop(ctx context.Context, mem agent.Memory, cfg Config, rc reclaimer, sem chan struct{}, wg *sync.WaitGroup) {
	t := time.NewTicker(cfg.ReclaimInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ds, err := rc.Reclaim(ctx, cfg.ReclaimMinIdle, cfg.MaxDeliveries, cfg.ReclaimBatch)
			if err != nil {
				slog.Error("reclaim failed", "error", err)
				continue
			}
			for _, d := range ds {
				slog.Info("reclaimed pending event", "event", d.Event.EventID, "deliveries", d.Deliveries)
				c.dispatch(ctx, mem, d, sem, wg)
			}
		}
	}
}

// dispatch resolves the agent and runs it under the concurrency semaphore. A
// failed resolve leaves the entry pending (no ack) so the reclaimer retries it,
// bounded by maxDeliveries — it never silently drops.
func (c *Consumer) dispatch(ctx context.Context, mem agent.Memory, d Delivery, sem chan struct{}, wg *sync.WaitGroup) {
	agt, err := c.registry.GetAgent(ctx, d.Event.AgentHash)
	if err != nil {
		slog.Error("resolving agent", "agent", d.Event.AgentHash, "event", d.Event.EventID, "deliveries", d.Deliveries, "error", err)
		return
	}

	sem <- struct{}{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { <-sem }()
		c.handle(ctx, mem, d, agt)
	}()
}

func (c *Consumer) handle(ctx context.Context, mem agent.Memory, d Delivery, agt *agent.AgentInfo) {
	userMsgs, err := msgsFromEvent(d.Event)
	if err != nil {
		slog.Error("building messages", "event", d.Event.EventID, "error", err)
		deadLetter(d, err.Error())
		return
	}

	prior, _ := mem.Load(ctx, d.Event.ContextID)
	msgs := append(prior, userMsgs...)

	runCtx := registry.WithCorrelation(ctx, d.Event.CorrelationID)
	resp, err := agt.Run(runCtx, msgs)
	if err != nil {
		slog.Error("agent run failed", "agent", d.Event.AgentHash, "event", d.Event.EventID, "deliveries", d.Deliveries, "error", err)
		return
	}

	mem.Append(ctx, d.Event.ContextID, userMsgs...)
	mem.Append(ctx, d.Event.ContextID, resp...)

	if err := d.Ack(); err != nil {
		slog.Error("ack failed", "event", d.Event.EventID, "error", err)
	}
	slog.Info("agent turn completed", "agent", d.Event.AgentHash, "event", d.Event.EventID, "correlation", d.Event.CorrelationID, "deliveries", d.Deliveries)
}

func deadLetter(d Delivery, reason string) {
	if d.Dead == nil {
		return
	}
	if err := d.Dead(reason); err != nil {
		slog.Error("dead-letter failed", "event", d.Event.EventID, "error", err)
	}
}

func msgsFromEvent(ev AgentEvent) ([]agent.Message, error) {
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(ev.Payload, &p)

	content := p.Text
	if content == "" {
		content = string(ev.Payload)
	}
	if content == "" {
		return nil, fmt.Errorf("empty payload")
	}

	return []agent.Message{{Role: "user", Content: content}}, nil
}
