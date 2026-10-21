package syncer_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func TestSkippedStagedVideoReleasesQuotaButUncertainUploadKeepsEvidence(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for _, excluded := range []bool{false, true} {
			t.Run(map[bool]string{false: "rejected", true: "uncertain"}[uncertain], func(t *testing.T) {
				dir := t.TempDir()
				store, err := syncer.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				a := photo("video")
				a.Kind, a.MIME = "VIDEO", "video/mp4"
				s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}}, data: map[string]string{"video": "hello"}}
				d := &targetFake{identity: "target"}
				calls := 0
				d.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
					calls++
					code := bridge.RateLimit
					if uncertain {
						code = bridge.Unconfirmed
					}
					return bridge.Node{}, &bridge.Error{Code: code}
				}
				opts := options()
				opts.IncludeVideos, opts.MaxStagingFiles = true, 1
				e := engine(t, store, s, d, opts)
				if err := e.Once(t.Context()); err == nil {
					t.Fatal("upload fault was hidden")
				}
				opts.IncludeVideos = false
				if excluded {
					a.ExcludedReason, a.SHA1, a.FileModifiedAt = "offline", "", time.Now()
					s.pages[""] = bridge.Page{Assets: []bridge.Asset{a}}
				}
				e = engine(t, store, s, d, opts)
				if err := e.Retry(t.Context()); err != nil {
					t.Fatal(err)
				}
				err = e.Once(t.Context())
				files, readErr := os.ReadDir(filepath.Join(dir, "staging"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				st := status(t, e)
				if uncertain {
					if len(files) != 1 || st.Unconfirmed != 1 || calls != 1 {
						t.Fatalf("lost uncertain evidence: %+v files=%d calls=%d", st, len(files), calls)
					}
				} else if err != nil || len(files) != 0 || st.Skipped != 1 {
					t.Fatalf("skip retained stage: %+v files=%d err=%v", st, len(files), err)
				}
			})
		}
	}
}
