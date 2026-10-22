// Package amazon adapts the public aphotos SDK to the non-destructive target.
package amazon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type Client struct {
	sdk metadataSDK
}

// Only metadata calls receive deadlines; the embedded SDK upload keeps its caller context.
type metadataSDK struct{ *aphotos.Client }

func metadataCall[T any](ctx context.Context, call func(context.Context) (T, error)) (T, error) {
	metadataCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := call(metadataCtx)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if metadataCtx.Err() != nil {
		return result, &bridge.Error{Code: bridge.Unavailable}
	}
	return result, err
}

func (s metadataSDK) GetRoot(ctx context.Context) (*aphotos.Node, error) {
	return metadataCall(ctx, s.Client.GetRoot)
}

func (s metadataSDK) GetNode(ctx context.Context, id string) (*aphotos.Node, error) {
	return metadataCall(ctx, func(ctx context.Context) (*aphotos.Node, error) {
		return s.Client.GetNode(ctx, id)
	})
}

func (s metadataSDK) MoveToTrash(ctx context.Context, nodeIDs []string) error {
	_, err := metadataCall(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.Client.MoveToTrash(ctx, nodeIDs)
	})
	return err
}

func (s metadataSDK) QueryNodes(ctx context.Context, opts aphotos.NodeQueryOptions) (*aphotos.Page[aphotos.Node], error) {
	return metadataCall(ctx, func(ctx context.Context) (*aphotos.Page[aphotos.Node], error) {
		return s.Client.QueryNodes(ctx, opts)
	})
}

func (s metadataSDK) ListChildren(ctx context.Context, id string, opts aphotos.ChildrenOptions) (*aphotos.Page[aphotos.Node], error) {
	return metadataCall(ctx, func(ctx context.Context) (*aphotos.Page[aphotos.Node], error) {
		return s.Client.ListChildren(ctx, id, opts)
	})
}

func (s metadataSDK) CreateAlbum(ctx context.Context, name string) (*aphotos.Node, error) {
	return metadataCall(ctx, func(ctx context.Context) (*aphotos.Node, error) {
		return s.Client.CreateAlbum(ctx, name)
	})
}

func (s metadataSDK) RenameNode(ctx context.Context, id, name string) (*aphotos.Node, error) {
	return metadataCall(ctx, func(ctx context.Context) (*aphotos.Node, error) {
		return s.Client.RenameNode(ctx, id, name)
	})
}

func (s metadataSDK) AddAlbumMembers(ctx context.Context, id string, members []string) error {
	_, err := metadataCall(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.Client.AddAlbumMembers(ctx, id, members)
	})
	return err
}

func (s metadataSDK) RemoveAlbumMembers(ctx context.Context, id string, members []string) error {
	_, err := metadataCall(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.Client.RemoveAlbumMembers(ctx, id, members)
	})
	return err
}

func (s metadataSDK) GetUsage(ctx context.Context) (*aphotos.Usage, error) {
	return metadataCall(ctx, s.Client.GetUsage)
}

func New(region, cookie string) (*Client, error) {
	cookie = strings.TrimSpace(cookie)
	if strings.ContainsAny(cookie, "\r\n") {
		return nil, &bridge.Error{Code: bridge.Authentication}
	}
	header := &http.Request{Header: http.Header{"Cookie": []string{cookie}}}
	cookies := make(map[string]string)
	for _, item := range header.Cookies() {
		key := aphotos.CanonicalCookieKey(item.Name)
		if old, exists := cookies[key]; exists && old != item.Value {
			return nil, &bridge.Error{Code: bridge.Authentication}
		}
		cookies[key] = item.Value
	}
	sdk, err := aphotos.NewClient(aphotos.Options{
		Region: aphotos.Region(region), Cookies: cookies, DisableRetries: true,
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("credential_redirect_rejected")
		}},
	})
	if err != nil {
		return nil, classify(err)
	}
	return NewWithClient(sdk)
}

// NewWithClient uses the SDK's HTTP transport seam for offline contract tests.
func NewWithClient(sdk *aphotos.Client) (*Client, error) {
	if sdk == nil {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	return &Client{sdk: metadataSDK{sdk}}, nil
}

// CaptureDateCapability is the capture-date write support proven by the pinned
// aphotos release. v0.1.4 exposes ContentDate and EXIF date fields only as
// response data and has no capture-date setter, so the finite answer is
// metadata_not_supported. Reporting supported requires a released SDK
// operation plus an authoritative read-back verification; rewriting original
// bytes or timestamps is never an acceptable substitute.
const CaptureDateCapability = bridge.MetadataNotSupported

func (c *Client) Probe(ctx context.Context) (bridge.Capabilities, error) {
	root, err := c.root(ctx)
	if err != nil {
		return bridge.Capabilities{}, err
	}
	rc := c.sdk.RegionConfig()
	return bridge.Capabilities{Region: string(rc.Region), Evidence: string(rc.Evidence), Identity: root.ID,
		CaptureDate: CaptureDateCapability}, nil
}

// Usage exposes only the SDK's categorized observation. LastCalculated stays
// zero when the provider omitted it; it is never replaced with "now".
func (c *Client) Usage(ctx context.Context) (bridge.Usage, error) {
	usage, err := c.sdk.GetUsage(ctx)
	if err != nil {
		return bridge.Usage{}, classify(err)
	}
	if usage == nil {
		return bridge.Usage{}, &bridge.Error{Code: bridge.Protocol}
	}
	categories := make(map[string]bridge.UsageCategory, len(usage.Categories))
	for name, category := range usage.Categories {
		categories[name] = bridge.UsageCategory{
			Billable: bridge.UsageBucket{Bytes: category.Billable.Bytes, Count: category.Billable.Count},
			Total:    bridge.UsageBucket{Bytes: category.Total.Bytes, Count: category.Total.Count},
		}
	}
	return bridge.Usage{LastCalculated: usage.LastCalculated, Categories: categories}, nil
}

func (c *Client) root(ctx context.Context) (*aphotos.Node, error) {
	node, err := c.sdk.GetRoot(ctx)
	if err != nil {
		return nil, classify(err)
	}
	if node == nil || node.ID == "" || !node.IsRoot || node.Kind != aphotos.NodeKindFolder ||
		node.Status != aphotos.NodeStatusAvailable {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	return node, nil
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var remote *bridge.Error
	if errors.As(err, &remote) {
		return remote
	}
	var auth *aphotos.AuthenticationError
	var permission *aphotos.PermissionError
	var invalid *aphotos.InvalidFilterError
	var rate *aphotos.RateLimitError
	var missing *aphotos.NotFoundError
	var conflict *aphotos.ConflictError
	var transient *aphotos.TransientNetworkError
	var partial *aphotos.PartialResultError
	switch {
	case errors.As(err, &auth):
		return &bridge.Error{Code: bridge.Authentication}
	case errors.As(err, &rate):
		return &bridge.Error{Code: bridge.RateLimit, RetryAfter: rate.RetryAfter}
	case errors.As(err, &transient):
		// 507 is an explicit capacity rejection, not a transient network
		// failure. Other 5xx responses remain retryable.
		if transient.StatusCode == http.StatusInsufficientStorage {
			return &bridge.Error{Code: bridge.Quota}
		}
		return &bridge.Error{Code: bridge.Unavailable, RetryAfter: transient.RetryAfter}
	case errors.As(err, &permission):
		if quotaMessage(permission.Message) {
			return &bridge.Error{Code: bridge.Quota}
		}
		return &bridge.Error{Code: bridge.Permission}
	case errors.As(err, &invalid):
		if quotaMessage(invalid.Message) {
			return &bridge.Error{Code: bridge.Quota}
		}
		return &bridge.Error{Code: bridge.Protocol}
	case errors.As(err, &conflict):
		if quotaMessage(conflict.Message) {
			return &bridge.Error{Code: bridge.Quota}
		}
		return &bridge.Error{Code: bridge.Conflict}
	case errors.As(err, &missing):
		return &bridge.Error{Code: bridge.NotFound}
	case errors.As(err, &partial):
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
	return &bridge.Error{Code: bridge.Protocol}
}

// quotaMessage recognises an explicit capacity rejection carried by an
// otherwise generic HTTP error class. The message is inspected only to choose a
// finite code; it is never logged, stored or returned to a caller.
func quotaMessage(message string) bool {
	lowered := strings.ToLower(message)
	for _, marker := range []string{
		"quota", "storage full", "storage limit", "storage capacity",
		"out of storage", "insufficient storage", "not enough storage",
		"out of space", "space limit", "not enough space",
	} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}
