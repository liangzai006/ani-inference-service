package work

import (
	"context"
	"testing"
)

type fakeStore struct {
	item      Item
	committed bool
	failed    bool
}

func (f *fakeStore) Claim(context.Context, string) (Item, bool, error) { return f.item, true, nil }
func (f *fakeStore) Commit(_ context.Context, item Item, result Result) error {
	if result.Generation != f.item.Generation {
		f.failed = true
		return ErrStaleGeneration
	}
	f.committed = item.Generation == result.Generation
	return nil
}
func (f *fakeStore) Retry(context.Context, Item, error) error { return nil }

func TestWorkerDoesNotCommitStaleResult(t *testing.T) {
	store := &fakeStore{item: Item{TenantID: "t1", ServiceID: "s1", Generation: 2}}
	w := Worker{Store: store, Execute: func(context.Context, Item) (Result, error) { return Result{Generation: 1}, nil }}
	if err := w.RunOnce(context.Background(), "t1"); err != ErrStaleGeneration {
		t.Fatalf("RunOnce error = %v, want stale generation", err)
	}
	if store.committed {
		t.Fatal("stale result was committed")
	}
}
