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
	for {
		if err := ctx.Err(); err != nil {
			return Delivery{}, err
		}

		streams, err := s.rdb.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group:    s.Group,
			Consumer: s.Consumer,
			Streams:  []string{s.Stream, ">"},
			Count:    1,
			Block:    5 * time.Second,
		}).Result()

		if err == goredis.Nil {
			continue
		}
		if err != nil {
			return Delivery{}, err
		}
		if len(streams) == 0 || len(streams[0].Messages) == 0 {
			continue
		}

		msg := streams[0].Messages[0]
		raw, ok := msg.Values["data"].(string)
		if !ok {
			_ = s.rdb.XAck(context.Background(), s.Stream, s.Group, msg.ID).Err()
			continue
		}

		var ev AgentEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			_ = s.newDelivery(msg.ID, raw, ev, 1).Dead("bad payload: " + err.Error())
			continue
		}

		return s.newDelivery(msg.ID, raw, ev, 1), nil
	}
}

// Reclaim brings back pending entries that have been idle longer than minIdle
// (a consumer that died between XReadGroup and Ack), so the PEL is not a
// write-only ledger. Entries past maxDeliveries are poison and get dead-lettered
// instead of reprocessed. The returned deliveries are claimed by this consumer.
func (s *RedisSource) Reclaim(ctx context.Context, minIdle time.Duration, maxDeliveries, batch int64) ([]Delivery, error) {
	pend, err := s.rdb.XPendingExt(ctx, &goredis.XPendingExtArgs{
		Stream: s.Stream,
		Group:  s.Group,
		Idle:   minIdle,
		Start:  "-",
		End:    "+",
		Count:  batch,
	}).Result()
	if err != nil || len(pend) == 0 {
		return nil, err
	}

	ids := make([]string, 0, len(pend))
	retry := make(map[string]int64, len(pend))
	for _, p := range pend {
		ids = append(ids, p.ID)
		retry[p.ID] = p.RetryCount
	}

	msgs, err := s.rdb.XClaim(ctx, &goredis.XClaimArgs{
		Stream:   s.Stream,
		Group:    s.Group,
		Consumer: s.Consumer,
		MinIdle:  minIdle,
		Messages: ids,
	}).Result()
	if err != nil {
		return nil, err
	}

	var out []Delivery
	for _, msg := range msgs {
		raw, ok := msg.Values["data"].(string)
		if !ok {
			_ = s.rdb.XAck(context.Background(), s.Stream, s.Group, msg.ID).Err()
			continue
		}

		var ev AgentEvent
		d := s.newDelivery(msg.ID, raw, ev, retry[msg.ID])
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			_ = d.Dead("reclaim: bad payload: " + err.Error())
			continue
		}
		if retry[msg.ID] > maxDeliveries {
			_ = d.Dead("exceeded max deliveries")
			continue
		}

		d.Event = ev
		out = append(out, d)
	}

	return out, nil
}

func (s *RedisSource) newDelivery(msgID, raw string, ev AgentEvent, deliveries int64) Delivery {
	return Delivery{
		Event:      ev,
		Deliveries: deliveries,
		Ack: func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return s.rdb.XAck(ctx, s.Stream, s.Group, msgID).Err()
		},
		Dead: func(reason string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			deadData, _ := json.Marshal(map[string]any{
				"data":      raw,
				"reason":    reason,
				"failed_at": time.Now().UTC().UnixMilli(),
			})
			if _, err := s.rdb.XAdd(ctx, s.Stream+":dead", deadData); err != nil {
				return err
			}
			return s.rdb.XAck(ctx, s.Stream, s.Group, msgID).Err()
		},
	}
}

func (s *RedisSource) ensureGroup(ctx context.Context) error {
	err := s.rdb.XGroupCreateMkStream(ctx, s.Stream, s.Group, "$").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}
