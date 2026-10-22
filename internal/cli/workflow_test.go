package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/cli"
)

type cliWorkflows struct {
	mu      sync.Mutex
	items   []map[string]any
	creates int
}

func (f *cliWorkflows) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/server/version":
		_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":4}`)
	case r.URL.Path == "/api/api-keys/me":
		_, _ = fmt.Fprint(w, `{"permissions":["all"]}`)
	case r.URL.Path == "/api/workflows/triggers":
		_, _ = fmt.Fprint(w, `[{"trigger":"AssetCreate","types":["AssetV1"]}]`)
	case r.URL.Path == "/api/workflows" && r.Method == http.MethodGet:
		if f.items == nil {
			f.items = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(f.items)
	case r.URL.Path == "/api/workflows" && r.Method == http.MethodPost:
		f.creates++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = "00000000-0000-4000-8000-000000000001"
		body["createdAt"], body["updatedAt"] = "2026-10-01", "2026-10-01"
		f.items = append(f.items, body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(body)
	case strings.HasPrefix(r.URL.Path, "/api/workflows/") && strings.HasSuffix(r.URL.Path, "/logs"):
		_, _ = fmt.Fprint(w, `[]`)
	case strings.HasPrefix(r.URL.Path, "/api/workflows/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/workflows/")
		for _, item := range f.items {
			if item["id"] == id {
				_ = json.NewEncoder(w).Encode(item)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		http.NotFound(w, r)
	}
}

func TestWorkflowCommandsAreReadOnlyUntilApply(t *testing.T) {
	api := &cliWorkflows{}
	server := httptest.NewServer(api)
	defer server.Close()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	workflowKey := filepath.Join(dir, "workflow-key")
	if err := os.WriteFile(key, []byte("source-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowKey, []byte("workflow-key"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"data_dir": filepath.Join(dir, "data"),
		"accounts": []map[string]any{{
			"id": "private-account", "immich_url": server.URL, "immich_api_key_file": key,
			"amazon_region": "us", "amazon_cookie_file": key,
			"album_ids":        []string{"11111111-1111-4111-8111-111111111111"},
			"managed_workflow": true, "immich_workflow_api_key_file": workflowKey,
			"workflow_callback_url": "https://sync.example.net/events/private-account",
			"workflow_secret_file":  filepath.Join(dir, "secrets", "workflow.secret"),
		}},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := cli.Execute(context.Background(), append(args, "--config", path), &out, &errOut, "dev")
		return code, out.String(), errOut.String()
	}

	code, out, errOut := run("workflow", "register", "--json")
	if code != 0 || api.creates != 0 || !strings.Contains(out, `"action":"create"`) || errOut != "" {
		t.Fatalf("plan mutated: code=%d out=%q err=%q creates=%d", code, out, errOut, api.creates)
	}
	code, out, errOut = run("workflow", "register", "--apply")
	if code != 0 || api.creates != 1 || !strings.Contains(out, "state=active") || errOut != "" {
		t.Fatalf("apply: code=%d out=%q err=%q creates=%d", code, out, errOut, api.creates)
	}
	code, out, errOut = run("workflow", "status", "--json")
	if code != 0 || !strings.Contains(out, `"state":"active"`) || errOut != "" {
		t.Fatalf("status: code=%d out=%q err=%q", code, out, errOut)
	}
	secret, err := os.ReadFile(filepath.Join(dir, "secrets", "workflow.secret"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"workflow-key", "source-key", "https://sync.example.net/events/private-account", strings.TrimSpace(string(secret))} {
		if strings.Contains(out, private) {
			t.Fatalf("status leaked %q: %s", private, out)
		}
	}
	code, _, errOut = run("workflow", "remove", "--apply")
	if code != 2 || !strings.Contains(errOut, "workflow_management_active") {
		t.Fatalf("remove removed a live workflow: code=%d err=%q", code, errOut)
	}
	code, out, errOut = run("workflow", "logs")
	if code != 0 || !strings.Contains(out, "completed=0") || errOut != "" {
		t.Fatalf("logs: code=%d out=%q err=%q", code, out, errOut)
	}
}
