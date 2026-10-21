package admin

import (
	"context"
	"net/http"
	"time"
)

func (d *dashboard) accountAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entry, ok := d.authorized(w, r)
		if !ok || !d.form(w, r, entry) {
			return
		}
		if action == "remove" {
			if !d.options.Writable {
				d.fail(w, http.StatusForbidden, "configuration_read_only")
				return
			}
			d.edits.Lock()
			defer d.edits.Unlock()
		}
		id := r.PathValue("id")
		cfg := d.backend.Config()
		index := -1
		for i, account := range cfg.Accounts {
			if account.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			d.fail(w, http.StatusNotFound, "account_not_found")
			return
		}
		var err error
		switch action {
		case "pause", "resume":
			err = d.backend.Pause(r.Context(), id, action == "pause")
		case "retry":
			err = d.backend.Retry(r.Context(), id)
		case "remove":
			cfg.Accounts = append(cfg.Accounts[:index], cfg.Accounts[index+1:]...)
			err = d.backend.Save(cfg)
		case "doctor":
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			checks, diagnoseErr := d.backend.Diagnose(ctx, id)
			d.render(w, "doctor", struct {
				ID, Code string
				Checks   []Diagnostic
			}{id, d.backend.Code(diagnoseErr), checks})
			return
		}
		if err != nil {
			d.fail(w, http.StatusBadRequest, d.backend.Code(err))
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}
