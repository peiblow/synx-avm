package ingress

import (
	"context"
	"encoding/json"
)

type AgentEvent struct {
	EventID       string          `json:"event_id"`
	AgentHash     string          `json:"agent_hash"`
	ContextID     string          `json:"context_id"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Source        string          `json:"source,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	EnqueuedAt    int64           `json:"enqueued_at"`
}

type Delivery struct {
	Event AgentEvent
	Ack   func() error
	Dead  func(reason string) error
}

type EventSource interface {
	Consume(ctx context.Context) (Delivery, error)
}
