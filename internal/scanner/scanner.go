package scanner

import (
	"context"
	"fmt"
	"github-release-notifier/internal/metrics"
	"github-release-notifier/internal/model"
	"log"
	"strings"
	"time"
)

// RepoStore defines what the scanner needs from the database
type RepoStore interface {
	GetActiveRepos() ([]string, error)
	GetRepoTracking(repo string) (*model.Repository, error)
	UpsertRepoTracking(repo, lastSeenTag string) error
	GetSubscribersByRepo(repo string) ([]model.Subscription, error)
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
	repo     RepoStore
	github   ReleaseChecker
	notifier ReleaseNotifier
	interval time.Duration
	baseURL  string
}

// create a new Scanner
func New(repo RepoStore, github ReleaseChecker, notifier ReleaseNotifier, intervalSecs int, baseURL string) *Scanner {
	return &Scanner{
		repo:     repo,
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
	repos, err := s.repo.GetActiveRepos()
	if err != nil {
		log.Printf("Scanner: failed to get active repos: %v", err)
		return
	}

	log.Printf("Scanner: checking %d repos for new releases", len(repos))

	for _, repoStr := range repos {
		s.checkRepo(ctx, repoStr)
	}
}

// checkRepo checks a single repo for new releases
func (s *Scanner) checkRepo(ctx context.Context, repoStr string) {
	parts := strings.SplitN(repoStr, "/", 2)
	if len(parts) != 2 {
		log.Printf("Scanner: invalid repo format: %s", repoStr)
		return
	}

	latestTag, err := s.github.GetLatestRelease(ctx, parts[0], parts[1])
	if err != nil {
		log.Printf("Scanner: failed to get latest release for %s: %v", repoStr, err)
		return
	}

	if latestTag == "" {
		return // no releases for this repo
	}

	// check if this is a new release
	tracking, err := s.repo.GetRepoTracking(repoStr)
	if err != nil {
		log.Printf("Scanner: failed to get tracking for %s: %v", repoStr, err)
		return
	}

	// if we've already seen this tag - skip
	if tracking != nil && tracking.LastSeenTag == latestTag {
		return
	}

	log.Printf("Scanner: new release detected for %s: %s", repoStr, latestTag)
	metrics.ReleasesDetected.Inc()

	// update the tracking record
	if err := s.repo.UpsertRepoTracking(repoStr, latestTag); err != nil {
		log.Printf("Scanner: failed to update tracking for %s: %v", repoStr, err)
		return
	}

	// notify all subscribers
	subscribers, err := s.repo.GetSubscribersByRepo(repoStr)
	if err != nil {
		log.Printf("Scanner: failed to get subscribers for %s: %v", repoStr, err)
		return
	}

	for _, sub := range subscribers {
		unsubURL := fmt.Sprintf("%s/api/unsubscribe/%s", s.baseURL, sub.Token)
		if err := s.notifier.SendReleaseNotification(sub.Email, repoStr, latestTag, unsubURL); err != nil {
			log.Printf("Scanner: failed to notify %s about %s: %v", sub.Email, repoStr, err)
		} else {
			metrics.NotificationsSent.Inc()
			log.Printf("Scanner: notified %s about %s %s", sub.Email, repoStr, latestTag)
		}
	}
}
