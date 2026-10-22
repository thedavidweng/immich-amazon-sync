package amazon_test

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

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/amazon"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

func newSDK(t *testing.T, region aphotos.Region, handler http.HandlerFunc) *aphotos.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	rc, err := aphotos.ResolveRegion(region)
	if err != nil {
		t.Fatal(err)
	}
	cookies := map[string]string{}
	for _, key := range rc.RequiredCookies {
		cookies[key] = "test-secret"
	}
	sdk, err := aphotos.NewClient(aphotos.Options{
		Region: region, Cookies: cookies, DriveURL: srv.URL, UploadURL: srv.URL + "/upload",
		DisableRetries: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sdk.Close(); err != nil {
			t.Error(err)
		}
	})
	return sdk
}

func newClient(t *testing.T, region aphotos.Region, handler http.HandlerFunc) *amazon.Client {
	t.Helper()
	sdk := newSDK(t, region, handler)
	c, err := amazon.NewWithClient(sdk)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetNodePreservesAuthenticationPermissionAndRetryAfter(t *testing.T) {
	for status, code := range map[int]string{
		401: bridge.Authentication, 403: bridge.Permission, 404: bridge.NotFound,
		409: bridge.Conflict, 429: bridge.RateLimit, 503: bridge.Unavailable,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var lookups atomic.Int32
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/nodes" {
					_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
					return
				}
				lookups.Add(1)
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"message":"cookie=private http://private/media"}`)
			})
			_, err := c.GetNode(t.Context(), "node-a")
			var remote *bridge.Error
			if !errors.As(err, &remote) || remote.Code != code || err.Error() != code || lookups.Load() != 1 {
				t.Fatalf("read error: %v, calls %d", err, lookups.Load())
			}
			if (status == 429 || status == 503) && remote.RetryAfter != 7*time.Second {
				t.Fatalf("lost RetryAfter: %v", remote.RetryAfter)
			}
		})
	}
}

func TestAmbiguousUploadNeverRepostsOrTrustsSuccessBodyAlone(t *testing.T) {
	for name, tt := range map[string]struct {
		status         int
		upload, remote string
	}{
		"server-error-after-body": {503, `{"message":"secret"}`, nodeJSON},
		"non-success-with-node":   {300, nodeJSON, nodeJSON},
		"malformed-response":      {200, `{`, nodeJSON},
		"missing-id":              {200, `{}`, nodeJSON},
		"remote-missing-size":     {200, nodeJSON, strings.ReplaceAll(nodeJSON, `,"size":3`, "")},
		"remote-wrong-md5":        {200, nodeJSON, strings.ReplaceAll(nodeJSON, "900150983cd24fb0d6963f7d28e17f72", "d41d8cd98f00b204e9800998ecf8427e")},
	} {
		t.Run(name, func(t *testing.T) {
			var posts atomic.Int32
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/nodes":
					_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
				case "/nodes/node-a":
					_, _ = fmt.Fprint(w, tt.remote)
				case "/upload":
					posts.Add(1)
					b, _ := io.ReadAll(r.Body)
					if string(b) != "abc" {
						t.Errorf("body = %q", b)
					}
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(tt.status)
					_, _ = fmt.Fprint(w, tt.upload)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
			})
			n, err := c.Upload(t.Context(), bridge.Content{Name: "photo.jpg", Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"}, strings.NewReader("abc"))
			var remote *bridge.Error
			if n.ID != "" || !errors.As(err, &remote) || remote.Code != bridge.Unconfirmed || posts.Load() != 1 {
				t.Fatalf("uncertain upload confirmed/replayed: %+v %v (%d)", n, err, posts.Load())
			}
			if tt.status == 503 && remote.RetryAfter != 7*time.Second {
				t.Fatalf("lost uncertain upload RetryAfter: %v", remote.RetryAfter)
			}
		})
	}
}

func TestPostUploadVerificationFailureCannotAuthorizeAnotherUpload(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/nodes":
					_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
				case "/upload":
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						t.Error(err)
					}
					_, _ = fmt.Fprint(w, nodeJSON)
				default:
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(status)
				}
			})
			_, err := c.Upload(t.Context(), bridge.Content{Name: "photo.jpg", Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"}, strings.NewReader("abc"))
			var remote *bridge.Error
			if !errors.As(err, &remote) || remote.Code != bridge.Unconfirmed {
				t.Fatalf("verification failure could cause re-upload: %v", err)
			}
			if (status == 429 || status == 503) && remote.RetryAfter != 7*time.Second {
				t.Fatal("lost verification RetryAfter")
			}
		})
	}
}

func TestAlbumMembershipRequiresCompleteStablePages(t *testing.T) {
	for _, mode := range []string{"complete", "count-drift", "count-decrease", "duplicate", "empty-last", "short-first", "short-last"} {
		t.Run(mode, func(t *testing.T) {
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/nodes":
					_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
					return
				case "/nodes/album-a":
					_, _ = fmt.Fprint(w, albumJSON)
					return
				}
				if r.URL.Path != "/nodes/album-a/children" || r.URL.Query().Get("limit") != "200" {
					t.Errorf("unbounded membership: %s", r.URL)
				}
				start, end, count := 0, 200, 201
				if mode == "short-first" {
					end = 1
				}
				if mode == "short-last" {
					count = 202
				}
				if r.URL.Query().Get("offset") == "200" {
					start, end = 200, 201
					switch mode {
					case "count-drift":
						count = 202
					case "count-decrease":
						count = 200
					case "duplicate":
						start, end = 0, 1
					case "empty-last":
						end = 200
					}
				} else if r.URL.Query().Get("offset") != "0" {
					t.Errorf("unexpected membership offset: %s", r.URL)
					return
				}
				nodes := []string{}
				for i := start; i < end; i++ {
					nodes = append(nodes, fmt.Sprintf(`{"id":"member-%d","kind":"FILE","status":"AVAILABLE","ownerId":"account-a"}`, i))
				}
				_, _ = fmt.Fprintf(w, `{"data":[%s],"count":%d}`, strings.Join(nodes, ","), count)
			})
			members, err := c.AlbumMembers(t.Context(), "album-a")
			if mode == "complete" {
				if err != nil || len(members) != 201 || members[200] != "member-200" {
					t.Fatalf("complete membership: %d %v", len(members), err)
				}
			} else if err == nil || len(members) != 0 {
				t.Fatalf("partial membership returned: %d %v", len(members), err)
			}
		})
	}
}

func TestExplicitEmptyAlbumHasNoMembers(t *testing.T) {
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes":
			_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
		case "/nodes/album-a":
			_, _ = fmt.Fprint(w, albumJSON)
		case "/nodes/album-a/children":
			_, _ = fmt.Fprint(w, `{"count":0,"data":[]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
		}
	})
	members, err := c.AlbumMembers(t.Context(), "album-a")
	if err != nil || len(members) != 0 {
		t.Fatalf("explicit empty membership: %+v %v", members, err)
	}
}

func TestCancellationAfterUploadDispatchIsUnconfirmed(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var posts atomic.Int32
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nodes" {
			_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
			return
		}
		posts.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		cancel()
		<-r.Context().Done()
	})
	_, err := c.Upload(ctx, bridge.Content{Name: "photo.jpg", Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"}, strings.NewReader("abc"))
	var remote *bridge.Error
	if !errors.As(err, &remote) || remote.Code != bridge.Unconfirmed || posts.Load() != 1 {
		t.Fatalf("canceled mutation: %v (%d posts)", err, posts.Load())
	}
}

func TestExplicitStorageFolderRetainsIdentity(t *testing.T) {
	const folder = `{"id":"folder-a","kind":"FOLDER","status":"AVAILABLE","ownerId":"account-a"}`
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes":
			_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
		case "/nodes/folder-a":
			_, _ = fmt.Fprint(w, folder)
		default:
			_, _ = fmt.Fprint(w, strings.ReplaceAll(nodeJSON, `["root-account-a"]`, `["folder-a"]`))
		}
	})
	n, err := c.GetNode(t.Context(), "node-a")
	if err != nil || n.ParentID != "folder-a" {
		t.Fatalf("folder node: %+v %v", n, err)
	}
}

func TestExplicitEmptySearchProvesAbsence(t *testing.T) {
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true" {
			_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
			return
		}
		if r.URL.Path != "/nodes" || r.URL.Query().Get("limit") != "200" || r.URL.Query().Get("offset") != "0" {
			t.Errorf("unbounded search: %s", r.URL)
		}
		_, _ = fmt.Fprint(w, `{"count":0,"data":[]}`)
	})
	nodes, err := c.Find(t.Context(), bridge.Content{Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"})
	if err != nil || len(nodes) != 0 {
		t.Fatalf("explicit empty search: %+v %v", nodes, err)
	}
}

func TestAmbiguousCandidatesAndIncompleteMembershipCannotProveAbsence(t *testing.T) {
	for name, payload := range map[string]string{
		"malformed-empty":    `{}`,
		"missing-count":      `{"data":[` + nodeJSON + `]}`,
		"null-count":         `{"data":[],"count":null}`,
		"missing-data":       `{"count":0}`,
		"null-data":          `{"data":null,"count":0}`,
		"empty-before-count": `{"data":[],"count":1}`,
		"premature-end":      `{"data":[` + nodeJSON + `],"count":201}`,
		"multiple":           `{"data":[` + nodeJSON + `,` + nodeJSON + `],"count":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true" {
					_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
					return
				}
				if r.URL.Path == "/nodes/album-a" {
					_, _ = fmt.Fprint(w, albumJSON)
					return
				}
				_, _ = fmt.Fprint(w, payload)
			})
			_, err := c.Find(t.Context(), bridge.Content{Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"})
			if err == nil {
				t.Fatal("ambiguous content confirmed")
			}
			_, err = c.AlbumMembers(t.Context(), "album-a")
			if err == nil {
				t.Fatal("incomplete membership certified")
			}
		})
	}
}

func TestMultipleVerifiedSearchMatchesRemainUnconfirmed(t *testing.T) {
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true" {
			_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"count":2,"data":[`+nodeJSON+`,`+strings.ReplaceAll(nodeJSON, "node-a", "node-b")+`]}`)
	})
	nodes, err := c.Find(t.Context(), bridge.Content{Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"})
	var remote *bridge.Error
	if len(nodes) != 0 || !errors.As(err, &remote) || remote.Code != bridge.Unconfirmed {
		t.Fatalf("ambiguous matching nodes: %+v %v", nodes, err)
	}
}

const nodeJSON = `{"id":"node-a","name":"photo.jpg","kind":"FILE","status":"AVAILABLE",
	"parents":["root-account-a"],"ownerId":"account-a",
	"contentProperties":{"md5":"900150983cd24fb0d6963f7d28e17f72","size":3}}`

func TestMediaConfirmationUsesRemoteDigestSizeStatusAndParent(t *testing.T) {
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true" {
			_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
			return
		}
		switch r.URL.Path {
		case "/nodes":
			filter := r.URL.Query().Get("filters")
			if !strings.Contains(filter, "900150983cd24fb0d6963f7d28e17f72") || !strings.Contains(filter, "root-account-a") {
				t.Errorf("unscoped digest search: %s", filter)
			}
			_, _ = fmt.Fprint(w, `{"data":[`+nodeJSON+`],"count":1}`)
		case "/nodes/node-a":
			_, _ = fmt.Fprint(w, nodeJSON)
		case "/upload":
			b, err := io.ReadAll(r.Body)
			if err != nil || string(b) != "abc" || r.URL.Query().Get("parentNodeId") != "root-account-a" {
				t.Errorf("upload = %q, %v; parent %q", b, err, r.URL.Query().Get("parentNodeId"))
			}
			_, _ = fmt.Fprint(w, nodeJSON)
		default:
			http.NotFound(w, r)
		}
	})
	content := bridge.Content{Name: "photo.jpg", Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72", MIME: "image/jpeg"}
	found, err := c.Find(t.Context(), content)
	if err != nil || len(found) != 1 || found[0].ID != "node-a" || found[0].ParentID != "" {
		t.Fatalf("found = %+v, %v", found, err)
	}
	uploaded, err := c.Upload(t.Context(), content, strings.NewReader("abc"))
	if err != nil || uploaded != found[0] {
		t.Fatalf("uploaded = %+v, %v", uploaded, err)
	}
	got, err := c.GetNode(t.Context(), "node-a")
	if err != nil || got != uploaded {
		t.Fatalf("node = %+v, %v", got, err)
	}
}

func TestFindDoesNotConfirmUnverifiableNodes(t *testing.T) {
	tests := map[string]string{
		"missing-md5":    strings.ReplaceAll(nodeJSON, `"md5":"900150983cd24fb0d6963f7d28e17f72",`, ""),
		"missing-size":   strings.ReplaceAll(nodeJSON, `,"size":3`, ""),
		"wrong-size":     strings.ReplaceAll(nodeJSON, `"size":3`, `"size":4`),
		"wrong-digest":   strings.ReplaceAll(nodeJSON, "900150983cd24fb0d6963f7d28e17f72", "d41d8cd98f00b204e9800998ecf8427e"),
		"trashed":        strings.ReplaceAll(nodeJSON, "AVAILABLE", "TRASH"),
		"missing-status": strings.ReplaceAll(nodeJSON, `"status":"AVAILABLE",`, ""),
		"wrong-kind":     strings.ReplaceAll(nodeJSON, `"kind":"FILE"`, `"kind":"FOLDER"`),
		"wrong-parent":   strings.ReplaceAll(nodeJSON, `["root-account-a"]`, `["other-root"]`),
		"missing-parent": strings.ReplaceAll(nodeJSON, `"parents":["root-account-a"],`, ""),
		"foreign-owner":  strings.ReplaceAll(nodeJSON, `"ownerId":"account-a"`, `"ownerId":"other-account"`),
		"missing-id":     strings.ReplaceAll(nodeJSON, `"id":"node-a",`, ""),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true" {
					_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":[`+payload+`],"count":1}`)
			})
			nodes, err := c.Find(t.Context(), bridge.Content{Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"})
			var remote *bridge.Error
			code := bridge.Unconfirmed
			if name == "missing-id" {
				code = bridge.Protocol
			}
			if len(nodes) != 0 || !errors.As(err, &remote) || remote.Code != code {
				t.Fatalf("unverifiable node confirmed: %+v %v", nodes, err)
			}
		})
	}
}

func TestEmptyFileStillRequiresAnExplicitValidRemoteSize(t *testing.T) {
	for name, size := range map[string]string{"zero": `0`, "numeric-string": `"0"`, "empty-string": `""`, "null": `null`} {
		t.Run(name, func(t *testing.T) {
			remote := strings.ReplaceAll(nodeJSON, "900150983cd24fb0d6963f7d28e17f72", "d41d8cd98f00b204e9800998ecf8427e")
			remote = strings.ReplaceAll(remote, `"size":3`, `"size":`+size)
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/nodes" && r.URL.Query().Get("filters") == "isRoot:true" {
					_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":[`+remote+`],"count":1}`)
			})
			nodes, err := c.Find(t.Context(), bridge.Content{Size: 0, MD5: "d41d8cd98f00b204e9800998ecf8427e"})
			if name == "zero" || name == "numeric-string" {
				if err != nil || len(nodes) != 1 || nodes[0].Size != 0 {
					t.Fatalf("empty file: %+v %v", nodes, err)
				}
			} else if err == nil {
				t.Fatal("missing size confirmed as zero")
			}
		})
	}
}

const rootJSON = `{"id":"root-account-a","isRoot":true,"kind":"FOLDER","status":"AVAILABLE","ownerId":"account-a"}`

func TestProbeReadsRemoteIdentityAndKeepsRegionEvidence(t *testing.T) {
	for _, region := range []aphotos.Region{aphotos.RegionUS, aphotos.RegionCA, aphotos.RegionJP} {
		t.Run(string(region), func(t *testing.T) {
			c := newClient(t, region, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/nodes" || r.URL.Query().Get("filters") != "isRoot:true" {
					t.Errorf("probe must only read root: %s %s", r.Method, r.URL)
				}
				_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
			})
			probe, err := c.Probe(t.Context())
			rc, _ := aphotos.ResolveRegion(region)
			if err != nil || probe.Identity != "root-account-a" ||
				probe.Region != string(region) || probe.Evidence != string(rc.Evidence) {
				t.Fatalf("probe = %+v, %v", probe, err)
			}
		})
	}
}

const albumJSON = `{"id":"album-a","name":"Family","kind":"VISUAL_COLLECTION","status":"AVAILABLE","ownerId":"account-a"}`

func TestAlbumOperationsUseStableIDAndNeverDeleteMedia(t *testing.T) {
	var creates atomic.Int32
	var renamed atomic.Bool
	c := newClient(t, aphotos.RegionCA, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/nodes" && r.Method == "GET":
			_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
		case r.URL.Path == "/nodes" && r.Method == "POST":
			creates.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["kind"] != "VISUAL_COLLECTION" || body["name"] != "Family" {
				t.Errorf("create payload: %+v", body)
			}
			_, _ = fmt.Fprint(w, albumJSON)
		case r.URL.Path == "/nodes/album-a" && (r.Method == "GET" || r.Method == "PATCH"):
			if r.Method == "PATCH" {
				renamed.Store(true)
			}
			if renamed.Load() {
				_, _ = fmt.Fprint(w, strings.ReplaceAll(albumJSON, "Family", "Renamed"))
			} else {
				_, _ = fmt.Fprint(w, albumJSON)
			}
		case r.URL.Path == "/nodes/node-a" && r.Method == "GET":
			_, _ = fmt.Fprint(w, nodeJSON)
		case r.URL.Path == "/nodes/album-a/children" && r.Method == "GET":
			_, _ = fmt.Fprint(w, `{"data":[`+nodeJSON+`],"count":1}`)
		case r.URL.Path == "/nodes/album-a/children" && r.Method == "PATCH":
			var body struct {
				Op    string
				Value []string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if (body.Op != "add" && body.Op != "remove") || len(body.Value) != 1 || body.Value[0] != "node-a" {
				t.Errorf("membership payload: %+v", body)
			}
			_, _ = fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected operation: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unsupported", 400)
		}
	})
	id, err := c.CreateAlbum(t.Context(), "Family")
	if err != nil || id != "album-a" {
		t.Fatalf("created = %q, %v", id, err)
	}
	if err := c.RenameAlbum(t.Context(), id, "Renamed"); err != nil {
		t.Fatal(err)
	}
	members, err := c.AlbumMembers(t.Context(), id)
	if err != nil || len(members) != 1 || members[0] != "node-a" {
		t.Fatalf("members = %+v, %v", members, err)
	}
	if err := c.AddMembers(t.Context(), id, []string{"node-a"}); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveMembers(t.Context(), id, []string{"node-a"}); err != nil {
		t.Fatal(err)
	}
	if creates.Load() != 1 {
		t.Fatalf("album recreated %d times", creates.Load())
	}
}

func TestUncertainAlbumCreationNeverAdoptsCollision(t *testing.T) {
	for _, status := range []int{409, 500, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var creates atomic.Int32
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					creates.Add(1)
					w.WriteHeader(status)
					_, _ = fmt.Fprint(w, `{"message":"private cookie=secret","id":"collision"}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
			})
			id, err := c.CreateAlbum(t.Context(), "Family")
			var remote *bridge.Error
			if id != "" || !errors.As(err, &remote) || remote.Code != bridge.Unconfirmed || creates.Load() != 1 {
				t.Fatalf("uncertain create adopted/replayed: %q %v (%d)", id, err, creates.Load())
			}
		})
	}
}
