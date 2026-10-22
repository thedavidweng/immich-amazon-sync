package service_test

import (
	"bytes"
	"database/sql"
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

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/service"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// fakeImmichWorkflows implements the pinned v3.3.4 Workflows contract and
// records every mutation so tests can prove only the owned ID is touched.
type fakeImmichWorkflows struct {
	mu                sync.Mutex
	items             []map[string]any
	next              int
	creates           int
	updates           int
	deletes           int
	lists             int
	version           string
	permissions       []string
	capability        int // count of version probes
	permissionsProbes int
	scanCalls         int
	asset             string
	trashed           bool
	logs              string
}

func newFakeImmich() *fakeImmichWorkflows {
	return &fakeImmichWorkflows{
		items:       []map[string]any{},
		version:     `{"major":3,"minor":3,"patch":4}`,
		permissions: []string{"all"},
		logs:        `[]`,
	}
}

func (f *fakeImmichWorkflows) addUnrelated() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "99999999-9999-4999-8999-999999999999"
	f.items = append(f.items, map[string]any{
		"id": id, "trigger": "AssetTagged", "name": "unrelated", "description": "someone else",
		"enabled": true, "logging": false, "createdAt": "2026-01-01", "updatedAt": "2026-01-01",
		"steps": []any{map[string]any{"method": "immich-plugin-core#assetFavorite", "enabled": true, "config": map[string]any{}}},
	})
	return id
}

func (f *fakeImmichWorkflows) workflowObjects() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.items...)
}

func (f *fakeImmichWorkflows) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/server/version":
		f.capability++
		_, _ = fmt.Fprint(w, f.version)
	case r.URL.Path == "/api/albums/"+albumID:
		count := 0
		if f.asset != "" {
			count = 1
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"albumName":"private","assetCount":%d,"shared":false}`, albumID, count)
	case r.URL.Path == "/api/api-keys/me":
		f.permissionsProbes++
		_ = json.NewEncoder(w).Encode(map[string]any{"permissions": f.permissions})
	case r.URL.Path == "/api/workflows/triggers":
		_, _ = fmt.Fprint(w, `[{"trigger":"AssetCreate","types":["AssetV1"]}]`)
	case r.URL.Path == "/api/workflows" && r.Method == http.MethodGet:
		f.lists++
		_ = json.NewEncoder(w).Encode(f.items)
	case r.URL.Path == "/api/workflows" && r.Method == http.MethodPost:
		f.creates++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.next++
		body["id"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", f.next)
		body["createdAt"], body["updatedAt"] = "2026-10-01T00:00:00Z", "2026-10-01T00:00:00Z"
		f.items = append(f.items, body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(body)
	case strings.HasPrefix(r.URL.Path, "/api/workflows/") && strings.HasSuffix(r.URL.Path, "/logs"):
		_, _ = fmt.Fprint(w, f.logs)
	case strings.HasPrefix(r.URL.Path, "/api/workflows/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/workflows/")
		index := -1
		for i, item := range f.items {
			if item["id"] == id {
				index = i
				break
			}
		}
		switch r.Method {
		case http.MethodGet:
			if index < 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(f.items[index])
		case http.MethodPut:
			if index < 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.updates++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body["id"] = id
			body["createdAt"], body["updatedAt"] = "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z"
			f.items[index] = body
			_ = json.NewEncoder(w).Encode(body)
		case http.MethodDelete:
			if index < 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.deletes++
			f.items = append(f.items[:index], f.items[index+1:]...)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	case r.URL.Path == "/api/search/metadata":
		f.scanCalls++
		count, items := 0, "[]"
		if f.asset != "" {
			count, items = 1, "["+assetJSON(f.asset, f.trashed)+"]"
		}
		_, _ = fmt.Fprintf(w, `{"assets":{"count":%d,"items":%s,"nextPage":null}}`, count, items)
	default:
		sourceRead(w, r)
	}
}

func assetJSON(id string, trashed bool) string {
	return fmt.Sprintf(`{"id":%q,"type":"IMAGE","originalFileName":"private.jpg","originalMimeType":"image/jpeg",
		"checksum":"qZk+NkcGgWq6PiVxeFDCbJzQ2J0=","libraryId":null,"visibility":"timeline",
		"isOffline":false,"isTrashed":%t,"exifInfo":{"fileSizeInByte":3},
		"fileCreatedAt":"2026-10-01T12:00:00Z","updatedAt":"2026-10-02T12:00:00Z"}`, id, trashed)
}

type workflowHarness struct {
	cfg  config.Config
	api  *fakeImmichWorkflows
	logs *bytes.Buffer
}

func newWorkflowHarness(t *testing.T, managed bool) *workflowHarness {
	t.Helper()
	h := &workflowHarness{api: newFakeImmich(), logs: &bytes.Buffer{}}
	source := httptest.NewServer(h.api)
	t.Cleanup(source.Close)
	h.cfg = setupConfig(t)
	h.cfg.Accounts = nil
	h.cfg.Accounts = []config.Account{{
		ID: "private-account", ImmichURL: source.URL, APIKeyFile: writeSecret(t, "source-key"),
		AmazonRegion: "us", CookieFile: writeSecret(t, "session-id=test; ubid-main=test; at-main=test"),
		AlbumIDs: []string{albumID},
	}}
	account := h.cfg.Accounts[0]
	if managed {
		account.ManagedWorkflow = true
		account.WorkflowAPIKeyFile = writeSecret(t, "workflow-key")
		account.WorkflowCallbackURL = "https://sync.example.net/events/private-account"
		account.WorkflowSecretFile = filepath.Join(t.TempDir(), "secrets", "workflow.secret")
	}
	h.cfg.Accounts[0] = account
	return h
}

func writeSecret(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (h *workflowHarness) open(t *testing.T) *service.Service {
	t.Helper()
	s, err := service.Open(h.cfg, h.logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestManagedWorkflowRegistersExactlyOneAndKeepsPolling(t *testing.T) {
	amazonReads(t)
	h := newWorkflowHarness(t, true)
	s := h.open(t)
	report, err := s.Once(t.Context(), "")
	if err != nil {
		t.Fatalf("cycle: %v; report=%+v", err, report)
	}
	if report.Accounts[0].Status.Workflow != "active" {
		t.Fatalf("workflow not active: %+v logs=%s", report.Accounts[0].Status, h.logs.String())
	}
	items := h.api.workflowObjects()
	if len(items) != 1 || h.api.creates != 1 {
		t.Fatalf("expected exactly one workflow: creates=%d items=%d", h.api.creates, len(items))
	}
	step := items[0]["steps"].([]any)[0].(map[string]any)
	config := step["config"].(map[string]any)
	if config["url"] != "https://sync.example.net/events/private-account" ||
		config["headerValue"] != "Bearer "+readFile(t, h.cfg.Accounts[0].WorkflowSecretFile) {
		t.Fatalf("callback contract mismatch: %+v", config)
	}
	info, err := os.Stat(h.cfg.Accounts[0].WorkflowSecretFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("generated secret is not file-backed 0600: %v %v", info, err)
	}
	if h.api.scanCalls == 0 {
		t.Fatal("periodic polling stopped when the workflow became active")
	}
	// A second cycle verifies in place instead of creating another workflow.
	if _, err := s.Once(t.Context(), ""); err != nil || h.api.creates != 1 || len(h.api.workflowObjects()) != 1 {
		t.Fatalf("duplicate workflow: creates=%d err=%v", h.api.creates, err)
	}
	text := h.logs.String()
	for _, private := range []string{
		"private-account", "source-key", "workflow-key",
		"https://sync.example.net/events/private-account", readFile(t, h.cfg.Accounts[0].WorkflowSecretFile),
		items[0]["id"].(string),
	} {
		if strings.Contains(text, private) {
			t.Fatalf("workflow logs leaked %q: %s", private, text)
		}
	}
}

func TestManagedWorkflowVersionFallbackPollsWithoutFailureLoop(t *testing.T) {
	amazonReads(t)
	h := newWorkflowHarness(t, true)
	h.api.version = `{"major":2,"minor":0,"patch":0}`
	s := h.open(t)
	report, err := s.Once(t.Context(), "")
	if err != nil {
		t.Fatalf("version fallback failed the cycle: %v", err)
	}
	status := report.Accounts[0].Status
	if status.Workflow != "polling" || status.WorkflowCode != "workflow_version_unsupported" {
		t.Fatalf("missing capability notice: %+v", status)
	}
	if h.api.creates != 0 || h.api.updates != 0 || h.api.deletes != 0 {
		t.Fatal("version fallback mutated the remote")
	}
	if h.api.scanCalls == 0 {
		t.Fatal("version fallback disabled polling")
	}
	probes := h.api.permissionsProbes
	if _, err := s.Once(t.Context(), ""); err != nil || h.api.permissionsProbes != probes {
		t.Fatalf("fallback re-probed in a loop: probes=%d err=%v", h.api.permissionsProbes, err)
	}
}

func TestManagedWorkflowPermissionFallbackNotice(t *testing.T) {
	amazonReads(t)
	h := newWorkflowHarness(t, true)
	h.api.permissions = []string{"workflow.read"}
	s := h.open(t)
	report, err := s.Once(t.Context(), "")
	if err != nil {
		t.Fatalf("permission fallback failed the cycle: %v", err)
	}
	status := report.Accounts[0].Status
	if status.Workflow != "polling" || status.WorkflowCode != "workflow_permission_missing" {
		t.Fatalf("missing permission notice: %+v", status)
	}
	if h.api.creates != 0 {
		t.Fatal("missing permission created a workflow")
	}
}

func TestManagedWorkflowRotationUpdatesOnlyTheOwnedWorkflow(t *testing.T) {
	amazonReads(t)
	h := newWorkflowHarness(t, true)
	unrelated := h.api.addUnrelated()
	s := h.open(t)
	if _, err := s.Once(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	owned := ""
	for _, item := range h.api.workflowObjects() {
		if item["name"] == "immich-amazon-sync" {
			owned = item["id"].(string)
		}
	}
	if owned == "" {
		t.Fatal("setup did not create the owned workflow")
	}
	h.cfg.Accounts[0].WorkflowCallbackURL = "https://sync.example.net/reverse-proxy/events/private-account"
	if err := s.Reload(h.cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Once(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if h.api.updates != 1 || h.api.deletes != 0 || h.api.creates != 1 {
		t.Fatalf("rotation calls: updates=%d deletes=%d creates=%d", h.api.updates, h.api.deletes, h.api.creates)
	}
	items := h.api.workflowObjects()
	if len(items) != 2 {
		t.Fatalf("unrelated workflow disappeared: %d", len(items))
	}
	for _, item := range items {
		if item["id"] == unrelated {
			if item["name"] != "unrelated" {
				t.Fatal("unrelated workflow was mutated")
			}
			continue
		}
		if item["id"] != owned {
			t.Fatalf("rotation addressed a different workflow: %v", item["id"])
		}
		step := item["steps"].([]any)[0].(map[string]any)["config"].(map[string]any)
		if step["url"] != h.cfg.Accounts[0].WorkflowCallbackURL {
			t.Fatalf("callback not rotated: %+v", step)
		}
	}
}

func TestManagedWorkflowCleanupOnDisableAndRemoval(t *testing.T) {
	amazonReads(t)
	h := newWorkflowHarness(t, true)
	unrelated := h.api.addUnrelated()
	s := h.open(t)
	if _, err := s.Once(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if len(h.api.workflowObjects()) != 2 {
		t.Fatal("setup did not create the owned workflow")
	}
	// Disabling managed workflow removes only ours.
	h.cfg.Accounts[0].ManagedWorkflow = false
	if err := s.Reload(h.cfg); err != nil {
		t.Fatal(err)
	}
	items := h.api.workflowObjects()
	if len(items) != 1 || items[0]["id"] != unrelated || h.api.deletes != 1 {
		t.Fatalf("disable cleanup: deletes=%d items=%+v", h.api.deletes, items)
	}
	// Re-enable and register again, then remove the account entirely.
	h.cfg.Accounts[0].ManagedWorkflow = true
	if err := s.Reload(h.cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Once(t.Context(), ""); err != nil || h.api.creates != 2 {
		t.Fatalf("re-register: creates=%d err=%v", h.api.creates, err)
	}
	h.cfg.Accounts = nil
	if err := s.Reload(h.cfg); err != nil {
		t.Fatal(err)
	}
	items = h.api.workflowObjects()
	if len(items) != 1 || items[0]["id"] != unrelated || h.api.deletes != 2 {
		t.Fatalf("removal cleanup: deletes=%d items=%+v", h.api.deletes, items)
	}
}

func TestManagedWorkflowSecretAuthenticatesOnlyItsAccountAndHintsAreRevalidated(t *testing.T) {
	amazonReads(t)
	h := newWorkflowHarness(t, true)
	s := h.open(t)
	if _, err := s.Once(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	secret := readFile(t, h.cfg.Accounts[0].WorkflowSecretFile)
	const id = "33333333-3333-4333-8333-333333333333"
	native := `{"type":"AssetV1","trigger":"AssetCreate","config":{"url":"http://evil.invalid"},
		"workflow":{"id":"private-workflow","authToken":"private-token","stepId":"private-step"},
		"data":{"asset":{"id":"` + id + `","originalPath":"/private/path","url":"http://evil.invalid","ownerId":"private-owner"}}}`
	for _, tc := range []struct {
		account, token string
		code           int
	}{
		{"private-account", secret, 202},
		{"private-account", strings.Repeat("w", 32), 401},
		// An unknown account has no configured credential, so the endpoint is
		// unavailable rather than authenticated.
		{"other-account", secret, 503},
	} {
		r := httptest.NewRequest("POST", "/events/"+tc.account, strings.NewReader(native))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s: status=%d want=%d", tc.account, w.Code, tc.code)
		}
	}
	// The hint carries only the UUID; the source decides scope and the asset is
	// out of the configured album, so no remote write is attempted.
	if _, err := s.Once(t.Context(), ""); err != nil {
		t.Fatalf("hint revalidation cycle: %v", err)
	}
	status, err := s.Status(t.Context(), "private-account")
	if err != nil || status.EventsPending != 0 || status.Confirmed != 0 || status.Existing != 0 {
		t.Fatalf("out-of-scope hint was not revalidated: %+v %v", status, err)
	}
	// A stale secret after cleanup grants nothing.
	h.cfg.Accounts[0].ManagedWorkflow = false
	if err := s.Reload(h.cfg); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/events/private-account", strings.NewReader(native))
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("removed workflow secret accepted: %d", w.Code)
	}
}

func TestManagedWorkflowDeletionRegression(t *testing.T) {
	const trashedID = "aaaaaaaa-3333-4333-8333-333333333333"
	for name, tt := range map[string]struct {
		deletions bool
		trash     int32
		wantCode  string
	}{
		"deletion-disabled": {deletions: false, trash: 0, wantCode: "ok"},
		"deletion-enabled":  {deletions: true, trash: 1, wantCode: "ok"},
	} {
		t.Run(name, func(t *testing.T) {
			var trashCalls atomic.Int32
			amazonTrashTarget(t, &trashCalls)
			h := newWorkflowHarness(t, true)
			h.api.asset, h.api.trashed = trashedID, true
			h.cfg.Accounts[0].Deletions = tt.deletions
			if tt.deletions {
				h.cfg.Accounts[0].DeletionConfirmation = config.DeletionConfirmationPhrase
			}
			seedConfirmedMappings(t, h.cfg.DataDir, h.cfg.Accounts[0].ID, trashedID)
			s := h.open(t)
			report, err := s.Once(t.Context(), "")
			if service.Code(err) != tt.wantCode {
				t.Fatalf("cycle: %+v %v", report, err)
			}
			if trashCalls.Load() != tt.trash {
				t.Fatalf("trash calls=%d want=%d report=%+v err=%v logs=%s",
					trashCalls.Load(), tt.trash, report, err, h.logs.String())
			}
			if report.Accounts[0].Status.Workflow != "active" {
				t.Fatalf("workflow not active: %+v", report.Accounts[0].Status)
			}
			if h.api.creates != 1 {
				t.Fatalf("workflow creates=%d", h.api.creates)
			}
		})
	}
}

// seedConfirmedMappings creates eleven durable mappings so one pending deletion
// stays below the 10% guard.
func seedConfirmedMappings(t *testing.T, dir, account, trashedID string) {
	t.Helper()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metadata, err := json.Marshal(bridge.Asset{
		ID: trashedID, Name: "private.jpg", MIME: "image/jpeg", Kind: "IMAGE",
		Size: 3, SHA1: "qZk+NkcGgWq6PiVxeFDCbJzQ2J0=",
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO accounts(id) VALUES(?) ON CONFLICT DO NOTHING`, account); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO rules(account_id,scope,album_id,name,complete,expected,generation)
		VALUES(?,?,?,?,1,1,1) ON CONFLICT(account_id,scope) DO UPDATE SET complete=1`,
		account, "album:"+albumID, albumID, "private"); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		id := fmt.Sprintf("bbbbbbbb-3333-4333-8333-%012d", i)
		if _, err = tx.Exec(`INSERT INTO assets(account_id,id,metadata,state,md5,sha1,size,node_id)
			VALUES(?,?,?,'confirmed','5d41402abc4b2a76b9719d911017c592','qZk+NkcGgWq6PiVxeFDCbJzQ2J0=',3,?)
			ON CONFLICT(account_id,id) DO NOTHING`, account, id, string(metadata), "node-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO assets(account_id,id,metadata,state,md5,sha1,size,node_id)
		VALUES(?,?,?,'confirmed','900150983cd24fb0d6963f7d28e17f72','qZk+NkcGgWq6PiVxeFDCbJzQ2J0=',3,'node-a')
		ON CONFLICT(account_id,id) DO UPDATE SET state='confirmed',node_id='node-a'`,
		account, trashedID, string(metadata)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func amazonTrashTarget(t *testing.T, calls *atomic.Int32) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "127.0.0.1" {
			return original.RoundTrip(r)
		}
		respond := func(body string) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r,
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/account/usage") && r.Method == http.MethodGet:
			return respond(`{"lastCalculated":"2026-10-20T08:30:00.000Z","photo":{"billable":{"bytes":1,"count":1},"total":{"bytes":1,"count":1}},"video":{"billable":{"bytes":0,"count":0},"total":{"bytes":0,"count":0}},"other":{"billable":{"bytes":0,"count":0},"total":{"bytes":0,"count":0}}}`)
		case strings.HasSuffix(r.URL.Path, "/nodes") && r.URL.Query().Get("filters") == "isRoot:true":
			return respond(`{"data":[{"id":"root","isRoot":true,"kind":"FOLDER","status":"AVAILABLE","ownerId":"owner"}],"count":1}`)
		case strings.HasSuffix(r.URL.Path, "/nodes/node-a") && r.Method == http.MethodGet:
			status := "AVAILABLE"
			if calls.Load() > 0 {
				status = "TRASH"
			}
			return respond(`{"id":"node-a","name":"private.jpg","kind":"FILE","status":"` + status + `",
				"parents":["root"],"ownerId":"owner","contentProperties":{"md5":"900150983cd24fb0d6963f7d28e17f72","size":3}}`)
		case strings.HasSuffix(r.URL.Path, "/trash") && r.Method == http.MethodPatch:
			calls.Add(1)
			return respond(``)
		default:
			t.Errorf("unexpected Amazon request: %s %s", r.Method, r.URL.Path)
			return &http.Response{StatusCode: 500, Header: make(http.Header), Request: r,
				Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}
