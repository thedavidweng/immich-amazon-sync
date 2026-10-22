package admin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/admin"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

type logBackend struct {
	cfg    config.Config
	status syncer.Status
	logs   []admin.WorkflowLog
	logErr error
}

func (b *logBackend) Config() config.Config    { return b.cfg }
func (b *logBackend) Save(config.Config) error { return nil }
func (b *logBackend) Albums(context.Context, config.Account) ([]bridge.Album, error) {
	return nil, nil
}
func (b *logBackend) Status(context.Context, string) (syncer.Status, error) { return b.status, nil }
func (b *logBackend) Pause(context.Context, string, bool) error             { return nil }
func (b *logBackend) Retry(context.Context, string) error                   { return nil }
func (b *logBackend) Diagnose(context.Context, string) ([]admin.Diagnostic, error) {
	return nil, nil
}
func (b *logBackend) WorkflowLogs(context.Context, string, int) ([]admin.WorkflowLog, error) {
	return b.logs, b.logErr
}
func (b *logBackend) OrphanWorkflows(context.Context) ([]admin.WorkflowOrphan, error) {
	return nil, nil
}
func (b *logBackend) Code(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func openLogDashboard(t *testing.T, managed bool) (*httptest.Server, *http.Client, *logBackend) {
	t.Helper()
	dir := t.TempDir()
	password := filepath.Join(dir, "admin")
	if err := os.WriteFile(password, []byte("offline-admin-passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &logBackend{
		cfg: config.Config{Accounts: []config.Account{{
			ID: "home", ManagedWorkflow: managed, AlbumIDs: []string{"album"},
		}}},
		status: syncer.Status{Workflow: "active"},
		logs: []admin.WorkflowLog{{Account: "0123456789abcdef", Completed: 4, Halted: 1,
			Error: 0, Last: "2026-10-22 08:00:00 UTC"}},
	}
	handler, err := admin.New(backend, admin.Options{CredentialFile: password})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	page, err := client.Get(server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	match := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(string(body))
	if len(match) != 2 {
		t.Fatalf("no CSRF token: %s", body)
	}
	login, err := client.PostForm(server.URL+"/login", url.Values{
		"csrf": {match[1]}, "password": {"offline-admin-passphrase"},
	})
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	return server, client, backend
}

func TestWorkflowLogDiagnosticsAreReadOnlyAndAuthorized(t *testing.T) {
	server, client, _ := openLogDashboard(t, true)
	page, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	match := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(string(body))
	if len(match) != 2 || !strings.Contains(string(body), "/accounts/home/workflow-logs") {
		t.Fatalf("dashboard does not offer workflow log diagnostics")
	}
	// Without a CSRF token the read-only request is rejected.
	response, err := client.PostForm(server.URL+"/accounts/home/workflow-logs", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d", response.StatusCode)
	}
	response, err = client.PostForm(server.URL+"/accounts/home/workflow-logs", url.Values{"csrf": {match[1]}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "completed 4 · halted 1 · error 0") {
		t.Fatalf("workflow logs page = %d %s", response.StatusCode, body)
	}
	for _, forbidden := range []string{"workflow-id", "http://", "secret"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("workflow logs page leaked %q", forbidden)
		}
	}
}

func TestWorkflowLogDiagnosticsAreHiddenWithoutManagement(t *testing.T) {
	server, client, _ := openLogDashboard(t, false)
	page, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if strings.Contains(string(body), "workflow-logs") {
		t.Fatal("workflow log diagnostics offered for an unmanaged account")
	}
}
