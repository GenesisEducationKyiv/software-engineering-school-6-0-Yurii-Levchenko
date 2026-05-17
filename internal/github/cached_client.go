package github

import (
	"context"
	"fmt"
	"github-release-notifier/internal/metrics"
	"log"
)

// Cache values for repo_exists results. We serialize booleans as
// strings because the cache (Redis) is string-valued.
const (
	cachedTrue  = "true"
	cachedFalse = "false"
)

// Cacher defines what the cached client needs from the cache layer
type Cacher interface {
	Get(key string) (string, error)
	Set(key, value string) error
}

// upstreamClient covers the methods CachedClient delegates to.
// The real *Client satisfies this interface; tests can substitute a fake
// to exercise the cache hit/miss logic in isolation (DIP — CachedClient
// depends on an abstraction, not on the concrete HTTP client).
type upstreamClient interface {
	CheckRepoExists(ctx context.Context, owner, repo string) (bool, error)
	GetLatestRelease(ctx context.Context, owner, repo string) (string, error)
}

// CachedClient wraps the GitHub Client with Redis caching
// Checks cache before making API calls. Cache TTL is configured in the cache layer (10 min)
type CachedClient struct {
	client upstreamClient
	cache  Cacher
}

// NewCachedClient creates a GitHub client with Redis caching
func NewCachedClient(client upstreamClient, cache Cacher) *CachedClient {
	return &CachedClient{client: client, cache: cache}
}

// CheckRepoExists checks cache first, then GitHub API
// The ctx is forwarded to the underlying GitHub client for cancellation
func (cc *CachedClient) CheckRepoExists(ctx context.Context, owner, repo string) (bool, error) {
	key := fmt.Sprintf("repo_exists:%s/%s", owner, repo)

	// check cache
	val, err := cc.cache.Get(key)
	if err != nil {
		log.Printf("Cache error (non-fatal): %v", err)
	}
	if val == cachedTrue {
		metrics.GitHubAPICalls.WithLabelValues("check_repo", "hit").Inc()
		log.Printf("Cache HIT: %s exists", key)
		return true, nil
	}
	if val == cachedFalse {
		metrics.GitHubAPICalls.WithLabelValues("check_repo", "hit").Inc()
		log.Printf("Cache HIT: %s not found", key)
		return false, nil
	}

	// cache miss - call GitHub API
	metrics.GitHubAPICalls.WithLabelValues("check_repo", "miss").Inc()
	log.Printf("Cache MISS: %s, calling GitHub API", key)
	exists, err := cc.client.CheckRepoExists(ctx, owner, repo)
	if err != nil {
		return false, err
	}

	// store result in cache
	cacheVal := cachedFalse
	if exists {
		cacheVal = cachedTrue
	}
	if cacheErr := cc.cache.Set(key, cacheVal); cacheErr != nil {
		log.Printf("Cache write error (non-fatal): %v", cacheErr)
	}

	return exists, nil
}

// GetLatestRelease checks cache first, then GitHub API
// The ctx is forwarded to the underlying GitHub client for cancellation
func (cc *CachedClient) GetLatestRelease(ctx context.Context, owner, repo string) (string, error) {
	key := fmt.Sprintf("latest_release:%s/%s", owner, repo)

	// check cache
	val, err := cc.cache.Get(key)
	if err != nil {
		log.Printf("Cache error (non-fatal): %v", err)
	}
	if val != "" {
		metrics.GitHubAPICalls.WithLabelValues("latest_release", "hit").Inc()
		log.Printf("Cache HIT: %s = %s", key, val)
		return val, nil
	}

	// cache miss — call GitHub API
	metrics.GitHubAPICalls.WithLabelValues("latest_release", "miss").Inc()
	log.Printf("Cache MISS: %s, calling GitHub API", key)
	tag, err := cc.client.GetLatestRelease(ctx, owner, repo)
	if err != nil {
		return "", err
	}

	// store in cache (even empty string means "no releases")
	if tag != "" {
		if cacheErr := cc.cache.Set(key, tag); cacheErr != nil {
			log.Printf("Cache write error (non-fatal): %v", cacheErr)
		}
	}

	return tag, nil
}
