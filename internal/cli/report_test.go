package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/service"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func TestHumanReportShowsDeferralAndWindow(t *testing.T) {
	opening := time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)
	report := service.Report{Accounts: []service.AccountResult{{
		Account: "anonymous", Code: "ok",
		Status: syncer.Status{
			Deferred: 2, Failed: 0, NextAttempt: &opening, DeferralReason: "transfer_window",
			TransferWindow: &syncer.WindowStatus{
				Timezone: "America/New_York", Start: "23:00", End: "06:00",
				Open: false, NextOpening: &opening,
			},
		},
	}}}
	var out bytes.Buffer
	if err := printReport(&out, report, false); err != nil {
		t.Fatal(err)
	}
	line := out.String()
	for _, want := range []string{"deferred=2", "window=America/New_York 23:00-06:00 closed",
		"next_opening=2026-10-22T23:00:00Z", "deferral_reason=transfer_window",
		"next_attempt=2026-10-22T23:00:00Z"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in %q", want, line)
		}
	}
	if strings.Contains(line, "failed=1") || strings.Contains(line, "unconfirmed=1") {
		t.Fatalf("deferral was reported as failure: %q", line)
	}
}
