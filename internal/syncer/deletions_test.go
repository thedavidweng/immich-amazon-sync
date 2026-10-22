package syncer_test

import (
	"context"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func photos(n int) []bridge.Asset {
	assets := make([]bridge.Asset, 0, n)
	names := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven"}
	for i := 0; i < n; i++ {
		assets = append(assets, photo(names[i]))
	}
	return assets
}

func deletionEngine(t *testing.T, deletions bool) (*syncer.Engine, *sourceFake, *targetFake, func()) {
	t.Helper()
	dir := t.TempDir()
	assets := photos(11)
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: assets}},
		data: map[string]string{}}
	for _, a := range assets {
		s.data[a.ID] = "hello"
	}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.Deletions = deletions
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return engine(t, store, s, d, opts), s, d, func() { _ = store.Close() }
}

func TestTrashedAssetIsMovedToAmazonTrash(t *testing.T) {
	e, s, d, done := deletionEngine(t, true)
	defer done()
	ctx := context.Background()
	if err := e.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 11 {
		t.Fatalf("setup: %+v", st)
	}
	plan, err := e.PlanDeletions(ctx)
	if err != nil || plan.Pending != 0 {
		t.Fatalf("unexpected plan before trash: %+v %v", plan, err)
	}

	trashed := photo("one")
	trashed.ExcludedReason = "trashed"
	s.pages[""] = bridge.Page{Assets: append([]bridge.Asset{trashed}, photos(11)[1:]...)}
	if err := e.Once(ctx); err != nil {
		t.Fatal(err)
	}

	st := status(t, e)
	if st.Deleted != 1 || st.DeletePending != 0 || st.Confirmed != 10 {
		t.Fatalf("not settled as deleted: %+v", st)
	}
	if st.LastSuccess == nil {
		t.Fatalf("deletion must settle the account: %+v", st)
	}
	if len(d.trashed) != 1 || d.trashed[0] != d.nodes[0].ID {
		t.Fatalf("wrong trash calls: %+v", d.trashed)
	}
	if plan, err = e.PlanDeletions(ctx); err != nil || plan.Pending != 0 {
		t.Fatalf("plan after trash: %+v %v", plan, err)
	}
}

func TestDeletionIsOptIn(t *testing.T) {
	e, s, d, done := deletionEngine(t, false)
	defer done()
	ctx := context.Background()
	if err := e.Once(ctx); err != nil {
		t.Fatal(err)
	}
	trashed := photo("one")
	trashed.ExcludedReason = "trashed"
	s.pages[""] = bridge.Page{Assets: append([]bridge.Asset{trashed}, photos(11)[1:]...)}
	if err := e.Once(ctx); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 11 || st.Deleted != 0 || st.DeletePending != 0 {
		t.Fatalf("disabled deletions changed state: %+v", st)
	}
	if len(d.trashed) != 0 {
		t.Fatalf("disabled deletions trashed a node")
	}
}

func TestDeletionGuardRefusesImplausiblePass(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{photo("one"), photo("two"), photo("three")}},
	}, data: map[string]string{"one": "hello", "two": "hello", "three": "hello"}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.Deletions = true
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	e := engine(t, store, s, d, opts)
	ctx := context.Background()
	if err := e.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 3 {
		t.Fatalf("setup: %+v", st)
	}

	trashed := photo("one")
	trashed.ExcludedReason = "trashed"
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{trashed, photo("two"), photo("three")}}
	if err := e.Once(ctx); err == nil || err.Error() != "deletion_guard" {
		t.Fatalf("guard error: %v", err)
	}
	plan, err := e.PlanDeletions(ctx)
	if err != nil || !plan.Blocked || plan.Reason != "deletion_guard" || plan.Pending != 1 || len(plan.Assets) != 1 {
		t.Fatalf("blocked plan: %+v %v", plan, err)
	}
	if len(d.trashed) != 0 {
		t.Fatalf("guard still trashed: %+v", d.trashed)
	}
	st := status(t, e)
	if st.DeletePending != 1 || st.Deleted != 0 || st.Errors["deletion_guard"] == 0 {
		t.Fatalf("guard not surfaced: %+v", st)
	}

	// A source restore before the pass must withdraw the intent, not trash a
	// live asset later.
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{photo("one"), photo("two"), photo("three")}}
	if err := e.Once(ctx); err != nil {
		t.Fatal(err)
	}
	st = status(t, e)
	if st.Confirmed != 3 || st.DeletePending != 0 {
		t.Fatalf("restore not applied: %+v", st)
	}
}

func TestDeletionRetriesFailedRemoteTrash(t *testing.T) {
	e, s, d, done := deletionEngine(t, true)
	defer done()
	ctx := context.Background()
	if err := e.Once(ctx); err != nil {
		t.Fatal(err)
	}
	d.trash = func(context.Context, bridge.Content, string) error {
		return &bridge.Error{Code: bridge.Unavailable}
	}
	trashed := photo("one")
	trashed.ExcludedReason = "trashed"
	s.pages[""] = bridge.Page{Assets: append([]bridge.Asset{trashed}, photos(11)[1:]...)}
	if err := e.Once(ctx); err == nil || err.Error() != bridge.Unavailable {
		t.Fatalf("trash failure: %v", err)
	}
	st := status(t, e)
	if st.DeletePending != 1 || st.Deleted != 0 || st.Errors[bridge.Unavailable] == 0 {
		t.Fatalf("failure not retained: %+v", st)
	}
	if st.NextAttempt == nil {
		t.Fatalf("retry not scheduled: %+v", st)
	}
	time.Sleep(300 * time.Millisecond)
	d.trash = nil
	if err := e.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if st = status(t, e); st.Deleted != 1 || st.DeletePending != 0 {
		t.Fatalf("retry did not settle: %+v", st)
	}
}

func TestDisablingDeletionsWithdrawsPendingIntents(t *testing.T) {
	dir := t.TempDir()
	assets := photos(11)
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: assets}},
		data: map[string]string{}}
	for _, a := range assets {
		s.data[a.ID] = "hello"
	}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.Deletions = true
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	e := engine(t, store, s, d, opts)
	ctx := context.Background()
	if err := e.Once(ctx); err != nil {
		t.Fatal(err)
	}
	d.trash = func(context.Context, bridge.Content, string) error {
		return &bridge.Error{Code: bridge.Unavailable}
	}
	trashed := photo("one")
	trashed.ExcludedReason = "trashed"
	s.pages[""] = bridge.Page{Assets: append([]bridge.Asset{trashed}, photos(11)[1:]...)}
	if err := e.Once(ctx); err == nil {
		t.Fatal("expected a trash failure")
	}
	if plan, _ := e.PlanDeletions(ctx); plan.Pending != 1 {
		t.Fatalf("intent not durable: %+v", plan)
	}

	opts.Deletions = false
	off := engine(t, store, s, d, opts)
	if err := off.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if plan, _ := off.PlanDeletions(ctx); plan.Pending != 0 {
		t.Fatalf("disabled propagation kept intents: %+v", plan)
	}
	st := status(t, off)
	if st.Confirmed != 11 || st.DeletePending != 0 || st.Deleted != 0 {
		t.Fatalf("withdrawal state: %+v", st)
	}
	if len(d.trashed) != 0 {
		t.Fatalf("disabled propagation trashed a node: %+v", d.trashed)
	}
}
