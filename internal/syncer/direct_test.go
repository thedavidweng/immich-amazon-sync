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
	"syscall"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/direct"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

const mountSource = "/immich/upload"

// mounted writes one asset under an approved host root and returns the root.
func mounted(t *testing.T, rel, content string) string {
	t.Helper()
	host := t.TempDir()
	path := filepath.Join(host, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return host
}

func mountedAsset(id string) bridge.Asset {
	asset := photo(id)
	asset.OriginalPath = mountSource + "/library/" + id + ".jpg"
	return asset
}

func directOptions(host string) syncer.Options {
	opts := options()
	opts.DirectRoots = []direct.Root{{ImmichPath: mountSource, HostPath: host}}
	return opts
}

type readResult struct {
	node     bridge.Node
	original int
	status   syncer.Status
}

func transfer(t *testing.T, opts syncer.Options, asset bridge.Asset, dataDir string) (readResult, error) {
	t.Helper()
	store, err := syncer.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{asset}}},
		assets: map[string]bridge.Asset{asset.ID: asset}, data: map[string]string{asset.ID: "hello"}}
	result := readResult{}
	if opts.DirectRoots != nil {
		source.original = func(context.Context, string) (io.ReadCloser, error) {
			result.original++
			return io.NopCloser(strings.NewReader("hello")), nil
		}
	}
	target := &targetFake{identity: "target"}
	e := engine(t, store, source, target, opts)
	err = e.Once(context.Background())
	result.status = status(t, e)
	if len(target.nodes) > 0 {
		result.node = target.nodes[0]
	}
	return result, err
}

func TestMountedOriginalTransfersWithoutAnAPIDownload(t *testing.T) {
	host := mounted(t, "library/one.jpg", "hello")
	info, err := os.Stat(filepath.Join(host, "library", "one.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	api, err := transfer(t, options(), mountedAsset("one"), t.TempDir())
	if err != nil || api.original != 0 {
		t.Fatalf("api baseline: %v (original reads: %d)", err, api.original)
	}
	directResult, err := transfer(t, directOptions(host), mountedAsset("one"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if directResult.original != 0 {
		t.Fatalf("mounted original still downloaded from the API %d times", directResult.original)
	}
	if directResult.status.Confirmed != 1 || directResult.status.Direct.Reads != 1 ||
		directResult.status.Direct.BytesAvoided != 5 || len(directResult.status.Direct.Fallbacks) != 0 {
		t.Fatalf("direct read was not accounted: %+v", directResult.status)
	}
	if directResult.node.MD5 != api.node.MD5 || directResult.node.Size != api.node.Size ||
		directResult.node.MD5 != "5d41402abc4b2a76b9719d911017c592" {
		t.Fatalf("direct bytes differ from the API path: %+v vs %+v", directResult.node, api.node)
	}
	after, err := os.Stat(filepath.Join(host, "library", "one.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if after.ModTime() != info.ModTime() || after.Size() != info.Size() || after.Mode() != info.Mode() {
		t.Fatalf("the source mount changed: before=%v after=%v", info, after)
	}
}

func TestIneligibleMountFallsBackToTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, host string)
		path    string
		outcome string
	}{
		{"no reported path", func(*testing.T, string) {}, "", "outside_root"},
		{"outside every root", func(*testing.T, string) {}, "/immich/elsewhere/library/one.jpg", "outside_root"},
		{"missing under an approved root", func(*testing.T, string) {}, mountSource + "/library/gone.jpg", "missing"},
		{"absent mount", func(t *testing.T, host string) {
			if err := os.RemoveAll(host); err != nil {
				t.Fatal(err)
			}
		}, mountSource + "/library/one.jpg", "root_unavailable"},
		{"unreadable file", func(t *testing.T, host string) {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("permission checks do not apply")
			}
			if err := os.Chmod(filepath.Join(host, "library", "one.jpg"), 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(host, "library", "one.jpg"), 0644) })
		}, mountSource + "/library/one.jpg", "unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := mounted(t, "library/one.jpg", "hello")
			tc.prepare(t, host)
			asset := mountedAsset("one")
			asset.OriginalPath = tc.path
			result, err := transfer(t, directOptions(host), asset, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if result.original != 1 || result.status.Confirmed != 1 || result.status.Direct.Reads != 0 {
				t.Fatalf("mount problem did not fall back: %+v (original reads: %d)", result.status, result.original)
			}
			if result.status.Direct.Fallbacks[tc.outcome] != 1 {
				t.Fatalf("fallback reason was not recorded: %+v", result.status.Direct)
			}
		})
	}
}

func TestSuspiciousMountedPathsFailClosedWithoutAnyDownload(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "root")
	if err := os.MkdirAll(host, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "outside.jpg"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(host, "one.jpg"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside.jpg"), filepath.Join(host, "link.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(host, "folder.jpg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(host, "pipe.jpg"), 0644); err != nil {
		t.Skipf("fifo unavailable: %v", err)
	}
	for _, tc := range []struct {
		name    string
		path    string
		code    string
		outcome string
	}{
		{"traversal", mountSource + "/../outside.jpg", "direct_path_invalid", "path_invalid"},
		{"symlink escape", mountSource + "/link.jpg", "direct_path_escape", "path_escape"},
		{"directory", mountSource + "/folder.jpg", "direct_path_not_regular", "path_not_regular"},
		{"fifo", mountSource + "/pipe.jpg", "direct_path_not_regular", "path_not_regular"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asset := mountedAsset("one")
			asset.OriginalPath = tc.path
			result, err := transfer(t, directOptions(host), asset, t.TempDir())
			if err == nil || err.Error() != tc.code {
				t.Fatalf("suspicious path did not fail closed: %v", err)
			}
			if result.original != 0 || result.status.Confirmed != 0 || len(result.status.Errors) == 0 {
				t.Fatalf("suspicious path downloaded or confirmed content: %+v (original reads: %d)", result.status, result.original)
			}
			if result.status.Direct.Failures[tc.outcome] != 1 || result.status.Blocked != 1 {
				t.Fatalf("fail-closed outcome was not recorded: %+v", result.status)
			}
		})
	}
}

func TestMountedIdentityMismatchFailsClosed(t *testing.T) {
	host := mounted(t, "library/one.jpg", "other")
	result, err := transfer(t, directOptions(host), mountedAsset("one"), t.TempDir())
	if err == nil || err.Error() != "source_changed" {
		t.Fatalf("mismatched bytes were accepted: %v", err)
	}
	if result.original != 0 || result.status.Confirmed != 0 || result.status.Blocked != 1 {
		t.Fatalf("mismatched bytes were used: %+v (original reads: %d)", result.status, result.original)
	}
}

func TestMutationDuringAMountedReadNeverRecordsADigest(t *testing.T) {
	host := mounted(t, "library/one.jpg", "")
	path := filepath.Join(host, "library", "one.jpg")
	const size = 256 << 10
	if err := os.WriteFile(path, make([]byte, size), 0644); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	store, err := syncer.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	asset := mountedAsset("one")
	asset.Size, asset.SHA1 = size, ""
	source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{asset}}},
		assets: map[string]bridge.Asset{asset.ID: asset}}
	var original int
	source.original = func(context.Context, string) (io.ReadCloser, error) {
		original++
		return io.NopCloser(strings.NewReader("hello")), nil
	}
	target := &targetFake{identity: "target"}
	opts := directOptions(host)
	opts.BytesPerSecond = size / 4 // four seconds of throttled reading
	e := engine(t, store, source, target, opts)
	// Mutate only once the transfer has actually opened the original and begun
	// staging it. A wall-clock sleep raced engine startup on a loaded CI
	// machine: the rewrite could land before the open, leaving a file that was
	// stable for the whole read.
	started := make(chan struct{})
	go func() {
		defer close(started)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			entries, readErr := os.ReadDir(filepath.Join(dataDir, "staging"))
			if readErr == nil {
				for _, entry := range entries {
					if info, statErr := entry.Info(); statErr == nil && info.Size() > 0 {
						_ = os.WriteFile(path, bytesOf(size, 0xff), 0644)
						return
					}
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
	err = e.Once(context.Background())
	<-started
	if err == nil || err.Error() != "source_changed" {
		t.Fatalf("mutating original was accepted: %v", err)
	}
	st := status(t, e)
	if original != 0 || len(target.nodes) != 0 || st.Confirmed != 0 || st.Direct.Reads != 0 ||
		st.Errors["source_changed"] == 0 || st.Direct.BytesAvoided != 0 {
		t.Fatalf("mutation was recorded as evidence: %+v (original reads: %d, uploads: %d)", st, original, len(target.nodes))
	}
	files, err := os.ReadDir(filepath.Join(dataDir, "staging"))
	if err != nil || len(files) != 0 {
		t.Fatalf("partial staging remains after a mutation: %v %v", files, err)
	}
}

func bytesOf(n int, value byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = value
	}
	return b
}

func TestMountedMultiMegabyteTransferStaysBounded(t *testing.T) {
	const size = 32 << 20
	// Independently precomputed MD5 of 32 MiB of zero bytes.
	const digest = "58f06dd588d8ffb3beb46ada6309436b"
	host := t.TempDir()
	path := filepath.Join(host, "library", "one.jpg")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	asset := mountedAsset("one")
	asset.Size, asset.SHA1 = size, ""
	source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{asset}}},
		assets: map[string]bridge.Asset{asset.ID: asset}}
	var original int
	source.original = func(context.Context, string) (io.ReadCloser, error) {
		original++
		return nil, errors.New("a mounted original must never be downloaded")
	}
	var uploaded int64
	target := &targetFake{identity: "target", upload: func(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
		n, _ := io.Copy(io.Discard, r)
		uploaded += n
		return bridge.Node{ID: "mounted", Name: c.Name, MD5: digest, Size: n, ParentID: c.ParentID}, nil
	}}
	target.get = func(context.Context, string) (bridge.Node, error) {
		return bridge.Node{ID: "mounted", Name: asset.Name, MD5: digest, Size: size}, nil
	}
	opts := directOptions(host)
	opts.MaxStagingBytes = size + (1 << 20)
	e := engine(t, store, source, target, opts)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("mounted 32 MiB transfer allocated %d bytes", allocated)
	if allocated > 16<<20 {
		t.Errorf("transfer allocated %d bytes for a streamed original", allocated)
	}
	if original != 0 || uploaded != size || status(t, e).Confirmed != 1 {
		t.Fatalf("mounted transfer incomplete: reads=%d uploaded=%d %+v", original, uploaded, status(t, e))
	}
	if st := status(t, e); st.Direct.Reads != 1 || st.Direct.BytesAvoided != size {
		t.Fatalf("bytes avoided were not recorded: %+v", st.Direct)
	}
}

func TestMountedCrashAfterDispatchReconcilesWithoutAnotherUpload(t *testing.T) {
	if dir := os.Getenv("SYNCER_MOUNTED_CRASH_DIR"); dir != "" {
		host := os.Getenv("SYNCER_MOUNTED_CRASH_HOST")
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		asset := mountedAsset("one")
		source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{asset}}},
			assets: map[string]bridge.Asset{asset.ID: asset}}
		source.original = func(context.Context, string) (io.ReadCloser, error) {
			return nil, errors.New("a mounted original must never be downloaded")
		}
		target := &targetFake{identity: "target", upload: func(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
			b, err := io.ReadAll(r)
			if err != nil || string(b) != "hello" || c.MD5 != "5d41402abc4b2a76b9719d911017c592" {
				t.Fatal("upload received an incomplete mounted original")
			}
			if err := os.WriteFile(filepath.Join(dir, "remote-commit"), b, 0600); err != nil {
				t.Fatal(err)
			}
			os.Exit(0)
			return bridge.Node{}, nil
		}}
		if err := engine(t, store, source, target, directOptions(host)).Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Fatal("helper did not crash")
	}
	dir, host := t.TempDir(), mounted(t, "library/one.jpg", "hello")
	cmd := exec.Command(os.Args[0], "-test.run=^TestMountedCrashAfterDispatchReconcilesWithoutAnotherUpload$")
	cmd.Env = append(os.Environ(), "SYNCER_MOUNTED_CRASH_DIR="+dir, "SYNCER_MOUNTED_CRASH_HOST="+host)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v: %s", err, out)
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	asset := mountedAsset("one")
	source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{asset}}},
		assets: map[string]bridge.Asset{asset.ID: asset}}
	var original int
	source.original = func(context.Context, string) (io.ReadCloser, error) {
		original++
		return nil, errors.New("reconciliation must not download again")
	}
	target := &targetFake{identity: "target", nodes: []bridge.Node{{ID: "remote", Name: "one.jpg",
		MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5}}}
	e := engine(t, store, source, target, directOptions(host))
	if st := status(t, e); st.Unconfirmed != 1 {
		t.Fatalf("interrupted mounted upload was not recovered as uncertain: %+v", st)
	}
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || len(target.bodies) != 0 || original != 0 {
		t.Fatalf("committed content was reuploaded or downloaded: %+v", st)
	}
	files, _ := os.ReadDir(filepath.Join(dir, "staging"))
	if len(files) != 0 {
		t.Fatalf("confirmed staging was not cleaned: %v", files)
	}
}

func TestDigestOfMountedBytesMatchesTheAPIStream(t *testing.T) {
	host := mounted(t, "library/one.jpg", "hello")
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	asset := mountedAsset("one")
	source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{asset}}},
		assets: map[string]bridge.Asset{asset.ID: asset}}
	target := &targetFake{identity: "target"}
	if err := engine(t, store, source, target, directOptions(host)).Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("hello"))
	if len(target.bodies) != 1 || target.nodes[0].MD5 != hex.EncodeToString(sum[:]) {
		t.Fatalf("staged digest: %+v", target.nodes)
	}
}

func TestMountedOriginalRespectsTheStagingQuota(t *testing.T) {
	host := mounted(t, "library/one.jpg", strings.Repeat("x", 64<<10))
	dataDir := t.TempDir()
	store, err := syncer.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	asset := mountedAsset("one")
	asset.Size, asset.SHA1 = -1, ""
	source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{asset}}},
		assets: map[string]bridge.Asset{asset.ID: asset}}
	var original, uploads int
	source.original = func(context.Context, string) (io.ReadCloser, error) {
		original++
		return nil, errors.New("quota failure must not download")
	}
	target := &targetFake{identity: "target", upload: func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		uploads++
		return bridge.Node{}, errors.New("quota failure must not upload")
	}}
	opts := directOptions(host)
	opts.MaxStagingBytes = 8 << 10
	e := engine(t, store, source, target, opts)
	if err := e.Once(context.Background()); err == nil || err.Error() != "staging_quota" {
		t.Fatalf("expected an explicit quota failure, got %v", err)
	}
	if original != 0 || uploads != 0 || status(t, e).Direct.Reads != 0 {
		t.Fatalf("quota path downloaded or uploaded: reads=%d uploads=%d %+v", original, uploads, status(t, e))
	}
	files, err := os.ReadDir(filepath.Join(dataDir, "staging"))
	if err != nil || len(files) != 0 {
		t.Fatalf("partial staging remains: %v %v", files, err)
	}
}

// The fast path changes only where original bytes come from. It must not change
// deletion behaviour: with propagation off a source trash is inert, and with it
// on the guarded pass trashes the mapped node without re-reading any mount.
func TestMountedOriginalFollowsOptInDeletion(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			host := t.TempDir()
			assets := make([]bridge.Asset, 0, 11)
			data := make(map[string]string, 11)
			for i := range 11 {
				id := fmt.Sprintf("mounted-%d", i)
				content := fmt.Sprintf("content-%02d", i)
				path := filepath.Join(host, "library", id+".jpg")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				asset := photo(id)
				asset.Size, asset.SHA1 = int64(len(content)), ""
				asset.OriginalPath = mountSource + "/library/" + id + ".jpg"
				assets = append(assets, asset)
				data[id] = content
			}
			source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: assets}}, data: data,
				assets: make(map[string]bridge.Asset, len(assets))}
			for _, asset := range assets {
				source.assets[asset.ID] = asset
			}
			apiReads := 0
			source.original = func(context.Context, string) (io.ReadCloser, error) {
				apiReads++
				return io.NopCloser(strings.NewReader("")), nil
			}
			target := &targetFake{identity: "target"}
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			opts := directOptions(host)
			opts.Deletions = deletions
			e := engine(t, store, source, target, opts)
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.Confirmed != 11 || st.Direct.Reads != 11 || apiReads != 0 {
				t.Fatalf("setup did not use the mount: %+v api=%d", st, apiReads)
			}
			trashed := assets[0]
			trashed.ExcludedReason = "trashed"
			source.assets[trashed.ID] = trashed
			source.pages[""] = bridge.Page{Assets: append([]bridge.Asset{trashed}, assets[1:]...)}
			if err := e.Once(t.Context()); err != nil {
				t.Fatal(err)
			}
			st = status(t, e)
			if deletions {
				if st.Deleted != 1 || st.Confirmed != 10 || st.DeletePending != 0 || len(target.trashed) != 1 {
					t.Fatalf("deletion-enabled pass did not trash exactly one mapped node: %+v trash=%v", st, target.trashed)
				}
			} else if st.Deleted != 0 || st.Confirmed != 11 || st.DeletePending != 0 || len(target.trashed) != 0 {
				t.Fatalf("deletion-disabled pass changed media: %+v trash=%v", st, target.trashed)
			}
			if st.Direct.Reads != 11 || apiReads != 0 {
				t.Fatalf("deletion changed original reads: %+v api=%d", st.Direct, apiReads)
			}
		})
	}
}
