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

func (c *Consumer) Start(ctx context.Context, cw agent.ContextWindow, cfg Config) error {
	slog.Info("consumer started", "max_concurrent", cfg.MaxConcurrent)

	sem := make(chan struct{}, cfg.MaxConcurrent)
	var wg sync.WaitGroup

	if rc, ok := c.Source.(reclaimer); ok {
		go c.reclaimLoop(ctx, cw, cfg, rc, sem, &wg)
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
		c.dispatch(ctx, cw, d, sem, &wg)
	}
}

func (c *Consumer) reclaimLoop(ctx context.Context, cw agent.ContextWindow, cfg Config, rc reclaimer, sem chan struct{}, wg *sync.WaitGroup) {
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
				c.dispatch(ctx, cw, d, sem, wg)
			}
		}
	}
}

// dispatch resolves the agent and runs it under the concurrency semaphore. A
// failed resolve leaves the entry pending (no ack) so the reclaimer retries it,
// bounded by maxDeliveries — it never silently drops.
func (c *Consumer) dispatch(ctx context.Context, cw agent.ContextWindow, d Delivery, sem chan struct{}, wg *sync.WaitGroup) {
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
		c.handle(ctx, cw, d, agt)
	}()
}

func (c *Consumer) handle(ctx context.Context, cw agent.ContextWindow, d Delivery, agt *agent.AgentInfo) {
	userMsgs, err := msgsFromEvent(d.Event)
	if err != nil {
		slog.Error("building messages", "event", d.Event.EventID, "error", err)
		deadLetter(d, err.Error())
		return
	}

	msgs, err := cw.LoadTurn(ctx, d.Event.EventID)
	if err != nil {
		slog.Error("loading turn checkpoint", "event", d.Event.EventID, "error", err)
	}
	if len(msgs) > 0 {
		slog.Info("resuming turn from checkpoint", "event", d.Event.EventID, "messages", len(msgs))
	} else {
		prior, _ := cw.Load(ctx, d.Event.ContextID)
		msgs = append(prior, userMsgs...)
	}

	runCtx := registry.WithCorrelation(ctx, d.Event.CorrelationID)
	runCtx = registry.WithContextID(runCtx, d.Event.ContextID)

	cp := agent.CheckpointFunc(func(ctx context.Context, m []agent.Message) error {
		return cw.SaveTurn(ctx, d.Event.EventID, m)
	})

	resp, err := agt.Run(runCtx, msgs, cp)
	if err != nil {
		if agent.IsTransient(err) {
			slog.Warn("agent run transient failure, leaving pending for reclaim", "agent", d.Event.AgentHash, "event", d.Event.EventID, "deliveries", d.Deliveries, "error", err)
			return
		}
		slog.Error("agent run failed, dead-lettering", "agent", d.Event.AgentHash, "event", d.Event.EventID, "deliveries", d.Deliveries, "error", err)
		_ = cw.DropTurn(ctx, d.Event.EventID)
		deadLetter(d, err.Error())
		return
	}

	persisted := resp[:0:0]
	for _, m := range resp {
		if m.Role != "system" {
			persisted = append(persisted, m)
		}
	}

	if err := cw.Replace(ctx, d.Event.ContextID, persisted); err != nil {
		slog.Error("context window replace failed", "event", d.Event.EventID, "error", err)
		return
	}
	_ = cw.DropTurn(ctx, d.Event.EventID)

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
