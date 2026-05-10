package scanner

import (
	"context"
	"fmt"
	"github-release-notifier/internal/metrics"
	"github-release-notifier/internal/model"
	"log"
	"time"
)

// SubscriberRepository covers aggregate queries scanner needs to find
// unique repos with active subscriptions and per-repo subscriber lists.
// Split from a previous RepoStore fat interface (ISP)
type SubscriberRepository interface {
	GetActiveRepos() ([]string, error)
	GetSubscribersByRepo(repo string) ([]model.Subscription, error)
}

// ReleaseTrackingStore reads and writes the per-repo last-seen-tag
// state that scanner uses to detect new releases
type ReleaseTrackingStore interface {
	GetRepoTracking(repo string) (*model.Repository, error)
	UpsertRepoTracking(repo, lastSeenTag string) error
}

// ReleaseChecker defines the GitHub API operations the scanner needs
type ReleaseChecker interface {
	GetLatestRelease(ctx context.Context, owner, repo string) (string, error)
}

// ReleaseNotifier defines the email operations the scanner needs
type ReleaseNotifier interface {
	SendReleaseNotification(to, repo, tag, unsubscribeURL string) error
}

// Scanner periodically checks GitHub for new releases with goroutine and ticker and notifies subscribers
type Scanner struct {
	subs     SubscriberRepository
	tracking ReleaseTrackingStore
	github   ReleaseChecker
	notifier ReleaseNotifier
	interval time.Duration
	baseURL  string
}

// create a new Scanner
func New(subs SubscriberRepository, tracking ReleaseTrackingStore, github ReleaseChecker, notifier ReleaseNotifier, intervalSecs int, baseURL string) *Scanner {
	return &Scanner{
		subs:     subs,
		tracking: tracking,
		github:   github,
		notifier: notifier,
		interval: time.Duration(intervalSecs) * time.Second,
		baseURL:  baseURL,
	}
}

// Start begins the periodic release scanning in a loop
// Accepts context for graceful shutdown — when context is canceled, the scanner stops cleanly
// The same ctx is also propagated to outgoing GitHub API calls so they can be canceled
// when the application shuts down (no orphaned in-flight requests)
func (s *Scanner) Start(ctx context.Context) {
	log.Printf("Scanner started, checking every %v", s.interval)

	// runs immediately on startup, then on ticker
	s.scan(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Scanner stopped gracefully")
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

// scan performs one check cycle for all active repos
func (s *Scanner) scan(ctx context.Context) {
	metrics.ScannerRunsTotal.Inc()
	repos, err := s.subs.GetActiveRepos()
	if err != nil {
		log.Printf("Scanner: failed to get active repos: %v", err)
		return
	}

	log.Printf("Scanner: checking %d repos for new releases", len(repos))

	for _, repoStr := range repos {
		s.checkRepo(ctx, repoStr)
	}
}

// checkRepo orchestrates the per-repo cycle for a single repository:
//
//	1 detect whether a new release tag exists (vs stored last_seen_tag)
//	2 if yes — persist the new tag and notify all subscribers
//
// All error logging happens inside the helpers; this function stays thin
func (s *Scanner) checkRepo(ctx context.Context, repoStr string) {
	newTag, ok := s.detectNewRelease(ctx, repoStr)
	if !ok {
		return
	}
	s.recordAndNotify(repoStr, newTag)
}

// detectNewRelease asks GitHub for the latest release of the given repo
// and compares against the stored last_seen_tag. Returns (tag, true) only
// when a genuinely new release is detected. Returns ("", false) for any
// of: parse error, GitHub error, repo has no releases, tag unchanged.
// All failures are logged here; callers just check the boolean
func (s *Scanner) detectNewRelease(ctx context.Context, repoStr string) (string, bool) {
	spec, err := model.ParseRepoSpec(repoStr)
	if err != nil {
		log.Printf("Scanner: invalid repo format: %s", repoStr)
		return "", false
	}

	latestTag, err := s.github.GetLatestRelease(ctx, spec.Owner, spec.Name)
	if err != nil {
		log.Printf("Scanner: failed to get latest release for %s: %v", repoStr, err)
		return "", false
	}
	if latestTag == "" {
		return "", false // repo has no releases yet
	}

	tracking, err := s.tracking.GetRepoTracking(repoStr)
	if err != nil {
		log.Printf("Scanner: failed to get tracking for %s: %v", repoStr, err)
		return "", false
	}
	if tracking != nil && tracking.LastSeenTag == latestTag {
		return "", false // already notified about this tag
	}

	log.Printf("Scanner: new release detected for %s: %s", repoStr, latestTag)
	metrics.ReleasesDetected.Inc()
	return latestTag, true
}

// recordAndNotify persists the new last_seen_tag and notifies all
// confirmed subscribers about the new release. Email-send failures are
// logged per subscriber but do not abort the loop — we want to attempt
// every recipient even if one address bounces.
// Note: ctx is not yet plumbed here because the notifier doesn't accept
// it; will be added when notifications move to an async worker pool
// (see TODO in system-design/README.md).
func (s *Scanner) recordAndNotify(repoStr, newTag string) {
	if err := s.tracking.UpsertRepoTracking(repoStr, newTag); err != nil {
		log.Printf("Scanner: failed to update tracking for %s: %v", repoStr, err)
		return
	}

	subscribers, err := s.subs.GetSubscribersByRepo(repoStr)
	if err != nil {
		log.Printf("Scanner: failed to get subscribers for %s: %v", repoStr, err)
		return
	}

	for _, sub := range subscribers {
		unsubURL := fmt.Sprintf("%s/api/unsubscribe/%s", s.baseURL, sub.Token)
		if err := s.notifier.SendReleaseNotification(sub.Email, repoStr, newTag, unsubURL); err != nil {
			log.Printf("Scanner: failed to notify %s about %s: %v", sub.Email, repoStr, err)
			continue
		}
		metrics.NotificationsSent.Inc()
		log.Printf("Scanner: notified %s about %s %s", sub.Email, repoStr, newTag)
	}
}
