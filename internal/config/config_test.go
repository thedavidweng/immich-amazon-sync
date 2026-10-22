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

func windowConfig(window *config.TransferWindow) config.Config {
	cfg := deletionConfig("")
	cfg.Accounts[0].Deletions = false
	cfg.Accounts[0].TransferWindow = window
	return cfg
}

func TestTransferWindowValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window *config.TransferWindow
		code   string
	}{
		{"absent", nil, ""},
		{"daytime", &config.TransferWindow{Timezone: "UTC", Start: "09:00", End: "17:00"}, ""},
		{"cross midnight", &config.TransferWindow{Timezone: "America/New_York", Start: "23:00", End: "06:00"}, ""},
		{"equal bounds", &config.TransferWindow{Timezone: "UTC", Start: "09:00", End: "09:00"}, "transfer_window_bounds_invalid"},
		{"unknown zone", &config.TransferWindow{Timezone: "Mars/Olympus", Start: "09:00", End: "17:00"}, "transfer_window_timezone_invalid"},
		{"local zone", &config.TransferWindow{Timezone: "Local", Start: "09:00", End: "17:00"}, "transfer_window_timezone_invalid"},
		{"missing zone", &config.TransferWindow{Start: "09:00", End: "17:00"}, "transfer_window_timezone_invalid"},
		{"malformed time", &config.TransferWindow{Timezone: "UTC", Start: "9:00", End: "17:00"}, "transfer_window_time_invalid"},
		{"missing bound", &config.TransferWindow{Timezone: "UTC", Start: "09:00"}, "transfer_window_time_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := windowConfig(tc.window).Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatalf("valid window rejected: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.code {
				t.Fatalf("window %+v: got %v want %s", tc.window, err, tc.code)
			}
		})
	}
}

func TestTransferWindowJSONRoundTripAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	key, cookie := filepath.Join(dir, "key"), filepath.Join(dir, "cookie")
	if err := os.WriteFile(key, []byte("source-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookie, []byte("session-id=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	handle := func(window string) string {
		return `{"accounts":[{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"` +
			key + `","amazon_region":"ca","amazon_cookie_file":"` + cookie + `","album_ids":["album-one"]` +
			window + `}]}`
	}
	if err := os.WriteFile(path, []byte(handle(`,"transfer_window":{"timezone":"America/New_York","start":"23:00","end":"06:00"}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts[0].TransferWindow == nil ||
		cfg.Accounts[0].TransferWindow.Timezone != "America/New_York" ||
		cfg.Accounts[0].TransferWindow.Start != "23:00" ||
		cfg.Accounts[0].TransferWindow.End != "06:00" {
		t.Fatalf("window not loaded: %+v", cfg.Accounts[0].TransferWindow)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(dir, "saved.json")
	if err := cfg.Save(saved); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(saved)
	if err != nil || !strings.Contains(string(body), `"transfer_window"`) {
		t.Fatalf("window was not persisted: %s %v", body, err)
	}
	roundTrip, err := config.Load(saved)
	if err != nil || roundTrip.Accounts[0].TransferWindow == nil ||
		*roundTrip.Accounts[0].TransferWindow != *cfg.Accounts[0].TransferWindow {
		t.Fatalf("window round trip: %+v %v", roundTrip.Accounts[0].TransferWindow, err)
	}
	if err := os.WriteFile(path, []byte(handle(`,"transfer_window":{"timezone":"UTC","start":"09:00","end":"17:00","extra":1}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || err.Error() != "config_invalid_json" {
		t.Fatalf("unknown window field accepted: %v", err)
	}
}

func TestTransferWindowEnvironmentMode(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("IMMICH_API_KEY", "source-secret")
	t.Setenv("AMAZON_REGION", "ca")
	t.Setenv("AMAZON_COOKIE", "destination-secret")
	t.Setenv("ALBUM_IDS", "album-one")
	t.Setenv("TRANSFER_TIMEZONE", "Europe/Berlin")
	t.Setenv("TRANSFER_WINDOW_START", "22:30")
	t.Setenv("TRANSFER_WINDOW_END", "05:15")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	window := cfg.Accounts[0].TransferWindow
	if window == nil || window.Timezone != "Europe/Berlin" || window.Start != "22:30" || window.End != "05:15" {
		t.Fatalf("environment window not loaded: %+v", window)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("environment window rejected: %v", err)
	}
}

func TestDirectRootsAreOptInAndValidated(t *testing.T) {
	dir := t.TempDir()
	key, cookie := filepath.Join(dir, "key"), filepath.Join(dir, "cookie")
	for path, value := range map[string]string{key: "private-key\n", cookie: "private-cookie\n"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	account := `{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"` + key +
		`","amazon_region":"ca","amazon_cookie_file":"` + cookie + `","library":true`
	path := filepath.Join(dir, "config.json")
	load := func(t *testing.T, body string) (config.Config, error) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"accounts":[`+body+`]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return config.Load(path)
	}
	cfg, err := load(t, account+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("absent direct roots must stay valid and disabled: %v", err)
	}
	if len(cfg.Accounts[0].Roots()) != 0 {
		t.Fatal("direct reads were enabled without an explicit mapping")
	}
	cfg, err = load(t, account+`,"direct_roots":[{"immich_path":"/immich/upload","host_path":"/originals"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("approved mapping rejected: %v", err)
	}
	roots := cfg.Accounts[0].Roots()
	if len(roots) != 1 || roots[0].ImmichPath != "/immich/upload" || roots[0].HostPath != "/originals" {
		t.Fatalf("mapping lost: %+v", roots)
	}
	saved := filepath.Join(dir, "saved.json")
	if err := cfg.Save(saved); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(saved)
	if err != nil || len(reloaded.Accounts[0].DirectRoots) != 1 ||
		reloaded.Accounts[0].DirectRoots[0] != (config.DirectRoot{ImmichPath: "/immich/upload", HostPath: "/originals"}) {
		t.Fatalf("mapping was not persisted: %+v, %v", reloaded.Accounts, err)
	}
	for name, mapping := range map[string]string{
		"relative host root":   `{"immich_path":"/immich/upload","host_path":"originals"}`,
		"relative immich path": `{"immich_path":"upload","host_path":"/originals"}`,
		"unclean immich path":  `{"immich_path":"/immich/upload/","host_path":"/originals"}`,
		"empty mapping":        `{}`,
		"duplicate mapping":    `{"immich_path":"/immich/upload","host_path":"/originals"},{"immich_path":"/immich/upload","host_path":"/other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := load(t, account+`,"direct_roots":[`+mapping+`]}`)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err == nil || err.Error() != "direct_root_invalid" {
				t.Fatalf("unsafe mapping accepted: %v", err)
			}
		})
	}
}
