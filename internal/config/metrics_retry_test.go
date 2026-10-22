package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func writeConfig(t *testing.T, body string) string {
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
	path := filepath.Join(dir, "config.json")
	full := `{"accounts":[{"id":"home","immich_url":"http://immich:2283","immich_api_key_file":"` + key +
		`","amazon_region":"ca","amazon_cookie_file":"` + cookie + `","album_ids":["album-one"]` + body + `}]}`
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRetryDefaultsAreConservative(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	account := cfg.Accounts[0]
	if account.RetryMaxAttempts() != config.DefaultMaxAttempts || account.RetryMaxDelay() != config.DefaultRetryDelay {
		t.Fatalf("defaults = %d/%s", account.RetryMaxAttempts(), account.RetryMaxDelay())
	}
}

func TestRetryTuningIsBoundedAndPersisted(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `,"retry":{"max_attempts":3,"max_delay":"2s"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	account := cfg.Accounts[0]
	if account.RetryMaxAttempts() != 3 || account.RetryMaxDelay() != 2*time.Second {
		t.Fatalf("tuning = %d/%s", account.RetryMaxAttempts(), account.RetryMaxDelay())
	}
	saved := filepath.Join(t.TempDir(), "saved.json")
	if err := cfg.Save(saved); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(saved)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Accounts[0].RetryMaxAttempts() != 3 || reloaded.Accounts[0].RetryMaxDelay() != 2*time.Second {
		t.Fatalf("tuning not persisted: %+v", reloaded.Accounts[0].Retry)
	}
}

func TestRetryTuningRejectsValuesOutsideBounds(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		code string
	}{
		{"attempts too high", `,"retry":{"max_attempts":11}`, "retry_max_attempts_invalid"},
		{"attempts negative", `,"retry":{"max_attempts":-1}`, "retry_max_attempts_invalid"},
		{"delay below floor", `,"retry":{"max_delay":"10ms"}`, "retry_max_delay_invalid"},
		{"delay above ceiling", `,"retry":{"max_delay":"1h"}`, "retry_max_delay_invalid"},
		{"delay invalid", `,"retry":{"max_delay":"soon"}`, "retry_max_delay_invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load(writeConfig(t, tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err == nil || err.Error() != tt.code {
				t.Fatalf("Validate = %v, want %s", err, tt.code)
			}
		})
	}
}

func TestRetryTuningFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	cookie := filepath.Join(dir, "cookie")
	for _, path := range []string{key, cookie} {
		if err := os.WriteFile(path, []byte("private-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("IMMICH_API_KEY_FILE", key)
	t.Setenv("AMAZON_REGION", "ca")
	t.Setenv("AMAZON_COOKIE_FILE", cookie)
	t.Setenv("ALBUM_IDS", "album-one")
	t.Setenv("RETRY_MAX_ATTEMPTS", "2")
	t.Setenv("RETRY_MAX_DELAY", "5s")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts[0].RetryMaxAttempts() != 2 || cfg.Accounts[0].RetryMaxDelay() != 5*time.Second {
		t.Fatalf("environment tuning ignored: %+v", cfg.Accounts[0].Retry)
	}
	t.Setenv("RETRY_MAX_ATTEMPTS", "99")
	cfg, _ = config.Load("")
	if err := cfg.Validate(); err == nil || err.Error() != "retry_max_attempts_invalid" {
		t.Fatalf("unbounded environment tuning accepted: %v", err)
	}
}

func TestMetricsAreOffByDefault(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metrics {
		t.Fatal("metrics enabled by default")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsRequirePrivateListenerOrToken(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Metrics = true
	cfg.Listen = "127.0.0.1:8080"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("loopback listener rejected: %v", err)
	}
	cfg.Listen = "0.0.0.0:8080"
	if err := cfg.Validate(); err == nil || err.Error() != "metrics_listener_public" {
		t.Fatalf("public listener accepted without authentication: %v", err)
	}
	cfg.MetricsTokenFile = "/run/secrets/metrics_token"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit authentication policy rejected: %v", err)
	}
	cfg.Metrics = false
	if err := cfg.Validate(); err == nil || err.Error() != "metrics_token_invalid" {
		t.Fatalf("token without metrics accepted: %v", err)
	}
}

func TestMetricsFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	cookie := filepath.Join(dir, "cookie")
	for _, path := range []string{key, cookie} {
		if err := os.WriteFile(path, []byte("private-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("IMMICH_API_KEY_FILE", key)
	t.Setenv("AMAZON_REGION", "ca")
	t.Setenv("AMAZON_COOKIE_FILE", cookie)
	t.Setenv("ALBUM_IDS", "album-one")
	t.Setenv("LISTEN", "127.0.0.1:8080")
	t.Setenv("METRICS", "true")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Metrics {
		t.Fatal("METRICS environment option ignored")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METRICS", "sometimes")
	if _, err := config.Load(""); err == nil || err.Error() != "metrics_invalid" {
		t.Fatalf("invalid metrics flag accepted: %v", err)
	}
}
