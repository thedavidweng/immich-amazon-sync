package syncer_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func usageObservation(videoBytes int64, calculated time.Time) bridge.Usage {
	return bridge.Usage{
		LastCalculated: calculated,
		Categories: map[string]bridge.UsageCategory{
			"photo": {
				Billable: bridge.UsageBucket{Bytes: 10, Count: 1},
				Total:    bridge.UsageBucket{Bytes: 20, Count: 2},
			},
			"video": {
				Billable: bridge.UsageBucket{Bytes: videoBytes, Count: 1},
				Total:    bridge.UsageBucket{Bytes: videoBytes, Count: 1},
			},
			"other": {
				Billable: bridge.UsageBucket{Bytes: 3, Count: 1},
				Total:    bridge.UsageBucket{Bytes: 3, Count: 1},
			},
		},
	}
}

func TestUsageStatusLabelsObservedStaleAndUnavailable(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	calculated := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
	s := &sourceFake{identity: "source"}
	d := &targetFake{identity: "target", usage: usageObservation(123, calculated)}
	e := engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Usage.State != "observed" || st.Usage.Capacity != "unknown" || st.Usage.Entitlement != "unknown" ||
		st.Usage.LastCalculated == nil || !st.Usage.LastCalculated.Equal(calculated) ||
		st.Usage.Photo().Billable.Bytes != 10 || st.Usage.Video().Billable.Bytes != 123 || st.Usage.Other().Total.Count != 1 {
		t.Fatalf("observed usage lost: %+v", st.Usage)
	}

	// A failed refresh preserves the observed values but must label them stale.
	d.usageErr = &bridge.Error{Code: bridge.Permission}
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = status(t, e)
	if st.Usage.State != "stale" || st.Usage.ErrorCode != bridge.Permission ||
		st.Usage.Photo().Billable.Bytes != 10 || st.Usage.Capacity != "unknown" {
		t.Fatalf("stale usage was zeroed or mislabelled: %+v", st.Usage)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d = &targetFake{identity: "target", usageErr: &bridge.Error{Code: bridge.Permission}}
	e = engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = status(t, e)
	if st.Usage.State != "unavailable" || len(st.Usage.Categories) != 0 || st.Usage.Capacity != "unknown" ||
		st.Usage.Entitlement != "unknown" || st.Usage.ErrorCode != bridge.Permission {
		t.Fatalf("unavailable usage invented values: %+v", st.Usage)
	}
}

func TestQuotaRejectionKeepsStagedOriginalAndResumesWithoutSecondUpload(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			dir := t.TempDir()
			one, two := photo("one"), photo("two")
			two.SHA1 = ""
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{one, two}},
			}, data: map[string]string{"one": "hello", "two": "world"}}
			d := &targetFake{identity: "target"}
			var mu sync.Mutex
			attempts := 0
			d.upload = func(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
				mu.Lock()
				attempts++
				first := attempts == 1
				mu.Unlock()
				if first {
					_, _ = io.Copy(io.Discard, r)
					return bridge.Node{}, &bridge.Error{Code: bridge.Quota}
				}
				body, err := io.ReadAll(r)
				if err != nil {
					return bridge.Node{}, err
				}
				sum := md5.Sum(body)
				d.mu.Lock()
				node := bridge.Node{ID: fmt.Sprint(len(d.nodes) + 1), Name: c.Name,
					MD5: hex.EncodeToString(sum[:]), Size: int64(len(body)), ParentID: c.ParentID}
				d.nodes = append(d.nodes, node)
				d.bodies = append(d.bodies, string(body))
				d.mu.Unlock()
				return node, nil
			}
			opts := options()
			opts.Workers = 1
			opts.Deletions = deletions
			store, err := syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			e := engine(t, store, s, d, opts)
			if err := e.Once(context.Background()); err == nil || err.Error() != bridge.Quota {
				t.Fatalf("expected a distinct quota failure: %v", err)
			}
			st := status(t, e)
			if st.Staged != 1 || st.Pending != 1 || st.Confirmed != 0 || st.Failed != 0 ||
				st.Errors[bridge.Quota] != 1 || !st.Usage.QuotaHold || st.WorkRemaining || st.NextAttempt != nil {
				t.Fatalf("quota rejection was not held safely: %+v", st)
			}
			if attempts != 1 {
				t.Fatalf("held uploads continued: attempts=%d", attempts)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			// Capacity restored: the next run re-reads the target first, then
			// uploads each original exactly once.
			store, err = syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			e = engine(t, store, s, d, opts)
			if err := e.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
			st = status(t, e)
			if st.Confirmed != 2 || st.Staged != 0 || st.Pending != 0 || st.Unconfirmed != 0 || st.Usage.QuotaHold {
				t.Fatalf("quota hold did not resume cleanly: %+v", st)
			}
			if attempts != 3 || len(d.bodies) != 2 || len(d.nodes) != 2 {
				t.Fatalf("resume replayed a write: attempts=%d uploads=%d nodes=%d", attempts, len(d.bodies), len(d.nodes))
			}
		})
	}
}

func video(id string) bridge.Asset {
	asset := photo(id)
	asset.Name = id + ".mp4"
	asset.MIME = "video/mp4"
	asset.Kind = "VIDEO"
	asset.SHA1 = ""
	return asset
}

func TestVideoThresholdHoldIsOptInAndResumesWhenUsageFalls(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			calculated := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{photo("photo"), video("clip")}},
			}, data: map[string]string{"photo": "hello", "clip": "world"}}
			d := &targetFake{identity: "target", usage: usageObservation(1000, calculated)}
			opts := options()
			opts.Workers = 1
			opts.Filters.MediaTypes = []string{"image", "video"}
			opts.Deletions = deletions
			opts.VideoUsageThresholdBytes = 1000
			opts.HoldVideosAtThreshold = true
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			e := engine(t, store, s, d, opts)
			if err := e.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.Confirmed != 1 || st.Pending != 1 || !st.Usage.VideoThresholdExceeded || !st.Usage.VideoHold || st.WorkRemaining {
				t.Fatalf("video hold did not isolate videos: %+v", st)
			}
			if len(d.bodies) != 1 {
				t.Fatalf("held video was uploaded: %v", d.bodies)
			}

			d.usage = usageObservation(999, calculated)
			if err := e.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
			st = status(t, e)
			if st.Confirmed != 2 || st.Pending != 0 || st.Usage.VideoThresholdExceeded || st.Usage.VideoHold {
				t.Fatalf("video hold did not resume below the threshold: %+v", st)
			}
			if len(d.bodies) != 2 {
				t.Fatalf("video did not resume exactly once: %v", d.bodies)
			}
		})
	}
}

func TestVideoThresholdWithoutHoldOrFreshUsageCannotPauseUploads(t *testing.T) {
	calculated := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		usage  bridge.Usage
		hold   bool
		expect bool
	}{
		{"advisory only", usageObservation(2000, calculated), false, false},
		{"stale usage", usageObservation(2000, time.Time{}), true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{video("clip")}},
			}, data: map[string]string{"clip": "world"}}
			d := &targetFake{identity: "target", usage: test.usage}
			opts := options()
			opts.Workers = 1
			opts.Filters.MediaTypes = []string{"image", "video"}
			opts.VideoUsageThresholdBytes = 1000
			opts.HoldVideosAtThreshold = test.hold
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			e := engine(t, store, s, d, opts)
			if err := e.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
			st := status(t, e)
			if st.Confirmed != 1 || st.Usage.VideoHold != test.expect || len(d.bodies) != 1 {
				t.Fatalf("video was held without fresh opt-in evidence: %+v", st)
			}
		})
	}
}
