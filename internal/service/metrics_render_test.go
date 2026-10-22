package service

import (
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func TestRenderMetricsCoversEverySignalOnceWithBoundedLabels(t *testing.T) {
	report := Report{Accounts: []AccountResult{{
		Account: "0123456789abcdef", Code: "ok",
		Status: syncer.Status{
			Confirmed: 4, Existing: 2, FavoritesPending: 1, FavoritesFailed: 3,
			Exclusions:    map[string]int{"media_type_excluded": 2},
			Errors:        map[string]int{"remote_unavailable": 1, "Not A Code!": 1},
			AlbumsPending: 2, AlbumsInaccessible: 1,
			AssociationsPending: 3, EventsPending: 1,
			Workflow: "active", WorkflowCode: "workflow_permission_missing",
			PairsComplete: 2, PairsIncomplete: 1,
			Direct: syncer.DirectCounters{
				Reads: 2, BytesAvoided: 10, APIBytes: 20,
				Fallbacks: map[string]int{"missing": 1},
				Failures:  map[string]int{"path_escape": 1},
			},
			Usage: syncer.UsageStatus{
				State: "observed",
				Categories: map[string]bridge.UsageCategory{
					"photo": {Billable: bridge.UsageBucket{Bytes: 5, Count: 1}, Total: bridge.UsageBucket{Bytes: 7, Count: 2}},
				},
				QuotaHold: true,
			},
			MaxAttempts: 3,
		},
	}}}
	text := renderMetrics(report)

	for family, want := range map[string]int{
		"immich_amazon_sync_account_available{":      1,
		"immich_amazon_sync_media{":                  12,
		"immich_amazon_sync_errors{":                 2,
		"immich_amazon_sync_exclusions{":             1,
		"immich_amazon_sync_favorites{":              9,
		"immich_amazon_sync_albums{":                 5,
		"immich_amazon_sync_associations{":           3,
		"immich_amazon_sync_events{":                 2,
		"immich_amazon_sync_workflow{":               1,
		"immich_amazon_sync_transfer_window_open{":   1,
		"immich_amazon_sync_live_photo_pairs{":       2,
		"immich_amazon_sync_source_bytes_total{":     2,
		"immich_amazon_sync_direct_reads_total{":     1,
		"immich_amazon_sync_direct_fallbacks_total{": 1,
		"immich_amazon_sync_direct_failures_total{":  1,
		"immich_amazon_sync_usage_state{":            1,
		"immich_amazon_sync_usage_bytes{":            6,
		"immich_amazon_sync_usage_items{":            6,
		"immich_amazon_sync_usage_hold{":             3,
	} {
		if got := strings.Count(text, family); got != want {
			t.Fatalf("%s series = %d, want %d\n%s", family, got, want, text)
		}
	}
	for _, want := range []string{
		`immich_amazon_sync_media{account="0123456789abcdef",state="confirmed"} 4`,
		`immich_amazon_sync_media{account="0123456789abcdef",state="existing"} 2`,
		`immich_amazon_sync_errors{account="0123456789abcdef",code="other"} 1`,
		`immich_amazon_sync_workflow{account="0123456789abcdef",state="active",code="workflow_permission_missing"} 1`,
		`immich_amazon_sync_transfer_window_open{account="0123456789abcdef"} 1`,
		`immich_amazon_sync_source_bytes_total{account="0123456789abcdef",mode="direct"} 10`,
		`immich_amazon_sync_source_bytes_total{account="0123456789abcdef",mode="api"} 20`,
		`immich_amazon_sync_usage_bytes{account="0123456789abcdef",category="photo",kind="billable"} 5`,
		`immich_amazon_sync_usage_hold{account="0123456789abcdef",kind="quota"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in metrics:\n%s", want, text)
		}
	}
}

func TestMetricLabelRejectsUnboundedValues(t *testing.T) {
	for value, want := range map[string]string{
		"":                             "none",
		"remote_unavailable":           "remote_unavailable",
		"Not A Code!":                  "other",
		strings.Repeat("a", 65):        "other",
		"https://private.example/path": "other",
		"asset-11111111-2222-3333-444": "other",
	} {
		if got := metricLabel(value); got != want {
			t.Fatalf("metricLabel(%q) = %q, want %q", value, got, want)
		}
	}
}
