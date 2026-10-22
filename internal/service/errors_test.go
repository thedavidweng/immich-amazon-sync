package service_test

import (
	"errors"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/service"
)

func TestEventAndAlbumErrorsHaveStablePublicCodes(t *testing.T) {
	for _, test := range []struct {
		code string
		exit int
	}{
		{"invalid_event", 2},
		{"album_unconfirmed", 1},
		{"membership_guard", 1},
		{"webhook_unauthorized", 3},
		{"webhook_unavailable", 1},
		{"library_invalid", 2},
		{"album_sync_mode_invalid", 2},
		{"album_sync_mode_conflict", 2},
		{"schema_migration_required", 4},
		{"ledger_absent", 4},
	} {
		t.Run(test.code, func(t *testing.T) {
			err := errors.New(test.code)
			if got := service.Code(err); got != test.code {
				t.Errorf("code = %q, want %q", got, test.code)
			}
			if got := service.ExitCode(err); got != test.exit {
				t.Errorf("exit = %d, want %d", got, test.exit)
			}
		})
	}
	if got := service.Code(errors.New("private remote response")); got != "operation_failed" {
		t.Fatalf("unrecognized error exposed: %q", got)
	}
}
