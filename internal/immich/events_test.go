package immich_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
)

func TestEventLookupCombinesAssetIDAndAlbumAllowlist(t *testing.T) {
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/server/version" {
			_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/api/search/metadata" {
			t.Errorf("unexpected endpoint %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			ID     string   `json:"id"`
			Albums []string `json:"albumIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.ID != assetID || len(body.Albums) != 1 || body.Albums[0] != albumID {
			t.Errorf("event lookup expanded scope: %+v", body)
		}
		seen = true
		_, _ = fmt.Fprint(w, `{"assets":{"count":1,"items":[`+photoJSON+`],"nextPage":null}}`)
	}))
	defer server.Close()
	source, err := immich.New(server.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	p, err := source.Assets(t.Context(), bridge.Scope{AlbumID: albumID, AssetID: assetID}, "")
	if err != nil || len(p.Assets) != 1 || !seen {
		t.Fatalf("event lookup: %+v %v", p, err)
	}
}
