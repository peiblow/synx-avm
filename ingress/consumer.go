package ingress

import (
	"context"
	"encoding/json"
	"fmt"

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
	fmt.Println("Consumer started")
	for {
		d, err := c.Source.Consume(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Println("Error consuming event:", err)
			continue
		}

		agt, err := c.registry.GetAgent(ctx, d.Event.AgentHash)
		if err != nil {
			fmt.Println("Error getting agent:", err)
			continue
		}

		go func(d Delivery, agt *agent.AgentInfo) {
			defer func() {
				if err := d.Ack(); err != nil {
					fmt.Println("ack failed:", err)
				}
			}()

			userMsgs, err := msgsFromEvent(d.Event)
			if err != nil {
				fmt.Println("Error building messages:", err)
				return
			}

			prior, _ := mem.Load(ctx, d.Event.ContextID)
			msgs := append(prior, userMsgs...)

			resp, err := agt.Run(ctx, msgs)
			if err != nil {
				fmt.Println("Error running agent:", err)
				return
			}

			mem.Append(ctx, d.Event.ContextID, userMsgs...)
			mem.Append(ctx, d.Event.ContextID, resp...)

			for _, m := range resp {
				fmt.Println("Agent response:", m.Content)
			}
		}(d, agt)
	}
}

func msgsFromEvent(ev AgentEvent) ([]agent.Message, error) {
	var p struct {
		Text string `json:"text"`
	}

	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload inválido: %w", err)
	}
	if p.Text == "" {
		return nil, fmt.Errorf("payload sem campo 'text'")
	}

	return []agent.Message{{Role: "user", Content: p.Text}}, nil
}
