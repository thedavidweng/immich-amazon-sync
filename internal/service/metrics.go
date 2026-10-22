package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// StatusReport is the canonical local status for every selected account. It
// reads only the durable ledger, never contacts either remote and never
// returns secrets, so the CLI, the dashboard, the JSON status endpoint and the
// metrics endpoint all agree for the same state.
func (s *Service) StatusReport(ctx context.Context, id string) (Report, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	accounts, err := Accounts(cfg, id)
	if err != nil {
		return Report{}, err
	}
	report := Report{Accounts: make([]AccountResult, len(accounts))}
	for i, account := range accounts {
		result := AccountResult{Account: AccountHash(account.ID), Code: "ok"}
		engine, _, engineErr := newEngine(s.store, cfg, account, s.accountState(account.ID), s.log)
		if engineErr != nil {
			result.Code = Code(engineErr)
			report.Accounts[i] = result
			continue
		}
		status, statusErr := engine.Status(ctx)
		result.Status = status
		if statusErr != nil {
			result.Code = Code(statusErr)
		}
		report.Accounts[i] = result
	}
	return report, nil
}

// observable enforces the optional metrics access policy. The routes do not
// exist while metrics are disabled. A configured bearer token is checked on
// every request, so rotating the mounted secret needs no restart.
func (s *Service) observable(w http.ResponseWriter, r *http.Request) (config.Config, bool) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if !cfg.Metrics {
		http.NotFound(w, r)
		return cfg, false
	}
	if cfg.MetricsTokenFile != "" {
		token, err := config.ReadSecret(cfg.MetricsTokenFile)
		if err != nil {
			writeHealth(w, http.StatusServiceUnavailable, "metrics_unavailable")
			return cfg, false
		}
		provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			writeHealth(w, http.StatusUnauthorized, "authentication_required")
			return cfg, false
		}
	}
	return cfg, true
}

// metrics serves the optional low-cardinality Prometheus text exposition.
func (s *Service) metrics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.observable(w, r); !ok {
		return
	}
	report, err := s.StatusReport(r.Context(), "")
	if err != nil {
		writeHealth(w, http.StatusServiceUnavailable, Code(err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, renderMetrics(report))
}

// statusJSON serves the same durable report as `status --json` and the
// dashboard, under the same off-by-default private access policy.
func (s *Service) statusJSON(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.observable(w, r); !ok {
		return
	}
	report, err := s.StatusReport(r.Context(), "")
	if err != nil {
		writeHealth(w, http.StatusServiceUnavailable, Code(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(report)
}

// metricLabel bounds every label value to a small, finite vocabulary. Codes,
// states and reasons produced by this service are lowercase identifiers; any
// other value collapses to one bucket instead of creating new series.
func metricLabel(value string) string {
	if value == "" {
		return "none"
	}
	if len(value) > 64 {
		return "other"
	}
	for _, r := range value {
		if r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return "other"
		}
	}
	return value
}

type metricWriter struct {
	b strings.Builder
}

func (m *metricWriter) family(name, help, kind string) {
	fmt.Fprintf(&m.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

func (m *metricWriter) series(name string, value int64, labels ...string) {
	m.b.WriteString(name)
	if len(labels) > 0 {
		m.b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				m.b.WriteByte(',')
			}
			fmt.Fprintf(&m.b, "%s=%q", labels[i], metricLabel(labels[i+1]))
		}
		m.b.WriteByte('}')
	}
	fmt.Fprintf(&m.b, " %d\n", value)
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// renderMetrics emits every declared signal exactly once per account. Labels
// are limited to the hashed account and finite state, reason, code, mode and
// category vocabularies; filenames, asset IDs, URLs and secrets never appear.
func renderMetrics(report Report) string {
	var m metricWriter
	m.family("immich_amazon_sync_account_available", "Durable status could be read for this account.", "gauge")
	m.family("immich_amazon_sync_media", "Media count by durable lifecycle state.", "gauge")
	m.family("immich_amazon_sync_errors", "Recorded finite error codes.", "gauge")
	m.family("immich_amazon_sync_exclusions", "Media excluded by finite filter or source reason.", "gauge")
	m.family("immich_amazon_sync_favorites", "Favorite reconciliation count by finite state.", "gauge")
	m.family("immich_amazon_sync_albums", "Managed album count by finite state.", "gauge")
	m.family("immich_amazon_sync_associations", "Album membership operation count by finite state.", "gauge")
	m.family("immich_amazon_sync_events", "Event hint count by finite state.", "gauge")
	m.family("immich_amazon_sync_workflow", "Owned managed Workflow state and notice.", "gauge")
	m.family("immich_amazon_sync_transfer_window_open", "Whether new uploads may dispatch now; 1 also covers no configured window.", "gauge")
	m.family("immich_amazon_sync_live_photo_pairs", "Live Photo pair completeness.", "gauge")
	m.family("immich_amazon_sync_source_bytes_total", "Original bytes read by source path since process start.", "counter")
	m.family("immich_amazon_sync_direct_reads_total", "Originals read from an approved read-only mount since process start.", "counter")
	m.family("immich_amazon_sync_direct_fallbacks_total", "Direct-read fallbacks to the Immich API by finite reason.", "counter")
	m.family("immich_amazon_sync_direct_failures_total", "Direct-read fail-closed outcomes by finite reason.", "counter")
	m.family("immich_amazon_sync_usage_state", "Observed storage usage state.", "gauge")
	m.family("immich_amazon_sync_usage_bytes", "Observed storage usage bytes by category and kind.", "gauge")
	m.family("immich_amazon_sync_usage_items", "Observed storage usage item count by category and kind.", "gauge")
	m.family("immich_amazon_sync_usage_hold", "Active quota or video hold flags.", "gauge")

	mediaStates := []struct {
		state string
		value func(syncer.Status) int
	}{
		{"pending", func(s syncer.Status) int { return s.Pending }},
		{"staged", func(s syncer.Status) int { return s.Staged }},
		{"running", func(s syncer.Status) int { return s.Running }},
		{"unconfirmed", func(s syncer.Status) int { return s.Unconfirmed }},
		{"confirmed", func(s syncer.Status) int { return s.Confirmed }},
		{"existing", func(s syncer.Status) int { return s.Existing }},
		{"failed", func(s syncer.Status) int { return s.Failed }},
		{"blocked", func(s syncer.Status) int { return s.Blocked }},
		{"skipped", func(s syncer.Status) int { return s.Skipped }},
		{"deferred", func(s syncer.Status) int { return s.Deferred }},
		{"deletion_pending", func(s syncer.Status) int { return s.DeletePending }},
		{"deleted", func(s syncer.Status) int { return s.Deleted }},
	}
	favoriteStates := []struct {
		state string
		value func(syncer.Status) int
	}{
		{"desired", func(s syncer.Status) int { return s.FavoritesDesired }},
		{"observed", func(s syncer.Status) int { return s.FavoritesObserved }},
		{"applied", func(s syncer.Status) int { return s.FavoritesApplied }},
		{"preserved", func(s syncer.Status) int { return s.FavoritesPreserved }},
		{"pending", func(s syncer.Status) int { return s.FavoritesPending }},
		{"failed", func(s syncer.Status) int { return s.FavoritesFailed }},
		{"uncertain", func(s syncer.Status) int { return s.FavoritesUncertain }},
		{"unknown", func(s syncer.Status) int { return s.FavoritesUnknown }},
		{"conflict", func(s syncer.Status) int { return s.FavoritesConflict }},
	}
	albumStates := []struct {
		state string
		value func(syncer.Status) int
	}{
		{"active", func(s syncer.Status) int { return s.AlbumsActive }},
		{"inaccessible", func(s syncer.Status) int { return s.AlbumsInaccessible }},
		{"pending", func(s syncer.Status) int { return s.AlbumsPending }},
		{"unconfirmed", func(s syncer.Status) int { return s.AlbumsUnconfirmed }},
		{"failed", func(s syncer.Status) int { return s.AlbumsFailed }},
	}
	associationStates := []struct {
		state string
		value func(syncer.Status) int
	}{
		{"pending", func(s syncer.Status) int { return s.AssociationsPending }},
		{"unconfirmed", func(s syncer.Status) int { return s.AssociationsUnconfirmed }},
		{"failed", func(s syncer.Status) int { return s.AssociationsFailed }},
	}
	categories := []string{"photo", "video", "other"}

	for _, account := range report.Accounts {
		available := account.Code == "ok"
		m.series("immich_amazon_sync_account_available", boolInt(available), "account", account.Account)
		st := account.Status
		for _, entry := range mediaStates {
			m.series("immich_amazon_sync_media", int64(entry.value(st)), "account", account.Account, "state", entry.state)
		}
		m.writeCounts("immich_amazon_sync_errors", account.Account, st.Errors, "code")
		m.writeCounts("immich_amazon_sync_exclusions", account.Account, st.Exclusions, "reason")
		for _, entry := range favoriteStates {
			m.series("immich_amazon_sync_favorites", int64(entry.value(st)), "account", account.Account, "state", entry.state)
		}
		for _, entry := range albumStates {
			m.series("immich_amazon_sync_albums", int64(entry.value(st)), "account", account.Account, "state", entry.state)
		}
		for _, entry := range associationStates {
			m.series("immich_amazon_sync_associations", int64(entry.value(st)), "account", account.Account, "state", entry.state)
		}
		m.series("immich_amazon_sync_events", int64(st.EventsPending), "account", account.Account, "state", "pending")
		m.series("immich_amazon_sync_events", int64(st.EventsFailed), "account", account.Account, "state", "failed")
		m.series("immich_amazon_sync_workflow", 1, "account", account.Account,
			"state", orNoneValue(st.Workflow, "disabled"), "code", orNoneValue(st.WorkflowCode, "none"))
		m.series("immich_amazon_sync_transfer_window_open", boolInt(st.TransferWindow == nil || st.TransferWindow.Open),
			"account", account.Account)
		m.series("immich_amazon_sync_live_photo_pairs", int64(st.PairsComplete), "account", account.Account, "state", "complete")
		m.series("immich_amazon_sync_live_photo_pairs", int64(st.PairsIncomplete), "account", account.Account, "state", "incomplete")
		m.series("immich_amazon_sync_source_bytes_total", st.Direct.BytesAvoided, "account", account.Account, "mode", "direct")
		m.series("immich_amazon_sync_source_bytes_total", st.Direct.APIBytes, "account", account.Account, "mode", "api")
		m.series("immich_amazon_sync_direct_reads_total", int64(st.Direct.Reads), "account", account.Account)
		m.writeCounts("immich_amazon_sync_direct_fallbacks_total", account.Account, st.Direct.Fallbacks, "reason")
		m.writeCounts("immich_amazon_sync_direct_failures_total", account.Account, st.Direct.Failures, "reason")
		m.series("immich_amazon_sync_usage_state", 1, "account", account.Account, "state", orNoneValue(st.Usage.State, "unavailable"))
		for _, category := range categories {
			usage := st.Usage.Categories[category]
			m.series("immich_amazon_sync_usage_bytes", usage.Billable.Bytes, "account", account.Account, "category", category, "kind", "billable")
			m.series("immich_amazon_sync_usage_bytes", usage.Total.Bytes, "account", account.Account, "category", category, "kind", "total")
			m.series("immich_amazon_sync_usage_items", usage.Billable.Count, "account", account.Account, "category", category, "kind", "billable")
			m.series("immich_amazon_sync_usage_items", usage.Total.Count, "account", account.Account, "category", category, "kind", "total")
		}
		m.series("immich_amazon_sync_usage_hold", boolInt(st.Usage.QuotaHold), "account", account.Account, "kind", "quota")
		m.series("immich_amazon_sync_usage_hold", boolInt(st.Usage.VideoHold), "account", account.Account, "kind", "video")
		m.series("immich_amazon_sync_usage_hold", boolInt(st.Usage.VideoThresholdExceeded), "account", account.Account, "kind", "video_threshold")
	}
	return m.b.String()
}

// writeCounts emits a sorted, zero-suppressed count series family.
func (m *metricWriter) writeCounts(name, account string, counts map[string]int, label string) {
	keys := make([]string, 0, len(counts))
	for key, count := range counts {
		if count > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		m.series(name, int64(counts[key]), "account", account, label, key)
	}
}

func orNoneValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
