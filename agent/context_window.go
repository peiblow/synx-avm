package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/peiblow/avm/database"
)

const (
	maxMessages = 20
	windowTTL   = 48 * time.Hour
	keyPrefix   = "synx:memory:"
)

type ContextWindow struct {
	rdb *database.RedisClient
}

func NewContextWindow(rdb *database.RedisClient) *ContextWindow {
	return &ContextWindow{rdb: rdb}
}

func windowKey(contextID string) string {
	return keyPrefix + contextID
}

func (w *ContextWindow) Load(ctx context.Context, contextID string) ([]Message, error) {
	raw, err := w.rdb.Range(ctx, windowKey(contextID), 0, -1)
	if err != nil {
		return nil, err
	}

	msgs := make([]Message, 0, len(raw))
	for _, r := range raw {
		var msg Message
		if err := json.Unmarshal([]byte(r), &msg); err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

func (w *ContextWindow) Append(ctx context.Context, contextID string, msgs ...Message) error {
	if len(msgs) == 0 {
		return nil
	}

	key := windowKey(contextID)
	values := make([]any, 0, len(msgs))
	for _, msg := range msgs {
		b, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		values = append(values, b)
	}

	if err := w.rdb.RPush(ctx, key, values...); err != nil {
		return err
	}
	if err := w.rdb.Trim(ctx, key, -maxMessages, -1); err != nil {
		return err
	}
	return w.rdb.Expire(ctx, key, windowTTL)
}
