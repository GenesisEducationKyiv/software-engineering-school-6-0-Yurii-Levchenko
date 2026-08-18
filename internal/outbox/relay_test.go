package outbox

import (
	"context"
	"errors"
	"testing"
)

// fakeStore implements relayStore: it returns preset batches and records which
// message ids were marked published.
type fakeStore struct {
	batches [][]Message
	fetched int
	marked  []int64
	markErr error
	// markCtxErr records ctx.Err() as seen by MarkPublished, so a test can prove
	// the bookkeeping write runs with cancellation detached.
	markCtxErr error
}

func (f *fakeStore) FetchUnpublished(_ context.Context, _ int) ([]Message, error) {
	if f.fetched < len(f.batches) {
		b := f.batches[f.fetched]
		f.fetched++
		return b, nil
	}
	return nil, nil
}

func (f *fakeStore) MarkPublished(ctx context.Context, id int64) error {
	f.markCtxErr = ctx.Err()
	if f.markErr != nil {
		return f.markErr
	}
	f.marked = append(f.marked, id)
	return nil
}

// fakePub implements Publisher and records published message ids; it can be told
// to fail on a specific id.
type fakePub struct {
	published []string
	failOn    string
}

func (f *fakePub) Publish(_ context.Context, _, messageID string, _ []byte) error {
	if f.failOn != "" && messageID == f.failOn {
		return errors.New("broker down")
	}
	f.published = append(f.published, messageID)
	return nil
}

// makeMessages builds outbox rows with ids 1..N and the given message ids.
func makeMessages(ids ...string) []Message {
	out := make([]Message, 0, len(ids))
	for i, id := range ids {
		out = append(out, Message{ID: int64(i + 1), RoutingKey: "confirmation", MessageID: id, Payload: []byte(`{}`)})
	}
	return out
}

func TestRelay_drain_publishesAndMarksAll(t *testing.T) {
	store := &fakeStore{batches: [][]Message{makeMessages("a", "b", "c")}}
	pub := &fakePub{}
	r := NewRelay(store, pub, 0)

	r.drain(context.Background())

	if len(pub.published) != 3 {
		t.Fatalf("published = %v, want 3 messages", pub.published)
	}
	if len(store.marked) != 3 {
		t.Fatalf("marked = %v, want 3 ids", store.marked)
	}
}

func TestRelay_drain_stopsBatchOnPublishError(t *testing.T) {
	store := &fakeStore{batches: [][]Message{makeMessages("a", "b", "c")}}
	pub := &fakePub{failOn: "b"}
	r := NewRelay(store, pub, 0)

	r.drain(context.Background())

	// "a" published+marked; "b" failed -> stop the batch; "c" never attempted.
	if len(pub.published) != 1 || pub.published[0] != "a" {
		t.Fatalf("published = %v, want [a]", pub.published)
	}
	if len(store.marked) != 1 || store.marked[0] != 1 {
		t.Fatalf("marked = %v, want [1]", store.marked)
	}
}

func TestRelay_drain_continuesWhenMarkFails(t *testing.T) {
	store := &fakeStore{batches: [][]Message{makeMessages("a", "b")}, markErr: errors.New("db blip")}
	pub := &fakePub{}
	r := NewRelay(store, pub, 0)

	r.drain(context.Background())

	// Both published even though marking failed: they will re-publish next tick
	// and the consumer dedups, so a mark error must not stop delivery.
	if len(pub.published) != 2 {
		t.Fatalf("published = %v, want 2 messages", pub.published)
	}
}

// TestRelay_drain_marksPublishedAfterCancellation guards the shutdown behavior:
// once a message is in the broker, stamping published_at must happen even if the
// relay's context was canceled. Otherwise every restart leaves ghost rows that
// look exactly like a real failure.
func TestRelay_drain_marksPublishedAfterCancellation(t *testing.T) {
	store := &fakeStore{batches: [][]Message{makeMessages("a")}}
	pub := &fakePub{}
	r := NewRelay(store, pub, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate a shutdown already in progress

	r.drain(ctx)

	if len(store.marked) != 1 {
		t.Fatalf("marked = %v, want [1] (bookkeeping must survive cancellation)", store.marked)
	}
	if store.markCtxErr != nil {
		t.Errorf("MarkPublished saw ctx.Err() = %v, want nil (cancellation must be detached)", store.markCtxErr)
	}
}
