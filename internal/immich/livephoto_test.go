package immich_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
)

const companionID = "33333333-3333-4333-8333-333333333333"

func livePhotoPage(stillJSON, companionJSON string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server/version":
			_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
		case "/api/search/metadata":
			_, _ = fmt.Fprint(w, `{"assets":{"count":2,"items":[`+stillJSON+`,`+companionJSON+`],"nextPage":null}}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func stillJSON(link string) string {
	return `{"id":"` + assetID + `","type":"IMAGE","originalFileName":"live.heic",
		"originalMimeType":"image/heic","checksum":"qZk+NkcGgWq6PiVxeFDCbJzQ2J0=",
		"libraryId":null,"visibility":"timeline","isOffline":false,"isTrashed":false,
		"originalPath":"/immich/upload/library/live.heic","exifInfo":{"fileSizeInByte":3},
		"fileCreatedAt":"2026-10-01T12:00:00Z","fileModifiedAt":"2026-10-01T11:00:00Z",
		"livePhotoVideoId":` + link + `}`
}

func companionJSON(link string) string {
	return `{"id":"` + companionID + `","type":"VIDEO","originalFileName":"live.mov",
		"originalMimeType":"video/quicktime","checksum":"qZk+NkcGgWq6PiVxeFDCbJzQ2J0=",
		"libraryId":null,"visibility":"hidden","isOffline":false,"isTrashed":false,
		"originalPath":"/immich/upload/library/live.mov","exifInfo":{"fileSizeInByte":4},
		"fileCreatedAt":"2026-10-01T12:00:00Z","fileModifiedAt":"2026-10-01T11:00:00Z",
		"livePhotoVideoId":` + link + `}`
}

// The still carries the only directional link Immich exposes; the hidden MOV
// is its own asset with no companion of its own.
func TestLivePhotoCompanionLinkIsDecoded(t *testing.T) {
	srv := livePhotoPage(stillJSON(`"`+companionID+`"`), companionJSON("null"))
	defer srv.Close()
	c, err := immich.New(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.Assets(t.Context(), bridge.Scope{Library: true}, "")
	if err != nil || len(page.Assets) != 2 {
		t.Fatalf("page: %+v %v", page, err)
	}
	if page.Assets[0].LivePhotoID != companionID || page.Assets[0].LivePhotoVideo {
		t.Fatalf("still link: %+v", page.Assets[0])
	}
	if page.Assets[1].LivePhotoID != "" || !page.Assets[1].LivePhotoVideo ||
		page.Assets[1].ExcludedReason != "hidden" {
		t.Fatalf("companion: %+v", page.Assets[1])
	}
}

// A malformed or self-referential companion link is a contract violation, not
// an invitation to guess. A link reported on non-image media is ignored.
func TestMalformedLivePhotoLinkFailsClosed(t *testing.T) {
	for name, tt := range map[string]struct{ still, companion string }{
		"not-a-uuid": {stillJSON(`"not-a-uuid"`), companionJSON("null")},
		"self":       {stillJSON(`"` + assetID + `"`), companionJSON("null")},
	} {
		t.Run(name, func(t *testing.T) {
			srv := livePhotoPage(tt.still, tt.companion)
			defer srv.Close()
			c, err := immich.New(srv.URL, "secret")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Assets(t.Context(), bridge.Scope{Library: true}, ""); err == nil ||
				err.Error() != bridge.Protocol {
				t.Fatalf("malformed link accepted: %v", err)
			}
		})
	}
}

// A companion link reported on a video is not acted on: only a still owns a
// companion, and the video is uploaded on its own terms.
func TestCompanionLinkOnVideoIsIgnored(t *testing.T) {
	const other = "44444444-4444-4444-8444-444444444444"
	video := strings.Replace(companionJSON(`"`+other+`"`), `"id":"`+companionID+`"`, `"id":"`+assetID+`"`, 1)
	srv := livePhotoPage(video, companionJSON("null"))
	defer srv.Close()
	c, err := immich.New(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.Assets(t.Context(), bridge.Scope{Library: true}, "")
	if err != nil || len(page.Assets) != 2 || page.Assets[0].LivePhotoID != "" {
		t.Fatalf("video link was acted on: %+v %v", page, err)
	}
}
