package admin

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// The dashboard must show the same scheduling hint as the CLI and HTTP status:
// the next attempt, or an explicit "none scheduled".
func TestDashboardRendersNextAttempt(t *testing.T) {
	next := time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)
	pages := template.Must(template.New("pages").Parse(pageTemplates))
	for _, test := range []struct {
		name   string
		status syncer.Status
		want   string
	}{
		{"scheduled", syncer.Status{NextAttempt: &next}, "2026-10-22 23:00:00 UTC"},
		{"none", syncer.Status{}, "Next attempt: none scheduled."},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := pages.ExecuteTemplate(&out, "index", overview{
				Accounts: []accountView{{Status: test.status}},
			}); err != nil {
				t.Fatal(err)
			}
			if body := out.String(); !strings.Contains(body, "Next attempt: ") ||
				!strings.Contains(body, test.want) {
				t.Fatalf("dashboard omitted %q", test.want)
			}
		})
	}
}
