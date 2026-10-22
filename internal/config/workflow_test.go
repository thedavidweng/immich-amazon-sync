package config_test

import (
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func workflowConfig() config.Config {
	return config.Config{
		DataDir: "data", Listen: "127.0.0.1:8080", ScanInterval: "1m", Workers: 1,
		MaxStagingBytes: 1, MaxStagingFiles: 1, LogLevel: "info",
		Accounts: []config.Account{{
			ID: "private-account", ImmichURL: "http://immich.local", APIKeyFile: "key",
			AmazonRegion: "ca", CookieFile: "cookie", AlbumIDs: []string{"album"},
			ManagedWorkflow: true, WorkflowAPIKeyFile: "workflow-key",
			WorkflowCallbackURL: "https://sync.example.net/events/private-account",
			WorkflowSecretFile:  filepath.Join("secrets", "workflow.secret"),
		}},
	}
}

func TestManagedWorkflowIsOffByDefaultAndValidatesExplicitOptIn(t *testing.T) {
	disabled := workflowConfig()
	disabled.Accounts[0] = config.Account{
		ID: "account", ImmichURL: "http://immich.local", APIKeyFile: "key",
		AmazonRegion: "ca", CookieFile: "cookie", AlbumIDs: []string{"album"},
	}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("existing account needed workflow fields: %v", err)
	}
	if disabled.Accounts[0].ManagedWorkflow {
		t.Fatal("workflow management must be opt-in")
	}
	if err := workflowConfig().Validate(); err != nil {
		t.Fatalf("valid managed workflow rejected: %v", err)
	}
}

func TestManagedWorkflowRequiresASeparateCredentialCallbackAndSecret(t *testing.T) {
	for name, mutate := range map[string]func(*config.Account){
		"credit-key":  func(a *config.Account) { a.WorkflowAPIKeyFile = "" },
		"callback":    func(a *config.Account) { a.WorkflowCallbackURL = "" },
		"secret-file": func(a *config.Account) { a.WorkflowSecretFile = "" },
		"shared-key":  func(a *config.Account) { a.WorkflowAPIKeyFile = a.APIKeyFile },
		"wrong-path":  func(a *config.Account) { a.WorkflowCallbackURL = "https://sync.example.net/events/other" },
		"query-string": func(a *config.Account) {
			a.WorkflowCallbackURL = "https://sync.example.net/events/private-account?token=x"
		},
		"plain-http-ok": func(a *config.Account) { a.WorkflowCallbackURL = "http://sync.example.net/events/private-account" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := workflowConfig()
			mutate(&cfg.Accounts[0])
			err := cfg.Validate()
			switch name {
			case "plain-http-ok":
				if err != nil {
					t.Fatalf("valid config rejected: %v", err)
				}
			case "shared-key":
				if err == nil || err.Error() != "workflow_credentials_conflict" {
					t.Fatalf("shared credential accepted: %v", err)
				}
			case "wrong-path", "query-string":
				if err == nil || err.Error() != "workflow_callback_url_invalid" {
					t.Fatalf("unsafe callback accepted: %v", err)
				}
			default:
				if err == nil || err.Error() != "workflow_configuration_required" {
					t.Fatalf("incomplete workflow config accepted: %v", err)
				}
			}
		})
	}
}

func TestDisabledWorkflowLeavesMountedPathsForCleanup(t *testing.T) {
	cfg := workflowConfig()
	cfg.Accounts[0].ManagedWorkflow = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabling management blocked cleanup credentials: %v", err)
	}
}

func TestInvalidManagedWorkflowEnvironmentIsAnError(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("MANAGED_WORKFLOW", "sometimes")
	if _, err := config.Load(""); err == nil || err.Error() != "managed_workflow_invalid" {
		t.Fatalf("invalid managed workflow setting accepted: %v", err)
	}
}
