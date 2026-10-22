package amazon_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

func TestUsageMapsObservedCategoriesAndKeepsUnknownCategories(t *testing.T) {
	fixture := loadSDKFixture(t)
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/account/usage" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(fixture.Usage)
	})
	usage, err := c.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC); !usage.LastCalculated.Equal(want) {
		t.Fatalf("last calculated = %v, want %v", usage.LastCalculated, want)
	}
	for name, want := range map[string]struct{ billableBytes, billableCount, totalBytes int64 }{
		"photo": {123456789, 42, 234567890},
		"video": {987654321, 3, 987654321},
		"other": {1024, 1, 2048},
		"audio": {5, 1, 5},
	} {
		got := usage.Categories[name]
		if got.Billable.Bytes != want.billableBytes || got.Billable.Count != want.billableCount || got.Total.Bytes != want.totalBytes {
			t.Fatalf("%s usage = %+v", name, got)
		}
	}
}

func TestUsageWithoutLastCalculatedRemainsVisiblyStale(t *testing.T) {
	fixture := loadSDKFixture(t)
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture.UsageWithoutLastCalculated)
	})
	usage, err := c.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !usage.LastCalculated.IsZero() {
		t.Fatalf("missing lastCalculated produced a timestamp: %v", usage.LastCalculated)
	}
	if usage.Categories["photo"].Billable.Bytes != 1 || usage.Categories["video"].Total.Count != 0 {
		t.Fatalf("categorized usage lost: %+v", usage)
	}
}

func TestQuotaRejectionIsDistinctFromRateLimitAndAuthentication(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		message string
		code    string
	}{
		{"permission quota", http.StatusForbidden, "storage quota exceeded", bridge.Quota},
		{"invalid request quota", http.StatusBadRequest, "insufficient storage space", bridge.Quota},
		{"conflict quota", http.StatusConflict, "storage limit reached", bridge.Quota},
		{"insufficient storage status", http.StatusInsufficientStorage, "", bridge.Quota},
		{"permission denial", http.StatusForbidden, "not authorized for this album", bridge.Permission},
		{"rate limit", http.StatusTooManyRequests, "slow down", bridge.RateLimit},
		{"authentication", http.StatusUnauthorized, "quota cookie expired", bridge.Authentication},
		{"transient", http.StatusServiceUnavailable, "", bridge.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				if test.message != "" {
					_, _ = fmt.Fprintf(w, `{"message":%q}`, test.message)
				}
			})
			_, err := c.Usage(t.Context())
			var remote *bridge.Error
			if !errors.As(err, &remote) || remote.Code != test.code || err.Error() != test.code {
				t.Fatalf("classification = %v, want %s", err, test.code)
			}
		})
	}
}
