package syncer_test

import (
	"fmt"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/filter"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// liveStill is one Immich still that references a companion original.
func liveStill(id, companion string) bridge.Asset {
	a := photo(id)
	a.Name, a.MIME = id+".heic", "image/heic"
	a.LivePhotoID = companion
	return a
}

// liveCompanion is the hidden MOV Immich exposes as its own asset.
func liveCompanion(id string) bridge.Asset {
	return bridge.Asset{ID: id, Name: id + ".mov", MIME: "video/quicktime", Kind: "VIDEO",
		Size: 5, ExcludedReason: "hidden", LivePhotoVideo: true}
}

func visibleVideo(id string) bridge.Asset {
	return bridge.Asset{ID: id, Name: id + ".mp4", MIME: "video/mp4", Kind: "VIDEO", Size: 5}
}

func hiddenPhoto(id string) bridge.Asset {
	a := photo(id)
	a.ExcludedReason = "hidden"
	return a
}

// original_pair preserves the still and the MOV as independent originals: two
// uploads, two Nodes with different hashes, and a reported complete pair. No
// mux, conversion or derived hash exists on either path.
func TestOriginalPairPreservesBothOriginals(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			still, companion := liveStill("still", "mov"), liveCompanion("mov")
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{still, companion}},
			}, assets: map[string]bridge.Asset{"still": still, "mov": companion},
				data: map[string]string{"still": "hello", "mov": "world"}}
			d := &targetFake{identity: "target"}
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			opts := options()
			opts.Deletions = deletions
			opts.LivePhotoMode = bridge.LivePhotoOriginalPair
			e := engine(t, store, s, d, opts)
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.LivePhotoMode != "original_pair" || st.Confirmed != 2 ||
				st.PairsComplete != 1 || st.PairsIncomplete != 0 || st.Errors["pair_incomplete"] != 0 {
				t.Fatalf("pair was not preserved: %+v", st)
			}
			if len(d.nodes) != 2 || d.nodes[0].MD5 == d.nodes[1].MD5 {
				t.Fatalf("pair did not produce two distinct nodes: %+v", d.nodes)
			}
			bodies := map[string]bool{}
			for _, body := range d.bodies {
				bodies[body] = true
			}
			if len(d.bodies) != 2 || !bodies["hello"] || !bodies["world"] {
				t.Fatalf("source bytes were not preserved independently: %q", d.bodies)
			}
			if len(d.trashed) != 0 {
				t.Fatalf("pair upload trashed %v", d.trashed)
			}
		})
	}
}

// The default still_only mode keeps every hidden companion excluded, and
// unrelated hidden or standalone video media stay excluded in both modes.
func TestLivePhotoModeOnlyAdmitsResolvedCompanions(t *testing.T) {
	still, companion := liveStill("still", "mov"), liveCompanion("mov")
	stranger := liveCompanion("stranger")
	standalone := visibleVideo("video")
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{still, companion, stranger, standalone}},
	}, assets: map[string]bridge.Asset{"still": still, "mov": companion},
		data: map[string]string{"still": "hello", "mov": "world", "stranger": "other", "video": "video"}}
	d := &targetFake{identity: "target"}

	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.LivePhotoMode != "still_only" || st.Confirmed != 1 || st.PairsComplete != 0 || st.PairsIncomplete != 0 {
		t.Fatalf("still_only changed hidden media behaviour: %+v", st)
	}
	if st.Exclusions["hidden"] != 2 || st.Exclusions[filter.MediaType] != 1 {
		t.Fatalf("still_only exclusions changed: %+v", st.Exclusions)
	}
	if len(d.bodies) != 1 {
		t.Fatalf("still_only downloaded %q", d.bodies)
	}
}

func TestOriginalPairKeepsUnrelatedHiddenAndStandaloneVideoExcluded(t *testing.T) {
	still, companion := liveStill("still", "mov"), liveCompanion("mov")
	stranger := liveCompanion("stranger")
	standalone := visibleVideo("video")
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{still, companion, stranger, standalone}},
	}, assets: map[string]bridge.Asset{"still": still, "mov": companion},
		data: map[string]string{"still": "hello", "mov": "world", "stranger": "other", "video": "video"}}
	d := &targetFake{identity: "target"}
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := options()
	opts.LivePhotoMode = bridge.LivePhotoOriginalPair
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 2 || st.PairsComplete != 1 {
		t.Fatalf("resolved pair did not transfer: %+v", st)
	}
	if st.Exclusions["hidden"] != 1 || st.Exclusions[filter.MediaType] != 1 {
		t.Fatalf("unrelated media gained eligibility: %+v", st.Exclusions)
	}
	for _, body := range d.bodies {
		if body == "other" || body == "video" {
			t.Fatalf("unrelated video transferred: %q", d.bodies)
		}
	}
}

// A companion Immich does not enumerate with the still is still resolved
// through the durable relationship and backed up as its own original.
func TestOriginalPairResolvesCompanionOutsideThePage(t *testing.T) {
	still, companion := liveStill("still", "mov"), liveCompanion("mov")
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{still}},
	}, assets: map[string]bridge.Asset{"still": still, "mov": companion},
		data: map[string]string{"still": "hello", "mov": "world"}}
	d := &targetFake{identity: "target"}
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := options()
	opts.LivePhotoMode = bridge.LivePhotoOriginalPair
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 2 || st.PairsComplete != 1 || len(d.nodes) != 2 {
		t.Fatalf("unenumerated companion was lost: %+v nodes=%d", st, len(d.nodes))
	}
}

// A missing or ineligible companion is reported as pair_incomplete and never
// presented as a complete Live Photo backup.
func TestPairIncompleteIsReportedWithoutClaimingFullBackup(t *testing.T) {
	for name, tt := range map[string]struct {
		policy   filter.Policy
		assets   map[string]bridge.Asset
		excluded string
	}{
		"missing": {},
		"ineligible": {
			policy:   filter.Policy{MaxOriginalBytes: 5},
			assets:   map[string]bridge.Asset{"mov": {ID: "mov", Name: "mov.mov", MIME: "video/quicktime", Kind: "VIDEO", Size: 6, ExcludedReason: "hidden", LivePhotoVideo: true}},
			excluded: filter.TooLarge,
		},
	} {
		t.Run(name, func(t *testing.T) {
			still := liveStill("still", "mov")
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{still}},
			}, assets: tt.assets, data: map[string]string{"still": "hello", "mov": "worlds"}}
			d := &targetFake{identity: "target"}
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			opts := options()
			opts.Filters = tt.policy
			opts.LivePhotoMode = bridge.LivePhotoOriginalPair
			e := engine(t, store, s, d, opts)
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.Confirmed != 1 || st.PairsComplete != 0 || st.PairsIncomplete != 1 {
				t.Fatalf("incomplete pair was misreported: %+v", st)
			}
			if st.Errors["pair_incomplete"] != 1 {
				t.Fatalf("pair_incomplete missing from errors: %+v", st.Errors)
			}
			if tt.excluded != "" && st.Exclusions[tt.excluded] != 1 {
				t.Fatalf("ineligible companion reason missing: %+v", st.Exclusions)
			}
			if len(d.bodies) != 1 {
				t.Fatalf("ineligible companion transferred: %q", d.bodies)
			}
		})
	}
}

// Switching the mode never re-uploads, overwrites or trashes a Node. Turning
// original_pair on adopts the durable companion link without a source rescan;
// turning it off withdraws pair bookkeeping but keeps every uploaded Node.
func TestLivePhotoModeSwitchNeverRewritesNodes(t *testing.T) {
	dir := t.TempDir()
	still, companion := liveStill("still", "mov"), liveCompanion("mov")
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{still, companion}},
	}, assets: map[string]bridge.Asset{"still": still, "mov": companion},
		data: map[string]string{"still": "hello", "mov": "world"}}
	d := &targetFake{identity: "target"}
	run := func(mode bridge.LivePhotoMode, once bool) syncer.Status {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		}()
		opts := options()
		opts.LivePhotoMode = mode
		e := engine(t, store, s, d, opts)
		if once {
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
		} else if err := e.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
		return status(t, e)
	}

	if st := run(bridge.LivePhotoStillOnly, true); st.Confirmed != 1 || st.PairsComplete != 0 || len(d.bodies) != 1 {
		t.Fatalf("still_only baseline: %+v uploads=%d", st, len(d.bodies))
	}
	// Step performs no new source scan: the pair is adopted from durable
	// metadata and only the companion is uploaded.
	st := run(bridge.LivePhotoOriginalPair, false)
	if st.Confirmed != 2 || st.PairsComplete != 1 || len(d.bodies) != 2 {
		t.Fatalf("enabling original_pair did not adopt the pair: %+v uploads=%d", st, len(d.bodies))
	}
	st = run(bridge.LivePhotoStillOnly, false)
	if st.Confirmed != 2 || st.PairsComplete != 0 || st.PairsIncomplete != 0 {
		t.Fatalf("disabling original_pair changed committed Nodes: %+v", st)
	}
	if len(d.bodies) != 2 || len(d.trashed) != 0 {
		t.Fatalf("mode switch wrote remote state: uploads=%d trashed=%v", len(d.bodies), d.trashed)
	}
}

// A companion only becomes eligible because a still in scope references it;
// unrelated hidden media keeps its exclusion in the same durable ledger.
func TestCompanionResolutionIsScopedToItsStill(t *testing.T) {
	still, companion := liveStill("still", "mov"), liveCompanion("mov")
	stranger := hiddenPhoto("stranger")
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{still, companion, stranger}},
	}, assets: map[string]bridge.Asset{"still": still, "mov": companion},
		data: map[string]string{"still": "hello", "mov": "world", "stranger": "other"}}
	d := &targetFake{identity: "target"}
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := options()
	opts.LivePhotoMode = bridge.LivePhotoOriginalPair
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 2 || st.PairsComplete != 1 || st.Exclusions["hidden"] != 1 {
		t.Fatalf("hidden photo gained eligibility: %+v", st)
	}
}

// A standalone still - including an Android motion photo with embedded motion
// bytes and no companion - keeps its current behaviour and causes no extra
// source read for a companion that does not exist.
func TestStandaloneOriginalIsUnaffectedByPairMode(t *testing.T) {
	a := photo("solo")
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{a}},
	}, data: map[string]string{"solo": "hello"}}
	d := &targetFake{identity: "target"}
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := options()
	opts.LivePhotoMode = bridge.LivePhotoOriginalPair
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 1 || st.PairsComplete != 0 || st.PairsIncomplete != 0 ||
		st.Errors["pair_incomplete"] != 0 || len(d.bodies) != 1 || d.bodies[0] != "hello" {
		t.Fatalf("standalone original changed: %+v uploads=%q", st, d.bodies)
	}
	if s.assetCalls != 0 {
		t.Fatalf("pair mode read %d companion assets for a standalone original", s.assetCalls)
	}
}

// A companion excluded by a filter is not lost: once the policy admits it the
// pair returns to pending, the companion uploads, and the still is not
// downloaded or uploaded again.
func TestPairRecoversWhenAPolicyChangeAdmitsTheCompanion(t *testing.T) {
	dir := t.TempDir()
	still, companion := liveStill("still", "mov"), liveCompanion("mov")
	companion.Size, companion.Name = 6, "mov.mov"
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{still, companion}},
	}, assets: map[string]bridge.Asset{"still": still, "mov": companion},
		data: map[string]string{"still": "hello", "mov": "worlds"}}
	d := &targetFake{identity: "target"}
	run := func(policy filter.Policy) syncer.Status {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		}()
		opts := options()
		opts.Filters = policy
		opts.LivePhotoMode = bridge.LivePhotoOriginalPair
		e := engine(t, store, s, d, opts)
		if err := e.Once(t.Context()); err != nil {
			t.Fatal(err)
		}
		return status(t, e)
	}
	st := run(filter.Policy{MaxOriginalBytes: 5})
	if st.Confirmed != 1 || st.PairsIncomplete != 1 || st.Exclusions[filter.TooLarge] != 1 {
		t.Fatalf("size rule did not report an incomplete pair: %+v", st)
	}
	st = run(filter.Policy{})
	if st.Confirmed != 2 || st.PairsComplete != 1 || st.PairsIncomplete != 0 || len(d.bodies) != 2 {
		t.Fatalf("admitted companion did not complete the pair: %+v uploads=%d", st, len(d.bodies))
	}
}
