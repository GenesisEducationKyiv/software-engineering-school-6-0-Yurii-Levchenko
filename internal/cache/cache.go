package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache wraps Redis client for simple key-value caching with TTL
type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

// New creates a new Redis cache with the given TTL
func New(redisURL string, ttl time.Duration) (*Cache, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opts)

	// verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return &Cache{client: client, ttl: ttl}, nil
}

// Get retrieves a cached value by key. Returns empty string if not found
func (c *Cache) Get(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	val, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil // cache miss, not an error
	}
	return val, err
}

// Set stores a value with the configured TTL
func (c *Cache) Set(key, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	return c.client.Set(ctx, key, value, c.ttl).Err()
}

// Close closes the Redis connection
func (c *Cache) Close() error {
	return c.client.Close()
}
