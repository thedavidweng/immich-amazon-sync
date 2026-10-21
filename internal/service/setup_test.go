package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/service"
)

func TestSetupRequiresExistingWritableParentWithoutCreatingConfig(t *testing.T) {
	dir := t.TempDir()
	adminFile := filepath.Join(dir, "admin")
	if err := os.WriteFile(adminFile, []byte("mounted-admin-passphrase"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMMICH_URL", "")
	t.Setenv("ADMIN_PASSWORD_FILE", adminFile)
	t.Setenv("DATA_DIR", filepath.Join(dir, "state"))
	t.Setenv("LISTEN", "127.0.0.1:0")
	t.Setenv("WEBHOOK_SECRET_FILE", "")

	for _, name := range []string{"missing", "not-directory", "unwritable", "writable"} {
		t.Run(name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			switch name {
			case "not-directory":
				if err := os.WriteFile(parent, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "unwritable", "writable":
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if name == "unwritable" {
					if os.Geteuid() == 0 {
						t.Skip("root can write to a directory without write mode bits")
					}
					if err := os.Chmod(parent, 0500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.Chmod(parent, 0700); err != nil {
							t.Error(err)
						}
					})
				}
			}
			path := filepath.Join(parent, "config.json")
			cfg, err := service.SetupConfig(path)
			if name == "writable" {
				if err != nil || len(cfg.Accounts) != 0 || cfg.AdminPasswordFile != adminFile {
					t.Fatalf("explicit bootstrap rejected: %+v %v", cfg, err)
				}
			} else if err == nil {
				t.Error("setup accepted a parent that cannot persist configuration")
			}
			if _, err := os.Lstat(path); err == nil {
				t.Error("setup created configuration before the first valid submission")
			}
			if name == "writable" || name == "unwritable" {
				entries, err := os.ReadDir(parent)
				if err != nil || len(entries) != 0 {
					t.Errorf("setup left probe files: %v %v", entries, err)
				}
			}
		})
	}
}
