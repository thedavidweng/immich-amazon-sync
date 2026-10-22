package syncer_test

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func favoritePhoto(id string, favorite bool) bridge.Asset {
	a := photo(id)
	a.Favorite = &favorite
	return a
}

func runOnce(t *testing.T, dir string, s bridge.Source, d bridge.Target, opts syncer.Options) syncer.Status {
	t.Helper()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	e := engine(t, store, s, d, opts)
	_ = e.Once(t.Context())
	return status(t, e)
}

// Add-only stars a newly uploaded node and a node reused as Existing, verified
// by an authoritative re-read. A restart does not write again, and the switch
// is orthogonal to deletion propagation.
func TestFavoritesAddOnlyStarsNewAndExistingNodes(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			one, two := favoritePhoto("one", true), favoritePhoto("two", true)
			two.SHA1 = ""
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{one, two}},
			}, data: map[string]string{"one": "hello", "two": "world"}}
			d := &targetFake{identity: "target", nodes: []bridge.Node{{
				ID: "existing-node", Name: "two.jpg", MD5: contentMD5("world"), Size: 5, ParentID: "other-folder",
			}}}
			opts := options()
			opts.Deletions = deletions
			opts.Favorites = "add_only"

			st := runOnce(t, dir, s, d, opts)
			if st.Confirmed != 1 || st.Existing != 1 {
				t.Fatalf("media states changed: %+v", st)
			}
			if st.FavoritesMode != "add_only" || st.FavoritesDesired != 2 || st.FavoritesApplied != 2 ||
				st.FavoritesPending != 0 || st.FavoritesUncertain != 0 || st.FavoritesUnknown != 0 {
				t.Fatalf("favorite outcome not verified: %+v", st)
			}
			if len(d.favWrites) != 2 || !d.favorites["existing-node"] || !d.favorites["2"] {
				t.Fatalf("expected exactly two verified stars: writes=%v favorites=%v", d.favWrites, d.favorites)
			}

			st = runOnce(t, dir, s, d, opts)
			if st.FavoritesApplied != 2 || len(d.favWrites) != 2 || len(d.bodies) != 1 {
				t.Fatalf("restart repeated a favorite write or upload: %+v writes=%v uploads=%d", st, d.favWrites, len(d.bodies))
			}
		})
	}
}

// Off is the default and performs no favorite read or write.
func TestFavoritesOffByDefaultPerformsNoWrites(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
			}, data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target"}
			opts := options()
			opts.Deletions = deletions
			st := runOnce(t, dir, s, d, opts)
			if st.FavoritesMode != "off" || st.FavoritesDesired != 0 || len(d.favWrites) != 0 {
				t.Fatalf("disabled favorites wrote: %+v writes=%v", st, d.favWrites)
			}
		})
	}
}

// An absent source field is unknown: no write, and status reports it instead of
// treating the asset as not favorite.
func TestFavoritesUnknownSourcePerformsNoWrite(t *testing.T) {
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{photo("one")}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.Favorites = "add_only"
	st := runOnce(t, t.TempDir(), s, d, opts)
	if st.FavoritesUnknown != 1 || st.FavoritesDesired != 0 || st.FavoritesApplied != 0 || len(d.favWrites) != 0 {
		t.Fatalf("unknown source favorite was treated as a decision: %+v writes=%v", st, d.favWrites)
	}
}

// An Amazon read without an explicit settings.favorite field is
// favorite_unconfirmed, never a settled success, and it authorizes no mutation.
func TestFavoritesMissingRemoteFieldIsUnconfirmed(t *testing.T) {
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	writes := 0
	d := &targetFake{identity: "target",
		readFav: func(context.Context, bridge.Content, string) (bool, error) {
			return false, &bridge.Error{Code: bridge.FavoriteUnconfirmed}
		},
		setFav: func(context.Context, bridge.Content, string) error {
			writes++
			return nil
		}}
	opts := options()
	opts.Favorites = "add_only"
	st := runOnce(t, t.TempDir(), s, d, opts)
	if st.FavoritesApplied != 0 || st.Errors[bridge.FavoriteUnconfirmed] != 1 || writes != 0 {
		t.Fatalf("absent field settled or wrote: %+v writes=%d errors=%v", st, writes, st.Errors)
	}
}

// A lost mutation stays uncertain. The retry re-reads first; because the write
// had committed, it settles as applied with no second mutation and no upload.
func TestFavoritesUncertainMutationRetriesAfterFreshRead(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	mutations := 0
	d := &targetFake{identity: "target", favorites: map[string]bool{}}
	d.setFav = func(_ context.Context, _ bridge.Content, id string) error {
		mutations++
		d.favorites[id] = true // the mutation committed; its response was lost
		return &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	opts := options()
	opts.Favorites = "add_only"
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err == nil || err.Error() != bridge.FavoriteUnconfirmed {
		t.Fatalf("lost mutation was reported as success: %v", err)
	}
	st := status(t, e)
	if st.FavoritesUncertain != 1 || st.FavoritesApplied != 0 || mutations != 1 {
		t.Fatalf("lost mutation state = %+v mutations=%d", st, mutations)
	}
	if err := e.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st = status(t, e)
	if st.FavoritesApplied != 1 || st.FavoritesUncertain != 0 || mutations != 1 {
		t.Fatalf("retry did not reconcile from the fresh read: %+v mutations=%d", st, mutations)
	}
	if len(d.bodies) != 1 {
		t.Fatalf("favorite retry uploaded media: %d", len(d.bodies))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// Two source assets that resolve to one Amazon node produce one durable
// outcome and one write.
func TestFavoritesSharedNodeProducesOneWrite(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("a", true), favoritePhoto("b", true)}},
	}, data: map[string]string{"a": "hello", "b": "hello"}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.Favorites = "add_only"
	st := runOnce(t, dir, s, d, opts)
	if st.Confirmed != 2 || len(d.nodes) != 1 || len(d.bodies) != 1 {
		t.Fatalf("shared content was not one node: %+v nodes=%d uploads=%d", st, len(d.nodes), len(d.bodies))
	}
	if len(d.favWrites) != 1 || st.FavoritesApplied != 1 {
		t.Fatalf("shared node produced %d writes: %+v", len(d.favWrites), st)
	}
}

// Source-trashed, deleted and Unconfirmed nodes are ineligible: no favorite
// write is ever issued for them.
func TestFavoritesIneligibleNodesReceiveNoWrite(t *testing.T) {
	t.Run("source-trashed", func(t *testing.T) {
		trashed := favoritePhoto("one", true)
		trashed.ExcludedReason = "trashed"
		s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
			"": {Assets: []bridge.Asset{trashed}},
		}, data: map[string]string{"one": "hello"}}
		d := &targetFake{identity: "target"}
		opts := options()
		opts.Deletions = true
		opts.Favorites = "add_only"
		st := runOnce(t, t.TempDir(), s, d, opts)
		if st.Skipped != 1 || len(d.favWrites) != 0 || st.FavoritesDesired != 0 {
			t.Fatalf("trashed source received a favorite write: %+v writes=%v", st, d.favWrites)
		}
	})
	t.Run("unconfirmed", func(t *testing.T) {
		s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
			"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
		}, data: map[string]string{"one": "hello"}}
		d := &targetFake{identity: "target", upload: func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
			return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
		}}
		opts := options()
		opts.Favorites = "add_only"
		st := runOnce(t, t.TempDir(), s, d, opts)
		if st.Unconfirmed != 1 || len(d.favWrites) != 0 || st.FavoritesApplied != 0 {
			t.Fatalf("unconfirmed node received a favorite write: %+v writes=%v", st, d.favWrites)
		}
	})
	t.Run("deleted", func(t *testing.T) {
		dir := t.TempDir()
		// First run maps the node without any favorite intent.
		s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
			"": {Assets: []bridge.Asset{photo("one")}},
		}, data: map[string]string{"one": "hello"}}
		d := &targetFake{identity: "target"}
		opts := options()
		opts.Favorites = "add_only"
		opts.Deletions = true
		runOnce(t, dir, s, d, opts)
		if len(d.favWrites) != 0 {
			t.Fatalf("unknown favorite wrote: %v", d.favWrites)
		}
		// The source now reports both trash and favorite; the node is pending
		// deletion and must not be starred on the way out.
		trashed := favoritePhoto("one", true)
		trashed.ExcludedReason = "trashed"
		s.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
			return bridge.Page{Assets: []bridge.Asset{trashed}}, nil
		}
		st := runOnce(t, dir, s, d, opts)
		if st.DeletePending+st.Deleted != 1 || len(d.favWrites) != 0 || st.FavoritesApplied != 0 {
			t.Fatalf("deleted node received a favorite write: %+v writes=%v trashed=%v", st, d.favWrites, d.trashed)
		}
	})
}
