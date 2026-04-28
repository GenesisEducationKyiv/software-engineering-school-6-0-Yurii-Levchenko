package github

import (
	"fmt"
	"github-release-notifier/internal/metrics"
	"log"
)

// Cacher defines what the cached client needs from the cache layer
type Cacher interface {
	Get(key string) (string, error)
	Set(key, value string) error
}

// CachedClient wraps the GitHub Client with Redis caching
// Checks cache before making API calls. Cache TTL is configured in the cache layer (10 min)
type CachedClient struct {
	client *Client
	cache  Cacher
}

// NewCachedClient creates a GitHub client with Redis caching
func NewCachedClient(client *Client, cache Cacher) *CachedClient {
	return &CachedClient{client: client, cache: cache}
}

// CheckRepoExists checks cache first, then GitHub API
func (cc *CachedClient) CheckRepoExists(owner, repo string) (bool, error) {
	key := fmt.Sprintf("repo_exists:%s/%s", owner, repo)

	// check cache
	val, err := cc.cache.Get(key)
	if err != nil {
		log.Printf("Cache error (non-fatal): %v", err)
	}
	if val == "true" {
		metrics.GitHubAPICalls.WithLabelValues("check_repo", "hit").Inc()
		log.Printf("Cache HIT: %s exists", key)
		return true, nil
	}
	if val == "false" {
		metrics.GitHubAPICalls.WithLabelValues("check_repo", "hit").Inc()
		log.Printf("Cache HIT: %s not found", key)
		return false, nil
	}

	// cache miss - call GitHub API
	metrics.GitHubAPICalls.WithLabelValues("check_repo", "miss").Inc()
	log.Printf("Cache MISS: %s, calling GitHub API", key)
	exists, err := cc.client.CheckRepoExists(owner, repo)
	if err != nil {
		return false, err
	}

	// store result in cache
	cacheVal := "false"
	if exists {
		cacheVal = "true"
	}
	if cacheErr := cc.cache.Set(key, cacheVal); cacheErr != nil {
		log.Printf("Cache write error (non-fatal): %v", cacheErr)
	}

	return exists, nil
}

// GetLatestRelease checks cache first, then GitHub API
func (cc *CachedClient) GetLatestRelease(owner, repo string) (string, error) {
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
	tag, err := cc.client.GetLatestRelease(owner, repo)
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
