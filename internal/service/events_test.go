package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/service"
)

func TestBackfillContinuesBoundedChunksWithoutWaitingForPeriodicInterval(t *testing.T) {
	amazonReads(t)
	finished := make(chan struct{})
	var once sync.Once
	cfg := remoteConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/albums/" + albumID:
			_, _ = fmt.Fprintf(w, `{"id":"%s","albumName":"backfill","assetCount":10}`, albumID)
		case "/api/search/metadata":
			var body struct {
				Page int `json:"page"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			next := fmt.Sprintf(`"%d"`, body.Page+1)
			if body.Page == 10 {
				next = "null"
				once.Do(func() { close(finished) })
			}
			_, _ = fmt.Fprintf(w, `{"assets":{"count":1,"items":[{"id":"%08d-3333-4333-8333-333333333333","type":"IMAGE","originalFileName":"hidden.jpg","libraryId":null,"checksum":"qZk+NkcGgWq6PiVxeFDCbJzQ2J0=","visibility":"hidden","isOffline":false,"isTrashed":false}],"nextPage":%s}}`, body.Page, next)
		default:
			sourceRead(w, r)
		}
	})
	cfg.ScanInterval = "15m"
	s, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("bounded backfill waited for the 15-minute interval")
	}
}

func TestWebhookAuthenticatesAndPersistsNativeHintsWithoutRemoteCalls(t *testing.T) {
	cfg := remoteConfig(t, func(http.ResponseWriter, *http.Request) { t.Error("handler called source") })
	secret := strings.Repeat("s", 32)
	cfg.WebhookSecretFile = filepath.Join(t.TempDir(), "webhook")
	if err := os.WriteFile(cfg.WebhookSecretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	// Neither remote credential is readable; accepting a hint is entirely local.
	cfg.Accounts[0].APIKeyFile, cfg.Accounts[0].CookieFile = "/missing/key", "/missing/cookie"
	s, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	const id = "33333333-3333-4333-8333-333333333333"
	native := `{"type":"AssetV1","trigger":"AssetCreate","config":{"url":"http://evil.invalid"},"workflow":{"id":"private-workflow","authToken":"private-token","stepId":"private-step"},"data":{"asset":{"id":"` + id + `","originalPath":"/private/path","url":"http://evil.invalid","ownerId":"private-owner"}}}`
	for _, tc := range []struct {
		body, token string
		code        int
	}{
		{native, "wrong", 401},
		{native, secret, 202},
		{`{"asset_id":"` + id + `"}`, secret, 202},
		{`{"asset_id":"../../etc/passwd"}`, secret, 400},
		{`{"asset_id":"` + id + `","url":"http://evil.invalid"}`, secret, 400},
		{native + `{}`, secret, 400},
		{`{"type":"AssetV1","trigger":"Unknown","data":{"asset":{"id":"` + id + `"}}}`, secret, 400},
		{strings.Repeat("x", 65537), secret, 400},
	} {
		r := httptest.NewRequest("POST", "/events/private-account", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("status=%d want=%d body=%s", w.Code, tc.code, w.Body.String())
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening and reading status proves the acknowledgement followed durable commit.
	cfg.Accounts[0].APIKeyFile = filepath.Join(t.TempDir(), "key")
	cfg.Accounts[0].CookieFile = filepath.Join(t.TempDir(), "cookie")
	if err := os.WriteFile(cfg.Accounts[0].APIKeyFile, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Accounts[0].CookieFile, []byte("session-id=x; ubid-main=x; at-main=x"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Status(t.Context(), "private-account")
	if err != nil || st.EventsPending != 1 {
		t.Fatalf("hints not durably coalesced: %+v %v", st, err)
	}
	if err := os.WriteFile(cfg.WebhookSecretFile, []byte(strings.Repeat("r", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/events/private-account", strings.NewReader(native))
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("rotated webhook secret accepted old token")
	}
}
