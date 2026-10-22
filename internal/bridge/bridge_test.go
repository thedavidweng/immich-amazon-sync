package bridge_test

import (
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
