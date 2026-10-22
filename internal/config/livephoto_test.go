package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func livePhotoConfig(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	cookie := filepath.Join(dir, "cookie")
	if err := os.WriteFile(key, []byte("private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookie, []byte("private-cookie"), 0o600); err != nil {
		t.Fatal(err)
	}
	value := ""
	if mode != "" {
		value = `,"live_photo_mode":"` + mode + `"`
	}
	path := filepath.Join(dir, "config.json")
	body := `{"accounts":[{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"` + key +
		`","amazon_region":"ca","amazon_cookie_file":"` + cookie + `","album_ids":["album-one"]` + value + `}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Live Photo pairing is a new mutating behaviour, so the default is still only
// and only the explicit original_pair value enables the companion upload.
func TestLivePhotoModeDefaultsToStillOnly(t *testing.T) {
	cfg, err := config.Load(livePhotoConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].PairMode(); got != bridge.LivePhotoStillOnly {
		t.Fatalf("default live photo mode = %q", got)
	}
	cfg, err = config.Load(livePhotoConfig(t, "original_pair"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].PairMode(); got != bridge.LivePhotoOriginalPair {
		t.Fatalf("explicit live photo mode = %q", got)
	}
	saved := filepath.Join(t.TempDir(), "saved.json")
	if err := cfg.Save(saved); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(saved)
	if err != nil || reloaded.Accounts[0].PairMode() != bridge.LivePhotoOriginalPair {
		t.Fatalf("live photo mode was not persisted: %v", err)
	}
}

func TestUnknownLivePhotoModeIsRejected(t *testing.T) {
	cfg, err := config.Load(livePhotoConfig(t, "mux"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || err.Error() != "live_photo_mode_invalid" {
		t.Fatalf("unknown live photo mode accepted: %v", err)
	}
}

func TestLivePhotoModeFromEnvironment(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  bridge.LivePhotoMode
		fails bool
	}{
		{"", bridge.LivePhotoStillOnly, false},
		{"still_only", bridge.LivePhotoStillOnly, false},
		{"original_pair", bridge.LivePhotoOriginalPair, false},
		{"pair", "", true},
	} {
		t.Run("value="+tt.value, func(t *testing.T) {
			t.Setenv("IMMICH_URL", "http://immich:2283")
			t.Setenv("IMMICH_API_KEY", "private-key")
			t.Setenv("AMAZON_REGION", "ca")
			t.Setenv("AMAZON_COOKIE", "private-cookie")
			t.Setenv("ALBUM_IDS", "album-one")
			t.Setenv("LIVE_PHOTO_MODE", tt.value)
			cfg, err := config.Load("")
			if tt.fails {
				if err == nil || err.Error() != "live_photo_mode_invalid" {
					t.Fatalf("invalid environment mode accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if mode := cfg.Accounts[0].PairMode(); mode != tt.want {
				t.Fatalf("environment mode = %q, want %q", mode, tt.want)
			}
		})
	}
}
