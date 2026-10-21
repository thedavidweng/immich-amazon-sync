package config_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
)

func validFileConfig() config.Config {
	return config.Config{
		DataDir: "data", Listen: "127.0.0.1:8080", ScanInterval: "15m",
		Workers: 2, MaxStagingBytes: 10 << 30, MaxStagingFiles: 2, LogLevel: "info",
		Accounts: []config.Account{{
			ID: "home", ImmichURL: "http://immich:2283", APIKeyFile: "/run/secrets/key",
			AmazonRegion: "us", CookieFile: "/run/secrets/cookie", AlbumIDs: []string{"album-one"},
		}},
	}
}

func TestConfigSaveRejectsOversizeWithoutReplacingPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := validFileConfig()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts[0].AlbumIDs = make([]string, 8192)
	for i := range cfg.Accounts[0].AlbumIDs {
		cfg.Accounts[0].AlbumIDs[i] = strings.Repeat("a", 128)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("local account data should otherwise be valid: %v", err)
	}
	if err := cfg.Save(path); err == nil {
		t.Error("oversized config replaced a restartable configuration")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, previous) {
		t.Errorf("rejected save changed persisted config: %v", err)
	}
	if _, err := config.Load(path); err != nil {
		t.Errorf("saved configuration cannot restart: %v", err)
	}
}

func TestConfigSerializedSizeBoundaryRoundTrip(t *testing.T) {
	for _, size := range []int{1 << 20, (1 << 20) + 1} {
		t.Run(stringSize(size), func(t *testing.T) {
			cfg := validFileConfig()
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			cfg.DataDir += strings.Repeat("d", size-len(encoded)-1)
			encoded, err = json.Marshal(cfg)
			if err != nil || len(encoded)+1 != size {
				t.Fatalf("boundary fixture size: %d %v", len(encoded)+1, err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			err = cfg.Save(path)
			if size > 1<<20 {
				if err == nil {
					t.Fatal("one byte over the serialized config bound was accepted")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("oversize first save created config: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := config.Load(path)
			if err != nil || !reflect.DeepEqual(loaded, cfg) {
				t.Fatalf("config at byte limit did not round trip: %v", err)
			}
		})
	}
}

func stringSize(size int) string {
	if size == 1<<20 {
		return "at-limit"
	}
	return "one-byte-over"
}

func TestConfigLoadRejectsDataBeyondValidJSONPrefix(t *testing.T) {
	cfg := validFileConfig()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, bytes.Repeat([]byte(" "), (1<<20)-len(body))...)
	body = append(body, []byte(`{"unexpected":"trailing"}`)...)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("config with data beyond the decoder's valid prefix was accepted")
	}
}

func TestMirrorAlbumsRequiresSelectedAlbumScopeBeforeSave(t *testing.T) {
	for _, tt := range []struct {
		name    string
		library bool
		mirror  bool
		albums  []string
		invalid bool
	}{
		{"library-mirror-conflict", true, true, nil, true},
		{"albums-mirror", false, true, []string{"album-one"}, false},
		{"library-only", true, false, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validFileConfig()
			path := filepath.Join(t.TempDir(), "config.json")
			if err := cfg.Save(path); err != nil {
				t.Fatal(err)
			}
			previous, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Accounts[0].Library, cfg.Accounts[0].MirrorAlbums = tt.library, tt.mirror
			cfg.Accounts[0].AlbumIDs = tt.albums
			validation, save := cfg.Validate(), cfg.Save(path)
			if !tt.invalid {
				if validation != nil || save != nil {
					t.Fatalf("valid scope rejected: validate=%v save=%v", validation, save)
				}
				loaded, err := config.Load(path)
				if err != nil || !reflect.DeepEqual(loaded, cfg) {
					t.Fatalf("valid scope did not round trip: %v", err)
				}
				return
			}
			if validation == nil || validation.Error() != "source_scope_conflict" {
				t.Errorf("conflicting mirroring validated: %v", validation)
			}
			if save == nil || save.Error() != "source_scope_conflict" {
				t.Errorf("conflicting mirroring persisted: %v", save)
			}
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(current, previous) {
				t.Errorf("invalid mirroring replaced previous config: %v", err)
			}
		})
	}
}
