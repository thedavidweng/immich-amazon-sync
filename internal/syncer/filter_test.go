package syncer_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/filter"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

const excludedTagID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

func filterAsset(id, name string) bridge.Asset {
	a := photo(id)
	a.Name, a.SHA1 = name, ""
	return a
}

// Each rule must be enforced before any remote write: an excluded Asset is
// never downloaded or uploaded, and an eligible sibling still transfers.
func TestFilterRulesAreEnforcedEndToEnd(t *testing.T) {
	raw := filterAsset("raw", "shot.CR2")
	large := filterAsset("large", "large.jpg")
	large.Size = 6
	unknownSize := filterAsset("unknown", "unknown.jpg")
	unknownSize.Size = -1
	archived := filterAsset("archived", "archived.jpg")
	archived.Archived = true
	tagged := filterAsset("tagged", "tagged.jpg")
	tagged.Tags = []bridge.Tag{{ID: excludedTagID}}
	untagged := filterAsset("untagged", "untagged.jpg")
	untagged.Tags = []bridge.Tag{}
	unknownTags := filterAsset("unknown-tags", "unknown-tags.jpg")
	videoAsset := filterAsset("video", "video.mp4")
	videoAsset.Kind, videoAsset.MIME = "VIDEO", "video/mp4"

	for _, tt := range []struct {
		name     string
		policy   filter.Policy
		assets   []bridge.Asset
		eligible string
		reasons  map[string]int
	}{
		{"media types", filter.Policy{}, []bridge.Asset{filterAsset("image", "image.jpg"), videoAsset},
			"image", map[string]int{filter.MediaType: 1}},
		{"maximum size", filter.Policy{MaxOriginalBytes: 5},
			[]bridge.Asset{filterAsset("small", "small.jpg"), large, unknownSize},
			"small", map[string]int{filter.TooLarge: 1, filter.SizeUnknown: 1}},
		{"skip RAW", filter.Policy{SkipRAW: true}, []bridge.Asset{raw, filterAsset("image", "image.jpg")},
			"image", map[string]int{filter.RAW: 1}},
		{"archived", filter.Policy{IncludeArchived: new(false)}, []bridge.Asset{archived, filterAsset("image", "image.jpg")},
			"image", map[string]int{filter.Archived: 1}},
		{"excluded tags", filter.Policy{ExcludedTags: []string{excludedTagID}},
			[]bridge.Asset{tagged, untagged}, "untagged", map[string]int{filter.Tag: 1}},
		{"unknown tag provenance", filter.Policy{ExcludedTags: []string{excludedTagID}},
			[]bridge.Asset{unknownTags, untagged}, "untagged", map[string]int{filter.TagUnavailable: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			pages := map[string]bridge.Page{"": {Assets: tt.assets}}
			data := map[string]string{}
			for _, a := range tt.assets {
				data[a.ID] = "hello"
			}
			downloaded := map[string]bool{}
			s := &sourceFake{identity: "source", pages: pages, data: data,
				original: func(_ context.Context, id string) (io.ReadCloser, error) {
					downloaded[id] = true
					return io.NopCloser(strings.NewReader("hello")), nil
				}}
			d := &targetFake{identity: "target"}
			opts := options()
			opts.Filters = tt.policy
			e := engine(t, store, s, d, opts)
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.Confirmed != 1 || len(d.bodies) != 1 || st.LastSuccess == nil {
				t.Fatalf("eligible original did not transfer normally: %+v uploads=%d", st, len(d.bodies))
			}
			for reason, count := range tt.reasons {
				if st.Exclusions[reason] != count {
					t.Errorf("exclusion %q = %d, want %d (%+v)", reason, st.Exclusions[reason], count, st.Exclusions)
				}
			}
			if len(st.Exclusions) != len(tt.reasons) || st.Skipped != len(tt.assets)-1 {
				t.Fatalf("unexpected durable exclusions: %+v skipped=%d", st.Exclusions, st.Skipped)
			}
			if len(downloaded) != 1 || !downloaded[tt.eligible] {
				t.Fatalf("excluded originals were downloaded: %v", downloaded)
			}
		})
	}
}

// Changing the policy requeues only previously filtered Assets; no source page
// is re-read, and already confirmed mappings are untouched.
func TestPolicyChangeRequeuesExclusionsWithoutFullRescan(t *testing.T) {
	dir := t.TempDir()
	videoAsset := filterAsset("video", "video.mp4")
	videoAsset.Kind, videoAsset.MIME = "VIDEO", "video/mp4"
	s := &sourceFake{identity: "source",
		pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{filterAsset("image", "image.jpg"), videoAsset}}},
		data:  map[string]string{"image": "hello", "video": "world"}}
	d := &targetFake{identity: "target"}
	opts := options()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || st.Exclusions[filter.MediaType] != 1 {
		t.Fatalf("video was not filtered: %+v", st)
	}
	calls := s.pageCalls
	opts.Filters.MediaTypes = []string{"image", "video"}
	e = engine(t, store, s, d, opts)
	if err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 2 || len(st.Exclusions) != 0 || st.Skipped != 0 {
		t.Fatalf("policy change did not requeue the exclusion: %+v", st)
	}
	if s.pageCalls != calls {
		t.Fatalf("policy change performed a full rescan: %d page reads", s.pageCalls-calls)
	}
	if len(d.bodies) != 2 || d.bodies[1] != "world" {
		t.Fatalf("previously filtered original did not upload: %v", d.bodies)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// A refreshed hint that carries tag provenance must clear the durable
// exclusion without another full scan.
func TestFilterReevaluationFollowsTagMetadataWithoutFullRescan(t *testing.T) {
	dir := t.TempDir()
	a := filterAsset("one", "one.jpg")
	s := &sourceFake{identity: "source",
		pages:  map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}},
		assets: map[string]bridge.Asset{"one": a},
		data:   map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.AlbumIDs, opts.Library = nil, true
	opts.Filters = filter.Policy{ExcludedTags: []string{excludedTagID}}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Exclusions[filter.TagUnavailable] != 1 {
		t.Fatalf("missing tag provenance did not fail closed: %+v", st)
	}
	calls := s.pageCalls
	updated := a
	updated.Tags = []bridge.Tag{}
	s.assets["one"] = updated
	if err := store.Enqueue(t.Context(), "account", "one", 10); err != nil {
		t.Fatal(err)
	}
	if err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || len(st.Exclusions) != 0 {
		t.Fatalf("hint did not reevaluate the excluded asset: %+v", st)
	}
	if s.pageCalls != calls || s.assetCalls == 0 {
		t.Fatalf("reevaluation used the wrong path: page reads=%d asset reads=%d", s.pageCalls-calls, s.assetCalls)
	}
}

// An Existing mapping (verified copy elsewhere in the account) is also
// protected: filtering later must neither trash it nor drop album membership.
func TestFiltersPreserveExistingMappings(t *testing.T) {
	dir := t.TempDir()
	a := filterAsset("one", "one.jpg")
	a.Tags = []bridge.Tag{}
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}},
		data: map[string]string{"one": "hello"}}
	d := newAlbumTarget()
	d.nodes = []bridge.Node{elsewhere("remote-node", "one.jpg", contentMD5("hello"), 5)}
	opts := options()
	opts.AlbumSyncMode = bridge.AlbumSyncSelected
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Existing != 1 || len(d.albums["album-1"]) != 1 {
		t.Fatalf("existing baseline did not settle: %+v members=%v", st, d.albums)
	}

	a.Tags = []bridge.Tag{{ID: excludedTagID}}
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{a}}
	opts.Filters = filter.Policy{ExcludedTags: []string{excludedTagID}}
	e = engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Existing != 1 || st.Confirmed != 0 || len(st.Exclusions) != 0 || st.DeletePending != 0 {
		t.Fatalf("filter demoted an Existing mapping: %+v", st)
	}
	if len(d.removed) != 0 || len(d.albums["album-1"]) != 1 || len(d.trashed) != 0 {
		t.Fatalf("filter removed Existing membership or trashed it: removed=%v members=%v trashed=%v",
			d.removed, d.albums, d.trashed)
	}
}

// Enabling filters after prior syncing must not trash media or remove album
// membership. Confirmed mappings survive under both deletion settings.
func TestFiltersNeverTrashOrRemoveMembership(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(map[bool]string{false: "deletions-off", true: "deletions-on"}[deletions], func(t *testing.T) {
			dir := t.TempDir()
			a := filterAsset("one", "one.jpg")
			a.Tags = []bridge.Tag{}
			s := &sourceFake{identity: "source",
				pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}},
				data:  map[string]string{"one": "hello"}}
			d := newAlbumTarget()
			opts := options()
			opts.AlbumSyncMode, opts.Deletions = bridge.AlbumSyncSelected, deletions
			store, err := syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			e := engine(t, store, s, d, opts)
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			if st := status(t, e); st.Confirmed != 1 || !deletions && len(d.albums["album-1"]) != 1 {
				t.Fatalf("baseline did not settle: %+v members=%v", st, d.albums)
			}

			a.Tags = []bridge.Tag{{ID: excludedTagID}}
			s.pages[""] = bridge.Page{Assets: []bridge.Asset{a}}
			opts.Filters = filter.Policy{ExcludedTags: []string{excludedTagID}}
			e = engine(t, store, s, d, opts)
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.Confirmed != 1 || st.Deleted != 0 || st.DeletePending != 0 || len(st.Exclusions) != 0 {
				t.Fatalf("filter changed a confirmed mapping or its deletion state: %+v", st)
			}
			if len(d.trashed) != 0 {
				t.Fatalf("filtering authorized trash: %v", d.trashed)
			}
			if len(d.removed) != 0 || !deletions && len(d.albums["album-1"]) != 1 {
				t.Fatalf("filtering removed membership: removed=%v members=%v", d.removed, d.albums)
			}
			if len(d.bodies) != 1 {
				t.Fatalf("filtering re-uploaded confirmed media: %d", len(d.bodies))
			}
		})
	}
}

// A source-trash observation is the only deletion evidence; a filtered asset
// that is also absent from an eligible page must never become delete_pending.
func TestFilteredAbsenceIsNotDeletionEvidence(t *testing.T) {
	dir := t.TempDir()
	a := filterAsset("one", "one.jpg")
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.Deletions = true
	opts.Filters = filter.Policy{MaxOriginalBytes: 1}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Exclusions[filter.TooLarge] != 1 || st.DeletePending != 0 || st.Deleted != 0 {
		t.Fatalf("filtered absence became deletion evidence: %+v", st)
	}
	if len(d.trashed) != 0 || len(d.nodes) != 0 {
		t.Fatalf("filtered absence touched the remote: nodes=%d trashed=%v", len(d.nodes), d.trashed)
	}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.DeletePending != 0 || st.Deleted != 0 {
		t.Fatalf("repeat absence created deletion intent: %+v", st)
	}
}
