package amazon_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

const otherFolderJSON = `{"id":"folder-b","kind":"FOLDER","status":"AVAILABLE","ownerId":"account-a"}`

// albumNodeJSON is a verified file whose only parent is an owned album.
const albumNodeJSON = `{"id":"node-a","name":"photo.jpg","kind":"FILE","status":"AVAILABLE",
	"parents":["album-a"],"ownerId":"account-a",
	"contentProperties":{"md5":"900150983cd24fb0d6963f7d28e17f72","size":3}}`

func TestFindExistingRequiresOneOwnedVerifiedNode(t *testing.T) {
	folderNode := strings.ReplaceAll(nodeJSON, `["root-account-a"]`, `["folder-b"]`)
	sizeNode := strings.ReplaceAll(nodeJSON, `"size":3`, `"size":4`)
	digestNode := strings.ReplaceAll(nodeJSON, "900150983cd24fb0d6963f7d28e17f72", "00000000000000000000000000000000")
	foreignNode := strings.ReplaceAll(nodeJSON, `"ownerId":"account-a"`, `"ownerId":"someone-else"`)
	trashedNode := strings.ReplaceAll(nodeJSON, `"status":"AVAILABLE"`, `"status":"TRASH"`)
	for name, tc := range map[string]struct {
		payload string
		node    string
		code    string
		parent  string
		album   bool
	}{
		"unique in another folder": {payload: `{"count":1,"data":[` + folderNode + `]}`, node: folderNode, parent: "folder-b"},
		"unique album only":        {payload: `{"count":1,"data":[` + albumNodeJSON + `]}`, node: albumNodeJSON, album: true},
		"zero matches":             {payload: `{"count":0,"data":[]}`},
		"multiple matches": {payload: `{"count":2,"data":[` + nodeJSON + `,` +
			strings.ReplaceAll(nodeJSON, "node-a", "node-b") + `]}`, code: bridge.Unconfirmed},
		"size mismatch": {payload: `{"count":1,"data":[` + sizeNode + `]}`, node: sizeNode, code: bridge.Unconfirmed},
		"digest mismatch": {payload: `{"count":1,"data":[` + digestNode + `]}`, node: digestNode,
			code: bridge.Unconfirmed},
		"incomplete pagination": {payload: `{"count":2,"data":[` + nodeJSON + `]}`, code: bridge.Unconfirmed},
		"foreign owner": {payload: `{"count":1,"data":[` + foreignNode + `]}`, node: foreignNode,
			code: bridge.Unconfirmed},
		"trashed": {payload: `{"count":1,"data":[` + trashedNode + `]}`, node: trashedNode, code: bridge.Unconfirmed},
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/nodes":
					if r.URL.Query().Get("filters") == "isRoot:true" {
						_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
						return
					}
					filter := r.URL.Query().Get("filters")
					if !strings.Contains(filter, "contentProperties.md5:900150983cd24fb0d6963f7d28e17f72") {
						t.Errorf("unscoped digest search: %s", filter)
					}
					if strings.Contains(filter, "parents:") {
						t.Errorf("account-wide lookup must not be parent scoped: %s", filter)
					}
					_, _ = fmt.Fprint(w, tc.payload)
				case "/nodes/folder-b":
					_, _ = fmt.Fprint(w, otherFolderJSON)
				case "/nodes/album-a":
					_, _ = fmt.Fprint(w, albumJSON)
				case "/nodes/node-a":
					// A search hit is only a pointer; the node is re-read here.
					if tc.node == "" {
						t.Errorf("unverified candidate was re-read: %s", r.URL)
						http.NotFound(w, r)
						return
					}
					_, _ = fmt.Fprint(w, tc.node)
				default:
					t.Errorf("unexpected request: %s", r.URL)
				}
			})
			node, err := c.FindExisting(t.Context(), bridge.Content{Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"})
			var remote *bridge.Error
			if tc.code != "" {
				if !errors.As(err, &remote) || remote.Code != tc.code || node.ID != "" {
					t.Fatalf("unverified lookup was accepted: %+v %v", node, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.node == "" {
				if node.ID != "" {
					t.Fatalf("absence reported a node: %+v", node)
				}
				return
			}
			if node.ID != "node-a" || node.MD5 != "900150983cd24fb0d6963f7d28e17f72" || node.Size != 3 ||
				node.AlbumOnly != tc.album || node.ParentID != tc.parent {
				t.Fatalf("verified placement lost: %+v", node)
			}
		})
	}
}

func TestFindExistingNeverInfersFromConflict(t *testing.T) {
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes":
			if r.URL.Query().Get("filters") == "isRoot:true" {
				_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
				return
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"message":"duplicate content"}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
		}
	})
	node, err := c.FindExisting(t.Context(), bridge.Content{Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"})
	var remote *bridge.Error
	if !errors.As(err, &remote) || remote.Code != bridge.Conflict || node.ID != "" {
		t.Fatalf("409 was read as a verified copy: %+v %v", node, err)
	}
}
