package releasetracking

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github-release-notifier/internal/subscription"
)

// --- Fakes for the four scanner interfaces. ---
// Each fake exposes its received calls/state so tests can assert on
// behavior (Lecture 6: test behavior, not implementation — but for a
// background process whose only externally visible effect is "did it
// record / did it send", checking those calls IS the behavior).

// fakeSubs implements SubscriberSource.
type fakeSubs struct {
	subscribersByRepo map[string][]subscription.Subscriber
	subscribersErr    error
}

func (f *fakeSubs) ActiveRepos() ([]string, error) {
	repos := make([]string, 0, len(f.subscribersByRepo))
	for r := range f.subscribersByRepo {
		repos = append(repos, r)
	}
	return repos, nil
}

func (f *fakeSubs) SubscribersForRepo(repo string) ([]subscription.Subscriber, error) {
	if f.subscribersErr != nil {
		return nil, f.subscribersErr
	}
	return f.subscribersByRepo[repo], nil
}

// fakeTracking implements ReleaseTrackingStore.
type fakeTracking struct {
	state     map[string]*Repository // repo -> tracking row
	getErr    error
	recordErr error
	recorded  map[string]string // repo -> tag recorded as a new release
	touched   map[string]int    // repo -> TouchLastChecked call count
}

func newFakeTracking() *fakeTracking {
	return &fakeTracking{
		state:    map[string]*Repository{},
		recorded: map[string]string{},
		touched:  map[string]int{},
	}
}

func (f *fakeTracking) GetRepoTracking(repo string) (*Repository, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.state[repo], nil
}

func (f *fakeTracking) TouchLastChecked(repo string) error {
	f.touched[repo]++
	return nil
}

func (f *fakeTracking) RecordRelease(repo, tag string) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded[repo] = tag
	return nil
}

// fakeRelease implements ReleaseChecker.
type fakeRelease struct {
	tags map[string]string // "owner/repo" -> tag (empty string means "no releases")
	err  error
}

func (f *fakeRelease) GetLatestRelease(_ context.Context, owner, repo string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.tags[owner+"/"+repo], nil
}

// fakeNotifier implements ReleaseNotifier.
type fakeNotifier struct {
	sent    []sentMessage
	failFor map[string]error // per-recipient failures
}

type sentMessage struct {
	To, Repo, Tag, UnsubURL string
}

func (f *fakeNotifier) SendReleaseNotification(to, repo, tag, unsubURL string) error {
	if e, ok := f.failFor[to]; ok && e != nil {
		return e
	}
	f.sent = append(f.sent, sentMessage{to, repo, tag, unsubURL})
	return nil
}

// newScanner returns a Scanner wired to fresh fakes plus the fakes for
// assertion. Tests should treat the scanner as the SUT and the returned
// fakes as both inputs (configure) and probes (assert calls).
func newScanner() (s *Scanner, subs *fakeSubs, tracking *fakeTracking, release *fakeRelease, notifier *fakeNotifier) {
	subs = &fakeSubs{subscribersByRepo: map[string][]subscription.Subscriber{}}
	tracking = newFakeTracking()
	release = &fakeRelease{tags: map[string]string{}}
	notifier = &fakeNotifier{failFor: map[string]error{}}
	s = New(subs, tracking, release, notifier, 60, "http://test.local")
	return
}

// --- detectNewRelease ---

func TestDetectNewRelease_FirstSeenTag_ReturnsTrue(t *testing.T) {
	s, _, _, release, _ := newScanner()
	release.tags["golang/go"] = "v1.22.0"

	tag, ok := s.detectNewRelease(context.Background(), "golang/go")
	if !ok {
		t.Fatal("ok = false, want true (first time seeing this repo)")
	}
	if tag != "v1.22.0" {
		t.Errorf("tag = %q, want v1.22.0", tag)
	}
}

func TestDetectNewRelease_NewerTagThanTracked_ReturnsTrue(t *testing.T) {
	s, _, tracking, release, _ := newScanner()
	release.tags["golang/go"] = "v1.22.0"
	tracking.state["golang/go"] = &Repository{Repo: "golang/go", LastSeenTag: "v1.21.0"}

	tag, ok := s.detectNewRelease(context.Background(), "golang/go")
	if !ok || tag != "v1.22.0" {
		t.Errorf("got (%q, %v), want (v1.22.0, true)", tag, ok)
	}
}

func TestDetectNewRelease_UnchangedTag_ReturnsFalse(t *testing.T) {
	s, _, tracking, release, _ := newScanner()
	release.tags["golang/go"] = "v1.22.0"
	tracking.state["golang/go"] = &Repository{Repo: "golang/go", LastSeenTag: "v1.22.0"}

	_, ok := s.detectNewRelease(context.Background(), "golang/go")
	if ok {
		t.Error("ok = true, want false (same tag already tracked)")
	}
}

func TestDetectNewRelease_InvalidRepoFormat_ReturnsFalse(t *testing.T) {
	s, _, _, _, _ := newScanner()

	_, ok := s.detectNewRelease(context.Background(), "not-a-valid-spec")
	if ok {
		t.Error("ok = true, want false for unparseable repo")
	}
}

func TestDetectNewRelease_RepoHasNoReleases_ReturnsFalse(t *testing.T) {
	s, _, _, release, _ := newScanner()
	// fakeRelease returns "" for repos not in the map, simulating GitHub's
	// 404 path which the real client maps to "" (per github.Client.GetLatestRelease).
	_ = release

	_, ok := s.detectNewRelease(context.Background(), "ghost/empty")
	if ok {
		t.Error("ok = true, want false when latestTag is empty")
	}
}

func TestDetectNewRelease_GitHubError_ReturnsFalse(t *testing.T) {
	s, _, _, release, _ := newScanner()
	release.err = errors.New("github 500")

	_, ok := s.detectNewRelease(context.Background(), "golang/go")
	if ok {
		t.Error("ok = true, want false on upstream error")
	}
}

func TestDetectNewRelease_TrackingError_ReturnsFalse(t *testing.T) {
	s, _, tracking, release, _ := newScanner()
	release.tags["golang/go"] = "v1.22.0"
	tracking.getErr = errors.New("db down")

	_, ok := s.detectNewRelease(context.Background(), "golang/go")
	if ok {
		t.Error("ok = true, want false on DB read error")
	}
}

// --- recordAndNotify ---

func TestRecordAndNotify_RecordsTagAndNotifiesAll(t *testing.T) {
	s, subs, tracking, _, notifier := newScanner()
	subs.subscribersByRepo["golang/go"] = []subscription.Subscriber{
		{Email: "a@b.com", Token: "tok-A"},
		{Email: "c@d.com", Token: "tok-C"},
	}

	s.recordAndNotify("golang/go", "v1.22.0")

	if tracking.recorded["golang/go"] != "v1.22.0" {
		t.Errorf("recorded tag = %q, want v1.22.0", tracking.recorded["golang/go"])
	}
	if len(notifier.sent) != 2 {
		t.Fatalf("sent %d notifications, want 2", len(notifier.sent))
	}
	for _, m := range notifier.sent {
		if !strings.Contains(m.UnsubURL, "/api/unsubscribe/") {
			t.Errorf("unsubscribe URL %q missing /api/unsubscribe/", m.UnsubURL)
		}
		if m.Tag != "v1.22.0" {
			t.Errorf("notification tag = %q, want v1.22.0", m.Tag)
		}
	}
}

func TestRecordAndNotify_RecordFails_NoNotificationsSent(t *testing.T) {
	s, subs, tracking, _, notifier := newScanner()
	tracking.recordErr = errors.New("db error")
	subs.subscribersByRepo["golang/go"] = []subscription.Subscriber{
		{Email: "a@b.com", Token: "tok-A"},
	}

	s.recordAndNotify("golang/go", "v1.22.0")

	// Function bails out after the record error — without persisting the
	// new tag we shouldn't be telling users it's released.
	if len(notifier.sent) != 0 {
		t.Errorf("sent %d, want 0 (must abort before notifying)", len(notifier.sent))
	}
}

func TestRecordAndNotify_OneRecipientFails_ContinuesOthers(t *testing.T) {
	s, subs, _, _, notifier := newScanner()
	subs.subscribersByRepo["golang/go"] = []subscription.Subscriber{
		{Email: "a@b.com", Token: "tok-A"},
		{Email: "broken@b.com", Token: "tok-B"},
		{Email: "c@d.com", Token: "tok-C"},
	}
	notifier.failFor["broken@b.com"] = errors.New("smtp bounce")

	s.recordAndNotify("golang/go", "v1.22.0")

	if len(notifier.sent) != 2 {
		t.Errorf("sent %d, want 2 (broken@b.com should be skipped, others should still go through)",
			len(notifier.sent))
	}
}

func TestRecordAndNotify_SubscribersFetchFails_NoNotificationsSent(t *testing.T) {
	s, subs, tracking, _, notifier := newScanner()
	subs.subscribersErr = errors.New("db error")

	s.recordAndNotify("golang/go", "v1.22.0")

	// Tag IS persisted before subscribers are fetched — that's actually
	// fine, the next cycle will see "tag unchanged" and skip. But no
	// emails should go out on this cycle.
	if tracking.recorded["golang/go"] != "v1.22.0" {
		t.Errorf("recorded = %q, want v1.22.0 (record happens before subscriber fetch)",
			tracking.recorded["golang/go"])
	}
	if len(notifier.sent) != 0 {
		t.Errorf("sent %d, want 0 on subscriber fetch error", len(notifier.sent))
	}
}

// --- checkRepo (orchestrator) ---

func TestCheckRepo_NewRelease_PersistsAndNotifies(t *testing.T) {
	s, subs, tracking, release, notifier := newScanner()
	release.tags["golang/go"] = "v1.22.0"
	subs.subscribersByRepo["golang/go"] = []subscription.Subscriber{
		{Email: "a@b.com", Token: "tok-A"},
	}

	s.checkRepo(context.Background(), "golang/go")

	if tracking.recorded["golang/go"] != "v1.22.0" {
		t.Errorf("recorded = %q, want v1.22.0", tracking.recorded["golang/go"])
	}
	if len(notifier.sent) != 1 {
		t.Errorf("sent %d, want 1", len(notifier.sent))
	}
}

func TestCheckRepo_UnchangedTag_DoesNothing(t *testing.T) {
	s, subs, tracking, release, notifier := newScanner()
	release.tags["golang/go"] = "v1.22.0"
	tracking.state["golang/go"] = &Repository{LastSeenTag: "v1.22.0"}
	subs.subscribersByRepo["golang/go"] = []subscription.Subscriber{
		{Email: "a@b.com", Token: "tok-A"},
	}

	s.checkRepo(context.Background(), "golang/go")

	if _, persisted := tracking.recorded["golang/go"]; persisted {
		t.Error("no release should be recorded when tag is unchanged")
	}
	if len(notifier.sent) != 0 {
		t.Errorf("sent %d, want 0", len(notifier.sent))
	}
	// The check itself must still be stamped — that's the whole point of
	// last_checked_at being independent from releases.
	if tracking.touched["golang/go"] != 1 {
		t.Errorf("touched = %d, want 1 (check time stamped even when nothing new)", tracking.touched["golang/go"])
	}
}

func TestCheckRepo_InvalidRepoFormat_DoesNothing(t *testing.T) {
	s, _, tracking, _, notifier := newScanner()

	s.checkRepo(context.Background(), "broken-spec")

	if len(tracking.recorded) != 0 {
		t.Errorf("recorded %v, want empty", tracking.recorded)
	}
	if len(notifier.sent) != 0 {
		t.Errorf("sent %d, want 0", len(notifier.sent))
	}
	// GitHub was never called, so no check happened — nothing to stamp.
	if len(tracking.touched) != 0 {
		t.Errorf("touched %v, want empty (no GitHub call, no check)", tracking.touched)
	}
}
