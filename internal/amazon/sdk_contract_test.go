package amazon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
)

type sdkFixture struct {
	Source struct {
		SDK       string   `json:"sdk"`
		Version   string   `json:"version"`
		TagCommit string   `json:"tag_commit"`
		Files     []string `json:"files"`
		Captured  string   `json:"captured"`
	} `json:"x-source"`
	Nodes                      map[string]json.RawMessage `json:"nodes"`
	FavoritePatch              json.RawMessage            `json:"favorite-patch-without-settings"`
	Usage                      json.RawMessage            `json:"usage"`
	UsageWithoutLastCalculated json.RawMessage            `json:"usage-without-last-calculated"`
}

func loadSDKFixture(t *testing.T) sdkFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/aphotos-v0.1.4-settings-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture sdkFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	// The recorded payloads were captured from v0.1.4. v0.1.5 changes only the
	// video-bitrate decode, so this settings/usage fixture stays valid; a real
	// shape change in either release must be re-recorded, not silently accepted.
	if fixture.Source.Version != "v0.1.4" || fixture.Source.SDK != "github.com/thedavidweng/amazon-photos" ||
		fixture.Source.TagCommit != "366da81406f806924af6d9a3934564a6c3d84e73" {
		t.Fatal("SDK fixture must retain pinned upstream provenance")
	}
	return fixture
}

// favoritePresence reads the raw payload the SDK retained. NodeSettings.Favorite
// is a plain bool, so only the raw JSON can distinguish an absent field from an
// explicit false; callers must not treat the typed zero value as evidence.
func favoritePresence(raw json.RawMessage) (value, present bool) {
	var envelope struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false, false
	}
	field, ok := envelope.Settings["favorite"]
	if !ok || json.Unmarshal(field, &value) != nil {
		return false, false
	}
	return value, true
}

func TestPinnedNodeFavoritePresence(t *testing.T) {
	fixture := loadSDKFixture(t)
	for name, want := range map[string]struct {
		settingsNil, favorite, present bool
	}{
		"favorite-absent":     {settingsNil: true},
		"favorite-false":      {favorite: false, present: true},
		"favorite-true":       {favorite: true, present: true},
		"favorite-key-absent": {settingsNil: false, favorite: false, present: false},
	} {
		t.Run(name, func(t *testing.T) {
			payload := fixture.Nodes[name]
			sdk := newSDK(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/nodes/"+name {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(payload)
			})
			node, err := sdk.GetNode(t.Context(), name)
			if err != nil {
				t.Fatal(err)
			}
			if (node.Settings == nil) != want.settingsNil {
				t.Fatalf("Settings nil = %v, want %v", node.Settings == nil, want.settingsNil)
			}
			if node.Settings != nil && node.Settings.Favorite != want.favorite {
				t.Fatalf("typed Favorite = %v, want %v", node.Settings.Favorite, want.favorite)
			}
			value, present := favoritePresence(node.Raw)
			if present != want.present {
				t.Fatalf("raw settings.favorite present = %v, want %v", present, want.present)
			}
			if present && value != want.favorite {
				t.Fatalf("raw settings.favorite = %v, want %v", value, want.favorite)
			}
			if name == "favorite-key-absent" && node.Settings.Favorite {
				t.Fatal("fixture no longer illustrates the ambiguous typed false")
			}
		})
	}
}

// SetFavorite synthesises a NodeSettings fallback when the PATCH response omits
// settings. The pinned fixture proves that fallback is not evidence; a caller
// must re-read the node and require an explicit raw settings.favorite field.
func TestPinnedFavoriteMutationResponseCannotStandAsProof(t *testing.T) {
	fixture := loadSDKFixture(t)
	var patches atomic.Int32
	sdk := newSDK(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/nodes/node-favorite-absent" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		patches.Add(1)
		_, _ = w.Write(fixture.FavoritePatch)
	})
	nodes, err := sdk.SetFavorite(t.Context(), []string{"node-favorite-absent"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || patches.Load() != 1 {
		t.Fatalf("mutation calls = %d, nodes = %d", patches.Load(), len(nodes))
	}
	if nodes[0].Settings == nil || !nodes[0].Settings.Favorite {
		t.Fatal("fixture no longer triggers the SDK fallback this test pins")
	}
	if value, present := favoritePresence(nodes[0].Raw); present {
		t.Fatalf("mutation response unexpectedly proved settings.favorite=%v", value)
	}
}

func TestPinnedUsageCategoriesAndLastCalculated(t *testing.T) {
	fixture := loadSDKFixture(t)
	sdk := newSDK(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/account/usage" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(fixture.Usage)
	})
	usage, err := sdk.GetUsage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC); !usage.LastCalculated.Equal(want) {
		t.Fatalf("lastCalculated = %v, want %v", usage.LastCalculated, want)
	}
	for name, want := range map[string]struct{ billableBytes, billableCount, totalBytes int64 }{
		"photo": {123456789, 42, 234567890},
		"video": {987654321, 3, 987654321},
		"other": {1024, 1, 2048},
		"doc":   {10, 1, 20},
	} {
		got := usage.Categories[name]
		if got.Billable.Bytes != want.billableBytes || got.Billable.Count != want.billableCount || got.Total.Bytes != want.totalBytes {
			t.Fatalf("%s usage = %+v", name, got)
		}
	}
	if got := usage.Photo; got.Billable.Bytes != 123456789 || got.Total.Count != 50 {
		t.Fatalf("typed photo usage = %+v", got)
	}
	if got := usage.Categories["audio"]; got.Billable.Bytes != 5 || got.Total.Count != 1 {
		t.Fatalf("unknown category was dropped: %+v", got)
	}
	if !bytes.Equal(usage.Raw, fixture.Usage) {
		t.Fatal("usage raw payload was not retained")
	}
}

// A usage response without lastCalculated must remain visibly stale. Zero time
// is the sentinel; it must never be replaced by "now" or an invented capacity.
func TestPinnedUsageWithoutLastCalculatedStaysStale(t *testing.T) {
	fixture := loadSDKFixture(t)
	sdk := newSDK(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/usage" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(fixture.UsageWithoutLastCalculated)
	})
	usage, err := sdk.GetUsage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !usage.LastCalculated.IsZero() {
		t.Fatalf("missing lastCalculated produced a fabricated timestamp: %v", usage.LastCalculated)
	}
	if usage.Photo.Billable.Bytes != 1 || usage.Video.Total.Count != 0 {
		t.Fatalf("categorized usage lost: %+v", usage)
	}
}
