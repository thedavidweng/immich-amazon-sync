package admin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func TestAdminSavesManagedWorkflowSettingsWithoutDisplayingSecrets(t *testing.T) {
	d := openDashboard(t)
	csrf := d.login(t)
	dir := filepath.Dir(d.path)
	form := d.accountForm(t, csrf, "family")
	form.Set("managed_workflow", "on")
	form.Set("workflow_key_file", filepath.Join(dir, "workflow-key"))
	form.Set("workflow_callback_url", "https://sync.example.net/events/family")
	form.Set("workflow_secret_file", filepath.Join(dir, "workflow.secret"))
	if status, body := d.post(t, "/accounts/save", form); status != 200 {
		t.Fatalf("managed workflow save: %d %s", status, body)
	}
	cfg, err := config.Load(d.path)
	if err != nil || len(cfg.Accounts) != 1 || !cfg.Accounts[0].ManagedWorkflow ||
		cfg.Accounts[0].WorkflowCallbackURL != "https://sync.example.net/events/family" ||
		cfg.Accounts[0].WorkflowAPIKeyFile != filepath.Join(dir, "workflow-key") {
		t.Fatalf("managed workflow settings not persisted: %+v %v", cfg.Accounts, err)
	}
	_, index := d.get(t, "/")
	if !strings.Contains(index, "Managed workflow") || !strings.Contains(index, "requested (pending first cycle)") ||
		!strings.Contains(index, "always enabled") {
		t.Fatalf("dashboard missing workflow state: %s", index)
	}
	for _, private := range []string{"workflow.secret", "workflow-key"} {
		if strings.Contains(index, private) {
			t.Fatalf("dashboard leaked %q", private)
		}
	}
	_, editor := d.get(t, "/accounts/family/edit")
	for _, field := range []string{`name="managed_workflow"`, `name="workflow_key_file"`, `name="workflow_callback_url"`, `name="workflow_secret_file"`} {
		if !strings.Contains(editor, field) {
			t.Fatalf("editor missing %s", field)
		}
	}
	// A callback that cannot reach this account's event endpoint is rejected
	// before persistence and reload.
	previous, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	bad := d.accountForm(t, csrf, "family")
	bad.Set("original_id", "family")
	bad.Set("managed_workflow", "on")
	bad.Set("workflow_key_file", filepath.Join(dir, "workflow-key"))
	bad.Set("workflow_callback_url", "https://elsewhere.invalid/events/other-account")
	bad.Set("workflow_secret_file", filepath.Join(dir, "workflow.secret"))
	if status, body := d.post(t, "/accounts/save", bad); status != 400 || !strings.Contains(body, "workflow_callback_url_invalid") {
		t.Fatalf("unsafe callback accepted: %d %s", status, body)
	}
	current, err := os.ReadFile(d.path)
	if err != nil || string(current) != string(previous) {
		t.Fatal("rejected workflow settings changed the persisted config")
	}
}
