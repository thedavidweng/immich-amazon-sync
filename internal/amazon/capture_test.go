package amazon_test

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/amazon"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// The pinned SDK is the only acceptable proof that a capture-date write
// operation exists. v0.1.4 exposes contentDate and EXIF dates as response data
// only: uploads, settings PATCH and rename cannot set them. If this test starts
// failing, the SDK gained such an operation; prove it against the released
// version and require an authoritative read-back before ever changing
// CaptureDateCapability to supported.
func TestPinnedSDKExposesNoCaptureDateSetter(t *testing.T) {
	suspicious := func(name string) bool {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "" {
			return false
		}
		for _, token := range []string{"capturedate", "contentdate", "datetime", "timestamp", "takenat", "exif"} {
			if strings.Contains(lower, token) {
				return true
			}
		}
		return strings.HasPrefix(lower, "set") && (strings.Contains(lower, "date") || strings.Contains(lower, "time"))
	}

	client := reflect.TypeOf((*aphotos.Client)(nil))
	for i := range client.NumMethod() {
		if name := client.Method(i).Name; suspicious(name) {
			t.Fatalf("aphotos.Client.%s may set a capture date; prove it and require read-back before claiming support", name)
		}
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(aphotos.UploadRequest{}),
		reflect.TypeOf(aphotos.UploadOptions{}),
		reflect.TypeOf(aphotos.NodeSettings{}),
	} {
		for i := range typ.NumField() {
			field := typ.Field(i)
			if suspicious(field.Name) || suspicious(field.Tag.Get("json")) {
				t.Fatalf("%s.%s is a capture-date write field; the capability must be re-proven", typ.Name(), field.Name)
			}
		}
	}
	if amazon.CaptureDateCapability != bridge.MetadataNotSupported {
		t.Fatalf("capability = %q, want %q: writable capture dates need a released SDK operation and read-back verification",
			amazon.CaptureDateCapability, bridge.MetadataNotSupported)
	}
}

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
