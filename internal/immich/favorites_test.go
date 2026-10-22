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

// The pinned v3.3.4 examples prove the source favorite setting is
// presence-aware: an absent isFavorite decodes as unknown, never false.
func TestPinnedAssetFavoritePresence(t *testing.T) {
	data, err := os.ReadFile("testdata/immich-v3.3.4-assets.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Assets map[string]map[string]json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	items := make([]json.RawMessage, 0, 3)
	for _, name := range []string{"favorite-true-archived", "favorite-false", "favorite-absent"} {
		raw, err := json.Marshal(doc.Assets[name])
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, raw)
	}
	body, err := json.Marshal(map[string]any{
		"assets": map[string]any{"count": len(items), "items": items, "nextPage": nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server/version":
			_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
		case "/api/search/metadata":
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := immich.New(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.Assets(t.Context(), bridge.Scope{Library: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Assets) != 3 {
		t.Fatalf("assets = %d", len(page.Assets))
	}
	for i, want := range []struct {
		present bool
		value   bool
	}{{true, true}, {true, false}, {}} {
		got := page.Assets[i].Favorite
		switch {
		case !want.present && got != nil:
			t.Fatalf("%s: absent source favorite decoded as %v", page.Assets[i].ID, *got)
		case want.present && (got == nil || *got != want.value):
			t.Fatalf("%s: favorite = %v, want %v", page.Assets[i].ID, got, want.value)
		}
	}
}
