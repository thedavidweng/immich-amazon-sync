package admin

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

type accountView struct {
	Account config.Account
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
	view := editor{CSRF: entry.CSRF, Original: r.PathValue("id")}
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
	}
	d.render(w, "editor", view)
}

func formAccount(r *http.Request) config.Account {
	ids := strings.FieldsFunc(r.PostForm.Get("album_ids"), func(c rune) bool {
		return c == ',' || c == ' ' || c == '\n' || c == '\r' || c == '\t'
	})
	for _, id := range r.PostForm["album_id"] {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
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
	return config.Account{
		ID: r.PostForm.Get("id"), ImmichURL: r.PostForm.Get("immich_url"),
		APIKeyFile: r.PostForm.Get("key_file"), CookieFile: r.PostForm.Get("cookie_file"),
		AmazonRegion: r.PostForm.Get("region"),
		AlbumIDs:     ids,
		Library:      r.PostForm.Get("library") == "on", IncludeVideos: r.PostForm.Get("include_videos") == "on",
		AlbumSyncMode:        r.PostForm.Get("album_sync_mode"),
		Favorites:            r.PostForm.Get("favorites"),
		Deletions:            r.PostForm.Get("deletions") == "on",
		DeletionConfirmation: r.PostForm.Get("deletions_confirmation"),
		ManagedWorkflow:      r.PostForm.Get("managed_workflow") == "on",
		WorkflowAPIKeyFile:   r.PostForm.Get("workflow_key_file"),
		WorkflowCallbackURL:  r.PostForm.Get("workflow_callback_url"),
		WorkflowSecretFile:   r.PostForm.Get("workflow_secret_file"),
		TransferWindow:       transferWindow,
	}
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
	view := editor{CSRF: entry.CSRF, Original: r.PostForm.Get("original_id"), Account: formAccount(r)}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var err error
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
	account := formAccount(r)
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
