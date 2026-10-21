package immich_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
)

func TestV2DiscoveryIncludesArchivedOriginals(t *testing.T) {
	for _, album := range []bool{false, true} {
		t.Run(fmt.Sprintf("album=%t", album), func(t *testing.T) {
			var searches []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/server/version":
					_, _ = fmt.Fprint(w, `{"major":2,"minor":0,"patch":0}`)
				case "/api/albums/" + albumID:
					_, _ = fmt.Fprint(w, `{"id":"`+albumID+`","albumName":"Owned","assetCount":4,"shared":false}`)
				case "/api/search/metadata":
					var body struct {
						Visibility   string   `json:"visibility"`
						Page         int      `json:"page"`
						AlbumIDs     []string `json:"albumIds"`
						UpdatedAfter string   `json:"updatedAfter"`
						WithExif     bool     `json:"withExif"`
						Size         int      `json:"size"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if r.Method != "POST" || !body.WithExif || body.Size != 250 ||
						body.UpdatedAfter != "2026-10-01T00:00:00Z" ||
						(album && (len(body.AlbumIDs) != 1 || body.AlbumIDs[0] != albumID)) ||
						(!album && len(body.AlbumIDs) != 0) {
						t.Errorf("search scope changed: %+v", body)
					}
					// v2 defaults an omitted visibility to timeline, not timeline + archive.
					if body.Visibility == "" {
						body.Visibility = "timeline"
					}
					searches = append(searches, fmt.Sprintf("%s:%d", body.Visibility, body.Page))
					index := body.Page
					if body.Visibility == "archive" {
						index += 2
					} else if body.Visibility != "timeline" {
						t.Errorf("unsupported v2 visibility: %s", body.Visibility)
					}
					id := fmt.Sprintf("%08d-1111-4111-8111-111111111111", index)
					asset := strings.ReplaceAll(photoJSON, assetID, id)
					asset = strings.ReplaceAll(asset, `"visibility":"timeline"`, `"visibility":"`+body.Visibility+`"`)
					next := "null"
					if body.Page == 1 {
						next = `"2"`
					} else if body.Page != 2 {
						t.Errorf("unexpected page: %d", body.Page)
					}
					_, _ = fmt.Fprintf(w, `{"assets":{"count":1,"items":[%s],"nextPage":%s}}`, asset, next)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			c, err := immich.New(srv.URL, "secret")
			if err != nil {
				t.Fatal(err)
			}
			scope := bridge.Scope{Library: !album, UpdatedAfter: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			if album {
				scope.AlbumID = albumID
			}
			var assets []bridge.Asset
			cursor := ""
			for range 5 {
				page, err := c.Assets(t.Context(), scope, cursor)
				if err != nil {
					t.Fatal(err)
				}
				assets = append(assets, page.Assets...)
				cursor = page.Next
				if cursor == "" {
					break
				}
			}
			if len(assets) != 4 || cursor != "" || strings.Join(searches, ",") != "timeline:1,timeline:2,archive:1,archive:2" {
				t.Fatalf("incomplete v2 discovery: assets=%d cursor=%q searches=%v", len(assets), cursor, searches)
			}
			for _, a := range assets {
				if a.ExcludedReason != "" {
					t.Fatalf("eligible original excluded: %+v", a)
				}
			}
		})
	}
}

func TestV2DiscoveryRequiresBothVisibilityStreamsToComplete(t *testing.T) {
	empty := `{"assets":{"count":0,"items":[],"nextPage":null}}`
	timeline := `{"assets":{"count":1,"items":[` + photoJSON + `],"nextPage":null}}`
	archive := `{"assets":{"count":1,"items":[` + strings.ReplaceAll(strings.ReplaceAll(photoJSON,
		assetID, "44444444-4444-4444-8444-444444444444"), `"visibility":"timeline"`, `"visibility":"archive"`) + `],"nextPage":null}}`
	for _, tt := range []struct {
		name, timeline, archive string
		count                   int
		invalid                 bool
	}{
		{"empty-timeline", empty, archive, 1, false},
		{"empty-archive", timeline, empty, 1, false},
		{"both-empty", empty, empty, 0, false},
		{"missing-archive-envelope", timeline, `{}`, 0, true},
		{"archive-omits-completion", timeline, `{"assets":{"count":0,"items":[]}}`, 0, true},
		{"archive-empty-continuation", timeline, `{"assets":{"count":0,"items":[],"nextPage":"2"}}`, 0, true},
		{"timeline-omits-completion", `{"assets":{"count":0,"items":[]}}`, archive, 0, true},
		{"duplicate-between-streams", timeline, timeline, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/server/version" {
					_, _ = fmt.Fprint(w, `{"major":2,"minor":0,"patch":0}`)
					return
				}
				var body struct{ Visibility string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				response := tt.timeline
				if body.Visibility == "archive" {
					response = tt.archive
				}
				_, _ = fmt.Fprint(w, response)
			}))
			defer srv.Close()
			c, err := immich.New(srv.URL, "secret")
			if err != nil {
				t.Fatal(err)
			}
			page, err := c.Assets(t.Context(), bridge.Scope{Library: true}, "")
			var remote *bridge.Error
			if tt.invalid {
				if !errors.As(err, &remote) || remote.Code != bridge.Protocol || len(page.Assets) != 0 || page.Next != "" {
					t.Fatalf("partial evidence returned: %+v %v", page, err)
				}
			} else if err != nil || len(page.Assets) != tt.count || page.Next != "" {
				t.Fatalf("unexpected completion: %+v %v", page, err)
			}
		})
	}
}

func TestV2DiscoveryRejectsMalformedCursors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/server/version" {
			t.Errorf("malformed cursor sent to server: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"major":2,"minor":0,"patch":0}`)
	}))
	defer srv.Close()
	c, err := immich.New(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []string{"archive:", "archive:0", "archive:01", "archive:-1", "archive:2:3", "bogus"} {
		_, err := c.Assets(t.Context(), bridge.Scope{Library: true}, cursor)
		var remote *bridge.Error
		if !errors.As(err, &remote) || remote.Code != bridge.Protocol {
			t.Errorf("cursor %q accepted: %v", cursor, err)
		}
	}
}
