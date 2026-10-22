package syncer_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

type sourceFake struct {
	bridge.Source
	identity string
	pages    map[string]bridge.Page
	assets   map[string]bridge.Asset
	data     map[string]string
	original func(context.Context, string) (io.ReadCloser, error)
	page     func(context.Context, bridge.Scope, string) (bridge.Page, error)
	album    func(context.Context, string) (bridge.Album, error)
	authErr  error
}

func (s *sourceFake) Identity(context.Context) (string, error) { return s.identity, s.authErr }
func (s *sourceFake) Album(ctx context.Context, id string) (bridge.Album, error) {
	if s.album != nil {
		return s.album(ctx, id)
	}
	count := 0
	for _, p := range s.pages {
		count += len(p.Assets)
	}
	return bridge.Album{ID: id, Count: count}, nil
}
func (s *sourceFake) Assets(ctx context.Context, scope bridge.Scope, cursor string) (bridge.Page, error) {
	if s.page != nil {
		return s.page(ctx, scope, cursor)
	}
	return s.pages[cursor], nil
}
func (s *sourceFake) Asset(_ context.Context, id string) (bridge.Asset, error) {
	return s.assets[id], nil
}
func (s *sourceFake) Original(ctx context.Context, id string) (io.ReadCloser, error) {
	if s.original != nil {
		return s.original(ctx, id)
	}
	return io.NopCloser(strings.NewReader(s.data[id])), nil
}

type targetFake struct {
	bridge.Target
	identity  string
	mu        sync.Mutex
	nodes     []bridge.Node
	bodies    []string
	upload    func(context.Context, bridge.Content, io.Reader) (bridge.Node, error)
	find      func(context.Context, bridge.Content) ([]bridge.Node, error)
	findAll   func(context.Context, bridge.Content) (bridge.Node, error)
	get       func(context.Context, string) (bridge.Node, error)
	trash     func(context.Context, bridge.Content, string) error
	trashed   []string
	authErr   error
	lookedAll int
}

func (t *targetFake) Probe(context.Context) (bridge.Capabilities, error) {
	return bridge.Capabilities{Identity: t.identity}, t.authErr
}

// fakeFind models the strict target lookup: only nodes at the requested parent.
func (t *targetFake) Find(ctx context.Context, c bridge.Content) ([]bridge.Node, error) {
	if t.find != nil {
		return t.find(ctx, c)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var nodes []bridge.Node
	for _, n := range t.nodes {
		if n.ParentID == c.ParentID && !n.AlbumOnly && strings.EqualFold(n.MD5, c.MD5) && n.Size == c.Size {
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

// FindExisting models the controlled account-wide lookup: only one verified
// node may match, and it may live in another folder or only in an album.
func (t *targetFake) FindExisting(ctx context.Context, c bridge.Content) (bridge.Node, error) {
	if t.findAll != nil {
		return t.findAll(ctx, c)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lookedAll++
	var matches []bridge.Node
	for _, n := range t.nodes {
		if n.MD5 == c.MD5 && n.Size == c.Size && n.ID != "" {
			matches = append(matches, n)
		}
	}
	if len(matches) == 0 {
		return bridge.Node{}, nil
	}
	if len(matches) > 1 {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	return matches[0], nil
}

func (t *targetFake) GetNode(ctx context.Context, id string) (bridge.Node, error) {
	if t.get != nil {
		return t.get(ctx, id)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, n := range t.nodes {
		if n.ID == id {
			return n, nil
		}
	}
	return bridge.Node{}, &bridge.Error{Code: bridge.NotFound}
}
func (t *targetFake) Trash(ctx context.Context, c bridge.Content, id string) error {
	if t.trash != nil {
		return t.trash(ctx, c, id)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.nodes {
		if t.nodes[i].ID == id {
			if t.nodes[i].MD5 != c.MD5 || t.nodes[i].Size != c.Size {
				return &bridge.Error{Code: bridge.Unconfirmed}
			}
			t.trashed = append(t.trashed, id)
			t.nodes[i].MD5 = ""
			return nil
		}
	}
	return &bridge.Error{Code: bridge.NotFound}
}

func (t *targetFake) Upload(ctx context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
	if t.upload != nil {
		return t.upload(ctx, c, r)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return bridge.Node{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	sum := md5.Sum(b)
	n := bridge.Node{ID: fmt.Sprint(len(t.nodes) + 1), Name: c.Name, MD5: hex.EncodeToString(sum[:]),
		Size: int64(len(b)), ParentID: c.ParentID}
	t.nodes = append(t.nodes, n)
	t.bodies = append(t.bodies, string(b))
	return n, nil
}

func photo(id string) bridge.Asset {
	return bridge.Asset{ID: id, Name: id + ".jpg", MIME: "image/jpeg", Kind: "IMAGE",
		Size: 5, SHA1: "qvTGHdzF6KLavt4PO0gs2a6pQ00="}
}

func TestOriginalIsConfirmedOnceAcrossAlbumsAndRestart(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{photo("one")}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.AlbumIDs = []string{"album", "second"}
	for i := 0; i < 2; i++ {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		e := engine(t, store, s, d, opts)
		if err := e.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		st := status(t, e)
		if st.Confirmed != 1 || st.LastSuccess == nil {
			t.Fatalf("incomplete confirmation: %+v", st)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.nodes) != 1 || len(d.bodies) != 1 || d.bodies[0] != "hello" {
		t.Fatalf("repeat scan duplicated or changed the original: %+v", d)
	}
}

func options() syncer.Options {
	return syncer.Options{AccountID: "account", AlbumIDs: []string{"album"}, Workers: 2,
		MaxStagingBytes: 1024 * 1024, MaxStagingFiles: 2}
}

func engine(t *testing.T, store *syncer.Store, s bridge.Source, d bridge.Target, opts syncer.Options) *syncer.Engine {
	t.Helper()
	e, err := syncer.New(store, s, d, opts)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func status(t *testing.T, e *syncer.Engine) syncer.Status {
	t.Helper()
	st, err := e.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestPausePersistsAcrossRestartAndAccountsAreIsolated(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if competing, err := syncer.Open(dir); err == nil {
		competing.Close()
		t.Fatal("a competing writer acquired the data directory")
	}
	s, d := &sourceFake{identity: "source"}, &targetFake{identity: "target"}
	e := engine(t, store, s, d, options())
	if err := e.Pause(context.Background(), true); err != nil {
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
	e = engine(t, store, s, d, options())
	if !status(t, e).Paused {
		t.Fatal("pause was lost on restart")
	}
	other := options()
	other.AccountID = "other"
	if status(t, engine(t, store, s, d, other)).Paused {
		t.Fatal("another account inherited pause")
	}
	if err := e.Once(context.Background()); err == nil {
		t.Fatal("paused account ran")
	}
}

type generated struct{ read int64 }

func (g *generated) Read(p []byte) (int, error) {
	if g.read >= 128<<10 {
		return 0, errors.New("test stream consumed beyond its limited sink")
	}
	clear(p)
	g.read += int64(len(p))
	return len(p), nil
}

func TestFiveGBOriginalStopsAtQuotaAndRemovesPartialStaging(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	large := photo("large")
	large.Size, large.SHA1 = -1, ""
	stream := &generated{}
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{large}}},
		original: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(io.LimitReader(stream, 5<<30)), nil
		}}
	uploads := 0
	d := &targetFake{identity: "target", upload: func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		uploads++
		return bridge.Node{}, errors.New("quota failure must not upload")
	}}
	opts := options()
	opts.MaxStagingBytes = 64 << 10
	e := engine(t, store, s, d, opts)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := e.Once(context.Background()); err == nil || err.Error() != "staging_quota" {
		t.Fatalf("expected explicit quota failure, got %v", err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("quota path allocated %d bytes", allocated)
	if allocated > 8<<20 {
		t.Errorf("quota path allocated %d bytes for a bounded partial stream", allocated)
	}
	if stream.read > 96<<10 || uploads != 0 {
		t.Fatalf("unbounded stream read %d or uploaded incomplete content", stream.read)
	}
	files, err := os.ReadDir(filepath.Join(dir, "staging"))
	if err != nil || len(files) != 0 {
		t.Fatalf("partial staging remains: %v, %v", files, err)
	}
	if st := status(t, e); st.Failed != 1 || st.Errors["staging_quota"] != 1 {
		t.Fatalf("quota failure was not persisted: %+v", st)
	}
}

type zeroStream struct{}

func (zeroStream) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestSuccessfulOriginalStreamsWithinHeapBudgetAndPersistsConfirmation(t *testing.T) {
	const size = 32 << 20
	// Independently precomputed MD5 of 32 MiB of zero bytes.
	const digest = "58f06dd588d8ffb3beb46ada6309436b"
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	asset := photo("large")
	asset.Size, asset.SHA1 = -1, ""
	source := &sourceFake{identity: "source",
		pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{asset}}},
		original: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(io.LimitReader(zeroStream{}, size)), nil
		}}
	var uploaded int64
	var uploads int
	target := &targetFake{identity: "target", get: func(_ context.Context, id string) (bridge.Node, error) {
		if id != "large-node" || uploaded != size || uploads != 1 {
			return bridge.Node{}, &bridge.Error{Code: bridge.NotFound}
		}
		return bridge.Node{ID: id, Name: asset.Name, MD5: digest, Size: size}, nil
	}, upload: func(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
		uploads++
		hash := md5.New()
		n, err := io.CopyBuffer(hash, struct{ io.Reader }{r}, make([]byte, 64<<10))
		uploaded += n
		actual := hex.EncodeToString(hash.Sum(nil))
		if err != nil || n != size || actual != digest || c.MD5 != digest || c.Size != size {
			return bridge.Node{}, fmt.Errorf("stream confirmation: bytes=%d md5=%s content=%+v err=%v", n, actual, c, err)
		}
		return bridge.Node{ID: "large-node", Name: c.Name, MD5: actual, Size: n, ParentID: c.ParentID}, nil
	}}
	opts := options()
	opts.MaxStagingBytes = size + (1 << 20)
	e := engine(t, store, source, target, opts)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("successful 32 MiB core transfer allocated %d bytes", allocated)
	if allocated > 16<<20 {
		t.Errorf("transfer allocated %d bytes; budget must remain below the payload size", allocated)
	}
	if uploaded != size || uploads != 1 || status(t, e).Confirmed != 1 {
		t.Fatalf("original not confirmed once: bytes=%d uploads=%d status=%+v", uploaded, uploads, status(t, e))
	}
	files, err := os.ReadDir(filepath.Join(dir, "staging"))
	if err != nil || len(files) != 0 {
		t.Fatalf("confirmed staging remains: %v %v", files, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st := status(t, engine(t, store, source, target, opts)); st.Confirmed != 1 {
		t.Fatalf("confirmation was not durable: %+v", st)
	}
}

func TestProcessCrashAfterRemoteCommitReconcilesWithoutAnotherUpload(t *testing.T) {
	if dir := os.Getenv("SYNCER_CRASH_HELPER"); dir != "" {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
			data: map[string]string{"one": "hello"}}
		d := &targetFake{identity: "target", upload: func(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
			b, err := io.ReadAll(r)
			if err != nil || string(b) != "hello" || c.MD5 != "5d41402abc4b2a76b9719d911017c592" || c.Size != 5 {
				t.Fatal("upload received an incomplete original")
			}
			if err := os.WriteFile(filepath.Join(dir, "remote-commit"), b, 0600); err != nil {
				t.Fatal(err)
			}
			os.Exit(0)
			return bridge.Node{}, nil
		}}
		if err := engine(t, store, s, d, options()).Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Fatal("helper did not crash")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessCrashAfterRemoteCommitReconcilesWithoutAnotherUpload$")
	cmd.Env = append(os.Environ(), "SYNCER_CRASH_HELPER="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v: %s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "remote-commit")); err != nil || string(b) != "hello" {
		t.Fatalf("remote did not commit: %q, %v", b, err)
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", nodes: []bridge.Node{{ID: "remote", Name: "one.jpg",
		MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5}}}
	e := engine(t, store, s, d, options())
	if st := status(t, e); st.Unconfirmed != 1 {
		t.Fatalf("interrupted upload was not recovered as uncertain: %+v", st)
	}
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || len(d.bodies) != 0 {
		t.Fatalf("committed content was reuploaded or not confirmed: %+v", st)
	}
	files, _ := os.ReadDir(filepath.Join(dir, "staging"))
	if len(files) != 0 {
		t.Fatalf("confirmed staging was not cleaned: %v", files)
	}
}

func TestAmbiguousUploadNeverRepostsEvenAfterRetryAndRestart(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	posts := 0
	d := &targetFake{identity: "target", upload: func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		posts++
		return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
	}}
	for i := 0; i < 3; i++ {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		e := engine(t, store, s, d, options())
		if i > 0 {
			if err := e.Retry(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Once(context.Background()); err == nil {
			t.Fatal("ambiguous write was silently reported successful")
		}
		if st := status(t, e); st.Unconfirmed != 1 || st.Confirmed != 0 {
			t.Fatalf("uncertainty was lost: %+v", st)
		}
		store.Close()
	}
	if posts != 1 {
		t.Fatalf("ambiguous upload was posted %d times", posts)
	}
}

func TestTargetScopedDedupRequiresConfirmedContentAndBoundParent(t *testing.T) {
	for _, tc := range []struct {
		name string
		node bridge.Node
		code string
	}{
		{"verified content with another name", bridge.Node{ID: "existing", Name: "old.jpg", MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5}, ""},
		{"same name other content", bridge.Node{ID: "existing", Name: "one.jpg", MD5: "other", Size: 5}, bridge.Conflict},
		{"missing digest", bridge.Node{ID: "existing", Name: "one.jpg", Size: 5}, bridge.Unconfirmed},
		{"wrong size", bridge.Node{ID: "existing", Name: "one.jpg", MD5: "5d41402abc4b2a76b9719d911017c592", Size: 4}, bridge.Conflict},
		{"different parent", bridge.Node{ID: "existing", Name: "one.jpg", MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5, ParentID: "other-account"}, bridge.Unconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
				data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target", nodes: []bridge.Node{tc.node}}
			// The strict target lookup returns the raw candidate so the engine
			// must reject anything unverified itself.
			d.find = func(context.Context, bridge.Content) ([]bridge.Node, error) {
				return []bridge.Node{tc.node}, nil
			}
			d.findAll = func(context.Context, bridge.Content) (bridge.Node, error) {
				return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
			}
			e := engine(t, store, s, d, options())
			err = e.Once(context.Background())
			if tc.code == "" {
				if err != nil || status(t, e).Confirmed != 1 {
					t.Fatalf("verified existing node was not reused: %v %+v", err, status(t, e))
				}
			} else if err == nil || err.Error() != tc.code || status(t, e).Confirmed != 0 {
				t.Fatalf("unverified content was accepted: %v %+v", err, status(t, e))
			}
			if len(d.bodies) != 0 {
				t.Fatal("candidate content was uploaded again")
			}
		})
	}
}

func TestExplicitRateLimitRespectsRetryAfterAndCanResume(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	var attempts []time.Time
	const delay = 150 * time.Millisecond
	d.upload = func(ctx context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
		attempts = append(attempts, time.Now())
		if len(attempts) == 1 {
			return bridge.Node{}, &bridge.Error{Code: bridge.RateLimit, RetryAfter: delay}
		}
		d.upload = nil
		return d.Upload(ctx, c, r)
	}
	e := engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err == nil || err.Error() != bridge.RateLimit {
		t.Fatalf("expected explicit rate limit, got %v", err)
	}
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed == 0 {
		if st.NextAttempt == nil {
			t.Fatal("rate limit omitted retry deadline")
		}
		time.Sleep(max(0, time.Until(*st.NextAttempt)) + time.Millisecond)
		if err := e.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(attempts) == 2 && attempts[1].Sub(attempts[0]) < delay {
		t.Fatal("Retry-After was ignored")
	}
	if len(attempts) != 2 || status(t, e).Confirmed != 1 {
		t.Fatalf("explicitly rejected write could not resume: %d %+v", len(attempts), status(t, e))
	}
}

func TestAuthenticationRotationResumesButRetryPreservesManualPause(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		return bridge.Node{}, &bridge.Error{Code: bridge.Authentication}
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err == nil {
		t.Fatal("expired authentication succeeded")
	}
	if st := status(t, e); !st.Paused || st.PauseReason != "auth" || st.Unconfirmed != 0 {
		t.Fatalf("explicit auth rejection did not pause safely: %+v", st)
	}
	store.Close()
	d.upload = nil
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e = engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || st.Paused {
		t.Fatalf("rotated credentials did not resume: %+v", st)
	}
	if err := e.Pause(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); !st.Paused || st.PauseReason != "manual" {
		t.Fatalf("retry resumed a manual pause: %+v", st)
	}
}

func TestExcludedRecordsCountTowardCompleteScanAndOptionalMIMEIsNotRequired(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	image := photo("image")
	image.MIME, image.Size, image.SHA1 = "", -1, ""
	hidden := photo("hidden")
	hidden.ExcludedReason = "hidden"
	video := photo("video")
	video.Kind, video.MIME = "VIDEO", ""
	companion := photo("companion")
	companion.LivePhotoVideo = true
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{image, hidden, video, companion}},
	}, data: map[string]string{"image": "hello"},
		original: func(_ context.Context, id string) (io.ReadCloser, error) {
			if id != "image" {
				t.Errorf("excluded original %s was downloaded", id)
			}
			return io.NopCloser(strings.NewReader("hello")), nil
		}}
	d := &targetFake{identity: "target"}
	e := engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || st.Skipped != 3 || st.LastSuccess == nil {
		t.Fatalf("valid source exclusions prevented a complete scan: %+v", st)
	}
	if len(d.bodies) != 1 {
		t.Fatalf("expected one original, got %d", len(d.bodies))
	}
}

func TestRediscoveryRefreshesExcludedAssetButBlocksChangedConfirmedOriginal(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := photo("one")
	a.ExcludedReason = "offline"
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	e := engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err != nil || status(t, e).Skipped != 1 {
		t.Fatalf("offline original: %v %+v", err, status(t, e))
	}
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{photo("one")}}
	if err := e.Once(context.Background()); err != nil || status(t, e).Confirmed != 1 {
		t.Fatalf("eligible original did not resume: %v %+v", err, status(t, e))
	}
	a = photo("one")
	a.SHA1 = "invalid-changed-digest"
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{a}}
	if err := e.Once(context.Background()); err == nil || err.Error() != "source_changed" {
		t.Fatalf("changed original was ignored: %v", err)
	}
	if st := status(t, e); st.Errors["source_changed"] != 1 || len(d.nodes) != 1 {
		t.Fatalf("changed original lost its existing mapping or uploaded: %+v", st)
	}
}

func TestRemovedPendingScopeAssetIsNotUploaded(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		return bridge.Node{}, &bridge.Error{Code: bridge.RateLimit, RetryAfter: time.Hour}
	}
	e := engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err == nil {
		t.Fatal("expected delayed upload")
	}
	s.pages[""] = bridge.Page{}
	d.upload = nil
	if err := e.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.nodes) != 0 {
		t.Fatal("asset removed from scope was uploaded from historical membership")
	}
}

func TestIncompleteOrLoopingScanNeverAdvancesLastSuccess(t *testing.T) {
	for _, problem := range []string{"self loop", "page cycle", "duplicate assets", "empty middle page", "empty final page", "missing album assets", "malformed asset", "missing album", "source unavailable"} {
		t.Run(problem, func(t *testing.T) {
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
				data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target"}
			e := engine(t, store, s, d, options())
			if err := e.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := status(t, e).LastSuccess
			switch problem {
			case "self loop":
				s.pages = map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}, Next: "x"},
					"x": {Assets: []bridge.Asset{photo("two")}, Next: "x"}}
			case "page cycle":
				s.pages = map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}, Next: "x"},
					"x": {Assets: []bridge.Asset{photo("two")}, Next: "y"},
					"y": {Assets: []bridge.Asset{photo("three")}, Next: "x"}}
			case "duplicate assets":
				s.pages = map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}, Next: "x"},
					"x": {Assets: []bridge.Asset{photo("one")}}}
			case "empty middle page":
				s.pages = map[string]bridge.Page{"": {Next: "x"}}
			case "empty final page":
				s.pages = map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}, Next: "x"}, "x": {}}
			case "missing album assets":
				s.pages = map[string]bridge.Page{"": {}}
				s.album = func(_ context.Context, id string) (bridge.Album, error) {
					return bridge.Album{ID: id, Count: 1}, nil
				}
			case "malformed asset":
				s.pages = map[string]bridge.Page{"": {Assets: []bridge.Asset{{}}}}
			case "missing album":
				s.album = func(context.Context, string) (bridge.Album, error) {
					return bridge.Album{}, &bridge.Error{Code: bridge.NotFound}
				}
			case "source unavailable":
				s.page = func(context.Context, bridge.Scope, string) (bridge.Page, error) {
					return bridge.Page{}, &bridge.Error{Code: bridge.Unavailable}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := e.Once(ctx); err == nil {
				t.Fatal("incomplete scan was accepted")
			}
			if st := status(t, e); st.LastSuccess == nil || !st.LastSuccess.Equal(*before) || st.Scanning == 0 {
				t.Fatalf("failed scan advanced completion: %+v", st)
			}
		})
	}
}

func TestDiscoveryYieldsAndResumesItsPageCursorAfterRestart(t *testing.T) {
	dir := t.TempDir()
	var visited []string
	s := &sourceFake{identity: "source", page: func(_ context.Context, _ bridge.Scope, token string) (bridge.Page, error) {
		visited = append(visited, token)
		n := 0
		if token != "" {
			if _, err := fmt.Sscan(token, &n); err != nil {
				t.Fatal(err)
			}
		}
		a := photo(fmt.Sprint(n))
		a.ExcludedReason = "hidden"
		next := fmt.Sprint(n + 1)
		if n == 19 {
			next = ""
		}
		return bridge.Page{Assets: []bridge.Asset{a}, Next: next}, nil
	}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.AlbumIDs, opts.Library = nil, true
	for i := 0; i < 3; i++ {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		e := engine(t, store, s, d, opts)
		start := len(visited)
		if err := e.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(visited)-start > 8 {
			t.Fatal("one invocation blocked on the full backfill")
		}
		st := status(t, e)
		if i < 2 && st.LastSuccess != nil {
			t.Fatal("partial discovery was marked complete")
		}
		if i == 2 && (st.LastSuccess == nil || st.Skipped != 20) {
			t.Fatalf("resumed scan did not finish: %+v", st)
		}
		store.Close()
	}
	if len(visited) != 20 || visited[8] != "8" || visited[16] != "16" {
		t.Fatalf("restart did not resume its committed cursor: %v", visited)
	}
}

func TestFilenameSafetyAndChecksumMismatchDoNotUpload(t *testing.T) {
	for _, tc := range []struct{ name, filename, sha, code string }{
		{"traversal", "../../outside.jpg", "", "unsafe_filename"},
		{"windows path", `C:\private\one.jpg`, "", "unsafe_filename"},
		{"NUL", "one\x00.jpg", "", "unsafe_filename"},
		{"newline", "one\n.jpg", "", "unsafe_filename"},
		{"source mismatch", "one.jpg", "AAAAAAAAAAAAAAAAAAAAAAAAAAA=", "checksum_mismatch"},
		{"known empty", "one.jpg", "", "size_mismatch"},
		{"safe unicode", "旅行.jpg", "qvTGHdzF6KLavt4PO0gs2a6pQ00=", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			a := photo("one")
			a.Name, a.SHA1 = tc.filename, tc.sha
			if tc.name == "known empty" {
				a.Size = 0
			}
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}},
				data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target"}
			e := engine(t, store, s, d, options())
			err = e.Once(context.Background())
			if tc.code == "" {
				if err != nil || len(d.nodes) != 1 || d.nodes[0].Name != tc.filename {
					t.Fatalf("safe original filename was lost: %v %+v", err, d.nodes)
				}
				return
			}
			if err == nil || err.Error() != tc.code || len(d.nodes) != 0 {
				t.Fatalf("unsafe content accepted: %v %+v", err, d.nodes)
			}
			files, _ := os.ReadDir(filepath.Join(dir, "staging"))
			if len(files) != 0 {
				t.Fatalf("rejected content left staging: %v", files)
			}
		})
	}
}

func TestAccountIdentityChangesCannotReuseStateOrWrite(t *testing.T) {
	for _, change := range []string{"source", "target"} {
		t.Run(change, func(t *testing.T) {
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
				data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target"}
			e := engine(t, store, s, d, options())
			if err := e.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
			if change == "source" {
				s.identity = "different"
			} else {
				d.identity = "different"
			}
			s.pages[""] = bridge.Page{Assets: []bridge.Asset{photo("two")}}
			if err := e.Once(context.Background()); err == nil || err.Error() != "identity_mismatch" {
				t.Fatalf("changed identity was accepted: %v", err)
			}
			if st := status(t, e); st.Confirmed != 1 || st.Errors["identity_mismatch"] != 1 || len(d.nodes) != 1 {
				t.Fatalf("changed identity reused or damaged state: %+v", st)
			}
		})
	}
}

func TestStalledAccountDoesNotBlockAnotherAccountAndReadinessNeedsNoAccount(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		original: func(ctx context.Context, _ string) (io.ReadCloser, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	e := engine(t, store, s, &targetFake{identity: "target"}, options())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Once(ctx) }()
	defer func() {
		cancel()
		<-done
		store.Close()
	}()
	<-entered
	if err := e.Once(context.Background()); err == nil || err.Error() != "engine_busy" {
		t.Fatalf("same account ran concurrently: %v", err)
	}
	other := options()
	other.AccountID = "second"
	second := engine(t, store, &sourceFake{identity: "other", pages: map[string]bridge.Page{"": {}}},
		&targetFake{identity: "other-target"}, other)
	if err := second.Once(context.Background()); err != nil {
		t.Fatalf("another account was blocked: %v", err)
	}
}

func TestTransferHonorsConfiguredByteRate(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.BytesPerSecond = 100
	e := engine(t, store, s, d, opts)
	start := time.Now()
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Fatalf("download and upload ignored byte cap: %v", elapsed)
	}
}

func TestChangedStagingAfterRestartCannotUploadCorruptOriginal(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", upload: func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		return bridge.Node{}, &bridge.Error{Code: bridge.RateLimit, RetryAfter: time.Hour}
	}}
	if err := engine(t, store, s, d, options()).Once(context.Background()); err == nil {
		t.Fatal("expected rejected write")
	}
	store.Close()
	files, err := os.ReadDir(filepath.Join(dir, "staging"))
	if err != nil || len(files) != 1 {
		t.Fatalf("staged original lost: %v %v", files, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staging", files[0].Name()), []byte("wrong"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d.upload = nil
	e := engine(t, store, s, d, options())
	if err := e.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(context.Background()); err == nil || err.Error() != "staging_corrupt" {
		t.Fatalf("corrupt staged original reached upload: %v", err)
	}
	if len(d.bodies) != 0 {
		t.Fatal("corrupt content was uploaded")
	}
}

func TestKnownUnconfirmedNodeCanRecoverWithoutSearchIndexVisibility(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", get: func(context.Context, string) (bridge.Node, error) {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
	}}
	if err := engine(t, store, s, d, options()).Once(context.Background()); err == nil {
		t.Fatal("unverified write succeeded")
	}
	store.Close()
	d.get = nil
	d.find = func(context.Context, bridge.Content) ([]bridge.Node, error) { return nil, nil }
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, options())
	if err := e.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || len(d.bodies) != 1 {
		t.Fatalf("known committed node was not recovered: %+v", st)
	}
}

func TestConcurrentAssetsWithSameContentCreateOneRemoteNode(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assets := []bridge.Asset{photo("one"), photo("two"), photo("three"), photo("four")}
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: assets}},
		data: map[string]string{"one": "hello", "two": "hello", "three": "hello", "four": "hello"}}
	d := &targetFake{identity: "target"}
	d.upload = func(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
		time.Sleep(30 * time.Millisecond)
		b, err := io.ReadAll(r)
		if err != nil {
			return bridge.Node{}, err
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		n := bridge.Node{ID: fmt.Sprint(len(d.nodes) + 1), Name: c.Name, MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5}
		d.nodes = append(d.nodes, n)
		d.bodies = append(d.bodies, string(b))
		return n, nil
	}
	// Search visibility may lag a successful upload; durable confirmation is also evidence.
	d.find = func(context.Context, bridge.Content) ([]bridge.Node, error) { return nil, nil }
	opts := options()
	opts.Workers, opts.MaxStagingFiles = 4, 4
	e := engine(t, store, s, d, opts)
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 4 || len(d.nodes) != 1 || len(d.bodies) != 1 {
		t.Fatalf("concurrent identical originals were duplicated: nodes=%d status=%+v", len(d.nodes), st)
	}
}

func TestDownloadedChecksumProtectsConfirmedOriginalWhenSourceChecksumWasUnknown(t *testing.T) {
	dir := t.TempDir()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("one")}}},
		data: map[string]string{"one": "hello"}}
	a := s.pages[""].Assets[0]
	a.SHA1 = ""
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{a}}
	d := &targetFake{identity: "target"}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine(t, store, s, d, options()).Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.Close()
	a.SHA1 = "AAAAAAAAAAAAAAAAAAAAAAAAAAA="
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{a}}
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err == nil || err.Error() != "source_changed" {
		t.Fatalf("new checksum replaced the confirmed original identity: %v", err)
	}
	if st := status(t, e); st.Confirmed != 1 || st.Errors["source_changed"] != 1 || len(d.nodes) != 1 {
		t.Fatalf("confirmed original evidence was lost: %+v", st)
	}
}

func TestCleanupFailureDoesNotDowngradeConfirmation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission semantics")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := photo("one")
	s := &sourceFake{identity: "source", data: map[string]string{"one": "hello"},
		pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}}}
	uploads := 0
	d := &targetFake{identity: "target"}
	d.upload = func(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
		b, err := io.ReadAll(r)
		if err != nil {
			return bridge.Node{}, err
		}
		uploads++
		sum := md5.Sum(b)
		n := bridge.Node{ID: "one-node", Name: c.Name, MD5: hex.EncodeToString(sum[:]), Size: int64(len(b))}
		d.mu.Lock()
		d.nodes = append(d.nodes, n)
		d.mu.Unlock()
		if err := os.Chmod(filepath.Join(dir, "staging"), 0o500); err != nil {
			return bridge.Node{}, err
		}
		return n, nil
	}
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err != nil {
		t.Fatalf("cleanup failure failed the job: %v", err)
	}
	if st := status(t, e); st.Confirmed != 1 || st.Failed != 0 || uploads != 1 {
		t.Fatalf("confirmation was downgraded: %+v uploads=%d", st, uploads)
	}
	if err := os.Chmod(filepath.Join(dir, "staging"), 0o700); err != nil {
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
	files, err := os.ReadDir(filepath.Join(dir, "staging"))
	if err != nil || len(files) != 0 {
		t.Fatalf("orphan staging was not cleaned: %v %v", files, err)
	}
	e = engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 {
		t.Fatalf("confirmed asset uploaded again: %d", uploads)
	}
}

func TestMetadataEditDoesNotPoisonChecksumlessAsset(t *testing.T) {
	s := &sourceFake{identity: "source", data: map[string]string{"one": "hello"}}
	a := photo("one")
	a.SHA1 = ""
	a.FileModifiedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.pages = map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}}
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
	// Immich metadata edits move updatedAt but leave the file's modification time alone.
	edited := a
	edited.TakenAt = a.TakenAt.Add(time.Hour)
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{edited}}
	if err := e.Once(t.Context()); err != nil {
		t.Fatalf("metadata edit poisoned the asset: %v", err)
	}
	if st := status(t, e); st.Confirmed != 1 || st.Errors["source_changed"] != 0 || st.LastSuccess == nil {
		t.Fatalf("metadata edit did not settle: %+v", st)
	}
	changed := edited
	changed.FileModifiedAt = edited.FileModifiedAt.Add(time.Hour)
	s.pages[""] = bridge.Page{Assets: []bridge.Asset{changed}}
	if err := e.Once(t.Context()); err == nil || err.Error() != "source_changed" {
		t.Fatalf("file modification was ignored: %v", err)
	}
	if st := status(t, e); st.Confirmed != 1 || st.Errors["source_changed"] != 1 || len(d.nodes) != 1 {
		t.Fatalf("file modification lost the confirmed mapping: %+v", st)
	}
}
