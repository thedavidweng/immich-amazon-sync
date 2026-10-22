package amazon_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

const favoriteFixtureMD5 = "900150983cd24fb0d6963f7d28e17f72"

func favoriteContent() bridge.Content {
	return bridge.Content{Name: "photo.jpg", MIME: "image/jpeg", MD5: favoriteFixtureMD5, Size: 3}
}

func favoriteRoot(owner string) string {
	return `{"id":"root-owner","isRoot":true,"kind":"FOLDER","status":"AVAILABLE","ownerId":"` + owner + `"}`
}

// A live ca account returned no settings object at all for a media node it had
// never changed a setting on, and one PATCH made the key appear. The omission is
// an authoritative false. Once a settings object exists, a missing favorite key
// is still unknown, because it cannot distinguish a stored false from an
// incomplete payload.
func TestFavoriteReadRequiresExplicitField(t *testing.T) {
	fixture := loadSDKFixture(t)
	for name, want := range map[string]struct {
		value   bool
		present bool
	}{
		"favorite-true":       {value: true, present: true},
		"favorite-false":      {value: false, present: true},
		"favorite-absent":     {value: false, present: true},
		"favorite-key-absent": {present: false},
	} {
		t.Run(name, func(t *testing.T) {
			payload := fixture.Nodes[name]
			var identity struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(payload, &identity); err != nil || identity.ID == "" {
				t.Fatal("fixture node lost its identity")
			}
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/nodes":
					_, _ = fmt.Fprint(w, `{"data":[`+favoriteRoot("owner")+`],"count":1}`)
				case r.URL.Path == "/nodes/"+identity.ID && r.Method == http.MethodGet:
					_, _ = w.Write(payload)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			value, err := c.ReadFavorite(t.Context(), favoriteContent(), identity.ID)
			if want.present {
				if err != nil || value != want.value {
					t.Fatalf("read = %v, %v", value, err)
				}
				return
			}
			var remote *bridge.Error
			if !errors.As(err, &remote) || remote.Code != bridge.FavoriteUnconfirmed {
				t.Fatalf("absent field read = %v, want favorite_unconfirmed", err)
			}
		})
	}
}

func TestFavoriteReadRejectsForeignOwnership(t *testing.T) {
	fixture := loadSDKFixture(t)
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes":
			_, _ = fmt.Fprint(w, `{"data":[`+favoriteRoot("someone-else")+`],"count":1}`)
		case "/nodes/node-favorite-true":
			_, _ = w.Write(fixture.Nodes["favorite-true"])
		default:
			http.NotFound(w, r)
		}
	})
	_, err := c.ReadFavorite(t.Context(), favoriteContent(), "node-favorite-true")
	var remote *bridge.Error
	if !errors.As(err, &remote) || remote.Code != bridge.Unconfirmed {
		t.Fatalf("foreign node read = %v", err)
	}
}

// The mutation response is never proof: SetFavorite re-reads the node and only
// survives when the fresh payload carries the requested explicit value.
func TestFavoriteMutationIsVerifiedByReread(t *testing.T) {
	fixture := loadSDKFixture(t)
	for name, tt := range map[string]struct {
		after   string
		patches int32
		want    string
	}{
		"verified":           {after: "favorite-true", patches: 1},
		"response-not-proof": {after: "favorite-absent", patches: 1, want: bridge.FavoriteUnconfirmed},
	} {
		t.Run(name, func(t *testing.T) {
			var patches atomic.Int32
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/nodes":
					_, _ = fmt.Fprint(w, `{"data":[`+favoriteRoot("owner")+`],"count":1}`)
				case r.URL.Path == "/nodes/node-favorite-false" && r.Method == http.MethodPatch:
					patches.Add(1)
					_, _ = w.Write(fixture.FavoritePatch)
				case r.URL.Path == "/nodes/node-favorite-false" && r.Method == http.MethodGet:
					if patches.Load() == 0 {
						_, _ = w.Write(fixture.Nodes["favorite-false"])
						return
					}
					_, _ = w.Write(bytes.ReplaceAll(fixture.Nodes[tt.after], []byte(`"node-favorite-true"`), []byte(`"node-favorite-false"`)))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := c.SetFavorite(t.Context(), favoriteContent(), "node-favorite-false", true)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var remote *bridge.Error
				if !errors.As(err, &remote) || remote.Code != tt.want {
					t.Fatalf("mutation = %v, want %s", err, tt.want)
				}
			}
			if patches.Load() != tt.patches {
				t.Fatalf("mutation count = %d, want %d", patches.Load(), tt.patches)
			}
		})
	}
}

// An already-starred node is verified from the fresh read and never mutated.
func TestFavoriteAlreadyAppliedIsNotRewritten(t *testing.T) {
	fixture := loadSDKFixture(t)
	var patches atomic.Int32
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/nodes":
			_, _ = fmt.Fprint(w, `{"data":[`+favoriteRoot("owner")+`],"count":1}`)
		case r.URL.Path == "/nodes/node-favorite-true" && r.Method == http.MethodGet:
			_, _ = w.Write(fixture.Nodes["favorite-true"])
		case r.Method == http.MethodPatch:
			patches.Add(1)
			http.Error(w, "must not mutate", 500)
		default:
			http.NotFound(w, r)
		}
	})
	if err := c.SetFavorite(t.Context(), favoriteContent(), "node-favorite-true", true); err != nil {
		t.Fatal(err)
	}
	if patches.Load() != 0 {
		t.Fatalf("already starred node was patched %d times", patches.Load())
	}
}

// A lost mutation response is favorite_unconfirmed and never a settled success;
// no second read pretends the write was verified.
func TestFavoriteLostMutationIsUnconfirmed(t *testing.T) {
	fixture := loadSDKFixture(t)
	var patches atomic.Int32
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/nodes":
			_, _ = fmt.Fprint(w, `{"data":[`+favoriteRoot("owner")+`],"count":1}`)
		case r.URL.Path == "/nodes/node-favorite-false" && r.Method == http.MethodPatch:
			patches.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"message":"private"}`)
		case r.URL.Path == "/nodes/node-favorite-false" && r.Method == http.MethodGet:
			_, _ = w.Write(fixture.Nodes["favorite-false"])
		default:
			http.NotFound(w, r)
		}
	})
	err := c.SetFavorite(t.Context(), favoriteContent(), "node-favorite-false", true)
	var remote *bridge.Error
	if !errors.As(err, &remote) || remote.Code != bridge.FavoriteUnconfirmed || patches.Load() != 1 {
		t.Fatalf("lost mutation = %v, patches %d", err, patches.Load())
	}
	if bytes.Contains([]byte(err.Error()), []byte("private")) {
		t.Fatal("error leaked a remote body")
	}
}

// An unstar is only issued against an explicit remote true and is verified by a
// fresh read. The request carries the requested value rather than assuming a
// star, and an already-unfavorited node is never rewritten.
func TestFavoriteUnstarIsExplicitAndVerified(t *testing.T) {
	fixture := loadSDKFixture(t)
	for name, tt := range map[string]struct {
		before  string
		after   string
		patches int32
		value   bool
		want    string
	}{
		"verified":        {before: "favorite-true", after: "favorite-false", patches: 1, value: false},
		"absent-settings": {before: "favorite-absent", patches: 0, value: false},
		"absent-field":    {before: "favorite-key-absent", patches: 0, value: false, want: bridge.FavoriteUnconfirmed},
		"already-cleared": {before: "favorite-false", patches: 0, value: false},
		"lost-response":   {before: "favorite-true", after: "favorite-true", patches: 1, value: false, want: bridge.FavoriteUnconfirmed},
	} {
		t.Run(name, func(t *testing.T) {
			var patches atomic.Int32
			var body []byte
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/nodes":
					_, _ = fmt.Fprint(w, `{"data":[`+favoriteRoot("owner")+`],"count":1}`)
				case r.URL.Path == "/nodes/node-favorite-true" && r.Method == http.MethodPatch:
					patches.Add(1)
					body, _ = io.ReadAll(r.Body)
					_, _ = w.Write(fixture.FavoritePatch)
				case r.URL.Path == "/nodes/node-favorite-true" && r.Method == http.MethodGet:
					name := tt.before
					if patches.Load() > 0 {
						name = tt.after
					}
					payload := fixture.Nodes[name]
					for _, id := range []string{"node-favorite-false", "node-favorite-absent", "node-favorite-key-absent"} {
						payload = bytes.ReplaceAll(payload, []byte(`"`+id+`"`), []byte(`"node-favorite-true"`))
					}
					_, _ = w.Write(payload)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := c.SetFavorite(t.Context(), favoriteContent(), "node-favorite-true", tt.value)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var remote *bridge.Error
				if !errors.As(err, &remote) || remote.Code != tt.want {
					t.Fatalf("mutation = %v, want %s", err, tt.want)
				}
			}
			if patches.Load() != tt.patches {
				t.Fatalf("mutation count = %d, want %d", patches.Load(), tt.patches)
			}
			if patches.Load() > 0 && !bytes.Contains(body, []byte(`"favorite":false`)) {
				t.Fatalf("unstar request did not carry an explicit false: %s", body)
			}
		})
	}
}

// A node with no settings object is starrable: the absent object is a proven
// false, the PATCH is issued once, and the re-read must show an explicit true.
func TestFavoriteStarOnNodeWithoutSettings(t *testing.T) {
	fixture := loadSDKFixture(t)
	var patches atomic.Int32
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/nodes":
			_, _ = fmt.Fprint(w, `{"data":[`+favoriteRoot("owner")+`],"count":1}`)
		case r.URL.Path == "/nodes/node-favorite-absent" && r.Method == http.MethodPatch:
			patches.Add(1)
			_, _ = w.Write(fixture.FavoritePatch)
		case r.URL.Path == "/nodes/node-favorite-absent" && r.Method == http.MethodGet:
			if patches.Load() == 0 {
				_, _ = w.Write(fixture.Nodes["favorite-absent"])
				return
			}
			_, _ = w.Write(bytes.ReplaceAll(fixture.Nodes["favorite-true"],
				[]byte(`"node-favorite-true"`), []byte(`"node-favorite-absent"`)))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	if err := c.SetFavorite(t.Context(), favoriteContent(), "node-favorite-absent", true); err != nil {
		t.Fatalf("star on a node without settings failed: %v", err)
	}
	if patches.Load() != 1 {
		t.Fatalf("mutation count = %d, want 1", patches.Load())
	}
	value, err := c.ReadFavorite(t.Context(), favoriteContent(), "node-favorite-absent")
	if err != nil || !value {
		t.Fatalf("re-read = %v, %v", value, err)
	}
}

// The fixture must keep proving that the mutation response alone has no
// settings field; otherwise the re-read requirement would silently weaken.
func TestFavoriteFixtureStillLacksMutationProof(t *testing.T) {
	fixture := loadSDKFixture(t)
	var envelope struct {
		Settings *json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(fixture.FavoritePatch, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Settings != nil {
		t.Fatal("favorite patch fixture now contains a settings field")
	}
}
