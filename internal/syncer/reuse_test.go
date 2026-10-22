package syncer_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// elsewhere returns a verified node that lives only in another owned folder.
func elsewhere(id, name, md5 string, size int64) bridge.Node {
	return bridge.Node{ID: id, Name: name, MD5: md5, Size: size, ParentID: "other-folder"}
}

// albumOnly returns a verified node reachable only through an owned album.
func albumOnly(id, name, md5 string, size int64) bridge.Node {
	return bridge.Node{ID: id, Name: name, MD5: md5, Size: size, AlbumOnly: true}
}

// stagingFiles reports the staged originals still occupying the quota.
func stagingFiles(t *testing.T, dir string) int {
	t.Helper()
	files, err := os.ReadDir(filepath.Join(dir, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func TestExistingContentOutsideTargetIsReusedWithoutUploadOrStaging(t *testing.T) {
	for name, node := range map[string]bridge.Node{
		"another folder": elsewhere("remote-node", "old-name.jpg", "5d41402abc4b2a76b9719d911017c592", 5),
		"album only":     albumOnly("remote-node", "old-name.jpg", "5d41402abc4b2a76b9719d911017c592", 5),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
				data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target", nodes: []bridge.Node{node}}
			originals := 0
			s.original = func(context.Context, string) (io.ReadCloser, error) {
				originals++
				return io.NopCloser(strings.NewReader("hello")), nil
			}
			e := engine(t, store, s, d, options())
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.Existing != 1 || st.Confirmed != 0 || st.Unconfirmed != 0 || st.LastSuccess == nil {
				t.Fatalf("remote copy was not recorded as existing: %+v", st)
			}
			if len(d.bodies) != 0 {
				t.Fatalf("existing content was uploaded again: %d", len(d.bodies))
			}
			if stagingFiles(t, dir) != 0 {
				t.Fatal("reused content kept its staged original")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			d.lookedAll = 0
			e = engine(t, store, s, d, options())
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			if st := status(t, e); st.Existing != 1 || len(d.bodies) != 0 {
				t.Fatalf("repeat run re-downloaded or re-uploaded: %+v uploads=%d", st, len(d.bodies))
			}
			if d.lookedAll != 0 {
				t.Fatalf("durable mapping did not avoid a global lookup: %d", d.lookedAll)
			}
			if originals != 1 {
				t.Fatalf("repeat run downloaded the original again: %d", originals)
			}
		})
	}
}

func TestReusedRemoteContentStillReceivesAlbumMembership(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := newAlbumTarget()
	d.nodes = []bridge.Node{albumOnly("remote-node", "old-name.jpg", "5d41402abc4b2a76b9719d911017c592", 5)}
	opts := options()
	opts.AlbumSyncMode = bridge.AlbumSyncSelected
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Existing != 1 || st.LastSuccess == nil || st.AssociationsPending != 0 {
		t.Fatalf("reused media did not settle membership: %+v", st)
	}
	if len(d.albums["album-1"]) != 1 || d.albums["album-1"][0] != "remote-node" {
		t.Fatalf("album membership was skipped for reused media: %v", d.albums)
	}
	if len(d.bodies) != 0 {
		t.Fatal("membership reuse uploaded the original again")
	}
}

func TestGlobalLookupAmbiguityNeverUploadsOrConfirms(t *testing.T) {
	for name, find := range map[string]func(context.Context, bridge.Content) (bridge.Node, error){
		"multiple candidates": func(context.Context, bridge.Content) (bridge.Node, error) {
			return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
		},
		"size mismatch": func(context.Context, bridge.Content) (bridge.Node, error) {
			return elsewhere("remote-node", "one.jpg", "5d41402abc4b2a76b9719d911017c592", 4), nil
		},
		"digest mismatch": func(context.Context, bridge.Content) (bridge.Node, error) {
			return elsewhere("remote-node", "one.jpg", "00000000000000000000000000000000", 5), nil
		},
		"incomplete pagination": func(context.Context, bridge.Content) (bridge.Node, error) {
			return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
		},
		"unavailable": func(context.Context, bridge.Content) (bridge.Node, error) {
			return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
				data: map[string]string{"one": "hello"}}
			posts := 0
			d := &targetFake{identity: "target", findAll: find,
				upload: func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
					posts++
					return bridge.Node{}, &bridge.Error{Code: bridge.Conflict}
				}}
			e := engine(t, store, s, d, options())
			if err := e.Once(t.Context()); err == nil {
				t.Fatal("ambiguous lookup was reported successful")
			}
			if st := status(t, e); st.Confirmed != 0 || st.Existing != 0 {
				t.Fatalf("ambiguous lookup confirmed content: %+v", st)
			}
			if posts != 0 {
				t.Fatalf("ambiguous lookup uploaded the original %d times", posts)
			}
			if stagingFiles(t, dir) == 0 {
				t.Fatal("unresolved staging was released without a verified match")
			}
		})
	}
}

func TestConflictReconcilesGlobalCopyAndReleasesStaging(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	posts, lookups := 0, 0
	d := &targetFake{identity: "target"}
	d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		posts++
		// Amazon's global MD5 dedupe rejects the write; the copy is elsewhere.
		return bridge.Node{}, &bridge.Error{Code: bridge.Conflict}
	}
	d.findAll = func(context.Context, bridge.Content) (bridge.Node, error) {
		lookups++
		if lookups == 1 {
			// The pre-upload lookup proved no copy; the 409 then arrived because
			// the copy lives under another parent.
			return bridge.Node{}, nil
		}
		return elsewhere("remote-node", "one.jpg", "5d41402abc4b2a76b9719d911017c592", 5), nil
	}
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if posts != 1 || st.Existing != 1 || st.Unconfirmed != 0 {
		t.Fatalf("409 was not reconciled to verified existing content: posts=%d %+v", posts, st)
	}
	if stagingFiles(t, dir) != 0 {
		t.Fatal("reconciled conflict kept its staging")
	}
}

func Test409AloneStillNeverConfirmsSuccess(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	posts := 0
	d := &targetFake{identity: "target"}
	d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		posts++
		return bridge.Node{}, &bridge.Error{Code: bridge.Conflict}
	}
	// The account-wide lookup proves absence; a 409 alone must stay a conflict.
	d.findAll = func(context.Context, bridge.Content) (bridge.Node, error) {
		return bridge.Node{}, nil
	}
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err == nil || err.Error() != bridge.Conflict {
		t.Fatalf("409 alone was treated as success: %v", err)
	}
	if st := status(t, e); st.Confirmed != 0 || st.Existing != 0 || st.Skipped != 0 {
		t.Fatalf("409 alone was recorded as backed up: %+v", st)
	}
	if posts != 1 {
		t.Fatalf("conflict was replayed: %d", posts)
	}
}

func TestCrashAfterRemoteCommitOutsideTargetReconcilesWithoutUpload(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	posts := 0
	d := &targetFake{identity: "target"}
	d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		posts++
		return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
	}
	if err := engine(t, store, s, d, options()).Once(t.Context()); err == nil {
		t.Fatal("uncertain write succeeded")
	}
	store.Close()
	// Restart: the committed copy is visible only through the account lookup.
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d.upload = nil
	d.findAll = func(context.Context, bridge.Content) (bridge.Node, error) {
		return elsewhere("remote-node", "one.jpg", "5d41402abc4b2a76b9719d911017c592", 5), nil
	}
	e := engine(t, store, s, d, options())
	if err := e.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Existing != 1 || st.Unconfirmed != 0 {
		t.Fatalf("uncertain write was not reconciled to its committed copy: %+v", st)
	}
	if posts != 1 {
		t.Fatalf("uncertain write was posted again: %d", posts)
	}
	if stagingFiles(t, dir) != 0 {
		t.Fatal("reconciled uncertainty kept its staging")
	}
}

func TestUncertainWriteWithZeroGlobalMatchesNeverReposts(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	posts := 0
	d := &targetFake{identity: "target", findAll: func(context.Context, bridge.Content) (bridge.Node, error) {
		return bridge.Node{}, nil
	}}
	d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		posts++
		return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
	}
	for i := range 3 {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		e := engine(t, store, s, d, options())
		if i > 0 {
			if err := e.Retry(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Once(t.Context()); err == nil {
			t.Fatal("an empty lookup after an uncertain write was treated as success")
		}
		if st := status(t, e); st.Unconfirmed != 1 || st.Confirmed != 0 || st.Existing != 0 {
			t.Fatalf("uncertainty was lost: %+v", st)
		}
		store.Close()
	}
	if posts != 1 {
		t.Fatalf("uncertain write was replayed %d times", posts)
	}
	if stagingFiles(t, dir) != 1 {
		t.Fatal("uncertain write lost its staging evidence")
	}
}

func TestReusedContentFreesStagingQuotaForLaterAssets(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, second := photo("one"), photo("two")
	second.Name, second.Size, second.SHA1 = "two.jpg", 5, ""
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{first, second}}},
		data: map[string]string{"one": "hello", "two": "world"}}
	d := &targetFake{identity: "target", nodes: []bridge.Node{
		elsewhere("remote-node", "one.jpg", "5d41402abc4b2a76b9719d911017c592", 5),
	}}
	opts := options()
	opts.MaxStagingFiles = 1
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Existing != 1 || st.Confirmed != 1 || st.Failed != 0 {
		t.Fatalf("reuse did not free the single staging slot: %+v", st)
	}
	if len(d.bodies) != 1 || d.bodies[0] != "world" {
		t.Fatalf("only the genuinely new original should be uploaded: %v", d.bodies)
	}
	if stagingFiles(t, dir) != 0 {
		t.Fatal("settled staging remains")
	}
}

func TestReusedNodeVerificationFailureKeepsUncertainty(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	posts := 0
	d := &targetFake{identity: "target"}
	d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		posts++
		return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
	}
	if err := engine(t, store, s, d, options()).Once(t.Context()); err == nil {
		t.Fatal("uncertain write succeeded")
	}
	// The lookup finds a candidate, but its re-read contradicts the metadata.
	d.findAll = func(context.Context, bridge.Content) (bridge.Node, error) {
		return elsewhere("remote-node", "one.jpg", "5d41402abc4b2a76b9719d911017c592", 6), nil
	}
	e := engine(t, store, s, d, options())
	if err := e.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(t.Context()); err == nil {
		t.Fatal("mismatched remote metadata was accepted")
	}
	if st := status(t, e); st.Existing != 0 || st.Confirmed != 0 || posts != 1 {
		t.Fatalf("mismatched candidate was adopted: %+v posts=%d", st, posts)
	}
	if stagingFiles(t, dir) != 1 {
		t.Fatal("mismatched candidate released the staging evidence")
	}
}

func TestReuseRequiresAuthoritativeTargetScopeForConfirmedNodes(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", nodes: []bridge.Node{
		{ID: "remote-node", Name: "one.jpg", MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5},
	}}
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || len(d.bodies) != 0 {
		t.Fatalf("target copy was not reused: %+v", st)
	}
	// A later read that contradicts the verified content must not be adopted.
	d.get = func(context.Context, string) (bridge.Node, error) {
		return bridge.Node{ID: "remote-node", MD5: "00000000000000000000000000000000", Size: 5}, nil
	}
	two := photo("two")
	s.data["two"] = "hello"
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{two}}
	if err := e.Once(t.Context()); err == nil {
		t.Fatal("contradicting remote metadata was adopted")
	}
	if len(d.bodies) != 0 {
		t.Fatal("contradicting metadata caused another upload")
	}
}
