package immich_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
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
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				c, err := immich.New("https://immich.test", "secret")
				if err != nil {
					t.Fatal(err)
				}
				started := time.Now()
				_, err = c.Identity(ctx)
				var remote *bridge.Error
				if !errors.As(err, &remote) || remote.Code != bridge.Unavailable ||
					ctx.Err() != nil || time.Since(started) != 30*time.Second {
					t.Fatalf("metadata did not expire independently: %v, caller=%v, elapsed=%v", err, ctx.Err(), time.Since(started))
				}
			})
		})
	}
}

func TestOriginalTransferCanExceedMetadataDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := http.DefaultTransport
		defer func() { http.DefaultTransport = original }()
		http.DefaultTransport = deadlineTransport(func(r *http.Request) (*http.Response, error) {
			reader, writer := io.Pipe()
			go func() {
				defer func() { _ = writer.Close() }()
				select {
				case <-time.After(45 * time.Second):
					_, _ = io.Copy(writer, strings.NewReader("original bytes"))
				case <-r.Context().Done():
					_ = writer.CloseWithError(r.Context().Err())
				}
			}()
			return &http.Response{StatusCode: 200, Body: reader, Header: make(http.Header)}, nil
		})
		c, err := immich.New("https://immich.test", "secret")
		if err != nil {
			t.Fatal(err)
		}
		stream, err := c.Original(t.Context(), assetID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = stream.Close() }()
		data, err := io.ReadAll(stream)
		if err != nil || string(data) != "original bytes" {
			t.Fatalf("original incorrectly bounded by metadata deadline: %q %v", data, err)
		}
	})
}

func TestMetadataPreservesEarlierCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := http.DefaultTransport
		defer func() { http.DefaultTransport = original }()
		http.DefaultTransport = deadlineTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: stalledBody{r.Context()}, Header: make(http.Header)}, nil
		})
		c, err := immich.New("https://immich.test", "secret")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		started := time.Now()
		_, err = c.Identity(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 5*time.Second {
			t.Fatalf("caller deadline not preserved: %v elapsed=%v", err, time.Since(started))
		}
	})
}
