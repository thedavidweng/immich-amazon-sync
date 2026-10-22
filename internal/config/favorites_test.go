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

// Favorites are off for every existing configuration and only an explicit
// mode turns the write path on.
func TestFavoritesDefaultOffAndExplicitModes(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  string
	}{
		{"", ""},
		{"off", "off"},
		{"add_only", "add_only"},
		{"mirror", "mirror"},
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
	cfg, err := config.Load(favoritesConfig(t, "authoritative"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || err.Error() != "favorites_invalid" {
		t.Fatalf("unknown favorites mode accepted: %v", err)
	}
}

// Takeover is a separate recorded authority and is only legal with mirror. It
// is never implied by a mode or a migration.
func TestFavoritesTakeoverRequiresMirror(t *testing.T) {
	for _, tt := range []struct {
		favorites string
		takeover  bool
		want      string
	}{
		{favorites: "mirror", takeover: true},
		{favorites: "mirror", takeover: false},
		{favorites: "add_only", takeover: false},
		{favorites: "add_only", takeover: true, want: "favorites_takeover_requires_mirror"},
		{favorites: "off", takeover: true, want: "favorites_takeover_requires_mirror"},
		{favorites: "", takeover: true, want: "favorites_takeover_requires_mirror"},
	} {
		t.Run(tt.favorites+"/"+map[bool]string{true: "takeover", false: "no-takeover"}[tt.takeover], func(t *testing.T) {
			dir := t.TempDir()
			key := filepath.Join(dir, "key")
			cookie := filepath.Join(dir, "cookie")
			if err := os.WriteFile(key, []byte("private-key"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cookie, []byte("private-cookie"), 0o600); err != nil {
				t.Fatal(err)
			}
			favorites, takeover := "", ""
			if tt.favorites != "" {
				favorites = `,"favorites":"` + tt.favorites + `"`
			}
			if tt.takeover {
				takeover = `,"favorites_takeover":true`
			}
			body := `{"accounts":[{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"` + key +
				`","amazon_region":"ca","amazon_cookie_file":"` + cookie + `","album_ids":["album-one"]` +
				favorites + takeover + `}]}`
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Accounts[0].FavoritesTakeover != tt.takeover {
					t.Fatalf("takeover = %v", cfg.Accounts[0].FavoritesTakeover)
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("takeover validation = %v, want %s", err, tt.want)
			}
		})
	}
}
