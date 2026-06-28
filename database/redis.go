package database

import (
	"context"
	"os"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type XReadGroupArgs struct {
	Group    string
	Consumer string
	Streams  []string
	Block    time.Duration
}

type RedisClient struct {
	*goredis.Client
}

func NewRedisClient() (*RedisClient, error) {
	client := &RedisClient{}
	return client.Open()
}

func (r *RedisClient) Open() (*RedisClient, error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	rdb := goredis.NewClient(&goredis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return &RedisClient{rdb}, nil
}

func (c *RedisClient) Close() error {
	return c.Client.Close()
}

func (c *RedisClient) Get(ctx context.Context, key string) (string, error) {
	return c.Client.Get(ctx, key).Result()
}

func (c *RedisClient) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	return c.Client.Set(ctx, key, value, ttl).Err()
}

func (c *RedisClient) Del(ctx context.Context, keys ...string) error {
	return c.Client.Del(ctx, keys...).Err()
}

func (c *RedisClient) LPush(ctx context.Context, key string, values ...any) error {
	return c.Client.LPush(ctx, key, values...).Err()
}

func (c *RedisClient) RPush(ctx context.Context, key string, values ...any) error {
	return c.Client.RPush(ctx, key, values...).Err()
}

func (c *RedisClient) LPop(ctx context.Context, key string) (string, error) {
	return c.Client.LPop(ctx, key).Result()
}

func (c *RedisClient) RPop(ctx context.Context, key string) (string, error) {
	return c.Client.RPop(ctx, key).Result()
}

func (c *RedisClient) Range(ctx context.Context, key string, start, stop int64) ([]string, error) {
	return c.Client.LRange(ctx, key, start, stop).Result()
}

func (c *RedisClient) Len(ctx context.Context, key string) (int64, error) {
	return c.Client.LLen(ctx, key).Result()
}

func (c *RedisClient) Trim(ctx context.Context, key string, start, stop int64) error {
	return c.Client.LTrim(ctx, key, start, stop).Err()
}

func (c *RedisClient) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return c.Client.Expire(ctx, key, ttl).Err()
}
