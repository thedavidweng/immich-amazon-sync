// Package amazon adapts the public aphotos SDK to the non-destructive target.
package amazon

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type Client struct {
	sdk *aphotos.Client
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
	return &Client{sdk: sdk}, nil
}

func (c *Client) Probe(ctx context.Context) (bridge.Capabilities, error) {
	root, err := c.root(ctx)
	if err != nil {
		return bridge.Capabilities{}, err
	}
	rc := c.sdk.RegionConfig()
	return bridge.Capabilities{Region: string(rc.Region), Evidence: string(rc.Evidence), Identity: root.ID}, nil
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
	var auth *aphotos.AuthenticationError
	var permission *aphotos.PermissionError
	var rate *aphotos.RateLimitError
	var missing *aphotos.NotFoundError
	var conflict *aphotos.ConflictError
	var transient *aphotos.TransientNetworkError
	var partial *aphotos.PartialResultError
	code := bridge.Protocol
	switch {
	case errors.As(err, &auth):
		code = bridge.Authentication
	case errors.As(err, &permission):
		code = bridge.Permission
	case errors.As(err, &rate):
		return &bridge.Error{Code: bridge.RateLimit, RetryAfter: rate.RetryAfter}
	case errors.As(err, &missing):
		code = bridge.NotFound
	case errors.As(err, &conflict):
		code = bridge.Conflict
	case errors.As(err, &partial):
		code = bridge.Unconfirmed
	case errors.As(err, &transient):
		return &bridge.Error{Code: bridge.Unavailable, RetryAfter: transient.RetryAfter}
	}
	return &bridge.Error{Code: code}
}
