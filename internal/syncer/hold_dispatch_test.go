package syncer_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// A held video backlog must not consume the bounded selection window. The hold
// predicate is part of the queue query, so a photo behind more than one batch
// of held videos still dispatches on the same pass.
func TestVideoHoldDoesNotStarvePhotosBehindTheSelectionBound(t *testing.T) {
	calculated := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
	assets := make([]bridge.Asset, 0, 71)
	data := map[string]string{}
	for i := range 70 {
		id := fmt.Sprintf("clip-%02d", i)
		assets = append(assets, video(id))
		data[id] = "movie"
	}
	// The photo sorts after every video, so a Go-side skip of the first 64 rows
	// would never reach it.
	assets = append(assets, photo("zz-photo"))
	data["zz-photo"] = "hello"
	s := &sourceFake{identity: "source",
		pages: map[string]bridge.Page{"": {Assets: assets}}, data: data}
	d := &targetFake{identity: "target", usage: usageObservation(2000, calculated)}
	opts := options()
	opts.Workers = 1
	opts.Filters.MediaTypes = []string{"image", "video"}
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
	if !st.Usage.VideoHold || st.Confirmed != 1 || st.Pending != 70 {
		t.Fatalf("held video backlog changed dispatch: %+v", st)
	}
	if len(d.bodies) != 1 || d.bodies[0] != "hello" {
		t.Fatalf("photo behind held videos did not dispatch exactly once: %v", d.bodies)
	}
}
