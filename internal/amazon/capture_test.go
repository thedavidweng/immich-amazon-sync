package amazon_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// Probe reports the static capability next to the remote root identity. It must
// stay a finite code so doctor and status can never imply success.
func TestProbeReportsFiniteCaptureDateCapability(t *testing.T) {
	c := newClient(t, aphotos.RegionUS, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/nodes" || r.URL.Query().Get("filters") != "isRoot:true" {
			t.Errorf("probe must only read root: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[`+rootJSON+`],"count":1}`)
	})
	probe, err := c.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if probe.CaptureDate != bridge.MetadataNotSupported {
		t.Fatalf("capture_date = %q, want %q", probe.CaptureDate, bridge.MetadataNotSupported)
	}
}
