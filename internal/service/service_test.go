package service_test

import (
	"bytes"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/service"
)

func setupConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = filepath.Join(t.TempDir(), "state")
	cfg.Listen = "127.0.0.1:0"
	cfg.AdminPasswordFile = "/not/read/by/health/probes"
	cfg.Accounts = nil
	return cfg
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const albumID = "11111111-1111-4111-8111-111111111111"

func remoteConfig(t *testing.T, handle http.HandlerFunc) config.Config {
	t.Helper()
	source := httptest.NewServer(handle)
	t.Cleanup(source.Close)
	cfg := setupConfig(t)
	key := filepath.Join(t.TempDir(), "key")
	cookie := filepath.Join(t.TempDir(), "cookie")
	if err := os.WriteFile(key, []byte("source-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookie, []byte("session-id=test; ubid-main=test; at-main=test"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = []config.Account{{
		ID: "private-account", ImmichURL: source.URL, APIKeyFile: key,
		AmazonRegion: "us", CookieFile: cookie, AlbumIDs: []string{albumID},
	}}
	return cfg
}

func amazonReads(t *testing.T) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "127.0.0.1" {
			return original.RoundTrip(r)
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/nodes") || r.URL.Query().Get("filters") != "isRoot:true" {
			t.Errorf("doctor mutated Amazon or accessed unrelated endpoint: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: 200, Header: make(http.Header), Request: r,
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"root","isRoot":true,"kind":"FOLDER","status":"AVAILABLE","ownerId":"owner"}],"count":1}`)),
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func sourceRead(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/users/me":
		fmt.Fprint(w, `{"id":"22222222-2222-4222-8222-222222222222"}`)
	case "/api/server/version":
		fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
	case "/api/albums":
		fmt.Fprint(w, `[{"id":"`+albumID+`","albumName":"Private album","assetCount":0}]`)
	case "/api/albums/" + albumID:
		fmt.Fprint(w, `{"id":"`+albumID+`","albumName":"Private album","assetCount":0}`)
	case "/api/search/metadata":
		fmt.Fprint(w, `{"assets":{"count":0,"items":[],"nextPage":null}}`)
	default:
		http.NotFound(w, r)
	}
}

func TestDoctorProbesReadOnlyWithoutClaimingWritePermissions(t *testing.T) {
	amazonReads(t)
	cfg := remoteConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !(r.Method == http.MethodPost && r.URL.Path == "/api/search/metadata") {
			t.Errorf("doctor made a source mutation: %s %s", r.Method, r.URL.Path)
		}
		sourceRead(w, r)
	})
	report, err := service.Diagnose(t.Context(), cfg, "")
	if err != nil {
		t.Fatalf("doctor: %v; report=%+v", err, report)
	}
	data, _ := json.Marshal(report)
	text := string(data)
	if !strings.Contains(text, `"version":"3.3.4"`) || !strings.Contains(text, `"target_writes":"untested"`) || !strings.Contains(text, `"code":"ok"`) {
		t.Fatalf("missing read/write evidence: %s", text)
	}
	for _, private := range []string{"private-account", "source-secret", "Private album", cfg.Accounts[0].ImmichURL, "source-user", `"identity"`} {
		if strings.Contains(text, private) {
			t.Fatalf("doctor leaked %q: %s", private, text)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "sync.sqlite")); !os.IsNotExist(err) {
		t.Fatal("doctor created business state")
	}
}

func TestAuthPauseRecoversAfterSecretRotationAndRestartButManualPauseStays(t *testing.T) {
	amazonReads(t)
	var probes atomic.Int32
	var blocked atomic.Bool
	blocked.Store(true)
	cfg := remoteConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/users/me" {
			probes.Add(1)
			if blocked.Load() {
				http.Error(w, "source-secret must never be logged", http.StatusUnauthorized)
				return
			}
		}
		sourceRead(w, r)
	})
	s, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.Once(t.Context(), "")
	if service.ExitCode(err) != 3 || report.Accounts[0].Status.PauseReason != "auth" {
		t.Fatalf("expected auth pause: %+v %v", report, err)
	}
	s.Once(t.Context(), "")
	if probes.Load() != 1 {
		t.Fatal("unchanged invalid credentials were probed again")
	}
	if err := os.WriteFile(cfg.Accounts[0].CookieFile, []byte("session-id=rotated; ubid-main=test; at-main=test"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked.Store(false)
	if _, err := s.Once(t.Context(), ""); err != nil {
		t.Fatal("rotated cookie did not resume auth pause:", err)
	}
	if err := s.Pause(t.Context(), cfg.Accounts[0].ID, true); err != nil {
		t.Fatal(err)
	}
	before := probes.Load()
	s.Close()
	s, err = service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	s.Once(t.Context(), "")
	if probes.Load() != before {
		t.Fatal("restart resumed a manual pause")
	}
	if err := s.Pause(t.Context(), cfg.Accounts[0].ID, false); err != nil {
		t.Fatal(err)
	}
	blocked.Store(true)
	s.Once(t.Context(), "")
	s.Close()
	if err := os.WriteFile(cfg.Accounts[0].CookieFile, []byte("session-id=restart-rotated; ubid-main=test; at-main=test"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked.Store(false)
	s, err = service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	report, err = s.Once(t.Context(), "")
	if err != nil || report.Accounts[0].Status.Paused {
		t.Fatalf("updated cookie plus restart did not resume: %+v %v", report, err)
	}
}

func TestHealthIsProcessAndReadinessIsOnlyLocalDatabase(t *testing.T) {
	cfg := setupConfig(t)
	s, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "{\"status\":\"ok\"}\n" {
			t.Fatalf("%s: status=%d body=%q", path, resp.StatusCode, body)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]int{"/healthz": 200, "/readyz": 503, "/api/status": 404} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != expected {
			t.Fatalf("%s: status=%d want=%d", path, resp.StatusCode, expected)
		}
	}
}

func TestRunStopsHTTPAndReleasesStoreOnCancellation(t *testing.T) {
	cfg := setupConfig(t)
	s, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal("writer lock not released:", err)
	}
	reopened.Close()
}

func TestOnceReportsPersistedBlockedWorkAsFailure(t *testing.T) {
	amazonReads(t)
	const assetID = "33333333-3333-4333-8333-333333333333"
	const photo = `{"id":"` + assetID + `","type":"IMAGE","originalFileName":"../private.jpg",
		"originalMimeType":"image/jpeg","checksum":"qZk+NkcGgWq6PiVxeFDCbJzQ2J0=",
		"libraryId":null,"visibility":"timeline","isOffline":false,"isTrashed":false,
		"exifInfo":{"fileSizeInByte":3},"fileCreatedAt":"2026-10-01T12:00:00Z",
		"updatedAt":"2026-10-02T12:00:00Z"}`
	cfg := remoteConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/albums/" + albumID:
			fmt.Fprint(w, `{"id":"`+albumID+`","albumName":"private","assetCount":1}`)
		case "/api/search/metadata":
			fmt.Fprint(w, `{"assets":{"count":1,"items":[`+photo+`],"nextPage":null}}`)
		default:
			sourceRead(w, r)
		}
	})
	s, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for cycle := range 2 {
		report, err := s.Once(t.Context(), "")
		if service.ExitCode(err) != 1 || report.Accounts[0].Code != "unsafe_filename" || report.Accounts[0].Status.Blocked != 1 {
			t.Fatalf("cycle %d reported blocked work as success: %+v %v", cycle, report, err)
		}
	}
}

func TestSchedulingDoesNotCancelSlowOriginalOrBlockOtherAccounts(t *testing.T) {
	amazonReads(t)
	started, transferCanceled := make(chan struct{}), make(chan struct{})
	healthyScanned := make(chan struct{})
	var startedOnce, healthyOnce sync.Once
	const assetID = "33333333-3333-4333-8333-333333333333"
	const photo = `{"id":"` + assetID + `","type":"IMAGE","originalFileName":"private.jpg",
		"originalMimeType":"image/jpeg","checksum":"qZk+NkcGgWq6PiVxeFDCbJzQ2J0=",
		"libraryId":null,"visibility":"timeline","isOffline":false,"isTrashed":false,
		"exifInfo":{"fileSizeInByte":3},"fileCreatedAt":"2026-10-01T12:00:00Z",
		"updatedAt":"2026-10-02T12:00:00Z"}`
	cfg := remoteConfig(t, func(w http.ResponseWriter, r *http.Request) {
		slow := r.Header.Get("x-api-key") == "source-secret"
		switch {
		case slow && r.URL.Path == "/api/albums/"+albumID:
			fmt.Fprint(w, `{"id":"`+albumID+`","albumName":"private","assetCount":1}`)
		case r.URL.Path == "/api/search/metadata" && slow:
			fmt.Fprint(w, `{"assets":{"count":1,"items":[`+photo+`],"nextPage":null}}`)
		case r.URL.Path == "/api/assets/"+assetID:
			fmt.Fprint(w, photo)
		case r.URL.Path == "/api/assets/"+assetID+"/original":
			w.Header().Set("Content-Length", "3")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			startedOnce.Do(func() { close(started) })
			<-r.Context().Done()
			close(transferCanceled)
		default:
			if !slow && r.URL.Path == "/api/search/metadata" {
				healthyOnce.Do(func() { close(healthyScanned) })
			}
			sourceRead(w, r)
		}
	})
	cfg.ScanInterval = "1s"
	key := filepath.Join(t.TempDir(), "healthy-key")
	if err := os.WriteFile(key, []byte("healthy-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	healthy := cfg.Accounts[0]
	healthy.ID, healthy.APIKeyFile = "healthy-account", key
	cfg.Accounts = append(cfg.Accounts, healthy)
	var logs bytes.Buffer
	s, err := service.Open(cfg, &logs)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	for _, signal := range []<-chan struct{}{started, healthyScanned} {
		select {
		case <-signal:
		case <-time.After(3 * time.Second):
			t.Fatal("one slow account blocked source work")
		}
	}
	select {
	case <-transferCanceled:
		t.Fatal("transfer was canceled at the scan interval")
	case <-time.After(1200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel transfer")
	}
	select {
	case <-transferCanceled:
	case <-time.After(time.Second):
		t.Fatal("remote request did not observe cancellation")
	}
	text := logs.String()
	for _, field := range []string{`"account"`, `"stage"`, `"elapsed_ms"`, `"error_code"`, `"counts"`, `"last_reconcile"`} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing structured log field %s: %s", field, text)
		}
	}
	for _, private := range []string{"private-account", "healthy-account", "source-secret", "healthy-secret", "private.jpg", cfg.Accounts[0].ImmichURL} {
		if strings.Contains(text, private) {
			t.Fatalf("logs leaked %q", private)
		}
	}
}
