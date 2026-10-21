package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
)

type Config struct {
	DataDir           string    `json:"data_dir"`
	Listen            string    `json:"listen"`
	ScanInterval      string    `json:"scan_interval"`
	Workers           int       `json:"workers"`
	MaxStagingBytes   int64     `json:"max_staging_bytes"`
	MaxStagingFiles   int       `json:"max_staging_files"`
	BytesPerSecond    int64     `json:"bytes_per_second"`
	LogLevel          string    `json:"log_level"`
	AdminPasswordFile string    `json:"admin_password_file,omitempty"`
	WebhookSecretFile string    `json:"webhook_secret_file,omitempty"`
	Accounts          []Account `json:"accounts"`
}

type Account struct {
	ID            string   `json:"id"`
	ImmichURL     string   `json:"immich_url"`
	APIKeyFile    string   `json:"immich_api_key_file"`
	AmazonRegion  string   `json:"amazon_region"`
	CookieFile    string   `json:"amazon_cookie_file"`
	AlbumIDs      []string `json:"album_ids"`
	Library       bool     `json:"library"`
	IncludeVideos bool     `json:"include_videos"`
	MirrorAlbums  bool     `json:"mirror_albums"`
	apiKey        string
	cookie        string
}

type Secrets struct {
	APIKey string
	Cookie string
}

const maxConfigBytes = 1 << 20

func Load(path string) (Config, error) {
	cfg := Config{
		DataDir: "data", Listen: "127.0.0.1:8080", ScanInterval: "15m",
		Workers: 2, MaxStagingBytes: 10 << 30, MaxStagingFiles: 2, LogLevel: "info",
	}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return cfg, errors.New("config_unreadable")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
		if err != nil || len(data) > maxConfigBytes {
			return cfg, errors.New("config_invalid_json")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return cfg, errors.New("config_invalid_json")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return cfg, errors.New("config_invalid_json")
		}
	} else if os.Getenv("IMMICH_URL") != "" {
		includeVideos := false
		if value := os.Getenv("INCLUDE_VIDEOS"); value != "" {
			var err error
			includeVideos, err = strconv.ParseBool(value)
			if err != nil {
				return cfg, errors.New("include_videos_invalid")
			}
		}
		cfg.Accounts = []Account{{
			ID: "default", ImmichURL: os.Getenv("IMMICH_URL"),
			APIKeyFile: os.Getenv("IMMICH_API_KEY_FILE"), apiKey: os.Getenv("IMMICH_API_KEY"),
			AmazonRegion: os.Getenv("AMAZON_REGION"),
			CookieFile:   os.Getenv("AMAZON_COOKIE_FILE"), cookie: os.Getenv("AMAZON_COOKIE"),
			AlbumIDs:      splitIDs(os.Getenv("ALBUM_IDS")),
			IncludeVideos: includeVideos,
		}}
	}
	if value := os.Getenv("DATA_DIR"); value != "" {
		cfg.DataDir = value
	}
	if value := os.Getenv("LISTEN"); value != "" {
		cfg.Listen = value
	}
	if value := os.Getenv("ADMIN_PASSWORD_FILE"); value != "" {
		cfg.AdminPasswordFile = value
	}
	if value := os.Getenv("WEBHOOK_SECRET_FILE"); value != "" {
		cfg.WebhookSecretFile = value
	}
	return cfg, nil
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (c Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("data_directory_required")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen_address_invalid")
	}
	duration, err := time.ParseDuration(c.ScanInterval)
	if err != nil || duration < time.Second {
		return errors.New("scan_interval_invalid")
	}
	if c.Workers < 1 || c.Workers > 16 || c.MaxStagingFiles < 1 || c.MaxStagingBytes < 1 || c.BytesPerSecond < 0 {
		return errors.New("resource_limits_invalid")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log_level_invalid")
	}
	if len(c.Accounts) == 0 && c.AdminPasswordFile == "" {
		return errors.New("accounts_required")
	}
	seen := make(map[string]bool)
	for _, account := range c.Accounts {
		if !identifier.MatchString(account.ID) || seen[account.ID] {
			return errors.New("account_id_invalid")
		}
		seen[account.ID] = true
		u, err := url.Parse(account.ImmichURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("immich_url_invalid")
		}
		if account.AmazonRegion == "" {
			return errors.New("amazon_region_required")
		}
		if _, err := aphotos.ResolveRegion(aphotos.Region(account.AmazonRegion)); err != nil {
			return errors.New("amazon_region_invalid")
		}
		if (account.APIKeyFile == "" && account.apiKey == "") || (account.CookieFile == "" && account.cookie == "") {
			return errors.New("credentials_required")
		}
		if account.APIKeyFile != "" && account.apiKey != "" || account.CookieFile != "" && account.cookie != "" {
			return errors.New("credential_sources_conflict")
		}
		if len(account.AlbumIDs) == 0 && !account.Library {
			return errors.New("source_scope_required")
		}
		if account.Library && (len(account.AlbumIDs) > 0 || account.MirrorAlbums) {
			return errors.New("source_scope_conflict")
		}
		for _, id := range account.AlbumIDs {
			if !identifier.MatchString(id) {
				return errors.New("album_id_invalid")
			}
		}
	}
	return nil
}

func ReadSecret(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("secret_unreadable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 || strings.TrimSpace(string(b)) == "" {
		return "", errors.New("secret_invalid")
	}
	return strings.TrimSpace(string(b)), nil
}

func (a Account) Secrets() (Secrets, error) {
	result := Secrets{Cookie: a.cookie}
	var err error
	result.APIKey, err = a.SourceKey()
	if err != nil {
		return Secrets{}, err
	}
	if a.CookieFile != "" {
		result.Cookie, err = ReadSecret(a.CookieFile)
		if err != nil {
			return Secrets{}, err
		}
	}
	return result, nil
}

func (a Account) SourceKey() (string, error) {
	if a.APIKeyFile == "" {
		if a.apiKey == "" {
			return "", errors.New("credentials_required")
		}
		return a.apiKey, nil
	}
	return ReadSecret(a.APIKeyFile)
}

func (c Config) Interval() time.Duration {
	duration, _ := time.ParseDuration(c.ScanInterval)
	return duration
}

func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var data bytes.Buffer
	if err := json.NewEncoder(&data).Encode(c); err != nil || data.Len() > maxConfigBytes {
		return errors.New("config_invalid_json")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return errors.New("config_write_failed")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data.Bytes()); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("config_write_failed")
	}
	if err = os.Rename(name, path); err != nil {
		return errors.New("config_write_failed")
	}
	return nil
}

func splitIDs(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
