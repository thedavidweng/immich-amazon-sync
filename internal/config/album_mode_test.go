package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func albumJSON(fields string) string {
	return `{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"/run/secrets/key",
	"amazon_region":"us","amazon_cookie_file":"/run/secrets/cookie"` + fields + `}`
}

func loadAccount(t *testing.T, fields string) (config.Account, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"accounts":[`+albumJSON(fields)+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Account{}, err
	}
	if err := cfg.Validate(); err != nil {
		return config.Account{}, err
	}
	return cfg.Accounts[0], nil
}

func TestLegacyMirrorAlbumsMigratesToSelectedNeverAllAccessible(t *testing.T) {
	account, err := loadAccount(t, `,"album_ids":["one"],"mirror_albums":true`)
	if err != nil {
		t.Fatal(err)
	}
	if account.AlbumMode() != bridge.AlbumSyncSelected {
		t.Fatalf("legacy mirroring became %q", account.AlbumMode())
	}
	// Saving the loaded account again keeps the explicit selected scope.
	if err := (config.Config{DataDir: "data", Listen: "127.0.0.1:1", ScanInterval: "1m",
		Workers: 1, MaxStagingBytes: 1, MaxStagingFiles: 1, LogLevel: "info",
		Accounts: []config.Account{account}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMirrorOffStaysOff(t *testing.T) {
	for _, fields := range []string{`,"album_ids":["one"]`, `,"album_ids":["one"],"mirror_albums":false`} {
		account, err := loadAccount(t, fields)
		if err != nil {
			t.Fatal(err)
		}
		if account.AlbumMode() != bridge.AlbumSyncOff {
			t.Fatalf("%s became %q", fields, account.AlbumMode())
		}
	}
}

func TestAllAccessibleWorksWithLibraryAndSelectedAlbums(t *testing.T) {
	library, err := loadAccount(t, `,"library":true,"album_sync_mode":"all_accessible"`)
	if err != nil || library.AlbumMode() != bridge.AlbumSyncAllAccessible || !library.Library {
		t.Fatalf("library all-accessible rejected: %+v %v", library, err)
	}
	selected, err := loadAccount(t, `,"album_ids":["one"],"album_sync_mode":"all_accessible"`)
	if err != nil || selected.AlbumMode() != bridge.AlbumSyncAllAccessible {
		t.Fatalf("album-backed all-accessible rejected: %+v %v", selected, err)
	}
}

func TestUnknownAlbumModeIsRejected(t *testing.T) {
	if _, err := loadAccount(t, `,"album_ids":["one"],"album_sync_mode":"everything"`); err == nil ||
		err.Error() != "album_sync_mode_invalid" {
		t.Fatalf("unknown mode accepted: %v", err)
	}
}

func TestLegacyMirrorConflictsWithNonSelectedMode(t *testing.T) {
	if _, err := loadAccount(t, `,"library":true,"album_sync_mode":"all_accessible","mirror_albums":true`); err == nil ||
		err.Error() != "album_sync_mode_conflict" {
		t.Fatalf("widening legacy migration accepted: %v", err)
	}
	if _, err := loadAccount(t, `,"album_ids":["one"],"album_sync_mode":"off","mirror_albums":true`); err == nil ||
		err.Error() != "album_sync_mode_conflict" {
		t.Fatalf("conflicting legacy boolean accepted: %v", err)
	}
	account, err := loadAccount(t, `,"album_ids":["one"],"album_sync_mode":"selected","mirror_albums":true`)
	if err != nil || account.AlbumMode() != bridge.AlbumSyncSelected {
		t.Fatalf("consistent legacy boolean rejected: %+v %v", account, err)
	}
}

func TestSelectedRequiresExplicitAlbumsAndNoLibrary(t *testing.T) {
	if _, err := loadAccount(t, `,"library":true,"album_sync_mode":"selected"`); err == nil ||
		err.Error() != "source_scope_conflict" {
		t.Fatalf("library selected accepted: %v", err)
	}
	if _, err := loadAccount(t, `,"library":true,"album_ids":["one"],"album_sync_mode":"selected"`); err == nil ||
		err.Error() != "source_scope_conflict" {
		t.Fatalf("ambiguous selected accepted: %v", err)
	}
}

func TestAlbumModeEnvironment(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("IMMICH_API_KEY", "source-secret")
	t.Setenv("AMAZON_REGION", "us")
	t.Setenv("AMAZON_COOKIE", "destination-secret")
	t.Setenv("LIBRARY", "true")
	t.Setenv("ALBUM_SYNC_MODE", "all_accessible")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if account := cfg.Accounts[0]; !account.Library || account.AlbumMode() != bridge.AlbumSyncAllAccessible {
		t.Fatalf("environment mode lost: %+v", account)
	}
	t.Setenv("LIBRARY", "sometimes")
	if _, err := config.Load(""); err == nil || err.Error() != "library_invalid" {
		t.Fatalf("invalid library value accepted: %v", err)
	}
}
