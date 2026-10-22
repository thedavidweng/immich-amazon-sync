package bridge_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

func TestNodePlacementNeverConfusesTargetWithElsewhere(t *testing.T) {
	for _, tc := range []struct {
		name   string
		node   bridge.Node
		target string
		want   bridge.Placement
	}{
		{"root target", bridge.Node{ID: "n", ParentID: ""}, "", bridge.PlacementTarget},
		{"explicit folder target", bridge.Node{ID: "n", ParentID: "folder-a"}, "folder-a", bridge.PlacementTarget},
		{"another owned folder", bridge.Node{ID: "n", ParentID: "folder-b"}, "folder-a", bridge.PlacementFolder},
		{"another owned folder under root target", bridge.Node{ID: "n", ParentID: "folder-b"}, "", bridge.PlacementFolder},
		{"album only", bridge.Node{ID: "n", AlbumOnly: true}, "", bridge.PlacementAlbum},
		{"album only never becomes target", bridge.Node{ID: "n", ParentID: "", AlbumOnly: true}, "", bridge.PlacementAlbum},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.node.Placement(tc.target); got != tc.want {
				t.Fatalf("placement = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTransientOriginalPathIsNeverPersisted(t *testing.T) {
	asset := bridge.Asset{ID: "asset", Name: "photo.jpg", Size: 1,
		OriginalPath: "/immich/upload/library/private.jpg"}
	encoded, err := json.Marshal(asset)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "originalpath") ||
		strings.Contains(string(encoded), "private.jpg") {
		t.Fatalf("durable asset metadata retained a source path: %s", encoded)
	}
}
