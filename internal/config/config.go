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
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/direct"
	"github.com/thedavidweng/immich-amazon-sync/internal/filter"
	"github.com/thedavidweng/immich-amazon-sync/internal/window"
)

type Config struct {
	DataDir           string `json:"data_dir"`
	Listen            string `json:"listen"`
	ScanInterval      string `json:"scan_interval"`
	Workers           int    `json:"workers"`
	MaxStagingBytes   int64  `json:"max_staging_bytes"`
	MaxStagingFiles   int    `json:"max_staging_files"`
	BytesPerSecond    int64  `json:"bytes_per_second"`
	LogLevel          string `json:"log_level"`
	AdminPasswordFile string `json:"admin_password_file,omitempty"`
	WebhookSecretFile string `json:"webhook_secret_file,omitempty"`
	// Metrics enables the optional Prometheus text endpoint. It is off by
	// default and shares the private listener. A non-loopback listener also
	// requires MetricsTokenFile so the endpoint has an explicit authentication
	// policy instead of relying on network placement alone.
	Metrics          bool      `json:"metrics,omitempty"`
	MetricsTokenFile string    `json:"metrics_token_file,omitempty"`
	Accounts         []Account `json:"accounts"`
}

type Account struct {
	ID           string   `json:"id"`
	ImmichURL    string   `json:"immich_url"`
	APIKeyFile   string   `json:"immich_api_key_file"`
	AmazonRegion string   `json:"amazon_region"`
	CookieFile   string   `json:"amazon_cookie_file"`
	AlbumIDs     []string `json:"album_ids"`
	Library      bool     `json:"library"`
	// IncludeVideos is the legacy video toggle. It is accepted only as a
	// migration input and is normalized into Filters, preserving its scope.
	IncludeVideos *bool          `json:"include_videos,omitempty"`
	Filters       *filter.Policy `json:"filters,omitempty"`
	// AlbumSyncMode is off, selected or all_accessible. MirrorAlbums is the
	// legacy boolean form: true maps to selected and never to all_accessible.
	AlbumSyncMode string `json:"album_sync_mode,omitempty"`
	MirrorAlbums  bool   `json:"mirror_albums,omitempty"`
	// Favorites is "off" (default), "add_only" or "mirror". Add-only stars a
	// verified node when the source explicitly reports the asset favorite and
	// never unstars. Mirror also clears a star this service applied.
	Favorites string `json:"favorites,omitempty"`
	// LivePhotoMode is "still_only" (default) or "original_pair". OriginalPair
	// backs up the hidden MOV companion of an included still as a second,
	// independent original; it never muxes, converts or pairs the media.
	LivePhotoMode string `json:"live_photo_mode,omitempty"`
	// VideoUsageThresholdBytes is an explicit user budget for observed
	// billable video bytes. Zero disables the advisory.
	VideoUsageThresholdBytes int64 `json:"video_usage_threshold_bytes,omitempty"`
	// HoldVideosAtThreshold stops dispatching new video uploads while fresh
	// observed billable video usage is at or above the threshold. It never
	// pauses photos and never deletes or rewrites a staged original.
	HoldVideosAtThreshold bool `json:"hold_videos_at_threshold,omitempty"`
	// FavoritesTakeover is only legal with Favorites "mirror". It records that
	// the source is authoritative over stars applied manually on the Amazon
	// side, so a cleared source favorite also clears those. It is never
	// implied by a migration or a default.
	FavoritesTakeover bool `json:"favorites_takeover,omitempty"`
	// Deletions propagates a source trash to the Amazon node. Enabling it also
	// requires DeletionConfirmation to equal DeletionConfirmationPhrase.
	Deletions            bool   `json:"deletions"`
	DeletionConfirmation string `json:"deletions_confirmation,omitempty"`
	// ManagedWorkflow opts in to creating one owned native Immich Workflow with
	// a separate write credential and a generated bearer secret. It never
	// replaces periodic polling.
	ManagedWorkflow     bool   `json:"managed_workflow"`
	WorkflowAPIKeyFile  string `json:"immich_workflow_api_key_file,omitempty"`
	WorkflowCallbackURL string `json:"workflow_callback_url,omitempty"`
	WorkflowSecretFile  string `json:"workflow_secret_file,omitempty"`
	// TransferWindow restricts new outbound uploads to a daily local-time
	// window. Absent means transfers are always allowed.
	TransferWindow *TransferWindow `json:"transfer_window,omitempty"`
	// DirectRoots maps source-reported original paths onto pre-approved
	// read-only local roots. Empty (the default, and for every existing
	// account) keeps every original on the Immich API.
	DirectRoots []DirectRoot `json:"direct_roots,omitempty"`
	// Retry bounds this account's durable retry schedule. It may only narrow
	// the conservative defaults: an Unconfirmed write is still reconciled by
	// re-reading the target and is never retried as a fresh POST.
	Retry  *Retry `json:"retry,omitempty"`
	apiKey string
	cookie string
}

// Retry is the optional per-account retry tuning. Zero values keep the
// engine's conservative defaults. MaxAttempts is a hard per-item cap and
// MaxDelay caps the exponential backoff; an explicit remote Retry-After still
// wins. Neither setting authorizes a retry whose prior write is Unconfirmed.
type Retry struct {
	MaxAttempts int    `json:"max_attempts,omitempty"`
	MaxDelay    string `json:"max_delay,omitempty"`
}

const (
	DefaultMaxAttempts = 5
	DefaultRetryDelay  = 30 * time.Second
	minRetryDelay      = time.Second
	maxRetryDelay      = 5 * time.Minute
	maxRetryAttempts   = 10
)

// RetryMaxAttempts resolves the bounded attempt cap without requiring the
// caller to distinguish an absent block from a zero value.
func (a Account) RetryMaxAttempts() int {
	if a.Retry != nil && a.Retry.MaxAttempts > 0 {
		return a.Retry.MaxAttempts
	}
	return DefaultMaxAttempts
}

// RetryMaxDelay resolves the bounded backoff ceiling. Validation guarantees a
// parseable, in-range value whenever the block is present.
func (a Account) RetryMaxDelay() time.Duration {
	if a.Retry != nil && a.Retry.MaxDelay != "" {
		if delay, err := time.ParseDuration(a.Retry.MaxDelay); err == nil {
			return delay
		}
	}
	return DefaultRetryDelay
}

func (r *Retry) validate() error {
	if r == nil {
		return nil
	}
	if r.MaxAttempts < 0 || r.MaxAttempts > maxRetryAttempts {
		return errors.New("retry_max_attempts_invalid")
	}
	if r.MaxDelay == "" {
		return nil
	}
	delay, err := time.ParseDuration(r.MaxDelay)
	if err != nil || delay < minRetryDelay || delay > maxRetryDelay {
		return errors.New("retry_max_delay_invalid")
	}
	return nil
}

// TransferWindow is one optional daily upload window in an IANA timezone.
// Start and End are local "HH:MM" wall-clock times; Start may be later than End
// to cross midnight. A nil window preserves continuous transfers.
type TransferWindow struct {
	Timezone string `json:"timezone"`
	Start    string `json:"start"`
	End      string `json:"end"`
}

// DirectRoot is one read-only mapping. Paths must be absolute and clean; the
// local root is never created, written or retimed by this service.
type DirectRoot struct {
	ImmichPath string `json:"immich_path"`
	HostPath   string `json:"host_path"`
}

// Roots converts the configured mapping into the validated resolver seam.
func (a Account) Roots() []direct.Root {
	roots := make([]direct.Root, 0, len(a.DirectRoots))
	for _, root := range a.DirectRoots {
		roots = append(roots, direct.Root{ImmichPath: root.ImmichPath, HostPath: root.HostPath})
	}
	return roots
}

// Policy returns the effective eligibility policy. A nil Filters block derives
// the historical behavior from IncludeVideos, which defaulted to images only.
func (a Account) Policy() filter.Policy {
	if a.Filters != nil {
		return a.Filters.Normalize()
	}
	return filter.Policy{MediaTypes: mediaTypes(a.IncludeVideos != nil && *a.IncludeVideos)}.Normalize()
}

func mediaTypes(videos bool) []string {
	if videos {
		return []string{"image", "video"}
	}
	return []string{"image"}
}

func (a Account) normalizeFilters() (Account, error) {
	if a.Filters != nil && a.IncludeVideos != nil {
		return a, errors.New("filter_conflict")
	}
	if a.Filters != nil {
		if err := a.Filters.Validate(); err != nil {
			return a, err
		}
		policy := a.Filters.Normalize()
		a.Filters = &policy
		return a, nil
	}
	policy := a.Policy()
	if err := policy.Validate(); err != nil {
		return a, err
	}
	a.Filters, a.IncludeVideos = &policy, nil
	return a, nil
}

// DeletionConfirmationPhrase is the exact text a configuration must contain to
// turn deletion propagation on. Turning it off is never gated.
const DeletionConfirmationPhrase = "ENABLE DELETIONS"

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
		for i := range cfg.Accounts {
			if cfg.Accounts[i].Filters == nil && cfg.Accounts[i].IncludeVideos == nil {
				continue
			}
			account, err := cfg.Accounts[i].normalizeFilters()
			if err != nil {
				return cfg, err
			}
			cfg.Accounts[i] = account
		}
	} else if os.Getenv("IMMICH_URL") != "" {
		library, err := envBool("LIBRARY")
		if err != nil {
			return cfg, errors.New("library_invalid")
		}
		managedWorkflow := false
		if value := os.Getenv("MANAGED_WORKFLOW"); value != "" {
			var err error
			managedWorkflow, err = strconv.ParseBool(value)
			if err != nil {
				return cfg, errors.New("managed_workflow_invalid")
			}
		}
		livePhoto := bridge.LivePhotoMode(os.Getenv("LIVE_PHOTO_MODE"))
		if livePhoto == "" {
			livePhoto = bridge.LivePhotoStillOnly
		}
		if !livePhoto.Valid() {
			return cfg, errors.New("live_photo_mode_invalid")
		}
		threshold := int64(0)
		if value := os.Getenv("VIDEO_USAGE_THRESHOLD_BYTES"); value != "" {
			var err error
			threshold, err = strconv.ParseInt(value, 10, 64)
			if err != nil || threshold < 0 {
				return cfg, errors.New("video_usage_threshold_invalid")
			}
		}
		hold, err := envBool("HOLD_VIDEOS_AT_THRESHOLD")
		if err != nil {
			return cfg, errors.New("hold_videos_at_threshold_invalid")
		}
		favoritesTakeover := false
		if value := os.Getenv("FAVORITES_TAKEOVER"); value != "" {
			var err error
			favoritesTakeover, err = strconv.ParseBool(value)
			if err != nil {
				return cfg, errors.New("favorites_takeover_invalid")
			}
		}
		account := Account{
			ID: "default", ImmichURL: os.Getenv("IMMICH_URL"),
			APIKeyFile: os.Getenv("IMMICH_API_KEY_FILE"), apiKey: os.Getenv("IMMICH_API_KEY"),
			AmazonRegion: os.Getenv("AMAZON_REGION"),
			CookieFile:   os.Getenv("AMAZON_COOKIE_FILE"), cookie: os.Getenv("AMAZON_COOKIE"),
			AlbumIDs:                 splitIDs(os.Getenv("ALBUM_IDS")),
			Library:                  library,
			AlbumSyncMode:            os.Getenv("ALBUM_SYNC_MODE"),
			Favorites:                os.Getenv("FAVORITES"),
			LivePhotoMode:            os.Getenv("LIVE_PHOTO_MODE"),
			FavoritesTakeover:        favoritesTakeover,
			ManagedWorkflow:          managedWorkflow,
			WorkflowAPIKeyFile:       os.Getenv("IMMICH_WORKFLOW_API_KEY_FILE"),
			WorkflowCallbackURL:      os.Getenv("WORKFLOW_CALLBACK_URL"),
			WorkflowSecretFile:       os.Getenv("WORKFLOW_SECRET_FILE"),
			VideoUsageThresholdBytes: threshold,
			HoldVideosAtThreshold:    hold,
			TransferWindow: environmentWindow(os.Getenv("TRANSFER_TIMEZONE"),
				os.Getenv("TRANSFER_WINDOW_START"), os.Getenv("TRANSFER_WINDOW_END")),
		}
		if attemptValue := os.Getenv("RETRY_MAX_ATTEMPTS"); attemptValue != "" {
			attempts, err := strconv.Atoi(attemptValue)
			if err != nil {
				return cfg, errors.New("retry_max_attempts_invalid")
			}
			if account.Retry == nil {
				account.Retry = &Retry{}
			}
			account.Retry.MaxAttempts = attempts
		}
		if delayValue := os.Getenv("RETRY_MAX_DELAY"); delayValue != "" {
			if _, err := time.ParseDuration(delayValue); err != nil {
				return cfg, errors.New("retry_max_delay_invalid")
			}
			if account.Retry == nil {
				account.Retry = &Retry{}
			}
			account.Retry.MaxDelay = delayValue
		}
		policy, err := envFilters()
		if err != nil {
			return cfg, err
		}
		account.Filters = policy
		cfg.Accounts = []Account{account}
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
	if value := os.Getenv("METRICS"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return cfg, errors.New("metrics_invalid")
		}
		cfg.Metrics = enabled
	}
	if value := os.Getenv("METRICS_TOKEN_FILE"); value != "" {
		cfg.MetricsTokenFile = value
	}
	return cfg, nil
}

// AlbumMode resolves the configured mirroring scope. The legacy mirror_albums
// boolean folds into selected, preserving the exact explicit album scope.
func (a Account) AlbumMode() bridge.AlbumSyncMode {
	if a.AlbumSyncMode != "" {
		return bridge.AlbumSyncMode(a.AlbumSyncMode)
	}
	if a.MirrorAlbums {
		return bridge.AlbumSyncSelected
	}
	return bridge.AlbumSyncOff
}

// PairMode resolves the configured Live Photo policy. An unset value preserves
// the historical still-only behavior and never widens upload scope.
func (a Account) PairMode() bridge.LivePhotoMode {
	if a.LivePhotoMode == "" {
		return bridge.LivePhotoStillOnly
	}
	return bridge.LivePhotoMode(a.LivePhotoMode)
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// envFilters builds the filter policy for the environment-only account. It
// keeps INCLUDE_VIDEOS as the legacy migration input and rejects combining it
// with the explicit MEDIA_TYPES rule.
func envFilters() (*filter.Policy, error) {
	policy := filter.Policy{MediaTypes: []string{"image"}}
	if value := os.Getenv("INCLUDE_VIDEOS"); value != "" {
		if os.Getenv("MEDIA_TYPES") != "" {
			return nil, errors.New("filter_conflict")
		}
		videos, err := strconv.ParseBool(value)
		if err != nil {
			return nil, errors.New("include_videos_invalid")
		}
		policy.MediaTypes = mediaTypes(videos)
	}
	if value := os.Getenv("MEDIA_TYPES"); value != "" {
		policy.MediaTypes = splitIDs(value)
	}
	if value := os.Getenv("MAX_ORIGINAL_BYTES"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil || limit < 0 {
			return nil, errors.New("filter_max_original_bytes_invalid")
		}
		policy.MaxOriginalBytes = limit
	}
	if value := os.Getenv("SKIP_RAW"); value != "" {
		skip, err := strconv.ParseBool(value)
		if err != nil {
			return nil, errors.New("filter_skip_raw_invalid")
		}
		policy.SkipRAW = skip
	}
	if value := os.Getenv("INCLUDE_ARCHIVED"); value != "" {
		include, err := strconv.ParseBool(value)
		if err != nil {
			return nil, errors.New("filter_include_archived_invalid")
		}
		policy.IncludeArchived = &include
	}
	if value := os.Getenv("EXCLUDED_TAGS"); value != "" {
		policy.ExcludedTags = splitIDs(value)
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	normalized := policy.Normalize()
	return &normalized, nil
}

// environmentWindow builds the environment-mode transfer window. Empty values
// leave the window absent; partial or invalid values fail validation.
func environmentWindow(timezone, start, end string) *TransferWindow {
	if timezone == "" && start == "" && end == "" {
		return nil
	}
	return &TransferWindow{Timezone: timezone, Start: start, End: end}
}

func (w *TransferWindow) validate() error {
	if w == nil {
		return nil
	}
	// One validated constructor: the engine dispatches with exactly the window
	// this configuration check accepted, so the two cannot diverge.
	_, err := window.New(w.Timezone, w.Start, w.End)
	return err
}

// loopbackListen reports whether a listen address only accepts local
// connections. Empty hosts bind every interface and are never private.
func loopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

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
	if c.MetricsTokenFile != "" && !c.Metrics {
		return errors.New("metrics_token_invalid")
	}
	if c.Metrics {
		if _, _, err := net.SplitHostPort(c.Listen); err != nil {
			return errors.New("listen_address_invalid")
		}
		if c.MetricsTokenFile == "" && !loopbackListen(c.Listen) {
			// Metrics are private by construction: either the listener is
			// loopback-only or an explicit bearer token is required.
			return errors.New("metrics_listener_public")
		}
	}
	seen := make(map[string]bool)
	for _, account := range c.Accounts {
		if !identifier.MatchString(account.ID) || seen[account.ID] {
			return errors.New("account_id_invalid")
		}
		seen[account.ID] = true
		if account.Filters != nil && account.IncludeVideos != nil {
			return errors.New("filter_conflict")
		}
		if account.Filters != nil {
			if err := account.Filters.Validate(); err != nil {
				return err
			}
		} else if err := account.Policy().Validate(); err != nil {
			return err
		}
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
		mode := account.AlbumMode()
		if !mode.Valid() {
			return errors.New("album_sync_mode_invalid")
		}
		if account.MirrorAlbums && account.AlbumSyncMode != "" && mode != bridge.AlbumSyncSelected {
			return errors.New("album_sync_mode_conflict")
		}
		if len(account.AlbumIDs) == 0 && !account.Library {
			return errors.New("source_scope_required")
		}
		if account.Library && len(account.AlbumIDs) > 0 {
			return errors.New("source_scope_conflict")
		}
		if mode == bridge.AlbumSyncSelected && (len(account.AlbumIDs) == 0 || account.Library) {
			return errors.New("source_scope_conflict")
		}
		if account.Deletions && account.DeletionConfirmation != DeletionConfirmationPhrase {
			return errors.New("deletion_confirmation_required")
		}
		if account.WorkflowAPIKeyFile != "" && account.WorkflowAPIKeyFile == account.APIKeyFile {
			return errors.New("workflow_credentials_conflict")
		}
		if account.WorkflowCallbackURL != "" {
			if err := validateCallback(account.WorkflowCallbackURL, account.ID); err != nil {
				return err
			}
		}
		if account.ManagedWorkflow {
			if account.WorkflowAPIKeyFile == "" || account.WorkflowSecretFile == "" || account.WorkflowCallbackURL == "" {
				return errors.New("workflow_configuration_required")
			}
		}
		switch account.Favorites {
		case "", "off", "add_only", "mirror":
		default:
			return errors.New("favorites_invalid")
		}
		if !account.PairMode().Valid() {
			return errors.New("live_photo_mode_invalid")
		}
		if account.VideoUsageThresholdBytes < 0 {
			return errors.New("video_usage_threshold_invalid")
		}
		if account.HoldVideosAtThreshold && account.VideoUsageThresholdBytes == 0 {
			return errors.New("video_usage_threshold_required")
		}
		if account.FavoritesTakeover && account.Favorites != "mirror" {
			return errors.New("favorites_takeover_requires_mirror")
		}
		if err := account.TransferWindow.validate(); err != nil {
			return err
		}
		if err := account.Retry.validate(); err != nil {
			return err
		}
		if _, err := direct.New(account.Roots()); err != nil {
			return errors.New("direct_root_invalid")
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

// WorkflowKey reads the separately authorized Workflow credential.
func (a Account) WorkflowKey() (string, error) {
	if a.WorkflowAPIKeyFile == "" {
		return "", errors.New("workflow_configuration_required")
	}
	return ReadSecret(a.WorkflowAPIKeyFile)
}

// validateCallback accepts only a plain HTTP(S) URL that ends in this account's
// event endpoint, so a generated workflow can never post events elsewhere.
func validateCallback(value, accountID string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.RawFragment != "" || u.RawPath != "" || !strings.HasSuffix(u.Path, "/events/"+accountID) {
		return errors.New("workflow_callback_url_invalid")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return errors.New("workflow_callback_url_invalid")
		}
	}
	return nil
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
	out := c
	out.Accounts = append([]Account(nil), c.Accounts...)
	for i := range out.Accounts {
		if out.Accounts[i].Filters == nil && out.Accounts[i].IncludeVideos == nil {
			continue
		}
		account, err := out.Accounts[i].normalizeFilters()
		if err != nil {
			return err
		}
		out.Accounts[i] = account
	}
	var data bytes.Buffer
	if err := json.NewEncoder(&data).Encode(out); err != nil || data.Len() > maxConfigBytes {
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

func envBool(name string) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}
