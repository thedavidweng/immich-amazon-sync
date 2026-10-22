package config_test

import (
	"os"
	"path/filepath"
	"slices"
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

func TestUnknownAmazonRegionIsAConfigurationError(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("IMMICH_API_KEY", "source-secret")
	t.Setenv("AMAZON_REGION", "unknown-marketplace")
	t.Setenv("AMAZON_COOKIE", "destination-secret")
	t.Setenv("ALBUM_IDS", "album-one")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || err.Error() != "amazon_region_invalid" {
		t.Fatalf("unknown marketplace accepted: %v", err)
	}
}

func TestAdminSecretFileConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"admin_password_file":"/run/secrets/admin"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminPasswordFile != "/run/secrets/admin" {
		t.Fatalf("admin secret path lost: %q", cfg.AdminPasswordFile)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(path)
	if err != nil || saved.AdminPasswordFile != cfg.AdminPasswordFile {
		t.Fatalf("admin configuration round trip: %v", err)
	}
}

func deletionConfig(withPhrase string) config.Config {
	return config.Config{
		DataDir: "data", Listen: "127.0.0.1:8080", ScanInterval: "1m", Workers: 1,
		MaxStagingBytes: 1, MaxStagingFiles: 1, LogLevel: "info",
		Accounts: []config.Account{{
			ID: "account", ImmichURL: "http://immich.local", APIKeyFile: "key",
			AmazonRegion: "ca", CookieFile: "cookie", AlbumIDs: []string{"album"},
			Deletions: true, DeletionConfirmation: withPhrase,
		}},
	}
}

func TestDeletionPropagationRequiresTypedConfirmation(t *testing.T) {
	if err := deletionConfig("").Validate(); err == nil || err.Error() != "deletion_confirmation_required" {
		t.Fatalf("missing phrase: %v", err)
	}
	if err := deletionConfig("enable deletions").Validate(); err == nil || err.Error() != "deletion_confirmation_required" {
		t.Fatalf("wrong phrase: %v", err)
	}
	if err := deletionConfig(config.DeletionConfirmationPhrase).Validate(); err != nil {
		t.Fatalf("exact phrase rejected: %v", err)
	}
	off := deletionConfig("")
	off.Accounts[0].Deletions = false
	if err := off.Validate(); err != nil {
		t.Fatalf("disabled deletion was gated: %v", err)
	}
}

func legacyConfig(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"accounts":[{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"/run/secrets/key","amazon_region":"us","amazon_cookie_file":"/run/secrets/cookie","album_ids":["album-one"]` + extra + `}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLegacyVideoToggleMigratesWithIdenticalScope(t *testing.T) {
	for _, tt := range []struct {
		name   string
		extra  string
		expect []string
	}{
		{"absent", "", []string{"image"}},
		{"images only", `,"include_videos":false`, []string{"image"}},
		{"videos included", `,"include_videos":true`, []string{"image", "video"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := legacyConfig(t, tt.extra)
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			policy := cfg.Accounts[0].Policy()
			if !slices.Equal(policy.MediaTypes, tt.expect) || !policy.ArchivedIncluded() {
				t.Fatalf("migrated policy changed scope: %+v", policy)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			saved := filepath.Join(t.TempDir(), "saved.json")
			if err := cfg.Save(saved); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "include_videos") {
				t.Fatal("legacy toggle was persisted instead of migrated")
			}
			reloaded, err := config.Load(saved)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(reloaded.Accounts[0].Policy().MediaTypes, tt.expect) {
				t.Fatalf("saved policy lost scope: %+v", reloaded.Accounts[0].Policy())
			}
		})
	}
}

func TestExplicitFiltersAreValidatedAndConflictWithLegacyToggle(t *testing.T) {
	const tag = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	for name, tt := range map[string]struct {
		extra string
		code  string
	}{
		"conflict":       {`,"include_videos":true,"filters":{"media_types":["image"]}`, "filter_conflict"},
		"media type":     {`,"filters":{"media_types":["audio"]}`, "filter_media_type_invalid"},
		"empty media":    {`,"filters":{"skip_raw":true}`, "filter_media_type_invalid"},
		"negative size":  {`,"filters":{"media_types":["image"],"max_original_bytes":-1}`, "filter_max_original_bytes_invalid"},
		"invalid tag":    {`,"filters":{"media_types":["image"],"excluded_tags":["private"]}`, "filter_excluded_tags_invalid"},
		"duplicate tag":  {`,"filters":{"media_types":["image"],"excluded_tags":["` + tag + `","` + tag + `"]}`, "filter_excluded_tags_invalid"},
		"unknown filter": {`,"filters":{"media_types":["image"],"unexpected":true}`, "config_invalid_json"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(legacyConfig(t, tt.extra))
			if err == nil || err.Error() != tt.code {
				t.Fatalf("got %v, want %s", err, tt.code)
			}
		})
	}
	cfg, err := config.Load(legacyConfig(t, `,"filters":{"media_types":["image","video"],"max_original_bytes":100,"skip_raw":true,"include_archived":false,"excluded_tags":["`+tag+`"]}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.Accounts[0].Policy()
	if !slices.Equal(policy.MediaTypes, []string{"image", "video"}) || policy.MaxOriginalBytes != 100 ||
		!policy.SkipRAW || policy.ArchivedIncluded() || !slices.Equal(policy.ExcludedTags, []string{tag}) {
		t.Fatalf("explicit filters were not retained: %+v", policy)
	}
}

func TestFilterEnvironmentMigratesAndRejectsInvalidValues(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("IMMICH_API_KEY", "source-secret")
	t.Setenv("AMAZON_REGION", "ca")
	t.Setenv("AMAZON_COOKIE", "destination-secret")
	t.Setenv("ALBUM_IDS", "album-one")
	t.Setenv("INCLUDE_VIDEOS", "true")
	t.Setenv("MAX_ORIGINAL_BYTES", "100")
	t.Setenv("SKIP_RAW", "true")
	t.Setenv("INCLUDE_ARCHIVED", "false")
	t.Setenv("EXCLUDED_TAGS", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.Accounts[0].Policy()
	if !slices.Equal(policy.MediaTypes, []string{"image", "video"}) || policy.MaxOriginalBytes != 100 ||
		!policy.SkipRAW || policy.ArchivedIncluded() || len(policy.ExcludedTags) != 1 {
		t.Fatalf("environment filters were not parsed: %+v", policy)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	for name, tt := range map[string]struct {
		key, value, videos, code string
	}{
		"video":    {"INCLUDE_VIDEOS", "sometimes", "", "include_videos_invalid"},
		"media":    {"MEDIA_TYPES", "audio", "", "filter_media_type_invalid"},
		"size":     {"MAX_ORIGINAL_BYTES", "-1", "true", "filter_max_original_bytes_invalid"},
		"raw":      {"SKIP_RAW", "sometimes", "true", "filter_skip_raw_invalid"},
		"archived": {"INCLUDE_ARCHIVED", "sometimes", "true", "filter_include_archived_invalid"},
		"excluded": {"EXCLUDED_TAGS", "private", "true", "filter_excluded_tags_invalid"},
		"conflict": {"MEDIA_TYPES", "image", "true", "filter_conflict"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if tt.key != "INCLUDE_VIDEOS" {
				t.Setenv("INCLUDE_VIDEOS", tt.videos)
			}
			_, err := config.Load("")
			if err == nil || err.Error() != tt.code {
				t.Fatalf("got %v, want %s", err, tt.code)
			}
		})
	}
}
