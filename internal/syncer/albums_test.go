package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

type albumTarget struct {
	*targetFake
	albums     map[string][]string
	names      map[string]string
	creates    int
	addErr     error
	makeErr    error
	removed    []string
	memberLag  int
	stale      []string
	staleReads int
}

func TestAlbumMirroringRequiresExplicitAlbumRules(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := options()
	opts.Library, opts.AlbumIDs, opts.AlbumSyncMode = true, nil, bridge.AlbumSyncSelected
	_, err = syncer.New(store, &sourceFake{}, newAlbumTarget(), opts)
	if err == nil || err.Error() != "invalid_options" {
		t.Fatalf("library scope silently ignored requested album mirroring: %v", err)
	}
}

func TestUncertainAlbumCreationSurvivesRestartWithoutAdoptingName(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := newAlbumTarget()
	d.makeErr = errors.New("lost response")
	// A same-name album that this service did not create must never be adopted.
	d.albums["preexisting"] = []string{}
	d.names["preexisting"] = ""
	opts := options()
	opts.AlbumSyncMode = bridge.AlbumSyncSelected
	for range 2 {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		e := engine(t, store, s, d, opts)
		if err := e.Retry(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := e.Once(t.Context()); err == nil || err.Error() != "album_unconfirmed" {
			t.Fatalf("uncertain create did not remain visible: %v", err)
		}
		if st := status(t, e); st.AlbumsUnconfirmed != 1 || st.LastSuccess != nil || st.Confirmed != 1 {
			t.Fatalf("uncertain album falsely completed: %+v", st)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if d.creates != 1 {
		t.Fatalf("uncertain album was recreated %d times", d.creates)
	}
	if len(d.albums["preexisting"]) != 0 || d.names["preexisting"] != "" {
		t.Fatalf("uncertain create adopted a same-name album: albums=%v names=%v", d.albums, d.names)
	}
}

func TestAlbumRemovalTouchesOnlyOwnedRelationsAfterCompleteNonemptyScan(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, b := photo("one"), photo("two")
	b.SHA1 = ""
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a, b}}},
		data: map[string]string{"one": "hello", "two": "world"}}
	d := newAlbumTarget()
	opts := options()
	opts.AlbumSyncMode, opts.AlbumIDs = bridge.AlbumSyncSelected, []string{"album", "second"}
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	d.albums["album-1"] = append(d.albums["album-1"], "manual")
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{a}}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.removed) != 2 || !slices.Contains(d.albums["album-1"], "manual") || len(d.nodes) != 2 {
		t.Fatalf("removal escaped owned memberships: removed=%v albums=%v nodes=%v", d.removed, d.albums, d.nodes)
	}
	s.pages[""] = bridge.Page{}
	before := len(d.removed)
	if err := e.Once(t.Context()); err == nil || err.Error() != "membership_guard" {
		t.Fatalf("empty scan removed owned membership: %v", err)
	}
	if len(d.removed) != before {
		t.Fatalf("empty scan removed members: %v", d.removed)
	}
}

func TestReaddedAbsentMembershipCountsTowardRemovalGuard(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, b, c := photo("one"), photo("two"), photo("three")
	b.SHA1, c.SHA1 = "", ""
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a, b, c}}}, data: map[string]string{"one": "hello", "two": "world", "three": "three"}}
	d := newAlbumTarget()
	opts := options()
	opts.AlbumSyncMode = bridge.AlbumSyncSelected
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{a, b}}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	removed := d.removed[0]
	d.albums["album-1"] = append(d.albums["album-1"], strings.TrimPrefix(removed, "album-1:"))
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{a}}
	before := len(d.removed)
	if err := e.Once(t.Context()); err == nil || err.Error() != "membership_guard" {
		t.Fatalf("actual two-member removal escaped threshold: %v", err)
	}
	if len(d.removed) != before {
		t.Fatal("guard allowed partial removals")
	}
}

func newAlbumTarget() *albumTarget {
	return &albumTarget{targetFake: &targetFake{identity: "target"},
		albums: map[string][]string{}, names: map[string]string{}}
}

func (d *albumTarget) CreateAlbum(_ context.Context, name string) (string, error) {
	d.creates++
	if d.makeErr != nil {
		return "", d.makeErr
	}
	id := fmt.Sprint("album-", d.creates)
	d.albums[id], d.names[id] = []string{}, name
	return id, nil
}
func (d *albumTarget) RenameAlbum(_ context.Context, id, name string) error {
	d.names[id] = name
	return nil
}
func (d *albumTarget) AlbumMembers(_ context.Context, id string) ([]string, error) {
	members, ok := d.albums[id]
	if !ok {
		return nil, &bridge.Error{Code: bridge.NotFound}
	}
	if d.staleReads > 0 {
		d.staleReads--
		return slices.Clone(d.stale), nil
	}
	return slices.Clone(members), nil
}
func (d *albumTarget) AddMembers(_ context.Context, id string, nodes []string) error {
	if d.addErr != nil {
		return d.addErr
	}
	d.stale = slices.Clone(d.albums[id])
	d.staleReads = d.memberLag
	for _, n := range nodes {
		if !slices.Contains(d.albums[id], n) {
			d.albums[id] = append(d.albums[id], n)
		}
	}
	return nil
}
func (d *albumTarget) RemoveMembers(_ context.Context, id string, nodes []string) error {
	for _, n := range nodes {
		d.albums[id] = slices.DeleteFunc(d.albums[id], func(s string) bool { return s == n })
		d.removed = append(d.removed, id+":"+n)
	}
	return nil
}

func TestAlbumMembershipSettlesThroughReadLag(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, b, c := photo("one"), photo("two"), photo("three")
	b.SHA1, c.SHA1 = "", ""
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a, b, c}}},
		data: map[string]string{"one": "hello", "two": "world", "three": "three"}}
	d := newAlbumTarget()
	d.memberLag = 3
	opts := options()
	opts.AlbumSyncMode = bridge.AlbumSyncSelected
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 3 || st.AlbumsFailed != 0 || st.AssociationsPending != 0 || st.LastSuccess == nil {
		t.Fatalf("membership did not settle in one run: %+v", st)
	}
	if len(d.albums["album-1"]) != 3 || len(d.nodes) != 3 {
		t.Fatalf("memberships=%v nodes=%d", d.albums["album-1"], len(d.nodes))
	}
}

func TestAlbumAssociationRetriesWithoutUploadingAgainAndRenameKeepsOwnedID(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	name := "Family"
	s.album = func(_ context.Context, id string) (bridge.Album, error) {
		return bridge.Album{ID: id, Name: name, Count: 1}, nil
	}
	d := newAlbumTarget()
	d.addErr = &bridge.Error{Code: bridge.Unavailable}
	opts := options()
	opts.AlbumSyncMode = bridge.AlbumSyncSelected
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := engine(t, store, s, d, opts)
	if err := e.Once(context.Background()); err == nil {
		t.Fatal("album association failure was hidden")
	}
	if st := status(t, e); st.Confirmed != 1 || st.LastSuccess != nil || len(d.nodes) != 1 {
		t.Fatalf("association changed media confirmation: %+v", st)
	}
	store.Close()
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	name, d.addErr = "Renamed", nil
	e = engine(t, store, s, d, opts)
	if err := e.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.LastSuccess == nil || d.creates != 1 ||
		d.names["album-1"] != "Renamed" || !slices.Equal(d.albums["album-1"], []string{"1"}) || len(d.nodes) != 1 {
		t.Fatalf("album did not converge independently: %+v albums=%v names=%v creates=%d", st, d.albums, d.names, d.creates)
	}
}
