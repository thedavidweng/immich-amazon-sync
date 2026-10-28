// Package immich reads originals and metadata from the configured Immich API.
package immich

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

func New(baseURL, apiKey string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	for segment := range strings.SplitSeq(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, &bridge.Error{Code: bridge.Protocol}
		}
	}
	if strings.TrimSpace(apiKey) == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, &bridge.Error{Code: bridge.Authentication}
	}
	base := strings.TrimRight(u.String(), "/")
	if !strings.HasSuffix(base, "/api") {
		base += "/api"
	}
	return &Client{
		base: base, apiKey: apiKey,
		http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (c *Client) Identity(ctx context.Context) (string, error) {
	var user struct {
		ID string `json:"id"`
	}
	if err := c.json(ctx, http.MethodGet, "/users/me", nil, &user); err != nil {
		return "", err
	}
	if !uuid.MatchString(user.ID) {
		return "", &bridge.Error{Code: bridge.Protocol}
	}
	identity := sha256.Sum256([]byte(c.base + "\x00" + strings.ToLower(user.ID)))
	return fmt.Sprintf("immich:%x", identity), nil
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct{ Major, Minor, Patch *int }
	if err := c.json(ctx, http.MethodGet, "/server/version", nil, &v); err != nil {
		return "", err
	}
	if v.Major == nil || v.Minor == nil || v.Patch == nil || *v.Major < 0 || *v.Minor < 0 || *v.Patch < 0 {
		return "", &bridge.Error{Code: bridge.Protocol}
	}
	return fmt.Sprintf("%d.%d.%d", *v.Major, *v.Minor, *v.Patch), nil
}

func (c *Client) request(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	return c.requestStatus(ctx, method, path, payload, http.StatusOK)
}

// requestStatus accepts only the listed success statuses. Every other response
// becomes a finite error; response bodies never reach callers or logs.
func (c *Client) requestStatus(ctx context.Context, method, path string, payload any, allowed ...int) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, &bridge.Error{Code: bridge.Protocol}
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, readError(ctx)
	}
	if !slices.Contains(allowed, resp.StatusCode) {
		_ = resp.Body.Close()
		code := bridge.Protocol
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			code = bridge.Authentication
		case http.StatusForbidden:
			code = bridge.Permission
		case http.StatusNotFound:
			code = bridge.NotFound
		case http.StatusConflict:
			code = bridge.Conflict
		case http.StatusTooManyRequests:
			code = bridge.RateLimit
		default:
			if resp.StatusCode >= 500 {
				code = bridge.Unavailable
			}
		}
		delay := time.Duration(0)
		if seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 32); err == nil && seconds > 0 {
			delay = time.Duration(seconds) * time.Second
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			delay = max(0, time.Until(date))
		}
		return nil, &bridge.Error{Code: code, RetryAfter: delay}
	}
	return resp, nil
}

var errDecode = errors.New("response_decode")

func (c *Client) json(ctx context.Context, method, path string, payload, out any) error {
	return c.jsonStatus(ctx, method, path, payload, out, http.StatusOK)
}

func (c *Client) jsonStatus(ctx context.Context, method, path string, payload, out any, allowed ...int) error {
	metadataCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.requestStatus(metadataCtx, method, path, payload, allowed...)
	if err != nil {
		if ctx.Err() == nil && metadataCtx.Err() != nil {
			return &bridge.Error{Code: bridge.Unavailable}
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if metadataCtx.Err() != nil {
			return &bridge.Error{Code: bridge.Unavailable}
		}
		return fmt.Errorf("%w: %w", &bridge.Error{Code: bridge.Protocol}, errDecode)
	}
	return nil
}

func readError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &bridge.Error{Code: bridge.Unavailable}
}
