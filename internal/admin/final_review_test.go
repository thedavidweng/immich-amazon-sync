package admin_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/service"
)

func TestAdminRejectsUnusableMirrorScopeBeforePersistenceAndReload(t *testing.T) {
	for _, mode := range []string{"library-mirror-conflict", "albums-mirror", "library-only"} {
		t.Run(mode, func(t *testing.T) {
			d := openDashboard(t)
			csrf := d.login(t)
			for _, id := range []string{"first", "other"} {
				if status, body := d.post(t, "/accounts/save", d.accountForm(t, csrf, id)); status != http.StatusOK {
					t.Fatalf("initial save: %d %s", status, body)
				}
			}
			previous, err := os.ReadFile(d.path)
			if err != nil {
				t.Fatal(err)
			}
			form := d.accountForm(t, csrf, "first")
			form.Set("original_id", "first")
			if mode != "albums-mirror" {
				form.Del("album_ids")
				form.Set("library", "on")
			}
			if mode != "library-only" {
				form.Set("mirror_albums", "on")
			}
			status, body := d.post(t, "/accounts/save", form)
			if mode == "library-mirror-conflict" {
				if status != http.StatusBadRequest || !strings.Contains(body, "source_scope_conflict") {
					t.Errorf("unusable scope was accepted: %d %s", status, body)
				}
				current, err := os.ReadFile(d.path)
				if err != nil || !bytes.Equal(current, previous) {
					t.Errorf("rejected scope changed persisted config: %v", err)
				}
				_, editor := d.get(t, "/accounts/first/edit")
				if strings.Contains(editor, `name="library" checked`) || strings.Contains(editor, `name="mirror_albums" checked`) {
					t.Error("rejected scope changed running configuration")
				}
			} else {
				if status != http.StatusOK {
					t.Fatalf("valid scope rejected: %d %s", status, body)
				}
				cfg, err := config.Load(d.path)
				if err != nil || len(cfg.Accounts) != 2 ||
					cfg.Accounts[0].Library != (mode == "library-only") ||
					cfg.Accounts[0].MirrorAlbums != (mode == "albums-mirror") {
					t.Fatalf("valid scope was not persisted: %v", err)
				}
			}
			for _, id := range []string{"first", "other"} {
				if _, err := d.run.Status(t.Context(), id); err != nil {
					t.Errorf("account %q unusable after save: %v", id, err)
				}
			}
		})
	}
}

func TestAdminOversizeAccumulatedConfigPreservesFileAndRunningAccounts(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	if status, body := d.post(t, "/accounts/save", d.accountForm(t, csrf, "first")); status != http.StatusOK {
		t.Fatalf("initial save: %d %s", status, body)
	}
	cfg, err := config.Load(d.path)
	if err != nil {
		t.Fatal(err)
	}
	seed := cfg.Accounts[0]
	for i := range 17 {
		account := seed
		account.ID = fmt.Sprintf("seed-%d", i)
		account.ImmichURL = "http://127.0.0.1:1/" + strings.Repeat("a", 60000)
		cfg.Accounts = append(cfg.Accounts, account)
	}
	if err := cfg.Save(d.path); err != nil {
		t.Fatal(err)
	}
	if err := d.run.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(d.path)
	if err != nil || len(previous) > 1<<20 || len(previous) < (1<<20)-(64<<10) {
		t.Fatalf("near-limit valid configuration fixture: bytes=%d err=%v", len(previous), err)
	}
	form := d.accountForm(t, csrf, "overflow")
	form.Set("immich_url", "http://127.0.0.1:1/"+strings.Repeat("b", 60000))
	if len(form.Encode()) >= 64<<10 {
		t.Fatal("submission must be valid under the individual form limit")
	}
	status, body := d.post(t, "/accounts/save", form)
	if status != http.StatusBadRequest || !strings.Contains(body, "config_invalid_json") {
		t.Errorf("oversize accumulated config accepted: status=%d", status)
	}
	current, err := os.ReadFile(d.path)
	if err != nil || !bytes.Equal(current, previous) {
		t.Errorf("rejected oversize save changed persisted configuration: %v", err)
	}
	if _, err := config.Load(d.path); err != nil {
		t.Errorf("configuration cannot restart: %v", err)
	}
	if _, err := d.run.Status(t.Context(), "overflow"); service.Code(err) != "account_not_found" {
		t.Errorf("rejected save changed running accounts: %v", err)
	}
	if _, err := d.run.Status(t.Context(), "first"); err != nil {
		t.Errorf("existing account changed after rejected save: %v", err)
	}
}

func TestAnonymousIssuanceThrottleDoesNotConsumeCredentialAttempts(t *testing.T) {
	d := openDashboard(t)
	_, login := d.get(t, "/login")
	csrf := token(t, login)
	throttled := false
	for range 256 {
		response, err := http.Get(d.server.URL + "/login")
		if err != nil {
			t.Fatal(err)
		}
		retryAfter := response.Header.Get("Retry-After")
		status, body := page(t, response, nil)
		if status == http.StatusTooManyRequests {
			if retryAfter == "" || !strings.Contains(body, "login_rate_limited") {
				t.Errorf("issuance throttle lacks safe code/retry delay: %d %s", status, body)
			}
			throttled = true
			break
		}
		if status != http.StatusOK {
			t.Errorf("anonymous issuance exhausted session capacity: %d %s", status, body)
			break
		}
	}
	if !throttled {
		t.Error("cookie-discarding login-page requests were not throttled")
	}
	for i := range 4 {
		status, body := d.post(t, "/login", url.Values{"csrf": {csrf}, "password": {"incorrect"}})
		if status != http.StatusUnauthorized || !strings.Contains(body, "authentication_required") {
			t.Fatalf("GET issuance consumed credential attempt %d: %d %s", i+1, status, body)
		}
	}
	status, body := d.post(t, "/login", url.Values{"csrf": {csrf}, "password": {password}})
	if status != http.StatusOK || !strings.Contains(body, "Backup control") {
		t.Fatalf("issuance throttle blocked valid authentication: %d %s", status, body)
	}
}

func TestAnonymousCapacityAcrossAddressesPreservesAuthentication(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	var addresses atomic.Uint32
	// The wrapper simulates distinct direct peers; forwarded headers stay unused.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := addresses.Add(1)
		r.RemoteAddr = fmt.Sprintf("198.18.%d.%d:12345", n/250, n%250+1)
		d.run.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	for i := range 300 {
		response, err := http.Get(server.URL + "/login")
		status, body := page(t, response, err)
		if status != http.StatusOK {
			t.Fatalf("anonymous entries denied fresh login at peer %d: %d %s", i+1, status, body)
		}
	}
	jarClient := &http.Client{}
	response, err := jarClient.Get(server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	cookies := response.Cookies()
	_, body := page(t, response, nil)
	if len(cookies) != 1 {
		t.Fatal("fresh peer did not receive a preauth session")
	}
	form := url.Values{"csrf": {token(t, body)}, "password": {password}}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookies[0])
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err = client.Do(request)
	status, body := page(t, response, err)
	if status != http.StatusSeeOther {
		t.Errorf("anonymous capacity prevented authentication: %d %s", status, body)
	}
	if status, body := d.get(t, "/"); status != http.StatusOK || !strings.Contains(body, "Backup control") {
		t.Errorf("anonymous traffic displaced authenticated session: %d %s", status, body)
	}
	if status, body := d.post(t, "/logout", url.Values{"csrf": {csrf}}); status != http.StatusOK {
		t.Errorf("anonymous traffic invalidated authenticated CSRF: %d %s", status, body)
	}
}

func TestLoginPageRefreshPreservesCurrentSession(t *testing.T) {
	d := openDashboard(t)
	_, first := d.get(t, "/login")
	firstToken := token(t, first)
	_, second := d.get(t, "/login")
	if token(t, second) != firstToken {
		t.Error("login-page refresh invalidated an existing preauth token")
	}
	d.login(t)
	status, body := d.get(t, "/login")
	if status != http.StatusOK || !strings.Contains(body, "Backup control") {
		t.Fatalf("authenticated login-page navigation replaced the session: %d %s", status, body)
	}
}
