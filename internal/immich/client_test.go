package immich_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
)

const (
	albumID = "11111111-1111-4111-8111-111111111111"
	assetID = "22222222-2222-4222-8222-222222222222"
)

func TestReadOnlyIdentitySurvivesKeyRotation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("x-api-key") == "" {
			t.Errorf("invalid probe: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/users/me":
			_, _ = fmt.Fprint(w, `{"id":"`+assetID+`"}`)
		case "/api/server/version":
			_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4,"prerelease":null}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	first, err := immich.New(srv.URL, "first-key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := immich.New(srv.URL+"/api/", "rotated-key")
	if err != nil {
		t.Fatal(err)
	}
	id, err := first.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := second.Identity(t.Context())
	if err != nil || id == "" || id != rotated || strings.Contains(id, srv.URL) || strings.Contains(id, "key") {
		t.Fatalf("unstable identity: %q %q %v", id, rotated, err)
	}
	version, err := first.Version(t.Context())
	if err != nil || version != "3.3.4" {
		t.Fatalf("version = %q, %v", version, err)
	}
}

func TestConfiguredBaseAndIDsCannotRedirectCredentials(t *testing.T) {
	for _, base := range []string{
		"https://user:secret@example.com", "https://example.com?token=secret",
		"https://example.com/#secret", "https://example.com?", "https://example.com/a/../api",
		"file:///etc/passwd", "https://example.com/%2e%2e",
	} {
		if _, err := immich.New(base, "secret"); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("unsafe base accepted/leaked: %q %v", base, err)
		}
	}
	var forwarded atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/secret", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, _ := immich.New(srv.URL, "secret-key")
	_, err := c.Identity(t.Context())
	if err == nil || forwarded.Load() != 0 || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("credential redirect: %v, forwarded %d", err, forwarded.Load())
	}
	for _, id := range []string{other.URL, "../users/me", assetID + "?key=secret", ""} {
		if _, err := c.Original(t.Context(), id); err == nil {
			t.Errorf("unsafe asset ID accepted: %q", id)
		}
	}
}

func TestReadFailuresAreFiniteAndKeepRetryAfter(t *testing.T) {
	for status, code := range map[int]string{
		400: bridge.Protocol, 401: bridge.Authentication, 403: bridge.Permission,
		404: bridge.NotFound, 409: bridge.Conflict, 429: bridge.RateLimit, 503: bridge.Unavailable,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"message":"secret media URL http://private"}`)
			}))
			defer srv.Close()
			c, _ := immich.New(srv.URL, "cookie=private")
			_, err := c.Identity(t.Context())
			var remote *bridge.Error
			if !errors.As(err, &remote) || remote.Code != code || remote.RetryAfter != 7*time.Second || err.Error() != code {
				t.Fatalf("classification: %#v", err)
			}
		})
	}
}

func TestOriginalStreamsWithCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "abc")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := immich.New(srv.URL, "secret")
	stream, err := c.Original(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	buf := make([]byte, 3)
	if _, err := io.ReadFull(stream, buf); err != nil || string(buf) != "abc" {
		t.Fatalf("not a streaming original: %q %v", buf, err)
	}
	cancel()
	_, err = stream.Read(buf)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost caller context: %v", err)
	}
}

func TestMalformedPagesCannotProveCompletion(t *testing.T) {
	for name, response := range map[string]string{
		"missing-envelope": `{}`, "null-envelope": `{"assets":null}`,
		"missing-count":   `{"assets":{"items":[],"nextPage":null}}`,
		"missing-token":   `{"assets":{"count":0,"items":[]}}`,
		"wrong-count":     `{"assets":{"count":2,"items":[` + photoJSON + `],"nextPage":null}}`,
		"numeric-token":   `{"assets":{"count":1,"items":[` + photoJSON + `],"nextPage":2}}`,
		"repeated-token":  `{"assets":{"count":1,"items":[` + photoJSON + `],"nextPage":"1"}}`,
		"empty-with-more": `{"assets":{"count":0,"items":[],"nextPage":"2"}}`,
		"duplicate-id":    `{"assets":{"count":2,"items":[` + photoJSON + `,` + photoJSON + `],"nextPage":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/server/version" {
					_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
					return
				}
				_, _ = fmt.Fprint(w, response)
			}))
			defer srv.Close()
			c, _ := immich.New(srv.URL, "secret")
			_, err := c.Assets(t.Context(), bridge.Scope{Library: true}, "")
			if err == nil {
				t.Fatal("malformed page certified completion")
			}
		})
	}
}

const photoJSON = `{"id":"` + assetID + `","type":"IMAGE","originalFileName":"photo.heic",
	"originalMimeType":"image/heic","checksum":"qZk+NkcGgWq6PiVxeFDCbJzQ2J0=",
	"libraryId":null,"visibility":"timeline","isOffline":false,"isTrashed":false,
	"originalPath":"/immich/upload/library/photo.heic",
	"exifInfo":{"fileSizeInByte":3},"fileCreatedAt":"2026-10-01T12:00:00Z",
	"fileModifiedAt":"2026-10-01T11:00:00Z",
	"updatedAt":"2026-10-02T12:00:00Z","livePhotoVideoId":"33333333-3333-4333-8333-333333333333"}`

func TestAlbumDiscoveryAndOriginalContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "secret" {
			t.Error("missing API key")
		}
		switch r.URL.Path {
		case "/api/server/version":
			_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
		case "/api/albums":
			_, _ = fmt.Fprint(w, `[{"id":"`+albumID+`","albumName":"Family","assetCount":2}]`)
		case "/api/albums/" + albumID:
			_, _ = fmt.Fprint(w, `{"id":"`+albumID+`","albumName":"Family","assetCount":2}`)
		case "/api/search/metadata":
			var body struct {
				AlbumIDs     []string `json:"albumIds"`
				WithExif     bool     `json:"withExif"`
				Page         int      `json:"page"`
				Size         int      `json:"size"`
				UpdatedAfter string   `json:"updatedAfter"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if r.Method != "POST" || len(body.AlbumIDs) != 1 || body.AlbumIDs[0] != albumID ||
				!body.WithExif || body.Size < 1 || body.UpdatedAfter != "2026-10-01T00:00:00Z" {
				t.Errorf("unexpected search request: %+v", body)
			}
			switch body.Page {
			case 1:
				_, _ = fmt.Fprint(w, `{"assets":{"count":1,"items":[`+photoJSON+`],"nextPage":"2"}}`)
			case 2:
				_, _ = fmt.Fprint(w, `{"assets":{"count":1,"items":[`+strings.ReplaceAll(photoJSON, assetID, "44444444-4444-4444-8444-444444444444")+`],"nextPage":null}}`)
			default:
				t.Errorf("page = %d", body.Page)
			}
		case "/api/assets/" + assetID:
			_, _ = fmt.Fprint(w, photoJSON)
		case "/api/assets/" + assetID + "/original":
			if r.Method != "GET" {
				t.Error("original must be a read")
			}
			if r.URL.RawQuery != "" {
				t.Error("original must use the common v2/v3 unedited API-key contract")
			}
			_, _ = fmt.Fprint(w, "abc")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := immich.New(srv.URL+"/api", "secret")
	if err != nil {
		t.Fatal(err)
	}
	albums, err := c.Albums(t.Context())
	if err != nil || len(albums) != 1 || albums[0] != (bridge.Album{ID: albumID, Name: "Family", Count: 2}) {
		t.Fatalf("albums: %+v, %v", albums, err)
	}
	album, err := c.Album(t.Context(), albumID)
	if err != nil || album != albums[0] {
		t.Fatalf("album: %+v %v", album, err)
	}
	after, _ := time.Parse(time.RFC3339, "2026-10-01T00:00:00Z")
	scope := bridge.Scope{AlbumID: albumID, UpdatedAfter: after}
	page, err := c.Assets(t.Context(), scope, "")
	if err != nil || len(page.Assets) != 1 || page.Next != "2" {
		t.Fatalf("page: %+v, %v", page, err)
	}
	a := page.Assets[0]
	if a.OriginalPath != "/immich/upload/library/photo.heic" {
		t.Fatalf("transient original path: %+v", a)
	}
	if a.SHA1 != "qZk+NkcGgWq6PiVxeFDCbJzQ2J0=" || a.Size != 3 ||
		a.Kind != "IMAGE" || a.MIME != "image/heic" || a.LivePhotoVideo ||
		a.TakenAt.Format(time.RFC3339) != "2026-10-01T12:00:00Z" ||
		a.FileModifiedAt.Format(time.RFC3339) != "2026-10-01T11:00:00Z" {
		t.Fatalf("asset: %+v", a)
	}
	last, err := c.Assets(t.Context(), scope, page.Next)
	if err != nil || len(last.Assets) != 1 || last.Next != "" {
		t.Fatalf("last page: %+v %v", last, err)
	}
	detail, err := c.Asset(t.Context(), assetID)
	if err != nil || detail != a {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	stream, err := c.Original(t.Context(), assetID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "abc" {
		t.Fatalf("original = %q, %v", data, err)
	}
}

func TestExcludedAssetsRemainInPagesWithoutExcludingTheirStill(t *testing.T) {
	for _, tt := range []struct{ name, old, replacement, reason string }{
		{"hidden", `"visibility":"timeline"`, `"visibility":"hidden"`, "hidden"},
		{"locked", `"visibility":"timeline"`, `"visibility":"locked"`, "locked"},
		{"trashed", `"isTrashed":false`, `"isTrashed":true`, "trashed"},
		{"offline", `"isOffline":false`, `"isOffline":true`, "offline"},
		{"audio", `"type":"IMAGE"`, `"type":"AUDIO"`, "unsupported_type"},
		{"other", `"type":"IMAGE"`, `"type":"OTHER"`, "unsupported_type"},
		{"external", `"libraryId":null`, `"libraryId":"` + albumID + `"`, ""},
		{"unknown-provenance", `"libraryId":null,`, ``, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			excluded := strings.ReplaceAll(photoJSON, tt.old, tt.replacement)
			excluded = strings.ReplaceAll(excluded, assetID, "44444444-4444-4444-8444-444444444444")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/server/version" {
					_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"assets":{"count":2,"items":[`+photoJSON+`,`+excluded+`],"nextPage":null}}`)
			}))
			defer srv.Close()
			c, _ := immich.New(srv.URL, "secret")
			page, err := c.Assets(t.Context(), bridge.Scope{Library: true}, "")
			if err != nil || len(page.Assets) != 2 || page.Assets[0].ExcludedReason != "" ||
				page.Assets[0].LivePhotoVideo || page.Assets[1].ExcludedReason != tt.reason {
				t.Fatalf("page exclusions: %+v %v", page, err)
			}
			if tt.name == "external" || tt.name == "unknown-provenance" {
				if page.Assets[1].SHA1 != "" {
					t.Fatal("non-content checksum advertised as byte SHA1")
				}
			}
		})
	}
}

func TestOriginalUnknownSizeAndOptionalMIME(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, strings.ReplaceAll(strings.ReplaceAll(photoJSON,
			`"exifInfo":{"fileSizeInByte":3},`, ""), `"originalMimeType":"image/heic",`, ""))
	}))
	defer srv.Close()
	c, _ := immich.New(srv.URL, "secret")
	a, err := c.Asset(t.Context(), assetID)
	if err != nil || a.Size != -1 || a.Kind != "IMAGE" || a.MIME != "" || a.ExcludedReason != "" {
		t.Fatalf("optional metadata excludes original: %+v %v", a, err)
	}
}

func TestSharedV2AlbumScopeIsExplicitlyUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server/version":
			_, _ = fmt.Fprint(w, `{"major":2,"minor":0,"patch":0}`)
		case "/api/albums/" + albumID:
			_, _ = fmt.Fprint(w, `{"id":"`+albumID+`","albumName":"Shared","assetCount":10,"shared":true}`)
		default:
			t.Errorf("unsafe shared scope searched: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c, _ := immich.New(srv.URL, "secret")
	_, err := c.Assets(t.Context(), bridge.Scope{AlbumID: albumID}, "")
	var remote *bridge.Error
	if !errors.As(err, &remote) || remote.Code != bridge.Unsupported {
		t.Fatalf("shared scope: %v", err)
	}
}

func TestIncludingTrashedRequestsDeletedAssets(t *testing.T) {
	var seen []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/server/version" {
			_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
			return
		}
		var body struct {
			WithDeleted bool `json:"withDeleted"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		seen = append(seen, body.WithDeleted)
		_, _ = fmt.Fprint(w, `{"assets":{"count":1,"items":[`+photoJSON+`],"nextPage":null}}`)
	}))
	defer srv.Close()
	c, _ := immich.New(srv.URL, "secret")
	if _, err := c.Assets(t.Context(), bridge.Scope{Library: true, IncludeTrashed: true}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Assets(t.Context(), bridge.Scope{Library: true}, ""); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !seen[0] || seen[1] {
		t.Fatalf("withDeleted flags: %v", seen)
	}
}
