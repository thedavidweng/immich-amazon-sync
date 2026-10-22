package admin_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/service"
)

const password = "offline-admin-passphrase"

type dashboard struct {
	server *httptest.Server
	client *http.Client
	cfg    config.Config
	path   string
	run    *service.Service
}

func openDashboard(t *testing.T) dashboard {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.DataDir, cfg.Listen = filepath.Join(dir, "state"), "127.0.0.1:0"
	cfg.AdminPasswordFile = filepath.Join(dir, "admin")
	cfg.Accounts = nil
	if err := os.WriteFile(cfg.AdminPasswordFile, []byte(password), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Close() })
	path := filepath.Join(dir, "config.json")
	if err := run.EnableAdmin(path); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(run.Handler())
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	return dashboard{server, &http.Client{Jar: jar}, cfg, path, run}
}

func page(t *testing.T, response *http.Response, err error) (int, string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}

func (d dashboard) get(t *testing.T, path string) (int, string) {
	t.Helper()
	response, err := d.client.Get(d.server.URL + path)
	return page(t, response, err)
}

func token(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing CSRF token: %s", body)
	}
	return match[1]
}

func (d dashboard) post(t *testing.T, path string, form url.Values) (int, string) {
	t.Helper()
	response, err := d.client.PostForm(d.server.URL+path, form)
	return page(t, response, err)
}

func (d dashboard) login(t *testing.T) string {
	t.Helper()
	_, body := d.get(t, "/login")
	status, body := d.post(t, "/login", url.Values{"csrf": {token(t, body)}, "password": {password}})
	if status != 200 || !strings.Contains(body, "Backup control") {
		t.Fatalf("login: status=%d body=%s", status, body)
	}
	return token(t, body)
}

func TestDashboardReferrerPolicyPreservesSameOriginForms(t *testing.T) {
	d := openDashboard(t)
	response, err := d.client.Get(d.server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	policy := response.Header.Get("Referrer-Policy")
	page(t, response, nil)
	if policy != "same-origin" {
		t.Fatalf("Referrer-Policy=%q, want same-origin for native form POST origins", policy)
	}
}

func TestDashboardAcceptsSameOriginNavigationForms(t *testing.T) {
	d := openDashboard(t)
	d.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			r.Header.Set("Origin", d.server.URL)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "navigate")
			r.Header.Set("Sec-Fetch-Dest", "document")
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	csrf := d.login(t)
	status, body := d.post(t, "/logout", url.Values{"csrf": {csrf}})
	if status != http.StatusOK || !strings.Contains(body, "Sign in") {
		t.Fatalf("same-origin logout: %d %s", status, body)
	}
}

func TestDashboardRequiresSessionAndCSRFAndRevokesOnLogout(t *testing.T) {
	d := openDashboard(t)
	response, err := http.Get(d.server.URL + "/")
	status, body := page(t, response, err)
	if status != 200 || !strings.Contains(body, "Sign in") || strings.Contains(body, "Backup control") {
		t.Fatalf("unauthenticated dashboard leaked state: %d %s", status, body)
	}
	_, login := d.get(t, "/login")
	status, body = d.post(t, "/login", url.Values{"password": {password}})
	if status != http.StatusForbidden || strings.Contains(body, password) {
		t.Fatalf("login accepted missing CSRF: %d %s", status, body)
	}
	status, body = d.post(t, "/login", url.Values{"csrf": {token(t, login)}, "password": {"incorrect-secret"}})
	if status != http.StatusUnauthorized || strings.Contains(body, "incorrect-secret") {
		t.Fatalf("invalid login: %d %s", status, body)
	}
	csrf := d.login(t)
	serverURL, _ := url.Parse(d.server.URL)
	cookies := d.client.Jar.Cookies(serverURL)
	if len(cookies) != 1 || cookies[0].Value == "" {
		t.Fatal("missing session cookie")
	}
	status, _ = d.post(t, "/logout", url.Values{"csrf": {"forged"}})
	if status != http.StatusForbidden {
		t.Fatal("logout accepted invalid CSRF")
	}
	status, body = d.post(t, "/logout", url.Values{"csrf": {csrf}})
	if status != 200 || !strings.Contains(body, "Sign in") {
		t.Fatal("logout failed")
	}
	status, body = d.get(t, "/")
	if status != 200 || strings.Contains(body, "Backup control") {
		t.Fatal("logout left an authenticated session")
	}
	if _, err := d.run.Status(context.Background(), "missing"); service.Code(err) != "account_not_found" {
		t.Fatal("test runtime is not account scoped")
	}
}

func (d dashboard) accountForm(t *testing.T, csrf, id string) url.Values {
	t.Helper()
	dir := filepath.Dir(d.path)
	key, cookie := filepath.Join(dir, id+"-key"), filepath.Join(dir, id+"-cookie")
	for path, value := range map[string]string{
		key: "private-source-key", cookie: "session-id=test; ubid-main=test; at-main=test",
	} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return url.Values{
		"csrf": {csrf}, "id": {id}, "immich_url": {"http://127.0.0.1:1"},
		"key_file": {key}, "cookie_file": {cookie}, "region": {"us"},
		"album_ids":   {"11111111-1111-4111-8111-111111111111"},
		"favorites":   {"off"},
		"media_image": {"on"}, "include_archived": {"on"},
	}
}

func TestFirstAccountPersistsBeforeReloadWithExplicitScopeAndNoSecretResponses(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	form := d.accountForm(t, "", "family")
	if status, _ := d.post(t, "/accounts/save", form); status != http.StatusForbidden {
		t.Fatalf("save without CSRF: %d", status)
	}
	form.Set("csrf", csrf)
	form.Del("album_ids")
	if status, body := d.post(t, "/accounts/save", form); status != http.StatusBadRequest || !strings.Contains(body, "source_scope_required") {
		t.Fatalf("save without scope: %d %s", status, body)
	}
	if _, err := os.Stat(d.path); !os.IsNotExist(err) {
		t.Fatal("invalid configuration was written")
	}
	form.Set("album_ids", "11111111-1111-4111-8111-111111111111")
	status, body := d.post(t, "/accounts/save", form)
	if status != 200 || !strings.Contains(body, "family") {
		t.Fatalf("first save: %d %s", status, body)
	}
	cfg, err := config.Load(d.path)
	if err != nil || len(cfg.Accounts) != 1 || cfg.Accounts[0].ID != "family" || cfg.Accounts[0].Library {
		t.Fatalf("saved config: %+v %v", cfg, err)
	}
	if _, err := d.run.Status(t.Context(), "family"); err != nil {
		t.Fatalf("runtime did not reload saved account: %v", err)
	}
	info, err := os.Stat(d.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("configuration permissions are not private")
	}
	_, edit := d.get(t, "/accounts/family/edit")
	for _, secret := range []string{"private-source-key", "session-id=test", password} {
		if strings.Contains(body+edit, secret) {
			t.Fatalf("response revealed %q", secret)
		}
	}
}

func TestAlbumDiscoveryNeedsOnlySourceAndDoesNotPersistOrAutoSelectScope(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/albums" || r.Header.Get("x-api-key") != "private-source-key" {
			t.Errorf("unexpected discovery request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `[{"id":"11111111-1111-4111-8111-111111111111","albumName":"<Family>","assetCount":7}]`)
	}))
	defer source.Close()
	form := d.accountForm(t, csrf, "family")
	form.Set("immich_url", source.URL)
	form.Del("cookie_file")
	form.Del("region")
	form.Del("album_ids")
	status, body := d.post(t, "/accounts/discover", form)
	if status != 200 || calls.Load() != 1 || !strings.Contains(body, "&lt;Family&gt;") || !strings.Contains(body, `name="album_id"`) {
		t.Fatalf("source-only discovery: %d %s (calls=%d)", status, body, calls.Load())
	}
	if strings.Contains(body, "private-source-key") || strings.Contains(body, `name="library" checked`) {
		t.Fatal("discovery leaked a secret or expanded the selected scope")
	}
	if _, err := os.Stat(d.path); !os.IsNotExist(err) {
		t.Fatal("discovery persisted unselected configuration")
	}
}

func TestAccountControlsAreIsolatedAndRemovalPreservesDurablePause(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	first := d.accountForm(t, csrf, "first")
	second := d.accountForm(t, csrf, "second")
	for _, form := range []url.Values{first, second} {
		if status, body := d.post(t, "/accounts/save", form); status != 200 {
			t.Fatalf("save: %d %s", status, body)
		}
	}
	action := func(id, verb string) {
		t.Helper()
		if status, body := d.post(t, "/accounts/"+id+"/"+verb, url.Values{"csrf": {csrf}}); status != 200 {
			t.Fatalf("%s/%s: %d %s", id, verb, status, body)
		}
	}
	action("first", "pause")
	for id, want := range map[string]bool{"first": true, "second": false} {
		status, err := d.run.Status(t.Context(), id)
		if err != nil || status.Paused != want {
			t.Fatalf("%s pause=%t want=%t err=%v", id, status.Paused, want, err)
		}
	}
	action("first", "retry")
	status, err := d.run.Status(t.Context(), "first")
	if err != nil || status.PauseReason != "manual" {
		t.Fatalf("retry cleared manual pause: %+v %v", status, err)
	}
	action("first", "remove")
	if _, err := d.run.Status(t.Context(), "first"); service.Code(err) != "account_not_found" {
		t.Fatal("removed account is still scheduled")
	}
	cfg, err := config.Load(d.path)
	if err != nil || len(cfg.Accounts) != 1 || cfg.Accounts[0].ID != "second" {
		t.Fatal("removal changed the wrong account")
	}
	if status, body := d.post(t, "/accounts/save", first); status != 200 {
		t.Fatalf("re-add: %d %s", status, body)
	}
	status, err = d.run.Status(t.Context(), "first")
	if err != nil || status.PauseReason != "manual" {
		t.Fatalf("removal erased durable state: %+v %v", status, err)
	}
	action("first", "resume")
	status, err = d.run.Status(t.Context(), "first")
	if err != nil || status.Paused {
		t.Fatalf("resume failed: %+v %v", status, err)
	}
	if code, _ := d.post(t, "/accounts/missing/pause", url.Values{"csrf": {csrf}}); code != 404 {
		t.Fatal("unknown account operation was accepted")
	}
	if code, _ := d.post(t, "/accounts/second/pause", url.Values{"csrf": {"forged"}}); code != 403 {
		t.Fatal("pause bypassed CSRF")
	}
}

func TestFailedPersistenceDoesNotChangeRuntimeOrOtherAccounts(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	if status, body := d.post(t, "/accounts/save", d.accountForm(t, csrf, "first")); status != 200 {
		t.Fatalf("first save: %d %s", status, body)
	}
	if err := os.Remove(d.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(d.path, 0700); err != nil {
		t.Fatal(err)
	}
	status, body := d.post(t, "/accounts/save", d.accountForm(t, csrf, "second"))
	if status != 400 || !strings.Contains(body, "config_write_failed") {
		t.Fatalf("failed persistence: %d %s", status, body)
	}
	if _, err := d.run.Status(t.Context(), "second"); service.Code(err) != "account_not_found" {
		t.Fatal("failed persistence still reloaded runtime")
	}
	if _, err := d.run.Status(t.Context(), "first"); err != nil {
		t.Fatalf("failed persistence changed another account: %v", err)
	}
}

func TestSessionRotationCookiePolicyCrossOriginAndCredentialRotation(t *testing.T) {
	d := openDashboard(t)
	response, err := d.client.Get(d.server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	preauth := response.Cookies()[0]
	_, login := page(t, response, nil)
	if !preauth.HttpOnly || preauth.SameSite != http.SameSiteStrictMode || preauth.Path != "/" || preauth.MaxAge != 600 {
		t.Fatalf("unsafe preauth cookie: %+v", preauth)
	}
	response, err = d.client.PostForm(d.server.URL+"/login", url.Values{"csrf": {token(t, login)}, "password": {password}})
	status, body := page(t, response, err)
	if status != 200 {
		t.Fatalf("login: %d %s", status, body)
	}
	u, _ := url.Parse(d.server.URL)
	if d.client.Jar.Cookies(u)[0].Value == preauth.Value {
		t.Fatal("login did not rotate session")
	}
	replay, _ := http.NewRequest("GET", d.server.URL+"/", nil)
	replay.AddCookie(preauth)
	response, err = http.DefaultClient.Do(replay)
	_, replayBody := page(t, response, err)
	if strings.Contains(replayBody, "Backup control") {
		t.Fatal("preauth session became an authenticated session")
	}
	form := url.Values{"csrf": {token(t, body)}}
	for _, origin := range []string{"null", "https://different.invalid", "https://" + u.Host} {
		req, _ := http.NewRequest("POST", d.server.URL+"/logout", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		response, err = d.client.Do(req)
		status, _ = page(t, response, err)
		if status != 403 {
			t.Fatal("cross-origin form with a valid CSRF token was accepted")
		}
	}
	if err := os.WriteFile(d.cfg.AdminPasswordFile, []byte("rotated-admin-passphrase"), 0600); err != nil {
		t.Fatal(err)
	}
	_, body = d.get(t, "/")
	if strings.Contains(body, "Backup control") || !strings.Contains(body, "Sign in") {
		t.Fatal("credential rotation did not revoke existing sessions")
	}
}

func TestLoginRateLimitBodyLimitAndPublicHealth(t *testing.T) {
	d := openDashboard(t)
	_, body := d.get(t, "/login")
	csrf := token(t, body)
	for i := 0; i < 6; i++ {
		status, body := d.post(t, "/login", url.Values{"csrf": {csrf}, "password": {"incorrect"}})
		want := http.StatusUnauthorized
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if status != want {
			t.Fatalf("attempt %d: %d %s", i+1, status, body)
		}
	}
	status, body := d.post(t, "/login", url.Values{"csrf": {csrf}, "password": {strings.Repeat("x", 65<<10)}})
	if status != 400 || !strings.Contains(body, "invalid_form") {
		t.Fatalf("oversized form was not rejected: %d", status)
	}
	for _, path := range []string{"/healthz", "/readyz", "/admin.css"} {
		response, err := http.Get(d.server.URL + path)
		status, body := page(t, response, err)
		if status != 200 || strings.Contains(body, password) {
			t.Fatalf("public resource %s: %d", path, status)
		}
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDashboardDoctorOnlyReadsRemoteStateAndReportsWritePermissionsUntested(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && (r.Method != "POST" || r.URL.Path != "/api/search/metadata") {
			t.Errorf("doctor mutated source: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/users/me":
			_, _ = io.WriteString(w, `{"id":"22222222-2222-4222-8222-222222222222"}`)
		case "/api/server/version":
			_, _ = io.WriteString(w, `{"major":3,"minor":3,"patch":4}`)
		case "/api/albums":
			_, _ = io.WriteString(w, `[{"id":"11111111-1111-4111-8111-111111111111","albumName":"Private","assetCount":0}]`)
		case "/api/albums/11111111-1111-4111-8111-111111111111":
			_, _ = io.WriteString(w, `{"id":"11111111-1111-4111-8111-111111111111","albumName":"Private","assetCount":0}`)
		case "/api/search/metadata":
			_, _ = io.WriteString(w, `{"assets":{"count":0,"items":[],"nextPage":null}}`)
		default:
			t.Errorf("unexpected source probe: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	original := http.DefaultTransport
	var targetReads atomic.Int32
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "127.0.0.1" {
			return original.RoundTrip(r)
		}
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/nodes") || r.URL.Query().Get("filters") != "isRoot:true" {
			t.Errorf("doctor mutated target: %s %s", r.Method, r.URL.Path)
		}
		targetReads.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r,
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"root","isRoot":true,"kind":"FOLDER","status":"AVAILABLE","ownerId":"owner"}],"count":1}`))}, nil
	})
	defer func() { http.DefaultTransport = original }()
	form := d.accountForm(t, csrf, "family")
	form.Set("immich_url", source.URL)
	if status, body := d.post(t, "/accounts/save", form); status != 200 {
		t.Fatalf("save: %d %s", status, body)
	}
	status, body := d.post(t, "/accounts/family/doctor", url.Values{"csrf": {csrf}})
	if status != 200 || targetReads.Load() != 1 || !strings.Contains(body, "untested") || !strings.Contains(body, "3.3.4") {
		t.Fatalf("doctor: %d %s (target reads=%d)", status, body, targetReads.Load())
	}
	for _, secret := range []string{"private-source-key", "session-id=test", password, source.URL} {
		if strings.Contains(body, secret) {
			t.Fatalf("doctor revealed %q", secret)
		}
	}
}

func TestUnauthenticatedLoginBoundsSlowBodyRead(t *testing.T) {
	d := openDashboard(t)
	_, login := d.get(t, "/login")
	u, _ := url.Parse(d.server.URL)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := "POST /login HTTP/1.1\r\nHost: " + u.Host + "\r\nCookie: " + d.client.Jar.Cookies(u)[0].String() +
		"\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 1000\r\n\r\ncsrf=" + token(t, login)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	status, body := page(t, response, err)
	if status != 400 || !strings.Contains(body, "invalid_form") {
		t.Fatalf("slow login was not bounded: %d %s", status, body)
	}
}

func TestReadOnlyConfigurationRetainsAccountControlsAndManagementIsOptIn(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	if status, body := d.post(t, "/accounts/save", d.accountForm(t, csrf, "family")); status != 200 {
		t.Fatalf("save: %d %s", status, body)
	}
	cfg, err := config.Load(d.path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = filepath.Join(t.TempDir(), "readonly-state")
	run, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	if err := run.EnableAdmin(""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(run.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	readonly := dashboard{server, &http.Client{Jar: jar}, cfg, d.path, run}
	csrf = readonly.login(t)
	if status, _ := readonly.get(t, "/accounts/family/edit"); status != 403 {
		t.Fatal("read-only mode allowed config editing")
	}
	if status, _ := readonly.post(t, "/accounts/save", readonly.accountForm(t, csrf, "another")); status != 403 {
		t.Fatal("read-only mode allowed config saving")
	}
	if status, body := readonly.post(t, "/accounts/family/pause", url.Values{"csrf": {csrf}}); status != 200 {
		t.Fatalf("read-only config disabled pause: %d %s", status, body)
	}
	state, err := run.Status(t.Context(), "family")
	if err != nil || !state.Paused {
		t.Fatal("read-only account control did not persist")
	}
	cfg.DataDir, cfg.AdminPasswordFile = filepath.Join(t.TempDir(), "headless-state"), ""
	headless, err := service.Open(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer headless.Close()
	if err := headless.EnableAdmin(d.path); err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(headless.Handler())
	defer server.Close()
	response, err := http.Get(server.URL + "/")
	if status, _ := page(t, response, err); status != 404 {
		t.Fatal("management was enabled without a mounted admin secret")
	}
}

func TestSessionCookieIsSecureOverTLS(t *testing.T) {
	d := openDashboard(t)
	server := httptest.NewTLSServer(d.run.Handler())
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	cookies := response.Cookies()
	page(t, response, nil)
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("TLS login did not set a secure HttpOnly cookie")
	}
}

func TestLoginWithoutSessionDoesNotDrainAnUnfinishedBody(t *testing.T) {
	d := openDashboard(t)
	u, _ := url.Parse(d.server.URL)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "POST /login HTTP/1.1\r\nHost: "+u.Host+"\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 1000\r\n\r\ncsrf=missing"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	status, _ := page(t, response, err)
	if status != http.StatusForbidden {
		t.Fatalf("missing preauth session: %d", status)
	}
}

func TestDashboardShowsAccountIsolatedBacklogsAndReadyWork(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	for _, id := range []string{"first", "second"} {
		if status, body := d.post(t, "/accounts/save", d.accountForm(t, csrf, id)); status != http.StatusOK {
			t.Fatalf("save %s: %d %s", id, status, body)
		}
	}
	cfg, err := config.Load(d.path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WebhookSecretFile = filepath.Join(t.TempDir(), "webhook")
	secret := strings.Repeat("w", 32)
	if err := os.WriteFile(cfg.WebhookSecretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.run.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest("POST", d.server.URL+"/events/first",
		strings.NewReader(`{"asset_id":"33333333-3333-4333-8333-333333333333"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := d.client.Do(request)
	if status, body := page(t, response, err); status != http.StatusAccepted {
		t.Fatalf("enqueue event: %d %s", status, body)
	}
	state, err := d.run.Status(t.Context(), "first")
	if err != nil || state.EventsPending != 1 || !state.WorkRemaining {
		t.Fatalf("queued event is not ready: %+v %v", state, err)
	}
	check := func(body, id, eventCount, work string) {
		t.Helper()
		section := regexp.MustCompile(`(?s)<section class="account" aria-label="Account ` + id + `">.*?</section>`).FindString(body)
		for _, text := range []string{
			`aria-label="Media queue"`,
			`aria-label="Event, album and association backlog"`,
			"<dt>Events pending</dt><dd>" + eventCount + "</dd>",
			"<dt>Events failed</dt><dd>0</dd>",
			"<dt>Albums pending</dt><dd>0</dd>",
			"<dt>Albums failed</dt><dd>0</dd>",
			"<dt>Albums unconfirmed</dt><dd>0</dd>",
			"<dt>Associations pending</dt><dd>0</dd>",
			"<dt>Associations failed</dt><dd>0</dd>",
			"<dt>Associations unconfirmed</dt><dd>0</dd>",
			"Incremental scan: idle.",
			"Last incremental reconcile: not yet completed.",
			"Work remaining: " + work + ".",
		} {
			if !strings.Contains(section, text) {
				t.Errorf("account %s missing %q: %s", id, text, section)
			}
		}
	}
	status, body := d.get(t, "/")
	if status != http.StatusOK {
		t.Fatalf("overview: %d %s", status, body)
	}
	check(body, "first", "1", "ready to run")
	check(body, "second", "0", "none ready to run")
	status, body = d.post(t, "/accounts/first/pause", url.Values{"csrf": {csrf}})
	if status != http.StatusOK {
		t.Fatalf("pause: %d %s", status, body)
	}
	check(body, "first", "1", "none ready to run")
	check(body, "second", "0", "none ready to run")
}

func TestFavoritesModeIsEditableAndPersisted(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	form := d.accountForm(t, csrf, "family")
	form.Set("favorites", "add_only")
	if status, body := d.post(t, "/accounts/save", form); status != http.StatusOK {
		t.Fatalf("save add_only: %d %s", status, body)
	}
	cfg, err := config.Load(d.path)
	if err != nil || len(cfg.Accounts) != 1 || cfg.Accounts[0].Favorites != "add_only" {
		t.Fatalf("favorites setting not persisted: %+v %v", cfg, err)
	}
	_, edit := d.get(t, "/accounts/family/edit")
	if !strings.Contains(edit, `name="favorites"`) {
		t.Fatal("favorites setting is not exposed in the account editor")
	}
	form.Set("original_id", "family")
	form.Set("favorites", "authoritative")
	if status, body := d.post(t, "/accounts/save", form); status != http.StatusBadRequest || !strings.Contains(body, "favorites_invalid") {
		t.Fatalf("unknown favorites mode: %d %s", status, body)
	}

	// Mirror with a recorded takeover is the only authority that clears manual
	// stars; the editor exposes both controls.
	form.Set("favorites", "mirror")
	form.Set("favorites_takeover", "on")
	if status, body := d.post(t, "/accounts/save", form); status != http.StatusOK {
		t.Fatalf("save mirror: %d %s", status, body)
	}
	cfg, err = config.Load(d.path)
	if err != nil || cfg.Accounts[0].Favorites != "mirror" || !cfg.Accounts[0].FavoritesTakeover {
		t.Fatalf("mirror authority not persisted: %+v %v", cfg, err)
	}
	_, edit = d.get(t, "/accounts/family/edit")
	for _, want := range []string{`value="mirror"`, `name="favorites_takeover" checked`} {
		if !strings.Contains(edit, want) {
			t.Fatalf("editor lacks %q", want)
		}
	}
	// Takeover cannot exist without mirror.
	form.Set("original_id", "family")
	form.Del("favorites")
	form.Set("favorites", "add_only")
	if status, body := d.post(t, "/accounts/save", form); status != http.StatusBadRequest ||
		!strings.Contains(body, "favorites_takeover_requires_mirror") {
		t.Fatalf("takeover without mirror: %d %s", status, body)
	}
}

func TestAdminFilterPolicyIsValidatedPersistedAndRendered(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	const tag = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	form := d.accountForm(t, csrf, "family")
	form.Set("media_video", "on")
	form.Set("max_original_bytes", "100")
	form.Set("skip_raw", "on")
	form.Del("include_archived")
	form.Set("excluded_tags", tag)
	if status, body := d.post(t, "/accounts/save", form); status != http.StatusOK {
		t.Fatalf("save filters: %d %s", status, body)
	}
	cfg, err := config.Load(d.path)
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.Accounts[0].Policy()
	if len(policy.MediaTypes) != 2 || policy.MaxOriginalBytes != 100 || !policy.SkipRAW ||
		policy.ArchivedIncluded() || len(policy.ExcludedTags) != 1 || policy.ExcludedTags[0] != tag {
		t.Fatalf("filters were not persisted: %+v", policy)
	}
	_, edit := d.get(t, "/accounts/family/edit")
	for _, want := range []string{`name="media_image" checked`, `name="media_video" checked`, `name="skip_raw" checked`,
		`name="excluded_tags"`, tag, `value="100"`} {
		if !strings.Contains(edit, want) {
			t.Errorf("editor missing %q", want)
		}
	}
	if strings.Contains(edit, `name="include_archived" checked`) {
		t.Error("explicit archive exclusion rendered as included")
	}

	previous, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	bad := d.accountForm(t, csrf, "family")
	bad.Set("max_original_bytes", "-1")
	if status, body := d.post(t, "/accounts/save", bad); status != http.StatusBadRequest ||
		!strings.Contains(body, "filter_max_original_bytes_invalid") {
		t.Fatalf("invalid size accepted: %d %s", status, body)
	}
	current, err := os.ReadFile(d.path)
	if err != nil || !bytes.Equal(current, previous) {
		t.Fatal("rejected filter edit changed persisted configuration")
	}
	noMedia := d.accountForm(t, csrf, "family")
	noMedia.Del("media_image")
	noMedia.Del("media_video")
	if status, body := d.post(t, "/accounts/save", noMedia); status != http.StatusBadRequest ||
		!strings.Contains(body, "filter_media_type_invalid") {
		t.Fatalf("empty media selection accepted: %d %s", status, body)
	}
}

func TestTransferWindowIsEditableAndShownWithoutSecrets(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	form := d.accountForm(t, csrf, "windowed")
	form.Set("transfer_timezone", "America/New_York")
	form.Set("transfer_window_start", "23:00")
	form.Set("transfer_window_end", "06:00")
	status, body := d.post(t, "/accounts/save", form)
	if status != 200 {
		t.Fatalf("save window: %d %s", status, body)
	}
	cfg, err := config.Load(d.path)
	if err != nil {
		t.Fatal(err)
	}
	window := cfg.Accounts[0].TransferWindow
	if window == nil || window.Timezone != "America/New_York" || window.Start != "23:00" || window.End != "06:00" {
		t.Fatalf("window not persisted: %+v", window)
	}
	_, overview := d.get(t, "/")
	for _, want := range []string{"Transfer window: America/New_York 23:00–06:00", "Deferred", "Transfer rate cap"} {
		if !strings.Contains(overview, want) {
			t.Fatalf("overview missing %q", want)
		}
	}
	_, edit := d.get(t, "/accounts/windowed/edit")
	for _, want := range []string{`value="America/New_York"`, `value="23:00"`, `value="06:00"`} {
		if !strings.Contains(edit, want) {
			t.Fatalf("editor missing %q", want)
		}
	}
	form.Set("original_id", "windowed")
	form.Set("transfer_window_end", "23:00")
	status, body = d.post(t, "/accounts/save", form)
	if status != http.StatusBadRequest || !strings.Contains(body, "transfer_window_bounds_invalid") {
		t.Fatalf("equal window bounds accepted: %d %s", status, body)
	}
}

func TestEditingAnAccountPreservesItsApprovedReadOnlyRoots(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	if status, body := d.post(t, "/accounts/save", d.accountForm(t, csrf, "family")); status != 200 {
		t.Fatalf("first save: %d %s", status, body)
	}
	cfg, err := config.Load(d.path)
	if err != nil {
		t.Fatal(err)
	}
	approved := []config.DirectRoot{{ImmichPath: "/photos", HostPath: "/originals"}}
	cfg.Accounts[0].DirectRoots = approved
	if err := d.run.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(d.path); err != nil {
		t.Fatal(err)
	}
	form := d.accountForm(t, csrf, "family")
	form.Set("original_id", "family")
	form.Set("media_video", "on")
	status, body := d.post(t, "/accounts/save", form)
	if status != 200 {
		t.Fatalf("edit save: %d %s", status, body)
	}
	saved, err := config.Load(d.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Accounts) != 1 || !saved.Accounts[0].Policy().Includes("video") ||
		len(saved.Accounts[0].DirectRoots) != 1 || saved.Accounts[0].DirectRoots[0] != approved[0] {
		t.Fatalf("approved mapping was dropped by an account edit: %+v", saved.Accounts)
	}
}
