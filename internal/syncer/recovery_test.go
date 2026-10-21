package syncer_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func TestInterruptedScanChangedMembershipCanRestartFreshGeneration(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 10
	s := &sourceFake{identity: "source"}
	s.album = func(_ context.Context, id string) (bridge.Album, error) {
		return bridge.Album{ID: id, Count: count}, nil
	}
	s.page = func(_ context.Context, _ bridge.Scope, cursor string) (bridge.Page, error) {
		n := 0
		if cursor != "" {
			if _, err := fmt.Sscan(cursor, &n); err != nil {
				t.Fatal(err)
			}
		}
		a := photo(fmt.Sprint("item", n))
		a.ExcludedReason = "hidden"
		if count == 11 && n == 0 {
			a.ID = "replacement"
		}
		next := ""
		if n+1 < count {
			next = fmt.Sprint(n + 1)
		}
		return bridge.Page{Assets: []bridge.Asset{a}, Next: next}, nil
	}
	d := &targetFake{identity: "target"}
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Scanning != 1 {
		t.Fatalf("expected bounded interruption: %+v", st)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	count = 11
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e = engine(t, store, s, d, options())
	_ = e.Step(t.Context())
	if err := e.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := e.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if st := status(t, e); st.Scanning != 0 || st.LastSuccess == nil {
		t.Fatalf("stale scan did not recover: %+v", st)
	}
}

func TestInterruptedScanLosingAlbumCannotUploadStagedOriginal(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	missing := false
	s := &sourceFake{identity: "source", data: map[string]string{"one": "hello"}}
	s.album = func(_ context.Context, id string) (bridge.Album, error) {
		if missing {
			return bridge.Album{}, &bridge.Error{Code: bridge.NotFound}
		}
		return bridge.Album{ID: id, Count: 10}, nil
	}
	s.page = func(_ context.Context, _ bridge.Scope, cursor string) (bridge.Page, error) {
		a := photo("old" + cursor)
		if cursor == "" {
			a = photo("one")
		} else {
			a.ExcludedReason = "hidden"
		}
		return bridge.Page{Assets: []bridge.Asset{a}, Next: cursor + "x"}, nil
	}
	d := &targetFake{identity: "target"}
	calls := 0
	d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		calls++
		return bridge.Node{}, &bridge.Error{Code: bridge.RateLimit, RetryAfter: time.Hour}
	}
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err == nil {
		t.Fatal("rejection hidden")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	missing = true
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e = engine(t, store, s, d, options())
	if err := e.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(t.Context()); err == nil {
		t.Fatal("missing source album not reported")
	}
	if calls != 1 {
		t.Fatalf("queued write ran before scope was revalidated: calls=%d", calls)
	}
}
