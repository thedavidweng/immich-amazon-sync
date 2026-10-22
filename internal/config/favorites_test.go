package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func favoritesConfig(t *testing.T, favorites string) string {
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
	if favorites != "" {
		value = `,"favorites":"` + favorites + `"`
	}
	path := filepath.Join(dir, "config.json")
	body := `{"accounts":[{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"` + key +
		`","amazon_region":"ca","amazon_cookie_file":"` + cookie + `","album_ids":["album-one"]` + value + `}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Favorites are off for every existing configuration and only the explicit
// add_only value turns the write path on.
func TestFavoritesDefaultOffAndExplicitAddOnly(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  string
	}{
		{"", ""},
		{"off", "off"},
		{"add_only", "add_only"},
	} {
		t.Run("value="+tt.value, func(t *testing.T) {
			cfg, err := config.Load(favoritesConfig(t, tt.value))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if got := cfg.Accounts[0].Favorites; got != tt.want {
				t.Fatalf("favorites = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnknownFavoritesModeIsRejected(t *testing.T) {
	cfg, err := config.Load(favoritesConfig(t, "mirror"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || err.Error() != "favorites_invalid" {
		t.Fatalf("unknown favorites mode accepted: %v", err)
	}
}
