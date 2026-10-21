package immich_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
)

func TestPinnedAlbumMetadataQueryContracts(t *testing.T) {
	for _, pin := range []struct {
		file, version, commit, sha256 string
		withoutAssets                 bool
	}{
		{"immich-v2.0.0-album.openapi.json", "v2.0.0", "e4234af3b3bd32f5dd12a2b0808158339deded43", "9fac5cedd34473a55cea926409ef196b1f46e5713b516b4fec0583cdd52589a3", true},
		{"immich-v3.3.4.openapi.json", "v3.3.4", "69e0cb8606137c4f8dc0e7207a672ff8be39f358", "4fcfea46d57553d0c1bff3093fbc7257f81f250267f0c2368933497daaabcf3a", false},
	} {
		t.Run(pin.version, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + pin.file)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Source struct{ Version, Commit, SHA256 string } `json:"x-source"`
				Paths  map[string]map[string]struct {
					Parameters []struct {
						Name, In string
						Required bool
						Schema   struct{ Type string }
					}
				}
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Source.Version != pin.version || doc.Source.Commit != pin.commit || doc.Source.SHA256 != pin.sha256 {
				t.Fatal("contract must retain pinned upstream provenance")
			}
			found := false
			for _, p := range doc.Paths["/albums/{id}"]["get"].Parameters {
				if p.Name == "withoutAssets" {
					found = true
					if p.In != "query" || p.Required || p.Schema.Type != "boolean" {
						t.Fatalf("album metadata query changed: %+v", p)
					}
				}
			}
			if found != pin.withoutAssets {
				t.Fatalf("withoutAssets contract changed for %s", pin.version)
			}
		})
	}
}

func TestAlbumMetadataOmitsEmbeddedAssetsForEachVersion(t *testing.T) {
	for _, major := range []int{2, 3} {
		for _, operation := range []string{"Album", "Assets"} {
			t.Run(fmt.Sprintf("v%d/%s", major, operation), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/server/version":
						_, _ = fmt.Fprintf(w, `{"major":%d,"minor":0,"patch":0}`, major)
					case "/api/albums/" + albumID:
						if r.Method != http.MethodGet {
							t.Errorf("album metadata method = %s", r.Method)
						}
						if major == 2 {
							if r.URL.RawQuery != "withoutAssets=true" {
								t.Errorf("v2 album metadata embeds all assets without withoutAssets=true: query=%q", r.URL.RawQuery)
							}
							// v2 includes assets by default; the query explicitly disables them.
							assets := ""
							if r.URL.Query().Get("withoutAssets") != "true" {
								assets = `,"assets":[]`
							}
							_, _ = fmt.Fprintf(w, `{"id":%q,"albumName":"Owned","assetCount":0,"shared":false%s}`, albumID, assets)
						} else {
							if r.URL.RawQuery != "" {
								t.Errorf("v3 album metadata has no query contract: %q", r.URL.RawQuery)
							}
							_, _ = fmt.Fprintf(w, `{"id":%q,"albumName":"Owned","assetCount":0,"shared":false}`, albumID)
						}
					case "/api/search/metadata":
						_, _ = fmt.Fprint(w, `{"assets":{"count":0,"items":[],"nextPage":null}}`)
					default:
						t.Errorf("unexpected metadata route: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				c, err := immich.New(srv.URL, "secret")
				if err != nil {
					t.Fatal(err)
				}
				if operation == "Album" {
					album, err := c.Album(t.Context(), albumID)
					if err != nil || album.ID != albumID || album.Count != 0 {
						t.Fatalf("album metadata: %+v %v", album, err)
					}
				} else {
					page, err := c.Assets(t.Context(), bridge.Scope{AlbumID: albumID}, "")
					if err != nil || len(page.Assets) != 0 || page.Next != "" {
						t.Fatalf("album discovery: %+v %v", page, err)
					}
				}
			})
		}
	}
}
