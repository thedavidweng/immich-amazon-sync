package amazon_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

const trashNodeJSON = `{"id":"node-a","name":"photo.jpg","kind":"FILE","status":"AVAILABLE",
	"parents":["root-account-a"],"ownerId":"account-a",
	"contentProperties":{"md5":"900150983cd24fb0d6963f7d28e17f72","size":3}}`

func trashContent() bridge.Content {
	return bridge.Content{Name: "photo.jpg", MIME: "image/jpeg", Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"}
}

func TestTrashVerifiesOwnershipAndContentBeforeMoving(t *testing.T) {
	var patches, reads atomic.Int32
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true":
			_, _ = w.Write([]byte(`{"data":[` + rootJSON + `],"count":1}`))
		case r.URL.Path == "/nodes/node-a" && r.Method == "GET":
			if reads.Add(1) == 1 {
				_, _ = w.Write([]byte(trashNodeJSON))
				return
			}
			_, _ = w.Write([]byte(strings.Replace(trashNodeJSON, `"status":"AVAILABLE"`, `"status":"TRASH"`, 1)))
		case r.URL.Path == "/trash" && r.Method == "PATCH":
			patches.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	if err := c.Trash(context.Background(), trashContent(), "node-a"); err != nil {
		t.Fatal(err)
	}
	if patches.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("trash=%d reads=%d", patches.Load(), reads.Load())
	}
}

func TestTrashRefusesUnverifiedNodes(t *testing.T) {
	for name, tt := range map[string]struct {
		node  string
		calls int32
	}{
		"content-mismatch": {strings.Replace(trashNodeJSON, "900150983cd24fb0d6963f7d28e17f72", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1), 0},
		"size-mismatch":    {strings.Replace(trashNodeJSON, `"size":3`, `"size":4`, 1), 0},
		"foreign-owner":    {strings.Replace(trashNodeJSON, `"ownerId":"account-a"`, `"ownerId":"other"`, 1), 0},
		"already-trashed":  {strings.Replace(trashNodeJSON, `"status":"AVAILABLE"`, `"status":"TRASH"`, 1), 0},
	} {
		t.Run(name, func(t *testing.T) {
			var patches atomic.Int32
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true":
					_, _ = w.Write([]byte(`{"data":[` + rootJSON + `],"count":1}`))
				case r.URL.Path == "/nodes/node-a":
					_, _ = w.Write([]byte(tt.node))
				case r.URL.Path == "/trash":
					patches.Add(1)
					w.WriteHeader(http.StatusOK)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			})
			err := c.Trash(context.Background(), trashContent(), "node-a")
			if name == "already-trashed" {
				if err != nil {
					t.Fatalf("committed retry must succeed: %v", err)
				}
			} else {
				var remote *bridge.Error
				if !errors.As(err, &remote) || remote.Code != bridge.Unconfirmed {
					t.Fatalf("error: %v", err)
				}
			}
			if patches.Load() != 0 {
				t.Fatalf("trashed an unverified node")
			}
		})
	}
}

func TestTrashNeverReportsUnprovenSuccess(t *testing.T) {
	for name, tt := range map[string]struct {
		status int
		reread string
		code   string
	}{
		"mutation-rejected": {http.StatusServiceUnavailable, trashNodeJSON, bridge.Unconfirmed},
		"still-available":   {http.StatusOK, trashNodeJSON, bridge.Unconfirmed},
	} {
		t.Run(name, func(t *testing.T) {
			var reads atomic.Int32
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true":
					_, _ = w.Write([]byte(`{"data":[` + rootJSON + `],"count":1}`))
				case r.URL.Path == "/nodes/node-a":
					if reads.Add(1) == 1 {
						_, _ = w.Write([]byte(trashNodeJSON))
						return
					}
					_, _ = w.Write([]byte(tt.reread))
				case r.URL.Path == "/trash":
					w.WriteHeader(tt.status)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			})
			err := c.Trash(context.Background(), trashContent(), "node-a")
			var remote *bridge.Error
			if !errors.As(err, &remote) || remote.Code != tt.code {
				t.Fatalf("error: %v", err)
			}
		})
	}
}
