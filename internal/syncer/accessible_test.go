package syncer_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"slices"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// accessibleSource models one whole-accessible-library listing plus the same
// originals reachable through two source albums. The shared original appears
// in both albums and in the library.
func contentPhoto(id, content string) bridge.Asset {
	asset := photo(id)
	asset.Size, asset.SHA1 = int64(len(content)), ""
	return asset
}

func accessibleSource() (*sourceFake, map[string]string) {
	one, shared, two := contentPhoto("one", "alpha"), contentPhoto("shared", "gamma"), contentPhoto("two", "beta")
	data := map[string]string{"one": "alpha", "two": "beta", "shared": "gamma"}
	return &sourceFake{
		identity: "source",
		albums: []bridge.Album{
			{ID: "album-a", Name: "Family", Count: 2},
			{ID: "album-b", Name: "Trips", Count: 2},
		},
		scoped: map[string]bridge.Page{
			"album-a\x00": {Assets: []bridge.Asset{one, shared}},
			"album-b\x00": {Assets: []bridge.Asset{shared, two}},
			"\x00":        {Assets: []bridge.Asset{one, shared, two}},
		},
		data: data,
	}, data
}

func accessibleOptions() syncer.Options {
	opts := options()
	opts.Library, opts.AlbumIDs = true, nil
	opts.AlbumSyncMode = bridge.AlbumSyncAllAccessible
	return opts
}

func nodeFor(t *testing.T, d *albumTarget, content string) string {
	t.Helper()
	sum := md5.Sum([]byte(content))
	digest := hex.EncodeToString(sum[:])
	for _, n := range d.nodes {
		if n.MD5 == digest {
			return n.ID
		}
	}
	t.Fatalf("no node for %q in %+v", content, d.nodes)
	return ""
}

func albumByScopes(t *testing.T, d *albumTarget) map[string][]string {
	t.Helper()
	byName := map[string][]string{}
	for id, name := range d.names {
		byName[name] = d.albums[id]
	}
	return byName
}

func TestAllAccessibleMirrorsEveryAccessibleAlbumWithLibraryBackup(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, _ := accessibleSource()
	d := newAlbumTarget()
	e := engine(t, store, s, d, accessibleOptions())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.nodes) != 3 || d.creates != 2 {
		t.Fatalf("expected one node per original and two owned albums: nodes=%d creates=%d", len(d.nodes), d.creates)
	}
	albums := albumByScopes(t, d)
	wantA := []string{nodeFor(t, d, "alpha"), nodeFor(t, d, "gamma")}
	wantB := []string{nodeFor(t, d, "beta"), nodeFor(t, d, "gamma")}
	if !sameSet(albums["Family"], wantA) || !sameSet(albums["Trips"], wantB) {
		t.Fatalf("album memberships: Family=%v Trips=%v", albums["Family"], albums["Trips"])
	}
	if len(d.removed) != 0 {
		t.Fatalf("unexpected membership removal: %v", d.removed)
	}
	st := status(t, e)
	if st.AlbumSyncMode != string(bridge.AlbumSyncAllAccessible) || st.AlbumsActive != 2 ||
		st.AlbumsInaccessible != 0 || st.AlbumsPending != 0 || st.AssociationsPending != 0 ||
		st.LastSuccess == nil || !sameContent(d.bodies) {
		t.Fatalf("all-accessible run did not settle: %+v", st)
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, id := range want {
		if !slices.Contains(got, id) {
			return false
		}
	}
	return true
}

func sameContent(bodies []string) bool {
	seen := map[string]bool{}
	for _, body := range bodies {
		if seen[body] {
			return false
		}
		seen[body] = true
	}
	return len(bodies) == 3
}

func TestAllAccessibleDiscoveryFailureChangesNothing(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := accessibleSource()
	d := newAlbumTarget()
	e := engine(t, store, s, d, accessibleOptions())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(d.nodes)
	albumsBefore := albumByScopes(t, d)
	s.albums = nil // an incomplete read is not evidence of absence
	if err := e.Once(t.Context()); err == nil || err.Error() != "remote_protocol" {
		t.Fatalf("failed discovery was treated as success: %v", err)
	}
	if len(d.removed) != 0 || len(d.nodes) != before || !sameSet(albumByScopes(t, d)["Family"], albumsBefore["Family"]) {
		t.Fatalf("failed discovery mutated state: removed=%v nodes=%d", d.removed, len(d.nodes))
	}
	st := status(t, e)
	if st.AlbumsInaccessible != 0 || st.AlbumsActive != 2 || st.LastSuccess == nil {
		t.Fatalf("failed discovery changed album scope: %+v", st)
	}
}

func TestInaccessibleAlbumIsNotDeletionEvidence(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, _ := accessibleSource()
	d := newAlbumTarget()
	e := engine(t, store, s, d, accessibleOptions())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	family := nodeFor(t, d, "alpha")
	trips := nodeFor(t, d, "beta")
	created := d.creates
	// One album stops being visible. Its mappings and memberships stay.
	s.albums = []bridge.Album{{ID: "album-a", Name: "Family", Count: 2}}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.removed) != 0 || !slices.Contains(d.albums["album-1"], family) || !slices.Contains(d.albums["album-2"], trips) {
		t.Fatalf("inaccessible album cleared unrelated state: removed=%v albums=%v", d.removed, d.albums)
	}
	st := status(t, e)
	if st.AlbumsInaccessible != 1 || st.AlbumsActive != 1 || st.Errors["album_inaccessible"] == 0 || d.creates != created {
		t.Fatalf("inaccessible album was not visible: %+v", st)
	}
	// The album returns: the owned destination album is reused, not recreated.
	s.albums = []bridge.Album{
		{ID: "album-a", Name: "Family", Count: 2},
		{ID: "album-b", Name: "Trips", Count: 2},
	}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.creates != created || !slices.Contains(d.albums["album-2"], trips) {
		t.Fatalf("returning album lost identity: creates=%d albums=%v", d.creates, d.albums)
	}
	if st = status(t, e); st.AlbumsInaccessible != 0 || st.AlbumsActive != 2 {
		t.Fatalf("returning album did not clear inaccessibility: %+v", st)
	}
}

func TestAllAccessiblePermissionErrorIsVisibleAndClearsNothing(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, _ := accessibleSource()
	d := newAlbumTarget()
	e := engine(t, store, s, d, accessibleOptions())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := albumByScopes(t, d)
	s.page = func(_ context.Context, scope bridge.Scope, cursor string) (bridge.Page, error) {
		if scope.AlbumID == "album-b" {
			return bridge.Page{}, &bridge.Error{Code: bridge.Permission}
		}
		return s.scoped[scope.AlbumID+"\x00"+cursor], nil
	}
	if err := e.Once(t.Context()); err == nil || err.Error() != bridge.Permission {
		t.Fatalf("album permission failure was hidden: %v", err)
	}
	if len(d.removed) != 0 || !sameSet(albumByScopes(t, d)["Trips"], before["Trips"]) {
		t.Fatalf("permission failure cleared membership: removed=%v albums=%v", d.removed, d.albums)
	}
	st := status(t, e)
	if st.Errors[bridge.Permission] == 0 || st.AlbumsActive != 2 {
		t.Fatalf("per-album permission failure not visible: %+v", st)
	}
}

func TestAllAccessibleRenameKeepsOwnedAlbumIdentity(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, _ := accessibleSource()
	d := newAlbumTarget()
	e := engine(t, store, s, d, accessibleOptions())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	var family string
	for id, name := range d.names {
		if name == "Family" {
			family = id
		}
	}
	s.albums[0].Name = "Family 2026"
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.creates != 2 || family == "" || d.names[family] != "Family 2026" {
		t.Fatalf("rename changed album identity: creates=%d names=%v", d.creates, d.names)
	}
	if len(d.albums[family]) != 2 {
		t.Fatalf("rename lost membership: %v", d.albums[family])
	}
}

func TestSelectedModeNeverDiscoversOtherAccessibleAlbums(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, _ := accessibleSource()
	s.albums = []bridge.Album{{ID: "album-a", Name: "Family", Count: 1}}
	d := newAlbumTarget()
	opts := options()
	opts.AlbumIDs = []string{"album-a"}
	opts.AlbumSyncMode = bridge.AlbumSyncSelected
	s.scoped = map[string]bridge.Page{
		"album-a\x00": {Assets: []bridge.Asset{contentPhoto("one", "alpha")}},
	}
	s.data = map[string]string{"one": "alpha"}
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.creates != 1 || len(d.names) != 1 {
		t.Fatalf("selected mode widened scope: creates=%d names=%v", d.creates, d.names)
	}
}

func TestAllAccessibleAlbumMembershipFollowsOptInDeletionWithoutDeletingTheAlbum(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Eleven originals keep the account-wide deletion pass under its 10%
	// guard while still exercising one bounded membership removal.
	names := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven"}
	assets := make([]bridge.Asset, 0, len(names))
	data := make(map[string]string, len(names))
	for i, name := range names {
		content := fmt.Sprintf("item-%02d", i)
		assets = append(assets, contentPhoto(name, content))
		data[name] = content
	}
	s := &sourceFake{
		identity: "source",
		albums:   []bridge.Album{{ID: "album-a", Name: "Family", Count: len(assets)}},
		scoped: map[string]bridge.Page{
			"album-a\x00": {Assets: assets},
			"\x00":        {Assets: assets},
		},
		data: data,
	}
	d := newAlbumTarget()
	opts := accessibleOptions()
	opts.Deletions = true
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.creates != 1 || len(d.albums["album-1"]) != len(assets) {
		t.Fatalf("deletion-enabled album mirroring did not run: creates=%d albums=%v", d.creates, d.albums)
	}
	trashed := assets[0]
	trashed.ExcludedReason = "trashed"
	s.scoped["album-a\x00"] = bridge.Page{Assets: append([]bridge.Asset{trashed}, assets[1:]...)}
	s.scoped["\x00"] = bridge.Page{Assets: append([]bridge.Asset{trashed}, assets[1:]...)}
	s.albums[0].Count = len(assets) - 1
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.trashed) != 1 {
		t.Fatalf("source trash did not reach the mapped node: %v", d.trashed)
	}
	if d.creates != 1 || len(d.albums["album-1"]) != len(assets)-1 || slices.Contains(d.albums["album-1"], d.trashed[0]) {
		t.Fatalf("album identity or membership changed incorrectly: creates=%d albums=%v trashed=%v",
			d.creates, d.albums, d.trashed)
	}
	if st := status(t, e); st.AlbumsFailed != 0 || st.AlbumsInaccessible != 0 {
		t.Fatalf("album mirroring failed after deletion: %+v", st)
	}
}

// Deletion-off regression for all-accessible mirroring: a source trash never
// becomes media deletion. The mapping and the owned album survive; the absent
// asset leaves the source album, so complete-scan membership reconciliation may
// drop that one relation without touching media or the album's identity.
func TestAllAccessibleDeletionOffKeepsMediaAndOwnedAlbum(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	names := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven"}
	assets := make([]bridge.Asset, 0, len(names))
	data := make(map[string]string, len(names))
	for i, name := range names {
		content := fmt.Sprintf("item-%02d", i)
		assets = append(assets, contentPhoto(name, content))
		data[name] = content
	}
	s := &sourceFake{
		identity: "source",
		albums:   []bridge.Album{{ID: "album-a", Name: "Family", Count: len(assets)}},
		scoped: map[string]bridge.Page{
			"album-a\x00": {Assets: assets},
			"\x00":        {Assets: assets},
		},
		data: data,
	}
	d := newAlbumTarget()
	byID := make(map[string]bridge.Asset, len(assets))
	for _, asset := range assets {
		byID[asset.ID] = asset
	}
	s.assets = byID
	e := engine(t, store, s, d, accessibleOptions())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.creates != 1 || len(d.albums["album-1"]) != len(assets) {
		t.Fatalf("setup did not mirror the album: creates=%d albums=%v", d.creates, d.albums)
	}
	trashed := assets[0]
	trashed.ExcludedReason = "trashed"
	s.assets[trashed.ID] = trashed
	s.scoped["album-a\x00"] = bridge.Page{Assets: assets[1:]}
	s.scoped["\x00"] = bridge.Page{Assets: assets[1:]}
	s.albums[0].Count = len(assets) - 1
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Deleted != 0 || st.DeletePending != 0 || st.Confirmed != len(assets) || len(d.trashed) != 0 {
		t.Fatalf("deletion-disabled mirroring changed media: %+v trashed=%v", st, d.trashed)
	}
	if d.creates != 1 || len(d.albums["album-1"]) != len(assets)-1 {
		t.Fatalf("deletion-disabled mirroring changed more than the absent relation: creates=%d albums=%v", d.creates, d.albums)
	}
	absent := nodeFor(t, d, data[trashed.ID])
	if slices.Contains(d.albums["album-1"], absent) {
		t.Fatalf("bounded reconciliation kept the absent relation: %v", d.albums["album-1"])
	}
}
