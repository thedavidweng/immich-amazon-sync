package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/filter"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

type accountView struct {
	Account config.Account
	Policy  filter.Policy
	Status  syncer.Status
	Code    string
}

type overview struct {
	CSRF     string
	Writable bool
	Accounts []accountView
	Orphans  []WorkflowOrphan
}

type editor struct {
	CSRF     string
	Original string
	Account  config.Account
	Policy   filter.Policy
	Albums   []bridge.Album
}

func (d *dashboard) accountPage(w http.ResponseWriter, r *http.Request) {
	entry, ok := d.authorized(w, r)
	if !ok {
		return
	}
	if !d.options.Writable {
		d.fail(w, http.StatusForbidden, "configuration_read_only")
		return
	}
	view := editor{CSRF: entry.CSRF, Original: r.PathValue("id"), Policy: config.Account{}.Policy()}
	if view.Original != "" {
		found := false
		for _, account := range d.backend.Config().Accounts {
			if account.ID == view.Original {
				view.Account, found = account, true
				break
			}
		}
		if !found {
			d.fail(w, http.StatusNotFound, "account_not_found")
			return
		}
		view.Policy = view.Account.Policy()
	}
	d.render(w, "editor", view)
}

func formAccount(r *http.Request) (config.Account, error) {
	ids := strings.FieldsFunc(r.PostForm.Get("album_ids"), func(c rune) bool {
		return c == ',' || c == ' ' || c == '\n' || c == '\r' || c == '\t'
	})
	for _, id := range r.PostForm["album_id"] {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	policy := filter.Policy{}
	if r.PostForm.Get("media_image") == "on" {
		policy.MediaTypes = append(policy.MediaTypes, "image")
	}
	if r.PostForm.Get("media_video") == "on" {
		policy.MediaTypes = append(policy.MediaTypes, "video")
	}
	if len(policy.MediaTypes) == 0 {
		return config.Account{}, errors.New("filter_media_type_invalid")
	}
	if value := strings.TrimSpace(r.PostForm.Get("max_original_bytes")); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil || limit < 0 {
			return config.Account{}, errors.New("filter_max_original_bytes_invalid")
		}
		policy.MaxOriginalBytes = limit
	}
	policy.SkipRAW = r.PostForm.Get("skip_raw") == "on"
	includeArchived := r.PostForm.Get("include_archived") == "on"
	policy.IncludeArchived = &includeArchived
	policy.ExcludedTags = strings.FieldsFunc(r.PostForm.Get("excluded_tags"), func(c rune) bool {
		return c == ',' || c == ' ' || c == '\n' || c == '\r' || c == '\t'
	})
	if err := policy.Validate(); err != nil {
		return config.Account{}, err
	}
	threshold := int64(0)
	if value := strings.TrimSpace(r.PostForm.Get("video_usage_threshold_bytes")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			return config.Account{}, errors.New("video_usage_threshold_invalid")
		}
		threshold = parsed
	}
	window := config.TransferWindow{
		Timezone: strings.TrimSpace(r.PostForm.Get("transfer_timezone")),
		Start:    strings.TrimSpace(r.PostForm.Get("transfer_window_start")),
		End:      strings.TrimSpace(r.PostForm.Get("transfer_window_end")),
	}
	var transferWindow *config.TransferWindow
	if window != (config.TransferWindow{}) {
		transferWindow = &window
	}
	retry := config.Retry{}
	if value := strings.TrimSpace(r.PostForm.Get("retry_max_attempts")); value != "" {
		attempts, err := strconv.Atoi(value)
		if err != nil || attempts < 1 || attempts > 10 {
			return config.Account{}, errors.New("retry_max_attempts_invalid")
		}
		retry.MaxAttempts = attempts
	}
	if value := strings.TrimSpace(r.PostForm.Get("retry_max_delay")); value != "" {
		delay, err := time.ParseDuration(value)
		if err != nil || delay < time.Second || delay > 5*time.Minute {
			return config.Account{}, errors.New("retry_max_delay_invalid")
		}
		retry.MaxDelay = value
	}
	var retryPolicy *config.Retry
	if retry != (config.Retry{}) {
		retryPolicy = &retry
	}
	return config.Account{
		ID: r.PostForm.Get("id"), ImmichURL: r.PostForm.Get("immich_url"),
		APIKeyFile: r.PostForm.Get("key_file"), CookieFile: r.PostForm.Get("cookie_file"),
		AmazonRegion:             r.PostForm.Get("region"),
		AlbumIDs:                 ids,
		Library:                  r.PostForm.Get("library") == "on",
		Filters:                  &policy,
		AlbumSyncMode:            r.PostForm.Get("album_sync_mode"),
		Favorites:                r.PostForm.Get("favorites"),
		LivePhotoMode:            r.PostForm.Get("live_photo_mode"),
		FavoritesTakeover:        r.PostForm.Get("favorites_takeover") == "on",
		Deletions:                r.PostForm.Get("deletions") == "on",
		DeletionConfirmation:     r.PostForm.Get("deletions_confirmation"),
		ManagedWorkflow:          r.PostForm.Get("managed_workflow") == "on",
		WorkflowAPIKeyFile:       r.PostForm.Get("workflow_key_file"),
		WorkflowCallbackURL:      r.PostForm.Get("workflow_callback_url"),
		WorkflowSecretFile:       r.PostForm.Get("workflow_secret_file"),
		VideoUsageThresholdBytes: threshold,
		HoldVideosAtThreshold:    r.PostForm.Get("hold_videos_at_threshold") == "on",
		TransferWindow:           transferWindow,
		Retry:                    retryPolicy,
	}, nil
}

func (d *dashboard) discover(w http.ResponseWriter, r *http.Request) {
	entry, ok := d.authorized(w, r)
	if !ok || !d.form(w, r, entry) {
		return
	}
	if !d.options.Writable {
		d.fail(w, http.StatusForbidden, "configuration_read_only")
		return
	}
	account, err := formAccount(r)
	if err != nil {
		d.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	view := editor{CSRF: entry.CSRF, Original: r.PostForm.Get("original_id"), Account: account, Policy: account.Policy()}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	view.Albums, err = d.backend.Albums(ctx, view.Account)
	if err != nil {
		d.fail(w, http.StatusBadRequest, d.backend.Code(err))
		return
	}
	d.render(w, "editor", view)
}

func (d *dashboard) saveAccount(w http.ResponseWriter, r *http.Request) {
	entry, ok := d.authorized(w, r)
	if !ok || !d.form(w, r, entry) {
		return
	}
	if !d.options.Writable {
		d.fail(w, http.StatusForbidden, "configuration_read_only")
		return
	}
	d.edits.Lock()
	defer d.edits.Unlock()
	account, err := formAccount(r)
	if err != nil {
		d.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := d.backend.Config()
	original := r.PostForm.Get("original_id")
	// Direct roots are configuration-file only; an account edit must not drop
	// an operator-approved read-only mapping.
	if original != "" {
		for _, existing := range cfg.Accounts {
			if existing.ID == original {
				account.DirectRoots = existing.DirectRoots
				break
			}
		}
	}
	if original == "" {
		for _, existing := range cfg.Accounts {
			if existing.ID == account.ID {
				d.fail(w, http.StatusBadRequest, "account_id_invalid")
				return
			}
		}
		cfg.Accounts = append(cfg.Accounts, account)
	} else {
		found := false
		for i, existing := range cfg.Accounts {
			if existing.ID == original && account.ID == original {
				cfg.Accounts[i], found = account, true
				break
			}
		}
		if !found {
			d.fail(w, http.StatusNotFound, "account_not_found")
			return
		}
	}
	if err := d.backend.Save(cfg); err != nil {
		d.fail(w, http.StatusBadRequest, d.backend.Code(err))
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
