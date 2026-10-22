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

// add_only never unstars, even when the source clears the flag. The star this
// service applied is preserved and reported as such, never cleared.
func TestFavoritesAddOnlyNeverUnstars(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
			}, data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target", favorites: map[string]bool{}}
			opts := options()
			opts.Deletions = deletions
			opts.Favorites = "add_only"
			runOnce(t, dir, s, d, opts)
			if len(d.favWrites) != 1 || !d.favorites["1"] {
				t.Fatalf("star not applied: %v %v", d.favWrites, d.favorites)
			}

			s.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
				return bridge.Page{Assets: []bridge.Asset{favoritePhoto("one", false)}}, nil
			}
			st := runOnce(t, dir, s, d, opts)
			if st.FavoritesMode != "add_only" || st.FavoritesAuthority != "star_only" {
				t.Fatalf("mode/authority = %+v", st)
			}
			if len(d.favWrites) != 1 || !d.favorites["1"] || st.FavoritesPreserved != 1 ||
				st.FavoritesApplied != 0 || st.FavoritesDesired != 0 {
				t.Fatalf("add-only cleared a star: %+v writes=%v favorites=%v", st, d.favWrites, d.favorites)
			}
			if len(d.bodies) != 1 {
				t.Fatalf("favorite clearing uploaded media: %d", len(d.bodies))
			}
		})
	}
}

// mirror clears a star only because this service applied it, verified by a
// fresh explicit remote read immediately before the mutation.
func TestFavoritesMirrorClearsServiceAppliedStar(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
			}, data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target", favorites: map[string]bool{}}
			opts := options()
			opts.Deletions = deletions
			opts.Favorites = "mirror"
			runOnce(t, dir, s, d, opts)
			if len(d.favWrites) != 1 || !d.favorites["1"] {
				t.Fatalf("star not applied: %v %v", d.favWrites, d.favorites)
			}

			s.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
				return bridge.Page{Assets: []bridge.Asset{favoritePhoto("one", false)}}, nil
			}
			st := runOnce(t, dir, s, d, opts)
			if st.FavoritesMode != "mirror" || st.FavoritesAuthority != "source_applied" {
				t.Fatalf("mode/authority = %+v", st)
			}
			if len(d.favWrites) != 2 || d.favorites["1"] || st.FavoritesApplied != 0 ||
				st.FavoritesObserved != 0 || st.FavoritesPending != 0 || st.FavoritesPreserved != 0 {
				t.Fatalf("authorized unstar did not settle: %+v writes=%v favorites=%v", st, d.favWrites, d.favorites)
			}
			st = runOnce(t, dir, s, d, opts)
			if len(d.favWrites) != 2 || len(d.bodies) != 1 || st.FavoritesPending != 0 {
				t.Fatalf("restart repeated the unstar or uploaded: %+v writes=%v uploads=%d", st, d.favWrites, len(d.bodies))
			}
		})
	}
}

// A manually starred Existing Node keeps its star by default. Only an explicit
// recorded takeover lets the source clear it.
func TestFavoritesMirrorPreservesManualStarUntilTakeover(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{favoritePhoto("one", false)}},
			}, data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target", favorites: map[string]bool{"existing-node": true}, nodes: []bridge.Node{{
				ID: "existing-node", Name: "one.jpg", MD5: contentMD5("hello"), Size: 5, ParentID: "other-folder",
			}}}
			opts := options()
			opts.Deletions = deletions
			opts.Favorites = "mirror"
			st := runOnce(t, dir, s, d, opts)
			if st.Confirmed != 0 || st.Existing != 1 {
				t.Fatalf("expected one reused node: %+v", st)
			}
			if len(d.favWrites) != 0 || !d.favorites["existing-node"] || st.FavoritesPreserved != 1 ||
				st.FavoritesObserved != 1 || st.FavoritesApplied != 0 {
				t.Fatalf("manual star was not preserved: %+v writes=%v favorites=%v", st, d.favWrites, d.favorites)
			}

			opts.FavoriteTakeover = true
			st = runOnce(t, dir, s, d, opts)
			if st.FavoritesAuthority != "takeover" || len(d.favWrites) != 1 || d.favorites["existing-node"] ||
				st.FavoritesPreserved != 0 || st.FavoritesApplied != 0 {
				t.Fatalf("recorded takeover did not clear the manual star: %+v writes=%v favorites=%v", st, d.favWrites, d.favorites)
			}
		})
	}
}

// Missing remote metadata never authorizes an unstar: an absent
// settings.favorite field stays favorite_unconfirmed, and an absent source flag
// withdraws the intent instead of treating it as false.
func TestFavoritesMirrorNeverUnstarsOnAbsentMetadata(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", favorites: map[string]bool{}}
	opts := options()
	opts.Favorites = "mirror"
	runOnce(t, dir, s, d, opts)
	if len(d.favWrites) != 1 {
		t.Fatalf("star not applied: %v", d.favWrites)
	}

	s.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
		return bridge.Page{Assets: []bridge.Asset{favoritePhoto("one", false)}}, nil
	}
	d.readFav = func(context.Context, bridge.Content, string) (bool, error) {
		return false, &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	st := runOnce(t, dir, s, d, opts)
	if len(d.favWrites) != 1 || !d.favorites["1"] || st.FavoritesUncertain != 1 ||
		st.Errors[bridge.FavoriteUnconfirmed] != 1 || st.FavoritesApplied != 0 {
		t.Fatalf("absent field unstarred or settled: %+v writes=%v", st, d.favWrites)
	}

	d.readFav = nil
	s.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
		return bridge.Page{Assets: []bridge.Asset{photo("one")}}, nil
	}
	st = runOnce(t, dir, s, d, opts)
	if len(d.favWrites) != 1 || !d.favorites["1"] || st.FavoritesUnknown != 1 {
		t.Fatalf("absent source flag unstarred: %+v writes=%v", st, d.favWrites)
	}
}

// Two local accounts that resolve to one Amazon node with contradictory
// wishes report shared_target_conflict and neither writes.
func TestFavoritesSharedTargetConflictPerformsNoWrites(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			first := &sourceFake{identity: "source-a", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
			}, data: map[string]string{"one": "hello"}}
			second := &sourceFake{identity: "source-b", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{favoritePhoto("two", false)}},
			}, data: map[string]string{"two": "hello"}}
			d := &targetFake{identity: "target", favorites: map[string]bool{}}
			a, b := options(), options()
			a.AccountID = "first"
			b.AccountID = "second"
			a.Deletions, b.Deletions = deletions, deletions
			a.Favorites, b.Favorites = "mirror", "mirror"

			if st := runOnce(t, dir, first, d, a); len(d.favWrites) != 1 || st.FavoritesApplied != 1 {
				t.Fatalf("first account did not star: %+v writes=%v", st, d.favWrites)
			}
			store, err := syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			e := engine(t, store, second, d, b)
			err = e.Once(t.Context())
			if err == nil || err.Error() != "shared_target_conflict" {
				t.Fatalf("second account error = %v", err)
			}
			st := status(t, e)
			if st.FavoritesConflict != 1 || st.FavoritesPending != 0 || len(d.favWrites) != 1 || !d.favorites["1"] {
				t.Fatalf("conflicting account wrote: %+v writes=%v favorites=%v", st, d.favWrites, d.favorites)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			// The first account now sees the peer's contradictory wish too.
			store, err = syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			e = engine(t, store, first, d, a)
			err = e.Once(t.Context())
			if err == nil || err.Error() != "shared_target_conflict" {
				t.Fatalf("first account error = %v", err)
			}
			st = status(t, e)
			if st.FavoritesConflict != 1 || len(d.favWrites) != 1 || !d.favorites["1"] {
				t.Fatalf("conflict did not block the first account: %+v writes=%v", st, d.favWrites)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A conflict withdraws once the peer stops contradicting; the account then
// converges again.
func TestFavoritesSharedTargetConflictWithdraws(t *testing.T) {
	dir := t.TempDir()
	first := &sourceFake{identity: "source-a", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	second := &sourceFake{identity: "source-b", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("two", false)}},
	}, data: map[string]string{"two": "hello"}}
	d := &targetFake{identity: "target", favorites: map[string]bool{}}
	a, b := options(), options()
	a.AccountID, b.AccountID = "first", "second"
	a.Favorites, b.Favorites = "mirror", "mirror"
	runOnce(t, dir, first, d, a)
	if err := func() error {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		return engine(t, store, second, d, b).Once(t.Context())
	}(); err == nil || err.Error() != "shared_target_conflict" {
		t.Fatalf("second account error = %v", err)
	}

	// The second source now agrees: one star, no conflict.
	second.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
		return bridge.Page{Assets: []bridge.Asset{favoritePhoto("two", true)}}, nil
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, second, d, b)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.FavoritesConflict != 0 || st.FavoritesApplied != 1 || len(d.favWrites) != 1 || !d.favorites["1"] {
		t.Fatalf("withdrawn conflict did not converge: %+v writes=%v", st, d.favWrites)
	}
}

// A crash after an unstar committed converges through read-before-retry: the
// retry sees the fresh false, performs no second mutation and uploads nothing.
func TestFavoritesUnstarCrashConvergesAfterFreshRead(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", favorites: map[string]bool{}}
	opts := options()
	opts.Favorites = "mirror"
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.favWrites) != 1 {
		t.Fatalf("star not applied: %v", d.favWrites)
	}

	s.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
		return bridge.Page{Assets: []bridge.Asset{favoritePhoto("one", false)}}, nil
	}
	mutations := 0
	d.setFav = func(_ context.Context, _ bridge.Content, id string) error {
		mutations++
		d.favorites[id] = false // committed; the response was lost
		return &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	if err := e.Once(t.Context()); err == nil || err.Error() != bridge.FavoriteUnconfirmed {
		t.Fatalf("lost unstar reported as success: %v", err)
	}
	if st := status(t, e); st.FavoritesUncertain != 1 || mutations != 1 {
		t.Fatalf("lost unstar state = %+v mutations=%d", st, mutations)
	}
	d.setFav = nil
	if err := e.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.FavoritesUncertain != 0 || st.FavoritesApplied != 0 || mutations != 1 || len(d.bodies) != 1 {
		t.Fatalf("unstar retry did not reconcile: %+v mutations=%d uploads=%d", st, mutations, len(d.bodies))
	}
}

// Drift is repaired by a bounded audit: a manual unstar is noticed and repaired
// after the audit interval, and an immediate restart does not write again.
func TestFavoritesRestartRepairsDriftWithoutRetryStorm(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", favorites: map[string]bool{}}
	opts := options()
	opts.Favorites = "mirror"
	opts.Now = func() time.Time { return now }
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.favWrites) != 1 {
		t.Fatalf("star not applied: %v", d.favWrites)
	}

	// Someone unstars the node in Amazon; the source still wants it.
	d.favorites["1"] = false
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.favWrites) != 1 {
		t.Fatalf("drift was re-read before its audit interval: %v", d.favWrites)
	}

	now = now.Add(7 * time.Hour)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.favWrites) != 2 || !d.favorites["1"] {
		t.Fatalf("drift was not repaired: %v %v", d.favWrites, d.favorites)
	}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.favWrites) != 2 || len(d.bodies) != 1 {
		t.Fatalf("audit repeated work or uploaded: writes=%v uploads=%d", d.favWrites, len(d.bodies))
	}
}

// An outage during the audit becomes an independent retry, not a write or a
// retry storm.
func TestFavoritesAuditOutageBacksOff(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", favorites: map[string]bool{}}
	opts := options()
	opts.Favorites = "mirror"
	opts.Now = func() time.Time { return now }
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}

	now = now.Add(7 * time.Hour)
	d.favorites["1"] = false
	d.readFav = func(context.Context, bridge.Content, string) (bool, error) {
		return false, &bridge.Error{Code: bridge.Unavailable}
	}
	if err := e.Once(t.Context()); err == nil || err.Error() != bridge.Unavailable {
		t.Fatalf("audit outage = %v", err)
	}
	st := status(t, e)
	if st.FavoritesFailed != 1 || st.FavoritesApplied != 0 || len(d.favWrites) != 1 || st.NextAttempt == nil {
		t.Fatalf("audit outage did not back off: %+v writes=%v", st, d.favWrites)
	}
	if err := e.Once(t.Context()); err != nil {
		t.Fatalf("backoff was not honored: %v", err)
	}
	if len(d.favWrites) != 1 {
		t.Fatalf("backoff window wrote again: %v", d.favWrites)
	}

	d.readFav = nil
	now = now.Add(time.Minute)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.favWrites) != 2 || !d.favorites["1"] {
		t.Fatalf("recovered audit did not repair drift: %v %v", d.favWrites, d.favorites)
	}
}

// A source trash never resurrects or alters the node's favorite state, whether
// or not deletion propagation is enabled.
func TestFavoritesTrashedSourceNeverAltersNodeFavorite(t *testing.T) {
	for _, favorite := range []bool{false, true} {
		t.Run(fmt.Sprintf("favorite=%v", favorite), func(t *testing.T) {
			dir := t.TempDir()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
			}, data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target", favorites: map[string]bool{}}
			opts := options()
			opts.Deletions = true
			opts.Favorites = "mirror"
			opts.FavoriteTakeover = true
			runOnce(t, dir, s, d, opts)
			if len(d.favWrites) != 1 || !d.favorites["1"] {
				t.Fatalf("star not applied: %v", d.favWrites)
			}

			trashed := favoritePhoto("one", favorite)
			trashed.ExcludedReason = "trashed"
			s.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
				return bridge.Page{Assets: []bridge.Asset{trashed}}, nil
			}
			st := runOnce(t, dir, s, d, opts)
			if st.DeletePending+st.Deleted != 1 {
				t.Fatalf("trash was not recorded: %+v", st)
			}
			if len(d.favWrites) != 1 || !d.favorites["1"] || st.FavoritesPreserved != 0 &&
				len(d.favWrites) != 1 {
				t.Fatalf("trashed source changed the favorite state: %+v writes=%v favorites=%v", st, d.favWrites, d.favorites)
			}
		})
	}
}

// A peer account whose only mapping became ineligible can never write, so it
// must not block this account's legitimate unstar. Trashing on one account
// changes no favorite state and creates no conflict.
func TestFavoritesConflictIgnoresIneligiblePeer(t *testing.T) {
	dir := t.TempDir()
	first := &sourceFake{identity: "source-a", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	second := &sourceFake{identity: "source-b", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("two", true)}},
	}, data: map[string]string{"two": "hello"}}
	d := &targetFake{identity: "target", favorites: map[string]bool{}}
	a, b := options(), options()
	a.AccountID, b.AccountID = "first", "second"
	a.Favorites, b.Favorites = "mirror", "add_only"
	b.Deletions = true

	runOnce(t, dir, first, d, a)
	if len(d.favWrites) != 1 || !d.favorites["1"] {
		t.Fatalf("first account did not star: %v", d.favWrites)
	}
	if st := runOnce(t, dir, second, d, b); st.Confirmed+st.Existing != 1 || len(d.bodies) != 1 ||
		len(d.favWrites) != 1 {
		t.Fatalf("second account did not reuse the starred node: %+v uploads=%d writes=%v", st, len(d.bodies), d.favWrites)
	}

	trashed := favoritePhoto("two", true)
	trashed.ExcludedReason = "trashed"
	second.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
		return bridge.Page{Assets: []bridge.Asset{trashed}}, nil
	}
	if st := runOnce(t, dir, second, d, b); st.DeletePending+st.Deleted != 1 {
		t.Fatalf("second account did not trash its node: %+v", st)
	}
	if len(d.favWrites) != 1 {
		t.Fatalf("trashing changed a favorite: %v", d.favWrites)
	}

	first.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
		return bridge.Page{Assets: []bridge.Asset{favoritePhoto("one", false)}}, nil
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, first, d, a)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.FavoritesConflict != 0 || st.FavoritesApplied != 0 || len(d.favWrites) != 2 || d.favorites["1"] {
		t.Fatalf("ineligible peer blocked the unstar: %+v writes=%v favorites=%v", st, d.favWrites, d.favorites)
	}
}
