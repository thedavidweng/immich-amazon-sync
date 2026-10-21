package amazon_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/amazon"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type deadlineTransport func(*http.Request) (*http.Response, error)

func (f deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type stalledBody struct{ ctx context.Context }

func (b stalledBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (stalledBody) Close() error { return nil }

func TestMetadataDeadlineBoundsHeadersAndBody(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				original := http.DefaultTransport
				defer func() { http.DefaultTransport = original }()
				http.DefaultTransport = deadlineTransport(func(r *http.Request) (*http.Response, error) {
					if phase == "headers" {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return &http.Response{StatusCode: http.StatusOK, Body: stalledBody{r.Context()}, Header: make(http.Header)}, nil
				})
				rc, err := aphotos.ResolveRegion(aphotos.RegionUS)
				if err != nil {
					t.Fatal(err)
				}
				var cookies []string
				for _, key := range rc.RequiredCookies {
					cookies = append(cookies, key+"=test-secret")
				}
				c, err := amazon.New("us", strings.Join(cookies, "; "))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				started := time.Now()
				_, err = c.Probe(ctx)
				var remote *bridge.Error
				if !errors.As(err, &remote) || remote.Code != bridge.Unavailable ||
					ctx.Err() != nil || time.Since(started) != 30*time.Second {
					t.Fatalf("metadata did not expire independently: %v, caller=%v, elapsed=%v", err, ctx.Err(), time.Since(started))
				}
			})
		})
	}
}

func TestEachSDKMetadataOperationHasItsOwnDeadline(t *testing.T) {
	content := bridge.Content{Name: "photo.jpg", Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"}
	for _, tt := range []struct {
		name, method, path, code string
		call                     func(context.Context, *amazon.Client) error
	}{
		{"search", "GET", "/search", bridge.Unavailable, func(ctx context.Context, c *amazon.Client) error { _, err := c.Find(ctx, content); return err }},
		{"node", "GET", "/nodes/node-a", bridge.Unavailable, func(ctx context.Context, c *amazon.Client) error { _, err := c.GetNode(ctx, "node-a"); return err }},
		{"member-page", "GET", "/nodes/album-a/children", bridge.Unavailable, func(ctx context.Context, c *amazon.Client) error {
			_, err := c.AlbumMembers(ctx, "album-a")
			return err
		}},
		{"create", "POST", "/nodes", bridge.Unconfirmed, func(ctx context.Context, c *amazon.Client) error { _, err := c.CreateAlbum(ctx, "Family"); return err }},
		{"rename", "PATCH", "/nodes/album-a", bridge.Unconfirmed, func(ctx context.Context, c *amazon.Client) error { return c.RenameAlbum(ctx, "album-a", "Renamed") }},
		{"add", "PATCH", "/nodes/album-a/children", bridge.Unconfirmed, func(ctx context.Context, c *amazon.Client) error {
			return c.AddMembers(ctx, "album-a", []string{"node-a"})
		}},
		{"remove", "PATCH", "/nodes/album-a/children", bridge.Unconfirmed, func(ctx context.Context, c *amazon.Client) error {
			return c.RemoveMembers(ctx, "album-a", []string{"node-a"})
		}},
		{"upload-verification", "GET", "/nodes/node-a", bridge.Unconfirmed, func(ctx context.Context, c *amazon.Client) error {
			_, err := c.Upload(ctx, content, strings.NewReader("abc"))
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, phase := range []string{"headers", "body"} {
				t.Run(phase, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						transport := deadlineTransport(func(r *http.Request) (*http.Response, error) {
							if r.Method == tt.method && r.URL.Path == tt.path {
								if phase == "headers" {
									<-r.Context().Done()
									return nil, r.Context().Err()
								}
								return &http.Response{StatusCode: 200, Body: stalledBody{r.Context()}, Header: make(http.Header)}, nil
							}
							body := nodeJSON
							switch r.URL.Path {
							case "/nodes":
								body = `{"count":1,"data":[` + rootJSON + `]}`
							case "/nodes/album-a":
								body = albumJSON
							case "/upload":
								if _, err := io.Copy(io.Discard, r.Body); err != nil {
									t.Error(err)
								}
							}
							return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
						})
						c := newDeadlineClient(t, transport)
						ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
						defer cancel()
						started := time.Now()
						err := tt.call(ctx, c)
						var remote *bridge.Error
						if !errors.As(err, &remote) || remote.Code != tt.code || ctx.Err() != nil || time.Since(started) != 30*time.Second {
							t.Fatalf("metadata deadline: %v, caller=%v, elapsed=%v", err, ctx.Err(), time.Since(started))
						}
					})
				})
			}
		})
	}
}

func newDeadlineClient(t *testing.T, transport http.RoundTripper) *amazon.Client {
	t.Helper()
	rc, err := aphotos.ResolveRegion(aphotos.RegionUS)
	if err != nil {
		t.Fatal(err)
	}
	cookies := make(map[string]string)
	for _, key := range rc.RequiredCookies {
		cookies[key] = "test-secret"
	}
	sdk, err := aphotos.NewClient(aphotos.Options{
		Region: aphotos.RegionUS, Cookies: cookies, DisableRetries: true,
		DriveURL: "https://amazon.test", UploadURL: "https://amazon.test/upload",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := amazon.NewWithClient(sdk)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUploadTransferCanExceedMetadataDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newDeadlineClient(t, deadlineTransport(func(r *http.Request) (*http.Response, error) {
			body := nodeJSON
			switch r.URL.Path {
			case "/nodes":
				body = `{"count":1,"data":[` + rootJSON + `]}`
			case "/upload":
				select {
				case <-time.After(45 * time.Second):
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
				}
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}))
		content := bridge.Content{Name: "photo.jpg", Size: 3, MD5: "900150983cd24fb0d6963f7d28e17f72"}
		node, err := c.Upload(t.Context(), content, strings.NewReader("abc"))
		if err != nil || node.ID != "node-a" || node.Size != 3 || node.MD5 != content.MD5 {
			t.Fatalf("upload incorrectly bounded by metadata deadline: %+v %v", node, err)
		}
	})
}

func TestMetadataPreservesEarlierCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newDeadlineClient(t, deadlineTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: stalledBody{r.Context()}, Header: make(http.Header)}, nil
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		started := time.Now()
		_, err := c.Probe(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 5*time.Second {
			t.Fatalf("caller deadline not preserved: %v elapsed=%v", err, time.Since(started))
		}
	})
}
