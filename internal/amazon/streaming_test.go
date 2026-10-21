package amazon_test

import (
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type zeroStream struct{}

func (zeroStream) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestFiveGiBUploadStreamsAndVerifiesRemoteContentWithinHeapBudget(t *testing.T) {
	const size int64 = 5 << 30
	// Independently precomputed MD5 of 5 GiB (5368709120) zero bytes.
	const digest = "ec4bcc8776ea04479b786e063a9ace45"
	node := strings.NewReplacer("900150983cd24fb0d6963f7d28e17f72", digest, `"size":3`, `"size":5368709120`).Replace(nodeJSON)
	var uploaded int64
	var posts, verified int
	c := newDeadlineClient(t, deadlineTransport(func(r *http.Request) (*http.Response, error) {
		body := node
		switch r.URL.Path {
		case "/nodes":
			body = `{"count":1,"data":[` + rootJSON + `]}`
		case "/upload":
			posts++
			if r.Method != http.MethodPost || r.ContentLength != size {
				t.Errorf("upload request: method=%s ContentLength=%d", r.Method, r.ContentLength)
			}
			buffer := make([]byte, 64<<10)
			for {
				n, err := r.Body.Read(buffer)
				uploaded += int64(n)
				if err == io.EOF {
					break
				}
				if err != nil {
					return nil, err
				}
			}
			if err := r.Body.Close(); err != nil {
				return nil, err
			}
		case "/nodes/node-a":
			verified++
		default:
			t.Errorf("unexpected SDK request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	confirmed, err := c.Upload(t.Context(), bridge.Content{
		Name: "photo.jpg", MIME: "image/jpeg", Size: size, MD5: digest,
	}, io.LimitReader(zeroStream{}, size))
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("successful 5 GiB SDK upload allocated %d bytes", allocated)
	if err != nil || confirmed.ID != "node-a" || confirmed.MD5 != digest ||
		confirmed.Size != size || confirmed.ParentID != "" || uploaded != size || posts != 1 || verified != 1 {
		t.Fatalf("stream not independently confirmed: %+v err=%v bytes=%d posts=%d verified=%d",
			confirmed, err, uploaded, posts, verified)
	}
	if allocated > 8<<20 {
		t.Errorf("5 GiB streaming allocated %d bytes", allocated)
	}
}
