package config_test

import (
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func usageConfig() config.Config {
	return config.Config{
		DataDir: "data", Listen: "127.0.0.1:8080", ScanInterval: "15m",
		Workers: 2, MaxStagingBytes: 1, MaxStagingFiles: 1, LogLevel: "info",
		Accounts: []config.Account{{
			ID: "home", ImmichURL: "http://immich:2283", APIKeyFile: "/run/key",
			AmazonRegion: "us", CookieFile: "/run/cookie", AlbumIDs: []string{"album-one"},
		}},
	}
}

func TestVideoUsageThresholdIsOptionalAndRequiresAnExplicitBudgetForAHold(t *testing.T) {
	cfg := usageConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled advisory rejected: %v", err)
	}
	cfg.Accounts[0].VideoUsageThresholdBytes = 10 << 30
	if err := cfg.Validate(); err != nil {
		t.Fatalf("advisory threshold rejected: %v", err)
	}
	cfg.Accounts[0].HoldVideosAtThreshold = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("hold with threshold rejected: %v", err)
	}
	cfg.Accounts[0].VideoUsageThresholdBytes = 0
	if err := cfg.Validate(); err == nil || err.Error() != "video_usage_threshold_required" {
		t.Fatalf("hold without a threshold accepted: %v", err)
	}
	cfg.Accounts[0].VideoUsageThresholdBytes = -1
	cfg.Accounts[0].HoldVideosAtThreshold = false
	if err := cfg.Validate(); err == nil || err.Error() != "video_usage_threshold_invalid" {
		t.Fatalf("negative threshold accepted: %v", err)
	}
}

func TestVideoUsageThresholdEnvironmentIsParsed(t *testing.T) {
	t.Setenv("IMMICH_URL", "http://immich:2283")
	t.Setenv("IMMICH_API_KEY", "source-secret")
	t.Setenv("AMAZON_REGION", "us")
	t.Setenv("AMAZON_COOKIE", "destination-secret")
	t.Setenv("ALBUM_IDS", "album-one")
	t.Setenv("VIDEO_USAGE_THRESHOLD_BYTES", "1073741824")
	t.Setenv("HOLD_VIDEOS_AT_THRESHOLD", "true")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts[0].VideoUsageThresholdBytes != 1073741824 || !cfg.Accounts[0].HoldVideosAtThreshold {
		t.Fatalf("environment advisory lost: %+v", cfg.Accounts[0])
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIDEO_USAGE_THRESHOLD_BYTES", "-1")
	if _, err := config.Load(""); err == nil || err.Error() != "video_usage_threshold_invalid" {
		t.Fatalf("negative environment threshold accepted: %v", err)
	}
	t.Setenv("VIDEO_USAGE_THRESHOLD_BYTES", "1024")
	t.Setenv("HOLD_VIDEOS_AT_THRESHOLD", "not-a-bool")
	if _, err := config.Load(""); err == nil || err.Error() != "hold_videos_at_threshold_invalid" {
		t.Fatalf("invalid hold flag accepted: %v", err)
	}
}
