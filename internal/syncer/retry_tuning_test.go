package syncer_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func tunedOptions() syncer.Options {
	opts := options()
	opts.MaxAttempts = 3
	opts.RetryMaxDelay = 2 * time.Second
	return opts
}

func TestRetryTuningIsBounded(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, d := &sourceFake{identity: "source"}, &targetFake{identity: "target"}
	for _, tt := range []struct {
		name string
		opts func(syncer.Options) syncer.Options
	}{
		{"attempts above the hard ceiling", func(o syncer.Options) syncer.Options { o.MaxAttempts = 11; return o }},
		{"negative attempts", func(o syncer.Options) syncer.Options { o.MaxAttempts = -1; return o }},
		{"delay below the floor", func(o syncer.Options) syncer.Options { o.RetryMaxDelay = 10 * time.Millisecond; return o }},
		{"delay above the ceiling", func(o syncer.Options) syncer.Options { o.RetryMaxDelay = time.Hour; return o }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := syncer.New(store, s, d, tt.opts(options())); err == nil || err.Error() != "invalid_options" {
				t.Fatalf("unbounded tuning accepted: %v", err)
			}
		})
	}
}

func TestStatusReportsEffectiveRetryTuning(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, &sourceFake{identity: "source"}, &targetFake{identity: "target"}, tunedOptions())
	st := status(t, e)
	if st.MaxAttempts != 3 || st.RetryMaxDelayMS != 2000 {
		t.Fatalf("status tuning = %d/%d", st.MaxAttempts, st.RetryMaxDelayMS)
	}
}

func TestRetryTuningCapsDefiniteFailureRetries(t *testing.T) {
	for _, deletions := range []bool{false, true} {
		t.Run(fmt.Sprintf("deletions=%v", deletions), func(t *testing.T) {
			clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			var uploads int
			s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
				"": {Assets: []bridge.Asset{photo("one")}},
			}, data: map[string]string{"one": "hello"}}
			d := &targetFake{identity: "target", upload: func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
				uploads++
				return bridge.Node{}, &bridge.Error{Code: bridge.RateLimit}
			}}
			opts := options()
			opts.MaxAttempts = 2
			opts.Deletions = deletions
			opts.Now = func() time.Time { return clock }
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			e := engine(t, store, s, d, opts)
			ctx := context.Background()
			for range 4 {
				_ = e.Once(ctx)
				clock = clock.Add(2 * time.Second)
			}
			if uploads != 2 {
				t.Fatalf("upload attempts = %d, want the configured cap 2", uploads)
			}
			if st := status(t, e); st.Failed+st.Blocked != 1 || st.Unconfirmed != 0 {
				t.Fatalf("definite failures were misreported: %+v", st)
			}
			if len(d.trashed) != 0 {
				t.Fatalf("retry tuning authorized a trash: %v", d.trashed)
			}
		})
	}
}

func TestRetryTuningNeverAuthorizesAnUnconfirmedWrite(t *testing.T) {
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var uploads int
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{photo("one")}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	d.upload = func(ctx context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
		uploads++
		b, err := io.ReadAll(r)
		if err != nil {
			return bridge.Node{}, err
		}
		sum := md5.Sum(b)
		d.mu.Lock()
		d.nodes = append(d.nodes, bridge.Node{ID: "committed", Name: c.Name,
			MD5: hex.EncodeToString(sum[:]), Size: int64(len(b)), ParentID: c.ParentID})
		d.mu.Unlock()
		// The write may have committed, but the response was lost.
		return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
	}
	d.find = func(context.Context, bridge.Content) ([]bridge.Node, error) {
		return nil, nil
	}
	d.findAll = func(context.Context, bridge.Content) (bridge.Node, error) {
		if uploads > 0 {
			// The committed write is not yet provable, so reconciliation keeps
			// the item uncertain instead of clearing it or writing again.
			return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
		}
		return bridge.Node{}, nil
	}
	opts := options()
	opts.MaxAttempts = 10
	opts.Now = func() time.Time { return clock }
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, s, d, opts)
	for range 3 {
		_ = e.Once(context.Background())
		clock = clock.Add(time.Minute)
	}
	if uploads != 1 {
		t.Fatalf("an unconfirmed write was POSTed %d times", uploads)
	}
	if st := status(t, e); st.Unconfirmed != 1 {
		t.Fatalf("uncertain write lost its state: %+v", st)
	}
}

func TestStatusReportsOriginalBytesBySourcePath(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{photo("one")}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target"}
	e := engine(t, store, s, d, options())
	if err := e.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Direct.APIBytes != int64(len("hello")) || st.Direct.BytesAvoided != 0 || st.Direct.Reads != 0 {
		t.Fatalf("API path bytes = %+v", st.Direct)
	}
}
