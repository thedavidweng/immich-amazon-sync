package immich_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
)

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
