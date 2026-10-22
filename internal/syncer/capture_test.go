package syncer_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func originalAsset(id string, data []byte) bridge.Asset {
	sum := sha1.Sum(data)
	return bridge.Asset{ID: id, Name: id + ".jpg", MIME: "image/jpeg", Kind: "IMAGE",
		Size: int64(len(data)), SHA1: base64.StdEncoding.EncodeToString(sum[:])}
}

type originalSnapshot struct {
	data  []byte
	mtime time.Time
	mode  fs.FileMode
	size  int64
}

func snapshotOriginal(t *testing.T, path string) originalSnapshot {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return originalSnapshot{data: data, mtime: info.ModTime(), mode: info.Mode(), size: info.Size()}
}

func writeOriginal(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original.jpg")
	data := []byte("original photo bytes that must survive every upload path unchanged")
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	// Establish the baseline timestamp the read paths must leave alone.
	stamp := time.Date(2021, 3, 4, 5, 6, 7, 123456789, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path, data
}

// Both the staged upload and the reuse/reconcile decisions read the source
// original. No path may rewrite its bytes, change its mode or touch its
// filesystem mtime, and the uploaded body must be byte-identical to the
// original (so embedded EXIF cannot change).
func TestUploadPathsNeverRewriteOriginalBytesOrTimestamps(t *testing.T) {
	for _, tc := range []struct {
		name        string
		prepare     func(*targetFake, []byte)
		wantUploads int
		wantErr     bool
	}{
		{
			name:        "staged-upload",
			prepare:     func(*targetFake, []byte) {},
			wantUploads: 1,
		},
		{
			name: "verified-existing-reuse",
			prepare: func(d *targetFake, data []byte) {
				d.nodes = []bridge.Node{{ID: "remote-node", Name: "one.jpg",
					MD5: contentMD5(string(data)), Size: int64(len(data)), ParentID: "other-folder"}}
			},
			wantUploads: 0,
		},
		{
			name: "uncertain-write",
			prepare: func(d *targetFake, _ []byte) {
				d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
					return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
				}
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, data := writeOriginal(t)
			before := snapshotOriginal(t, path)
			asset := originalAsset("one", data)
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{asset}},
			}}
			s.original = func(context.Context, string) (io.ReadCloser, error) { return os.Open(path) }
			d := &targetFake{identity: "target"}
			tc.prepare(d, data)

			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			err = engine(t, store, s, d, options()).Once(t.Context())
			if (err != nil) != tc.wantErr {
				t.Fatalf("sync error = %v, wantErr %v", err, tc.wantErr)
			}
			if len(d.bodies) != tc.wantUploads {
				t.Fatalf("uploads = %d, want %d", len(d.bodies), tc.wantUploads)
			}
			if tc.wantUploads == 1 && d.bodies[0] != string(data) {
				t.Fatal("uploaded bytes differ from the original")
			}

			after := snapshotOriginal(t, path)
			if string(after.data) != string(before.data) {
				t.Fatal("source original bytes were rewritten")
			}
			if !after.mtime.Equal(before.mtime) {
				t.Fatalf("source original mtime changed: %v -> %v", before.mtime, after.mtime)
			}
			if after.mode != before.mode || after.size != before.size {
				t.Fatalf("source original metadata changed: %v/%d -> %v/%d", before.mode, before.size, after.mode, after.size)
			}
		})
	}
}

// The service must never influence Amazon metadata by rewriting original bytes
// or filesystem timestamps. This guards every production file, including read
// paths added later such as a direct mounted-original reader.
func TestProductionCodeContainsNoTimestampOrExifWrites(t *testing.T) {
	forbidden := []string{"Chtimes", "Lutimes", "Utimes", "utimensat", "futimes", "SetFileTime", "goexif", "exiftool"}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "docs", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, token := range forbidden {
			if strings.Contains(string(source), token) {
				t.Errorf("%s contains %q: originals and their timestamps must never be written", path, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Status exposes the finite capture-date capability declared by the target.
// An absent declaration is metadata_not_supported, never an implied success,
// and values outside the vocabulary are refused.
func TestStatusNeverImpliesCaptureDateSupport(t *testing.T) {
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{}}
	d := &targetFake{identity: "target"}
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()

	e := engine(t, store, s, d, options())
	if got := status(t, e).CaptureDate; got != bridge.MetadataNotSupported {
		t.Fatalf("default capture_date = %q, want %q", got, bridge.MetadataNotSupported)
	}

	opts := options()
	opts.CaptureDate = bridge.MetadataSupported
	e = engine(t, store, s, d, opts)
	if got := status(t, e).CaptureDate; got != bridge.MetadataSupported {
		t.Fatalf("declared capture_date = %q, want %q", got, bridge.MetadataSupported)
	}

	opts.CaptureDate = "best_effort"
	if _, err := syncer.New(store, s, d, opts); err == nil {
		t.Fatal("unknown capture-date capability accepted")
	}
}
