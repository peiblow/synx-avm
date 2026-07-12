package ingress

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/peiblow/avm/database"
	goredis "github.com/redis/go-redis/v9"
)

type RedisSource struct {
	Group    string
	Stream   string
	Consumer string
	rdb      database.RedisClient
}

func NewRedisSource(ctx context.Context, client *database.RedisClient, stream, group, consumer string) (*RedisSource, error) {
	s := &RedisSource{
		Group:    group,
		Stream:   stream,
		Consumer: consumer,
		rdb:      *client,
	}

	if err := s.ensureGroup(ctx); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *RedisSource) Consume(ctx context.Context) (Delivery, error) {
	streams, err := s.rdb.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    s.Group,
		Consumer: s.Consumer,
		Streams:  []string{s.Stream, ">"},
		Count:    1,
		Block:    5 * time.Second,
	}).Result()

	if err == goredis.Nil {
		return s.Consume(ctx)
	}

	if err != nil {
		return Delivery{}, err
	}

	msg := streams[0].Messages[0]
	raw, ok := msg.Values["data"].(string)
	if !ok {
		return Delivery{}, err
	}

	var ev AgentEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return Delivery{}, err
	}

	return Delivery{
		Event: ev,
		Ack: func() error {
			return s.rdb.XAck(ctx, s.Stream, s.Group, msg.ID).Err()
		},
		Dead: func(reason string) error {
			deadData, _ := json.Marshal(map[string]any{
				"data":      raw,
				"reason":    reason,
				"failed_at": time.Now().UTC().UnixMilli(),
			})
			if _, err := s.rdb.XAdd(ctx, s.Stream+":dead", deadData); err != nil {
				return err
			}
			return s.rdb.XAck(ctx, s.Stream, s.Group, msg.ID).Err()
		},
	}, nil
}

func (s *RedisSource) ensureGroup(ctx context.Context) error {
	err := s.rdb.XGroupCreateMkStream(ctx, s.Stream, s.Group, "$").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}
