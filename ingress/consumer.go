package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/peiblow/avm/agent"
	"github.com/peiblow/avm/registry"
)

type Consumer struct {
	Source   EventSource
	registry registry.Registry
}

func NewConsumer(source EventSource, registry registry.Registry) *Consumer {
	return &Consumer{
		Source:   source,
		registry: registry,
	}
}

func (c *Consumer) Start(ctx context.Context, mem agent.Memory) error {
	slog.Info("consumer started")
	for {
		d, err := c.Source.Consume(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("consuming event", "error", err)
			continue
		}

		agt, err := c.registry.GetAgent(ctx, d.Event.AgentHash)
		if err != nil {
			slog.Error("resolving agent", "agent", d.Event.AgentHash, "event", d.Event.EventID, "error", err)
			deadLetter(d, "agent not found: "+err.Error())
			continue
		}

		go func(d Delivery, agt *agent.AgentInfo) {
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
				slog.Error("agent run failed", "agent", d.Event.AgentHash, "event", d.Event.EventID, "error", err)
				deadLetter(d, err.Error())
				return
			}

			mem.Append(ctx, d.Event.ContextID, userMsgs...)
			mem.Append(ctx, d.Event.ContextID, resp...)

			if err := d.Ack(); err != nil {
				slog.Error("ack failed", "event", d.Event.EventID, "error", err)
			}
			slog.Info("agent turn completed", "agent", d.Event.AgentHash, "event", d.Event.EventID, "correlation", d.Event.CorrelationID)
		}(d, agt)
	}
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
