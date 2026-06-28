package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/peiblow/avm/database"
)

const (
	maxMessages = 20
	memoryTTL   = 48 * time.Hour
	keyPrefix   = "synx:memory:"
)

type Memory struct {
	rdb *database.RedisClient
}

func NewMemory(rdb *database.RedisClient) *Memory {
	return &Memory{rdb: rdb}
}

func memKey(contextID string) string {
	return keyPrefix + contextID
}

func (m *Memory) Load(ctx context.Context, contextID string) ([]Message, error) {
	raw, err := m.rdb.Range(ctx, memKey(contextID), 0, -1)
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

func (m *Memory) Append(ctx context.Context, contextID string, msgs ...Message) error {
	if len(msgs) == 0 {
		return nil
	}

	key := memKey(contextID)
	values := make([]any, 0, len(msgs))
	for _, msg := range msgs {
		b, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		values = append(values, b)
	}

	if err := m.rdb.RPush(ctx, key, values...); err != nil {
		return err
	}
	if err := m.rdb.Trim(ctx, key, -maxMessages, -1); err != nil {
		return err
	}
	return m.rdb.Expire(ctx, key, memoryTTL)
}
