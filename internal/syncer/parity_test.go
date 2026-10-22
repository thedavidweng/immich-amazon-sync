package syncer_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// The baseline mapping contract must never regress: a newly uploaded asset is
// Confirmed and a verified copy elsewhere in the same account is Existing, and
// both survive restart without another upload. The deletion switch is orthogonal
// to content state.
func TestBaselineConfirmedAndExistingAreDistinct(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			one, two := photo("one"), photo("two")
			two.SHA1 = ""
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{one, two}},
			}, data: map[string]string{"one": "hello", "two": "world"}}
			d := &targetFake{identity: "target", nodes: []bridge.Node{{
				ID: "remote-node", Name: "two.jpg", MD5: contentMD5("world"), Size: 5, ParentID: "other-folder",
			}}}
			opts := options()
			opts.Deletions = deletions
			run := func() syncer.Status {
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
				if err := e.Once(context.Background()); err != nil {
					t.Fatal(err)
				}
				return status(t, e)
			}

			st := run()
			if st.Confirmed != 1 || st.Existing != 1 || st.Unconfirmed != 0 || st.Deleted != 0 || st.DeletePending != 0 {
				t.Fatalf("baseline content states changed: %+v", st)
			}
			if st.LastSuccess == nil || len(d.bodies) != 1 || len(d.nodes) != 2 {
				t.Fatalf("baseline write/scan changed: success=%v uploads=%d nodes=%d", st.LastSuccess, len(d.bodies), len(d.nodes))
			}

			st = run()
			if st.Confirmed != 1 || st.Existing != 1 || len(d.bodies) != 1 || len(d.nodes) != 2 {
				t.Fatalf("restart re-uploaded or lost a mapping: %+v uploads=%d nodes=%d", st, len(d.bodies), len(d.nodes))
			}
		})
	}
}

// An Unconfirmed write may have committed. Recovery must re-read the target and
// adopt the node that appears, with exactly one media POST across the restart.
func TestBaselineUnconfirmedRecoveryDoesNotRepost(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{photo("one")}},
			}, data: map[string]string{"one": "hello"}}
			posts := 0
			d := &targetFake{identity: "target", upload: func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
				posts++
				return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
			}}
			opts := options()
			opts.Deletions = deletions
			store, err := syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			e := engine(t, store, s, d, opts)
			if err := e.Once(context.Background()); err == nil {
				t.Fatal("ambiguous write was reported as success")
			}
			if st := status(t, e); st.Unconfirmed != 1 || st.Confirmed != 0 || st.Existing != 0 {
				t.Fatalf("uncertainty was lost: %+v", st)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			// The remote write becomes observable only after a restart.
			d.nodes = []bridge.Node{{ID: "remote-node", Name: "one.jpg",
				MD5: contentMD5("hello"), Size: 5, ParentID: "other-folder"}}
			store, err = syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			e = engine(t, store, s, d, opts)
			if err := e.Retry(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := e.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.Unconfirmed != 0 || st.Existing != 1 || st.Confirmed != 0 {
				t.Fatalf("uncertain write did not reconcile to the remote node: %+v", st)
			}
			if posts != 1 {
				t.Fatalf("uncertain write was posted %d times", posts)
			}
		})
	}
}

func contentMD5(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
