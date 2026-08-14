//go:build integration

package integration

import (
	"context"
	"testing"

	"github-release-notifier/internal/outbox"
)

// TestOutboxStore_RoundTrip exercises the outbox Store against real Postgres:
// enqueue -> fetch (unpublished, oldest first) -> mark published -> the marked
// row drops out of the unpublished set.
func TestOutboxStore_RoundTrip(t *testing.T) {
	ctx := context.Background()

	if _, err := testDB.Exec(`TRUNCATE TABLE outbox RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate outbox: %v", err)
	}

	store := outbox.NewStore(testDB)

	// Enqueue two messages directly via the shared *sqlx.DB (no saga yet -> NULL).
	if err := store.Enqueue(ctx, testDB, "", "confirmation", "confirm:tok-1", []byte(`{"to":"a@b.com"}`)); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if err := store.Enqueue(ctx, testDB, "", "release", "release:r:v1:tok-2", []byte(`{"repo":"x/y"}`)); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}

	got, err := store.FetchUnpublished(ctx, 10)
	if err != nil {
		t.Fatalf("fetch unpublished: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("unpublished count = %d, want 2", len(got))
	}
	if got[0].MessageID != "confirm:tok-1" || got[1].MessageID != "release:r:v1:tok-2" {
		t.Fatalf("wrong order: %q, %q", got[0].MessageID, got[1].MessageID)
	}

	if err := store.MarkPublished(ctx, got[0].ID); err != nil {
		t.Fatalf("mark published: %v", err)
	}

	after, err := store.FetchUnpublished(ctx, 10)
	if err != nil {
		t.Fatalf("fetch after mark: %v", err)
	}
	if len(after) != 1 || after[0].MessageID != "release:r:v1:tok-2" {
		t.Fatalf("after mark = %+v, want only the release message", after)
	}
}
