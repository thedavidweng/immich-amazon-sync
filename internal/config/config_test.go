package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func TestExplicitScopeAndSecretFiles(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	cookie := filepath.Join(dir, "cookie")
	if err := os.WriteFile(key, []byte("private-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookie, []byte("private-cookie\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	body := `{"accounts":[{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"` + key + `","amazon_region":"ca","amazon_cookie_file":"` + cookie + `","album_ids":["album-one"]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 1 || cfg.ScanInterval != "15m" || cfg.Workers != 2 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	saved := filepath.Join(dir, "saved.json")
	if err := cfg.Save(saved); err != nil {
		t.Fatal(err)
	}
	savedConfig, err := config.Load(saved)
	if err != nil || savedConfig.Accounts[0].ID != "home" {
		t.Fatalf("persisted configuration lost: %v", err)
	}
	info, err := os.Stat(saved)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("configuration permissions: %v", err)
	}
	secrets, err := cfg.Accounts[0].Secrets()
	if err != nil || secrets.APIKey != "private-key" || secrets.Cookie != "private-cookie" {
		t.Fatalf("secret loading failed: %v", err)
	}
	body = strings.Replace(body, `"album-one"`, "", 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || err.Error() != "source_scope_required" {
		t.Fatalf("empty scope accepted: %v", err)
	}
}

func TestLibraryAndAlbumScopesAreMutuallyExclusive(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("IMMICH_API_KEY", "source-secret")
	t.Setenv("AMAZON_REGION", "ca")
	t.Setenv("AMAZON_COOKIE", "destination-secret")
	t.Setenv("ALBUM_IDS", "album-one")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts[0].Library = true
	if err := cfg.Validate(); err == nil || err.Error() != "source_scope_conflict" {
		t.Fatalf("ambiguous scope accepted: %v", err)
	}
}

func TestInvalidVideoEnvironmentIsAnError(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("INCLUDE_VIDEOS", "sometimes")
	if _, err := config.Load(""); err == nil || err.Error() != "include_videos_invalid" {
		t.Fatalf("invalid video setting accepted: %v", err)
	}
}
