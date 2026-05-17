package github

import (
	"context"
	"errors"
	"testing"
)

// --- Fakes ---

// fakeUpstream implements upstreamClient (the underlying HTTP GitHub
// client) and records every call so tests can assert whether the cache
// hit/miss behavior correctly skips or invokes the upstream.
type fakeUpstream struct {
	existsResults map[string]bool // "owner/repo" -> exists
	existsErr     error
	latestTags    map[string]string // "owner/repo" -> tag (empty = no releases)
	latestErr     error

	existsCalls []string
	latestCalls []string
}

func newFakeUpstream() *fakeUpstream {
	return &fakeUpstream{
		existsResults: map[string]bool{},
		latestTags:    map[string]string{},
	}
}

func (f *fakeUpstream) CheckRepoExists(_ context.Context, owner, repo string) (bool, error) {
	key := owner + "/" + repo
	f.existsCalls = append(f.existsCalls, key)
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.existsResults[key], nil
}

func (f *fakeUpstream) GetLatestRelease(_ context.Context, owner, repo string) (string, error) {
	key := owner + "/" + repo
	f.latestCalls = append(f.latestCalls, key)
	if f.latestErr != nil {
		return "", f.latestErr
	}
	return f.latestTags[key], nil
}

// fakeCache implements Cacher.
type fakeCache struct {
	store  map[string]string
	getErr error
	setErr error
}

func newFakeCache() *fakeCache { return &fakeCache{store: map[string]string{}} }

func (f *fakeCache) Get(key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.store[key], nil
}

func (f *fakeCache) Set(key, value string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.store[key] = value
	return nil
}

// --- CheckRepoExists ---

func TestCheckRepoExists_CacheHitTrue_SkipsUpstream(t *testing.T) {
	upstream := newFakeUpstream()
	cache := newFakeCache()
	cache.store["repo_exists:golang/go"] = "true"
	cc := NewCachedClient(upstream, cache)

	exists, err := cc.CheckRepoExists(context.Background(), "golang", "go")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !exists {
		t.Error("exists = false, want true")
	}
	if len(upstream.existsCalls) != 0 {
		t.Errorf("upstream called %d times, want 0 (cache hit must skip upstream)",
			len(upstream.existsCalls))
	}
}

func TestCheckRepoExists_CacheHitFalse_SkipsUpstream(t *testing.T) {
	upstream := newFakeUpstream()
	cache := newFakeCache()
	cache.store["repo_exists:ghost/repo"] = "false"
	cc := NewCachedClient(upstream, cache)

	exists, err := cc.CheckRepoExists(context.Background(), "ghost", "repo")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if exists {
		t.Error("exists = true, want false (cached negative)")
	}
	if len(upstream.existsCalls) != 0 {
		t.Errorf("upstream called %d times, want 0", len(upstream.existsCalls))
	}
}

func TestCheckRepoExists_CacheMiss_CallsUpstreamAndStores(t *testing.T) {
	upstream := newFakeUpstream()
	upstream.existsResults["golang/go"] = true
	cache := newFakeCache()
	cc := NewCachedClient(upstream, cache)

	exists, err := cc.CheckRepoExists(context.Background(), "golang", "go")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !exists {
		t.Error("exists = false, want true")
	}
	if len(upstream.existsCalls) != 1 {
		t.Errorf("upstream called %d times, want 1", len(upstream.existsCalls))
	}
	if cache.store["repo_exists:golang/go"] != "true" {
		t.Errorf("cache not populated: got %q, want %q",
			cache.store["repo_exists:golang/go"], "true")
	}
}

func TestCheckRepoExists_CacheMissNotFound_StoresFalse(t *testing.T) {
	upstream := newFakeUpstream()
	// Not in existsResults map → fake returns (false, nil) — simulates 404.
	cache := newFakeCache()
	cc := NewCachedClient(upstream, cache)

	exists, err := cc.CheckRepoExists(context.Background(), "ghost", "repo")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if exists {
		t.Error("exists = true, want false")
	}
	// Negative results should also be cached so we don't re-ask GitHub
	// on every subsequent 404.
	if cache.store["repo_exists:ghost/repo"] != "false" {
		t.Errorf("cache not populated with false: got %q",
			cache.store["repo_exists:ghost/repo"])
	}
}

func TestCheckRepoExists_UpstreamError_Propagates(t *testing.T) {
	upstream := newFakeUpstream()
	upstream.existsErr = errors.New("github 500")
	cache := newFakeCache()
	cc := NewCachedClient(upstream, cache)

	_, err := cc.CheckRepoExists(context.Background(), "golang", "go")
	if err == nil {
		t.Fatal("err = nil, want upstream error")
	}
}

func TestCheckRepoExists_CacheReadError_FallsThroughToUpstream(t *testing.T) {
	// Cache outage must not break the system; we log and treat it as a miss.
	upstream := newFakeUpstream()
	upstream.existsResults["golang/go"] = true
	cache := newFakeCache()
	cache.getErr = errors.New("redis down")
	cc := NewCachedClient(upstream, cache)

	exists, err := cc.CheckRepoExists(context.Background(), "golang", "go")
	if err != nil {
		t.Fatalf("err = %v, want nil (cache read errors are non-fatal)", err)
	}
	if !exists {
		t.Error("exists = false, want true")
	}
	if len(upstream.existsCalls) != 1 {
		t.Errorf("upstream called %d times, want 1 (cache read error → upstream)",
			len(upstream.existsCalls))
	}
}

// --- GetLatestRelease ---

func TestGetLatestRelease_CacheHit_ReturnsCachedTag(t *testing.T) {
	upstream := newFakeUpstream()
	cache := newFakeCache()
	cache.store["latest_release:golang/go"] = "v1.22.0"
	cc := NewCachedClient(upstream, cache)

	tag, err := cc.GetLatestRelease(context.Background(), "golang", "go")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if tag != "v1.22.0" {
		t.Errorf("tag = %q, want v1.22.0", tag)
	}
	if len(upstream.latestCalls) != 0 {
		t.Errorf("upstream called %d times, want 0", len(upstream.latestCalls))
	}
}

func TestGetLatestRelease_CacheMiss_CallsUpstreamAndStores(t *testing.T) {
	upstream := newFakeUpstream()
	upstream.latestTags["golang/go"] = "v1.22.0"
	cache := newFakeCache()
	cc := NewCachedClient(upstream, cache)

	tag, err := cc.GetLatestRelease(context.Background(), "golang", "go")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if tag != "v1.22.0" {
		t.Errorf("tag = %q, want v1.22.0", tag)
	}
	if len(upstream.latestCalls) != 1 {
		t.Errorf("upstream called %d times, want 1", len(upstream.latestCalls))
	}
	if cache.store["latest_release:golang/go"] != "v1.22.0" {
		t.Errorf("cache not populated: got %q", cache.store["latest_release:golang/go"])
	}
}

func TestGetLatestRelease_EmptyResult_NotCached(t *testing.T) {
	// Repo with no releases — upstream returns "". We must NOT cache the
	// empty string, otherwise once the first release lands we'd keep
	// serving "no release" until TTL expires.
	upstream := newFakeUpstream()
	// latestTags has no entry → fake returns ("", nil)
	cache := newFakeCache()
	cc := NewCachedClient(upstream, cache)

	tag, err := cc.GetLatestRelease(context.Background(), "ghost", "norelease")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if tag != "" {
		t.Errorf("tag = %q, want empty", tag)
	}
	if _, cached := cache.store["latest_release:ghost/norelease"]; cached {
		t.Error("empty result must NOT be cached (or first release would be invisible until TTL)")
	}
}

func TestGetLatestRelease_UpstreamError_Propagates(t *testing.T) {
	upstream := newFakeUpstream()
	upstream.latestErr = errors.New("github 500")
	cache := newFakeCache()
	cc := NewCachedClient(upstream, cache)

	_, err := cc.GetLatestRelease(context.Background(), "golang", "go")
	if err == nil {
		t.Fatal("err = nil, want upstream error")
	}
}
