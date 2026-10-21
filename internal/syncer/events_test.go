package syncer_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func TestDurableEventValidatesAlbumScopeWithoutCompletingBackfill(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := &sourceFake{identity: "source", data: map[string]string{"new": "hello", "foreign": "hello"}}
	s.album = func(_ context.Context, id string) (bridge.Album, error) {
		return bridge.Album{ID: id, Name: "Family", Count: 20}, nil
	}
	s.page = func(_ context.Context, scope bridge.Scope, cursor string) (bridge.Page, error) {
		if scope.AssetID != "" {
			if scope.AlbumID != "album" {
				t.Fatal("event expanded configured scope")
			}
			if scope.AssetID == "foreign" {
				return bridge.Page{}, nil
			}
			return bridge.Page{Assets: []bridge.Asset{photo(scope.AssetID)}}, nil
		}
		a := photo("old" + cursor)
		a.ExcludedReason = "hidden"
		return bridge.Page{Assets: []bridge.Asset{a}, Next: cursor + "x"}, nil
	}
	d := newAlbumTarget()
	opts := options()
	opts.MirrorAlbums = true
	e := engine(t, store, s, d, opts)
	if err := e.Enqueue(ctx, "new", 10); err != nil {
		t.Fatal(err)
	}
	if err := e.Enqueue(ctx, "new", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.Enqueue(ctx, "foreign", 10); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e = engine(t, store, s, d, opts)
	if err := e.Step(ctx); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 1 || st.Scanning != 1 || st.LastSuccess != nil || st.EventsPending != 0 || len(d.bodies) != 1 {
		t.Fatalf("event lost or accepted outside scope: %+v uploads=%v", st, d.bodies)
	}
	if len(d.albums["album-1"]) != 1 || len(d.removed) != 0 {
		t.Fatalf("backfill blocked event association: albums=%v removed=%v", d.albums, d.removed)
	}
}

func TestIncrementalFailureKeepsWatermarkAndResumesWithOverlap(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := options()
	opts.Library, opts.AlbumIDs = true, nil
	fail := true
	var starts []time.Time
	s := &sourceFake{identity: "source", data: map[string]string{"new": "hello"}}
	s.page = func(_ context.Context, scope bridge.Scope, cursor string) (bridge.Page, error) {
		if scope.UpdatedAfter.IsZero() {
			return bridge.Page{}, nil
		}
		starts = append(starts, scope.UpdatedAfter)
		if cursor == "" {
			return bridge.Page{Assets: []bridge.Asset{photo("new")}, Next: "second"}, nil
		}
		if fail {
			return bridge.Page{}, nil
		}
		a := photo("terminal")
		a.ExcludedReason = "hidden"
		return bridge.Page{Assets: []bridge.Asset{a}}, nil
	}
	e := engine(t, store, s, &targetFake{identity: "target"}, opts)
	if err := e.Reconcile(t.Context()); err == nil {
		t.Fatal("incomplete incremental scan reported success")
	}
	if st := status(t, e); st.LastIncremental != nil || st.LastSuccess != nil || !st.IncrementalScanning {
		t.Fatalf("partial incremental scan advanced success: %+v", st)
	}
	fail = false
	if err := e.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.LastIncremental == nil || st.LastSuccess == nil || st.IncrementalScanning || st.Confirmed != 1 {
		t.Fatalf("incremental scan did not recover: %+v", st)
	}
	prior := *st.LastIncremental
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if since := starts[len(starts)-1]; !since.Before(prior) || prior.Sub(since) < time.Minute {
		t.Fatalf("incremental scan omitted overlap: since=%v watermark=%v", since, prior)
	}
}

func TestEventGetsNextWorkerDuringBackfill(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var order []string
	var mu sync.Mutex
	s := &sourceFake{identity: "source", assets: map[string]bridge.Asset{"new": photo("new")}}
	var assets []bridge.Asset
	for i := range 20 {
		assets = append(assets, photo(string(rune('a'+i))))
	}
	s.pages = map[string]bridge.Page{"": {Assets: assets}}
	s.original = func(ctx context.Context, id string) (io.ReadCloser, error) {
		mu.Lock()
		order = append(order, id)
		mu.Unlock()
		if id == "a" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return io.NopCloser(strings.NewReader("hello")), nil
	}
	opts := options()
	opts.Library, opts.AlbumIDs, opts.Workers = true, nil, 1
	e := engine(t, store, s, &targetFake{identity: "target"}, opts)
	done := make(chan error, 1)
	go func() { done <- e.Once(t.Context()) }()
	<-started
	if err := e.Enqueue(t.Context(), "new", 10); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(order) != 21 || order[0] != "a" || order[1] != "new" {
		t.Fatalf("event was starved behind historical jobs: %v", order)
	}
}
