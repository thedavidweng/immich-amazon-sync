package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// ReadFavorite re-reads one verified, owned node and returns its favorite
// setting. The retained raw payload must contain an explicit settings.favorite
// boolean; an absent settings object or key is unknown, never false.
func (c *Client) ReadFavorite(ctx context.Context, content bridge.Content, nodeID string) (bool, error) {
	if nodeID == "" || !validMD5(content.MD5) || content.Size < 0 {
		return false, &bridge.Error{Code: bridge.Protocol}
	}
	root, err := c.root(ctx)
	if err != nil {
		return false, err
	}
	n, err := c.remoteNode(ctx, nodeID)
	if err != nil {
		return false, err
	}
	if err := favoriteNode(n, root, content); err != nil {
		return false, err
	}
	return favoriteValue(n)
}

// SetFavorite ensures one verified, owned node carries the requested favorite
// value. It reads the authoritative field first, so an unstar is only ever
// issued against an explicit remote true; performs at most one mutation; and
// re-reads the outcome. The SDK's mutation response is never treated as proof.
// A lost or timed-out mutation therefore leaves the node unconfirmed and the
// next call begins with a fresh read instead of another write.
func (c *Client) SetFavorite(ctx context.Context, content bridge.Content, nodeID string, favorite bool) error {
	if nodeID == "" || !validMD5(content.MD5) || content.Size < 0 {
		return &bridge.Error{Code: bridge.Protocol}
	}
	root, err := c.root(ctx)
	if err != nil {
		return err
	}
	n, err := c.remoteNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if err := favoriteNode(n, root, content); err != nil {
		return err
	}
	observed, err := favoriteValue(n)
	if err != nil {
		return err
	}
	if observed == favorite {
		return nil
	}
	if err := c.sdk.SetFavorite(ctx, []string{nodeID}, favorite); err != nil {
		return favoriteUncertain(err)
	}
	after, err := c.remoteNode(ctx, nodeID)
	if err != nil {
		return &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	if err := favoriteNode(after, root, content); err != nil {
		return &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	value, err := favoriteValue(after)
	if err != nil {
		return err
	}
	if value != favorite {
		return &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	return nil
}

// favoriteNode proves the node is an available, owned FILE whose content still
// matches the evidence recorded when the durable mapping was verified.
func favoriteNode(n, root *aphotos.Node, content bridge.Content) error {
	if n == nil || n.ID == "" || n.Kind != aphotos.NodeKindFile || n.Status != aphotos.NodeStatusAvailable ||
		root.OwnerID == "" || n.OwnerID != root.OwnerID ||
		n.ContentProperties == nil || !validMD5(n.ContentProperties.MD5) {
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
	size, err := explicitBytes(n)
	if err != nil {
		return err
	}
	if size != content.Size || !strings.EqualFold(n.ContentProperties.MD5, content.MD5) {
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
	return nil
}

// favoriteValue decodes only an explicit raw settings.favorite boolean. The
// SDK's typed NodeSettings.Favorite is a plain bool, so a settings object
// without the key decodes as a synthetic false; presence must come from Raw.
func favoriteValue(n *aphotos.Node) (bool, error) {
	var envelope struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if n == nil || json.Unmarshal(n.Raw, &envelope) != nil {
		return false, &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	field, ok := envelope.Settings["favorite"]
	if !ok {
		return false, &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	var value *bool
	if json.Unmarshal(field, &value) != nil || value == nil {
		return false, &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	return *value, nil
}

// favoriteUncertain maps a mutation whose committed state is unknowable to the
// finite favorite code. Definite remote rejections keep their own code.
func favoriteUncertain(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &bridge.Error{Code: bridge.FavoriteUnconfirmed}
	}
	mapped := classify(err)
	var remote *bridge.Error
	if errors.As(mapped, &remote) && (remote.Code == bridge.Unavailable || remote.Code == bridge.Protocol ||
		remote.Code == bridge.Conflict || remote.Code == bridge.Unconfirmed) {
		return &bridge.Error{Code: bridge.FavoriteUnconfirmed, RetryAfter: remote.RetryAfter}
	}
	return mapped
}
