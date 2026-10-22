package service_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/cli"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/service"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// ledgerSource is a minimal bridge.Source used only to seed durable state.
type ledgerSource struct {
	bridge.Source
	mu     sync.Mutex
	assets []bridge.Asset
	data   map[string]string
}

func (s *ledgerSource) Identity(context.Context) (string, error) { return "source", nil }
func (s *ledgerSource) Albums(context.Context) ([]bridge.Album, error) {
	return nil, nil
}
func (s *ledgerSource) Album(_ context.Context, id string) (bridge.Album, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bridge.Album{ID: id, Count: len(s.assets)}, nil
}
func (s *ledgerSource) Assets(_ context.Context, _ bridge.Scope, cursor string) (bridge.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cursor != "" {
		return bridge.Page{}, nil
	}
	return bridge.Page{Assets: append([]bridge.Asset(nil), s.assets...)}, nil
}
func (s *ledgerSource) Asset(_ context.Context, id string) (bridge.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, asset := range s.assets {
		if asset.ID == id {
			return asset, nil
		}
	}
	return bridge.Asset{}, &bridge.Error{Code: bridge.NotFound}
}
func (s *ledgerSource) Original(_ context.Context, id string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return io.NopCloser(strings.NewReader(s.data[id])), nil
}

// ledgerTarget is a minimal verified target; it never contacts a network.
type ledgerTarget struct {
	bridge.Target
	mu    sync.Mutex
	nodes []bridge.Node
}

func (t *ledgerTarget) Probe(context.Context) (bridge.Capabilities, error) {
	return bridge.Capabilities{Identity: "target", Region: "us"}, nil
}
func (t *ledgerTarget) Usage(context.Context) (bridge.Usage, error) {
	return bridge.Usage{}, nil
}
func (t *ledgerTarget) Find(_ context.Context, c bridge.Content) ([]bridge.Node, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var nodes []bridge.Node
	for _, node := range t.nodes {
		if node.ParentID == c.ParentID && node.MD5 == c.MD5 && node.Size == c.Size {
			nodes = append(nodes, node)
		}
	}
	return nodes, nil
}
func (t *ledgerTarget) FindExisting(_ context.Context, c bridge.Content) (bridge.Node, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, node := range t.nodes {
		if node.MD5 == c.MD5 && node.Size == c.Size && node.ID != "" {
			return node, nil
		}
	}
	return bridge.Node{}, nil
}
func (t *ledgerTarget) Upload(_ context.Context, c bridge.Content, r io.Reader) (bridge.Node, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return bridge.Node{}, err
	}
	sum := md5.Sum(b)
	t.mu.Lock()
	defer t.mu.Unlock()
	node := bridge.Node{ID: fmt.Sprint(len(t.nodes) + 1), Name: c.Name,
		MD5: hex.EncodeToString(sum[:]), Size: int64(len(b)), ParentID: c.ParentID}
	t.nodes = append(t.nodes, node)
	return node, nil
}
func (t *ledgerTarget) GetNode(_ context.Context, id string) (bridge.Node, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, node := range t.nodes {
		if node.ID == id {
			return node, nil
		}
	}
	return bridge.Node{}, &bridge.Error{Code: bridge.NotFound}
}
func (t *ledgerTarget) Trash(_ context.Context, c bridge.Content, id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.nodes {
		if t.nodes[i].ID == id {
			if t.nodes[i].MD5 != c.MD5 || t.nodes[i].Size != c.Size {
				return &bridge.Error{Code: bridge.Unconfirmed}
			}
			t.nodes[i].MD5 = ""
			return nil
		}
	}
	return &bridge.Error{Code: bridge.NotFound}
}

func ledgerAssets(count int) []bridge.Asset {
	assets := make([]bridge.Asset, 0, count)
	names := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven"}
	for i := range count {
		assets = append(assets, bridge.Asset{ID: "asset-private-" + names[i], Name: "private-" + names[i] + ".jpg",
			MIME: "image/jpeg", Kind: "IMAGE", Size: 5, SHA1: "qvTGHdzF6KLavt4PO0gs2a6pQ00="})
	}
	return assets
}

// seedLedger writes durable confirmed state, then trashes one source asset so
// the deletion-on and deletion-off cases can be compared. The returned engine
// is closed and the directory is safe to open as a Service.
func seedLedger(t *testing.T, dir string, deletions bool) {
	t.Helper()
	assets := ledgerAssets(11)
	source := &ledgerSource{assets: assets, data: map[string]string{}}
	for _, asset := range assets {
		source.data[asset.ID] = "hello"
	}
	target := &ledgerTarget{}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := syncer.New(store, source, target, syncer.Options{
		AccountID: "private-account", AlbumIDs: []string{albumID}, Workers: 1,
		MaxStagingBytes: 1 << 20, MaxStagingFiles: 2, Deletions: deletions,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := engine.Once(ctx); err != nil {
		store.Close()
		t.Fatal(err)
	}
	source.mu.Lock()
	trashed := source.assets[0]
	trashed.ExcludedReason = "trashed"
	source.assets[0] = trashed
	source.mu.Unlock()
	if err := engine.Once(ctx); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// metricsConfig builds a valid service config for the seeded ledger.
func metricsConfig(t *testing.T, dir, listen string, deletions bool) config.Config {
	t.Helper()
	key := filepath.Join(dir, "key")
	cookie := filepath.Join(dir, "cookie")
	if err := os.WriteFile(key, []byte("private-source-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookie, []byte("session-id=private; ubid-main=private; at-main=private"), 0o600); err != nil {
		t.Fatal(err)
	}
	account := config.Account{
		ID: "private-account", ImmichURL: "http://127.0.0.1:1", APIKeyFile: key,
		AmazonRegion: "us", CookieFile: cookie, AlbumIDs: []string{albumID},
		Deletions: deletions,
	}
	if deletions {
		account.DeletionConfirmation = config.DeletionConfirmationPhrase
	}
	admin := filepath.Join(dir, "admin")
	if err := os.WriteFile(admin, []byte("offline-admin-passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Config{
		DataDir: filepath.Join(dir, "state"), Listen: listen, ScanInterval: "15m",
		Workers: 1, MaxStagingBytes: 1 << 20, MaxStagingFiles: 2, LogLevel: "info",
		Metrics: true, AdminPasswordFile: admin, Accounts: []config.Account{account},
	}
}

func TestMetricsAreOffByDefault(t *testing.T) {
	cfg := setupConfig(t)
	runtime, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()
	for _, path := range []string{"/metrics", "/status"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while disabled", path, response.StatusCode)
		}
	}
}

func TestMetricsAndStatusEndpointsAgreeWithCLIAndUI(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, filepath.Join(dir, "state"), true)
	cfg := metricsConfig(t, dir, "127.0.0.1:0", true)
	runtime, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("metrics status=%d err=%v", response.StatusCode, err)
	}
	text := string(body)
	if !strings.Contains(response.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("content type = %q", response.Header.Get("Content-Type"))
	}
	hash := service.AccountHash("private-account")
	for _, want := range []string{
		`immich_amazon_sync_media{account="` + hash + `",state="confirmed"} 10`,
		`immich_amazon_sync_media{account="` + hash + `",state="deleted"} 1`,
		`immich_amazon_sync_media{account="` + hash + `",state="deletion_pending"} 0`,
		`immich_amazon_sync_transfer_window_open{account="` + hash + `"} 1`,
		`immich_amazon_sync_live_photo_pairs{account="` + hash + `",state="complete"} 0`,
		`immich_amazon_sync_usage_hold{account="` + hash + `",kind="quota"} 0`,
		`immich_amazon_sync_source_bytes_total{account="` + hash + `",mode="api"} 0`,
		`immich_amazon_sync_workflow{account="` + hash + `",state="disabled",code="none"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in metrics:\n%s", want, text)
		}
	}
	for _, secret := range []string{"private-account", "private-source-secret", "ubid-main",
		"private-one.jpg", "asset-private-one", "127.0.0.1:1"} {
		if strings.Contains(text, secret) {
			t.Fatalf("metrics leaked private value %q", secret)
		}
	}

	statusResponse, err := http.Get(server.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var httpReport service.Report
	if err := json.NewDecoder(statusResponse.Body).Decode(&httpReport); err != nil {
		t.Fatal(err)
	}
	statusResponse.Body.Close()
	localReport, err := runtime.StatusReport(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(httpReport) != fmt.Sprint(localReport) {
		t.Fatalf("HTTP status differs from the durable report:\n%+v\n%+v", httpReport, localReport)
	}
	if len(httpReport.Accounts) != 1 || httpReport.Accounts[0].Code != "ok" ||
		httpReport.Accounts[0].Status.Confirmed != 10 || httpReport.Accounts[0].Status.Deleted != 1 {
		t.Fatalf("unexpected durable counts: %+v", httpReport)
	}

	// The dashboard renders the same Status structure.
	if err := runtime.EnableAdmin(""); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	loginPage, err := client.Get(server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	loginBody, _ := io.ReadAll(loginPage.Body)
	loginPage.Body.Close()
	match := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(string(loginBody))
	if len(match) != 2 {
		t.Fatalf("login page has no CSRF token: %s", loginBody)
	}
	login, err := client.PostForm(server.URL+"/login", url.Values{
		"csrf": {match[1]}, "password": {"offline-admin-passphrase"},
	})
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("dashboard login = %d", login.StatusCode)
	}
	uiResponse, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	uiBody, _ := io.ReadAll(uiResponse.Body)
	uiResponse.Body.Close()
	for _, want := range []string{"<dt>Confirmed</dt><dd>10</dd>", "<dt>In Amazon trash</dt><dd>1</dd>",
		"Retry tuning: 5 attempts", "<dt>Workflow</dt><dd>not requested</dd>"} {
		if !strings.Contains(string(uiBody), want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}
	server.Close()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}

	// The CLI reads only the same ledger; no remote is contacted.
	path := filepath.Join(dir, "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := cli.Execute(t.Context(), []string{"--config", path, "status", "--json"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("cli status exit=%d stderr=%s", code, errOut.String())
	}
	var cliReport service.Report
	if err := json.Unmarshal(out.Bytes(), &cliReport); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(cliReport) != fmt.Sprint(localReport) {
		t.Fatalf("CLI status differs:\n%+v\n%+v", cliReport, localReport)
	}
	if !strings.Contains(out.String(), hash) || strings.Contains(out.String(), "private-account") {
		t.Fatalf("CLI status leaked or lost the account identity: %s", out.String())
	}
}

func TestMetricsDeletionDisabledNeverReportsDeletionEvidence(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, filepath.Join(dir, "state"), false)
	cfg := metricsConfig(t, dir, "127.0.0.1:0", false)
	runtime, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()
	report, err := runtime.StatusReport(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	st := report.Accounts[0].Status
	if st.Deleted != 0 || st.DeletePending != 0 || st.Confirmed != 11 {
		t.Fatalf("disabled deletions changed durable status: %+v", st)
	}
	response, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `state="deleted"} 0`) {
		t.Fatalf("disabled deletions reported deletion evidence: %s", body)
	}
}

func TestMetricsEndpointRequiresExplicitAuthentication(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, filepath.Join(dir, "state"), false)
	cfg := metricsConfig(t, dir, "0.0.0.0:0", false)
	token := filepath.Join(dir, "metrics-token")
	if err := os.WriteFile(token, []byte("private-metrics-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.MetricsTokenFile = token
	runtime, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()

	for _, header := range []string{"", "Bearer wrong-token"} {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/metrics", nil)
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("header %q status = %d, want 401", header, response.StatusCode)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/metrics", nil)
	request.Header.Set("Authorization", "Bearer private-metrics-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "immich_amazon_sync_media") {
		t.Fatalf("configured token rejected: status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(string(body), "private-metrics-token") {
		t.Fatal("metrics exposed the bearer token")
	}
}

func TestHealthAndReadinessDoNotDependOnAmazon(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	cookie := filepath.Join(dir, "cookie")
	for _, path := range []string{key, cookie} {
		if err := os.WriteFile(path, []byte("mounted-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := setupConfig(t)
	// Unreachable remotes: Open performs only local validation and no network
	// access, and the probes must stay independent of Amazon availability.
	cfg.Accounts = []config.Account{{
		ID: "offline", ImmichURL: "http://127.0.0.1:1", APIKeyFile: key,
		AmazonRegion: "us", CookieFile: cookie, AlbumIDs: []string{albumID},
	}}
	runtime, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()
	for _, path := range []string{"/healthz", "/readyz"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d with an unavailable remote", path, response.StatusCode)
		}
	}
}
