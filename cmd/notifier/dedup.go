package main

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// deduper records which messages have already been processed, so a redelivered
// or re-published command doesn't trigger a second email (idempotent consumer).
type deduper interface {
	AlreadyProcessed(ctx context.Context, id string) (bool, error)
	MarkProcessed(ctx context.Context, id string) error
}

// redisDeduper stores processed message ids as Redis keys with a TTL — long
// enough to cover redeliveries/re-publishes, short enough to self-clean.
type redisDeduper struct {
	client *redis.Client
	ttl    time.Duration
}

func newRedisDeduper(url string, ttl time.Duration) (*redisDeduper, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	return &redisDeduper{client: redis.NewClient(opt), ttl: ttl}, nil
}

func (d *redisDeduper) AlreadyProcessed(ctx context.Context, id string) (bool, error) {
	n, err := d.client.Exists(ctx, dedupKey(id)).Result()
	return n > 0, err
}

func (d *redisDeduper) MarkProcessed(ctx context.Context, id string) error {
	return d.client.Set(ctx, dedupKey(id), "1", d.ttl).Err()
}

func (d *redisDeduper) Close() error {
	return d.client.Close()
}

func dedupKey(id string) string {
	return "processed:" + id
}
