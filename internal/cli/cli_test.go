package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/cli"
)

func TestHelpAndVersionNeedNoConfiguration(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://invalid.invalid")
	t.Setenv("IMMICH_API_KEY_FILE", "/does/not/exist")
	for _, args := range [][]string{
		{"--help"}, {"doctor", "--help"}, {"serve", "--help"},
		{"sync", "once", "--help"}, {"albums", "list", "--help"},
		{"version"}, {"--version"}, {"version", "--json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := cli.Execute(context.Background(), append([]string{"--config", "/missing"}, args...), &out, &errOut, "v1.2.3")
			if code != 0 || errOut.Len() != 0 || out.Len() == 0 {
				t.Fatalf("exit=%d out=%q stderr=%q", code, out.String(), errOut.String())
			}
			if strings.Contains(strings.Join(args, " "), "version") && !strings.Contains(out.String(), "v1.2.3") {
				t.Fatalf("missing version: %q", out.String())
			}
		})
	}
}

func TestAlbumsListWorksBeforeSelectingAlbumsOrConfiguringAmazon(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/albums" || r.Header.Get("x-api-key") != "source-secret" {
			t.Errorf("unexpected source request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`[{"id":"11111111-1111-4111-8111-111111111111","albumName":"Family","assetCount":7}]`))
	}))
	defer source.Close()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("source-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{"accounts": []map[string]any{
		{"id": "unselected", "immich_url": "http://invalid.invalid", "immich_api_key_file": "/missing"},
		{"id": "family", "immich_url": source.URL, "immich_api_key_file": key},
	}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	exit := cli.Execute(context.Background(), []string{"albums", "list", "--config", path, "--account", "family", "--json"}, &out, &errOut, "dev")
	var albums []struct {
		ID, Name string
		Count    int
	}
	if exit != 0 || json.Unmarshal(out.Bytes(), &albums) != nil || len(albums) != 1 || albums[0].Name != "Family" || albums[0].Count != 7 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatal("album listing opened local sync state")
	}
}

func TestUsageErrorsAreFiniteAndNeverEchoInput(t *testing.T) {
	for _, args := range [][]string{
		{"private-cookie-value"}, {"--token=private-cookie-value"},
		{"sync", "once", "private-cookie-value"}, {"serve", "--json=private-cookie-value"},
		{"albums", "private-cookie-value"}, {"sync"},
	} {
		var out, errOut bytes.Buffer
		code := cli.Execute(context.Background(), append([]string{"--json"}, args...), &out, &errOut, "dev")
		var result struct {
			Error struct{ Code string } `json:"error"`
		}
		if code != 2 || json.Unmarshal(errOut.Bytes(), &result) != nil || result.Error.Code != "usage" || out.Len() != 0 {
			t.Fatalf("args=%v exit=%d out=%q stderr=%q", args, code, out.String(), errOut.String())
		}
		if strings.Contains(errOut.String(), "private-cookie-value") {
			t.Fatal("CLI echoed a private argument")
		}
	}
}

func TestOperationalCommandsReturnSafeConfigurationErrors(t *testing.T) {
	for _, args := range [][]string{{"sync", "once"}, {"serve"}, {"doctor"}} {
		var out, errOut bytes.Buffer
		exit := cli.Execute(t.Context(), append(args, "--json", "--config", "/missing/private-secret"), &out, &errOut, "dev")
		if exit != 2 || out.Len() != 0 || errOut.String() != "{\"error\":{\"code\":\"config_unreadable\"}}\n" {
			t.Fatalf("args=%v exit=%d out=%q stderr=%q", args, exit, out.String(), errOut.String())
		}
	}
}

func TestServeSetupIsExplicitRequiresMountedAdminSecretAndNewConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMMICH_URL", "")
	t.Setenv("DATA_DIR", filepath.Join(dir, "state"))
	t.Setenv("LISTEN", "127.0.0.1:0")
	t.Setenv("ADMIN_PASSWORD_FILE", "")
	path := filepath.Join(dir, "config.json")
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	check := func(args []string, want int, code string) {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var out, errOut bytes.Buffer
		got := cli.Execute(ctx, append(args, "--json"), &out, &errOut, "dev")
		if got != want || (code != "" && !strings.Contains(errOut.String(), `"`+code+`"`)) {
			t.Fatalf("%v: exit=%d stderr=%q", args, got, errOut.String())
		}
	}
	check([]string{"serve", "--config", path}, 2, "config_unreadable")
	check([]string{"serve", "--setup"}, 2, "invalid_options")
	check([]string{"serve", "--setup", "--config", existing}, 2, "invalid_options")
	check([]string{"serve", "--setup", "--config", path}, 3, "credentials_required")
	adminFile := filepath.Join(dir, "admin")
	if err := os.WriteFile(adminFile, []byte("mounted-admin-passphrase"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_PASSWORD_FILE", adminFile)
	check([]string{"serve", "--setup", "--config", path, "--account", "family"}, 2, "invalid_options")
	t.Setenv("IMMICH_URL", "http://127.0.0.1:1")
	check([]string{"serve", "--setup", "--config", path}, 2, "invalid_options")
	t.Setenv("IMMICH_URL", "")
	check([]string{"serve", "--setup", "--config", path}, 0, "")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("setup wrote configuration before UI submission")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSyncOnceContinuesAfterAccountFailureAndReportsAggregateExit(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/users/me" && r.Header.Get("x-api-key") == "bad-secret":
			http.Error(w, "https://private.invalid/bad-secret", http.StatusUnauthorized)
		case r.URL.Path == "/api/users/me":
			_, _ = w.Write([]byte(`{"id":"22222222-2222-4222-8222-222222222222"}`))
		case r.URL.Path == "/api/server/version":
			_, _ = w.Write([]byte(`{"major":3,"minor":3,"patch":4}`))
		case r.URL.Path == "/api/albums/11111111-1111-4111-8111-111111111111":
			_, _ = w.Write([]byte(`{"id":"11111111-1111-4111-8111-111111111111","albumName":"Private","assetCount":0}`))
		case r.URL.Path == "/api/search/metadata":
			_, _ = w.Write([]byte(`{"assets":{"items":[],"nextPage":null,"count":0}}`))
		default:
			t.Errorf("unexpected Immich request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "127.0.0.1" {
			return original.RoundTrip(r)
		}
		body := `{"data":[{"id":"root","kind":"FOLDER","status":"AVAILABLE","isRoot":true,"ownerId":"owner"}],"count":1}`
		if strings.HasSuffix(r.URL.Path, "/account/usage") {
			body = `{"lastCalculated":"2026-10-20T08:30:00.000Z","photo":{"billable":{"bytes":1,"count":1},"total":{"bytes":1,"count":1}},"video":{"billable":{"bytes":0,"count":0},"total":{"bytes":0,"count":0}},"other":{"billable":{"bytes":0,"count":0},"total":{"bytes":0,"count":0}}}`
		} else if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/nodes") {
			t.Errorf("unexpected Amazon request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: 200, Request: r, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	defer func() { http.DefaultTransport = original }()
	dir := t.TempDir()
	key1, key2, cookie := filepath.Join(dir, "key1"), filepath.Join(dir, "key2"), filepath.Join(dir, "cookie")
	for path, data := range map[string]string{key1: "bad-secret", key2: "good-secret", cookie: "session-id=test; ubid-main=test; at-main=test"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var accounts []map[string]any
	for id, key := range map[string]string{"private-first": key1, "private-second": key2} {
		accounts = append(accounts, map[string]any{
			"id": id, "immich_url": source.URL, "immich_api_key_file": key,
			"amazon_cookie_file": cookie, "amazon_region": "us",
			"album_ids": []string{"11111111-1111-4111-8111-111111111111"},
		})
	}
	data, _ := json.Marshal(map[string]any{"data_dir": filepath.Join(dir, "state"), "accounts": accounts})
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	exit := cli.Execute(t.Context(), []string{"sync", "once", "--config", path, "--json"}, &out, &errOut, "dev")
	var report struct {
		Accounts []struct {
			Code   string `json:"code"`
			Status struct {
				LastSuccess *string `json:"last_success"`
			} `json:"status"`
		} `json:"accounts"`
	}
	if exit != 3 || json.Unmarshal(out.Bytes(), &report) != nil || len(report.Accounts) != 2 {
		t.Fatalf("exit=%d out=%q stderr=%q", exit, out.String(), errOut.String())
	}
	var healthy, failed bool
	for _, account := range report.Accounts {
		healthy = healthy || account.Code == "ok" && account.Status.LastSuccess != nil
		failed = failed || account.Code == "authentication_required"
	}
	if !healthy || !failed {
		t.Fatalf("one failure blocked the healthy account: %s", out.String())
	}
	for _, private := range []string{"bad-secret", "good-secret", "private-first", "private-second", source.URL, "private.invalid", "Private"} {
		if strings.Contains(out.String()+errOut.String(), private) {
			t.Fatalf("output leaked %q", private)
		}
	}
}

func TestDeletionsPlanIsReadOnlyAndReportsAnEmptyManifest(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	cookie := filepath.Join(dir, "cookie")
	for path, value := range map[string]string{key: "private-key", cookie: "session-id=1; ubid-acbca=2; at-acbca=3"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, "config.json")
	document := `{"data_dir":"` + dir + `/data","listen":"127.0.0.1:18080","scan_interval":"15m",
		"workers":1,"max_staging_bytes":1024,"max_staging_files":1,"log_level":"info",
		"accounts":[{"id":"home","immich_url":"http://immich.local","immich_api_key_file":"` + key + `",
		"amazon_region":"ca","amazon_cookie_file":"` + cookie + `","album_ids":["album"],
		"deletions":true,"deletions_confirmation":"ENABLE DELETIONS"}]}`
	if err := os.WriteFile(cfg, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(), []string{"--config", cfg, "deletions", "plan", "--json"}, &out, &errOut, "dev")
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	var report struct {
		Accounts []struct {
			Account string `json:"account"`
			Plan    struct {
				Pending int  `json:"pending"`
				Synced  int  `json:"synced"`
				Blocked bool `json:"blocked"`
			} `json:"plan"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Accounts) != 1 || report.Accounts[0].Plan.Pending != 0 ||
		report.Accounts[0].Plan.Synced != 0 || report.Accounts[0].Plan.Blocked {
		t.Fatalf("manifest: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "sync.sqlite")); err != nil {
		t.Fatalf("plan did not open local state read-only: %v", err)
	}
}

// CLI status exposes the favorites setting and its independent counters without
// mixing them into media states.
func TestSyncOnceStatusExposesFavorites(t *testing.T) {
	dir := t.TempDir()
	key, cookie := filepath.Join(dir, "key"), filepath.Join(dir, "cookie")
	for path, value := range map[string]string{
		key: "private-key", cookie: "session-id=test; ubid-main=test; at-main=test",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, "config.json")
	document := `{"data_dir":"` + dir + `/data","listen":"127.0.0.1:18081","scan_interval":"15m",
		"workers":1,"max_staging_bytes":1024,"max_staging_files":1,"log_level":"info",
		"accounts":[{"id":"home","immich_url":"http://127.0.0.1:1","immich_api_key_file":"` + key + `",
		"amazon_region":"us","amazon_cookie_file":"` + cookie + `","album_ids":["album"],
		"favorites":"add_only"}]}`
	if err := os.WriteFile(cfg, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	exit := cli.Execute(t.Context(), []string{"--config", cfg, "sync", "once", "--json"}, &out, &errOut, "dev")
	var report struct {
		Accounts []struct {
			Status struct {
				FavoritesMode string `json:"favorites_mode"`
				Desired       int    `json:"favorites_desired"`
				Pending       int    `json:"favorites_pending"`
				Applied       int    `json:"favorites_applied"`
				Failed        int    `json:"favorites_failed"`
				Uncertain     int    `json:"favorites_uncertain"`
				Unknown       int    `json:"favorites_unknown"`
				Unconfirmed   int    `json:"unconfirmed"`
				Confirmed     int    `json:"confirmed"`
			} `json:"status"`
		} `json:"accounts"`
	}
	if exit != 1 || json.Unmarshal(out.Bytes(), &report) != nil || len(report.Accounts) != 1 {
		t.Fatalf("exit=%d out=%q stderr=%q", exit, out.String(), errOut.String())
	}
	st := report.Accounts[0].Status
	if st.FavoritesMode != "add_only" || st.Desired != 0 || st.Pending != 0 || st.Applied != 0 ||
		st.Failed != 0 || st.Uncertain != 0 || st.Unknown != 0 {
		t.Fatalf("favorites state leaked into or out of media counts: %+v", st)
	}
	if st.Unconfirmed != 0 || st.Confirmed != 0 {
		t.Fatalf("media counts changed: %+v", st)
	}

	out.Reset()
	errOut.Reset()
	exit = cli.Execute(t.Context(), []string{"--config", cfg, "sync", "once"}, &out, &errOut, "dev")
	if exit != 1 || !strings.Contains(out.String(), "favorites_mode=add_only") ||
		!strings.Contains(out.String(), "favorites_applied=0") {
		t.Fatalf("human status lacks favorites: exit=%d out=%q stderr=%q", exit, out.String(), errOut.String())
	}
}

func TestFiltersCommandShowsEffectivePolicyWithoutRemoteCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	tag := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	body := `{"data_dir":"` + filepath.Join(dir, "state") + `","accounts":[{"id":"home","immich_url":"http://127.0.0.1:1","immich_api_key_file":"/missing","amazon_region":"us","amazon_cookie_file":"/missing","album_ids":["album-one"],"filters":{"media_types":["image","video"],"max_original_bytes":100,"skip_raw":true,"include_archived":false,"excluded_tags":["` + tag + `"]}}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	exit := cli.Execute(t.Context(), []string{"filters", "--config", path, "--json"}, &out, &errOut, "dev")
	var report struct {
		Accounts []struct {
			Account string `json:"account"`
			Filters struct {
				MediaTypes       []string `json:"media_types"`
				MaxOriginalBytes int64    `json:"max_original_bytes"`
				SkipRAW          bool     `json:"skip_raw"`
				IncludeArchived  bool     `json:"include_archived"`
				ExcludedTags     []string `json:"excluded_tags"`
			} `json:"filters"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), errOut.String())
	}
	if exit != 0 || len(report.Accounts) != 1 || report.Accounts[0].Account != "home" ||
		len(report.Accounts[0].Filters.MediaTypes) != 2 || report.Accounts[0].Filters.MaxOriginalBytes != 100 ||
		!report.Accounts[0].Filters.SkipRAW || report.Accounts[0].Filters.IncludeArchived ||
		len(report.Accounts[0].Filters.ExcludedTags) != 1 {
		t.Fatalf("effective policy: %+v exit=%d stderr=%q", report, exit, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatal("filters command opened local sync state")
	}

	out.Reset()
	exit = cli.Execute(t.Context(), []string{"filters", "--config", path}, &out, &errOut, "dev")
	if exit != 0 || !strings.Contains(out.String(), "media_types=image,video") ||
		!strings.Contains(out.String(), "skip_raw=true") {
		t.Fatalf("text policy: exit=%d stdout=%q stderr=%q", exit, out.String(), errOut.String())
	}
}

func TestFiltersCommandShowsLivePhotoModeWithoutRemoteCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"data_dir":"` + filepath.Join(dir, "state") + `","accounts":[{"id":"home","immich_url":"http://127.0.0.1:1","immich_api_key_file":"/missing","amazon_region":"us","amazon_cookie_file":"/missing","album_ids":["album-one"],"live_photo_mode":"original_pair"}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	exit := cli.Execute(t.Context(), []string{"filters", "--config", path, "--json"}, &out, &errOut, "dev")
	var report struct {
		Accounts []struct {
			Account       string `json:"account"`
			LivePhotoMode string `json:"live_photo_mode"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), errOut.String())
	}
	if exit != 0 || len(report.Accounts) != 1 || report.Accounts[0].LivePhotoMode != "original_pair" {
		t.Fatalf("effective live photo mode: %+v exit=%d stderr=%q", report, exit, errOut.String())
	}
	out.Reset()
	exit = cli.Execute(t.Context(), []string{"filters", "--config", path}, &out, &errOut, "dev")
	if exit != 0 || !strings.Contains(out.String(), "live_photo_mode=original_pair") {
		t.Fatalf("text live photo mode: exit=%d stdout=%q stderr=%q", exit, out.String(), errOut.String())
	}
}
