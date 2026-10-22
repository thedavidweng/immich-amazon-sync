package syncer_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// testClock keeps transfer-window decisions deterministic while leaving retry
// scheduling on real time.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func windowedOptions(t *testing.T, clock *testClock, zone, start, end string) (syncer.Options, *syncer.WindowState) {
	t.Helper()
	window := mustWindow(t, zone, start, end)
	state := syncer.NewWindowState(window)
	opts := options()
	opts.Window = state
	opts.Now = clock.Now
	return opts, state
}

func singlePhoto() (*sourceFake, *targetFake) {
	return &sourceFake{identity: "source", pages: map[string]bridge.Page{
			"": {Assets: []bridge.Asset{photo("one")}},
		}, data: map[string]string{"one": "hello"}},
		&targetFake{identity: "target"}
}

func TestClosedTransferWindowDefersNewUploadWithoutFailureOrRetry(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	opts, _ := windowedOptions(t, clock, "UTC", "23:00", "06:00")
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, target := singlePhoto()
	e := engine(t, store, source, target, opts)

	if err := e.Once(t.Context()); err != nil {
		t.Fatalf("deferral surfaced as a cycle failure: %v", err)
	}
	st := status(t, e)
	if st.Deferred != 1 || st.Pending != 0 || st.Staged != 0 || st.Failed != 0 ||
		st.Blocked != 0 || st.Unconfirmed != 0 || len(st.Errors) != 0 {
		t.Fatalf("deferral was persisted as failure: %+v", st)
	}
	opening := time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)
	if st.TransferWindow == nil || st.TransferWindow.Open || st.TransferWindow.Timezone != "UTC" ||
		st.TransferWindow.Start != "23:00" || st.TransferWindow.End != "06:00" ||
		st.TransferWindow.NextOpening == nil || !st.TransferWindow.NextOpening.Equal(opening) {
		t.Fatalf("window status: %+v", st.TransferWindow)
	}
	if st.DeferralReason != "transfer_window" {
		t.Fatalf("missing deferral reason: %+v", st)
	}
	if st.NextAttempt == nil || !st.NextAttempt.Equal(opening) {
		t.Fatalf("next attempt does not expose the opening: %+v", st.NextAttempt)
	}
	if st.WorkRemaining {
		t.Fatal("window-deferred work was reported as runnable now")
	}
	if len(target.bodies) != 0 {
		t.Fatalf("uploaded while the window was closed: %d", len(target.bodies))
	}
	files, err := os.ReadDir(filepath.Join(dir, "staging"))
	if err != nil || len(files) != 0 {
		t.Fatalf("closed-window work downloaded source bytes: %v %v", files, err)
	}

	// Opening the window at the persisted time uploads without an explicit retry.
	clock.Set(opening)
	if err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	st = status(t, e)
	if st.Confirmed != 1 || st.Deferred != 0 || st.Failed != 0 || len(target.bodies) != 1 {
		t.Fatalf("deferred work did not resume at the opening: %+v", st)
	}
}

func TestCrossMidnightWindowAllowsUploadAfterMidnight(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 23, 0, 30, 0, 0, time.UTC)}
	opts, _ := windowedOptions(t, clock, "UTC", "23:00", "06:00")
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, target := singlePhoto()
	e := engine(t, store, source, target, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 1 || len(target.bodies) != 1 || st.TransferWindow == nil || !st.TransferWindow.Open {
		t.Fatalf("cross-midnight window blocked an upload: %+v", st)
	}
}

func TestDSTSpringGapAndFallRepeatGateUploads(t *testing.T) {
	cases := []struct {
		name        string
		now         time.Time
		start, end  string
		opening     time.Time
		resumeAfter time.Time
	}{
		{
			name:        "spring gap opens at the first real wall time",
			now:         time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC), // 01:30 EST
			start:       "02:30",
			end:         "04:00",
			opening:     time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), // 03:00 EDT
			resumeAfter: time.Date(2026, 3, 8, 7, 1, 0, 0, time.UTC), // inside the window
		},
		{
			name:        "fall repeat keeps both wall-clock occurrences open",
			now:         time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC), // 00:30 EDT
			start:       "01:00",
			end:         "02:00",
			opening:     time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC),  // 01:00 EDT
			resumeAfter: time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), // 01:30 EST
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &testClock{now: tc.now}
			opts, _ := windowedOptions(t, clock, "America/New_York", tc.start, tc.end)
			store, err := syncer.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			source, target := singlePhoto()
			e := engine(t, store, source, target, opts)
			if err := e.Once(t.Context()); err != nil {
				t.Fatalf("deferral surfaced as a failure: %v", err)
			}
			st := status(t, e)
			if st.Deferred != 1 || len(target.bodies) != 0 {
				t.Fatalf("upload dispatched outside the window: %+v", st)
			}
			if st.TransferWindow == nil || st.TransferWindow.NextOpening == nil ||
				!st.TransferWindow.NextOpening.Equal(tc.opening) {
				t.Fatalf("DST opening: got %+v want %s", st.TransferWindow, tc.opening)
			}
			clock.Set(tc.resumeAfter)
			if err := e.Step(t.Context()); err != nil {
				t.Fatal(err)
			}
			if st = status(t, e); st.Confirmed != 1 || st.Deferred != 0 || len(target.bodies) != 1 {
				t.Fatalf("window did not allow the upload after DST: %+v", st)
			}
		})
	}
}

func TestWindowEditAppliesToTheNextDispatchWithoutRestart(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	opts, state := windowedOptions(t, clock, "UTC", "23:00", "06:00")
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, target := singlePhoto()
	e := engine(t, store, source, target, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Deferred != 1 || len(target.bodies) != 0 {
		t.Fatalf("closed window did not defer: %+v", st)
	}
	state.Set(mustWindow(t, "UTC", "09:00", "17:00"))
	if err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || st.Deferred != 0 || len(target.bodies) != 1 {
		t.Fatalf("edited window did not apply to the next dispatch: %+v", st)
	}
}

func TestInFlightUploadCompletesWhenTheWindowCloses(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	opts, state := windowedOptions(t, clock, "UTC", "09:00", "17:00")
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, target := singlePhoto()
	started, release := make(chan struct{}), make(chan struct{})
	target.upload = func(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
		close(started)
		<-release
		b, err := io.ReadAll(r)
		if err != nil {
			return bridge.Node{}, err
		}
		sum := md5.Sum(b)
		node := bridge.Node{ID: "one", Name: c.Name, MD5: hex.EncodeToString(sum[:]),
			Size: int64(len(b)), ParentID: c.ParentID}
		target.mu.Lock()
		target.nodes = append(target.nodes, node)
		target.mu.Unlock()
		return node, nil
	}
	e := engine(t, store, source, target, opts)
	done := make(chan error, 1)
	go func() { done <- e.Step(t.Context()) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	// A live window edit while the upload is running must not cancel it.
	state.Set(mustWindow(t, "UTC", "23:00", "06:00"))
	clock.Set(time.Date(2026, 10, 22, 17, 30, 0, 0, time.UTC))
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("closing the window failed an in-flight upload: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight upload did not finish")
	}
	st := status(t, e)
	if st.Confirmed != 1 || st.Unconfirmed != 0 || st.Failed != 0 || st.Deferred != 0 {
		t.Fatalf("closing the window changed the transfer outcome: %+v", st)
	}
}

func TestAccountsWithDifferentWindowsStayIndependent(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	closed, _ := windowedOptions(t, clock, "UTC", "23:00", "06:00")
	open, _ := windowedOptions(t, clock, "UTC", "09:00", "17:00")
	closed.BytesPerSecond, open.BytesPerSecond = 1024, 1024
	open.AccountID = "other"
	closedSource, closedTarget := singlePhoto()
	openSource, openTarget := singlePhoto()
	closedEngine := engine(t, store, closedSource, closedTarget, closed)
	openEngine := engine(t, store, openSource, openTarget, open)
	if err := closedEngine.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := openEngine.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, closedEngine); st.Deferred != 1 || st.Confirmed != 0 || len(closedTarget.bodies) != 0 {
		t.Fatalf("closed account window leaked: %+v", st)
	}
	if st := status(t, openEngine); st.Confirmed != 1 || st.Deferred != 0 || len(openTarget.bodies) != 1 {
		t.Fatalf("open account window was blocked: %+v", st)
	}
	// Both accounts share the configured staging and byte-rate limits while the
	// window decision stays per account.
	for _, e := range []*syncer.Engine{closedEngine, openEngine} {
		if st := status(t, e); st.BytesPerSecond != 1024 {
			t.Fatalf("shared byte-rate policy was not reported: %+v", st)
		}
	}
}

func TestUnconfirmedWriteStillReconcilesWhileWindowClosed(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	opts, state := windowedOptions(t, clock, "UTC", "09:00", "17:00")
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, target := singlePhoto()
	posts := 0
	target.upload = func(context.Context, bridge.Content, io.Reader) (bridge.Node, error) {
		posts++
		return bridge.Node{}, &bridge.Error{Code: bridge.Unavailable}
	}
	e := engine(t, store, source, target, opts)
	if err := e.Once(t.Context()); err == nil {
		t.Fatal("ambiguous write was reported successful")
	}
	if st := status(t, e); st.Unconfirmed != 1 {
		t.Fatalf("ambiguous write was not uncertain: %+v", st)
	}
	// The write did commit remotely even though the response was lost.
	target.mu.Lock()
	target.nodes = append(target.nodes, bridge.Node{ID: "remote", Name: "one.jpg",
		MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5})
	target.mu.Unlock()
	if err := e.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	state.Set(mustWindow(t, "UTC", "23:00", "06:00"))
	clock.Set(time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC))
	if err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 1 || st.Unconfirmed != 0 || st.Deferred != 0 || posts != 1 {
		t.Fatalf("closed window broke unconfirmed reconciliation: %+v posts=%d", st, posts)
	}
}

func TestTransferWindowDoesNotGateDeletionPropagation(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	opts, state := windowedOptions(t, clock, "UTC", "09:00", "17:00")
	opts.Deletions = true
	assets := photos(11)
	source := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: assets}},
		data: map[string]string{}}
	for _, asset := range assets {
		source.data[asset.ID] = "hello"
	}
	target := &targetFake{identity: "target"}
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, source, target, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 11 {
		t.Fatalf("setup: %+v", st)
	}
	uploads := len(target.bodies)
	state.Set(mustWindow(t, "UTC", "23:00", "06:00"))
	clock.Set(time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC))
	trashed := photo("one")
	trashed.ExcludedReason = "trashed"
	source.pages[""] = bridge.Page{Assets: append([]bridge.Asset{trashed}, photos(11)[1:]...)}
	if err := e.Once(t.Context()); err != nil {
		t.Fatalf("deletion pass was blocked by the transfer window: %v", err)
	}
	st := status(t, e)
	if st.Deleted != 1 || st.DeletePending != 0 || st.Confirmed != 10 {
		t.Fatalf("deletion did not settle outside the window: %+v", st)
	}
	if len(target.trashed) != 1 || len(target.bodies) != uploads {
		t.Fatalf("window changed deletion behaviour: trash=%v uploads=%d want=%d", target.trashed, len(target.bodies), uploads)
	}
}
