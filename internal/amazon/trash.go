package amazon

import (
	"context"
	"errors"
	"strings"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// Trash moves one verified, owned node to the account trash. It re-reads the
// node, proves ownership and content, and re-reads the outcome. It never
// permanently deletes and never adopts a node by name.
func (c *Client) Trash(ctx context.Context, content bridge.Content, nodeID string) error {
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
	if err := trashTarget(n, root, content); err != nil {
		return err
	}
	if n.Status == aphotos.NodeStatusTrash {
		// A retry after a committed mutation already reached the desired state.
		return nil
	}
	if err := c.sdk.MoveToTrash(ctx, []string{nodeID}); err != nil {
		return uncertainWrite(err, false)
	}
	after, err := c.remoteNode(ctx, nodeID)
	if err != nil {
		var remote *bridge.Error
		if errors.As(err, &remote) && remote.Code == bridge.NotFound {
			return nil
		}
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
	if after.Status != aphotos.NodeStatusTrash {
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
	return nil
}

// trashTarget proves the node is an owned FILE whose content still matches the
// evidence recorded when the mapping was verified. A trashed node may omit
// content properties, so metadata matching is required only while it is still
// available.
func trashTarget(n, root *aphotos.Node, content bridge.Content) error {
	if n == nil || n.ID == "" || n.Kind != aphotos.NodeKindFile ||
		root.OwnerID == "" || n.OwnerID != root.OwnerID {
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
	switch n.Status {
	case aphotos.NodeStatusAvailable:
		if n.ContentProperties == nil || !validMD5(n.ContentProperties.MD5) {
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
	case aphotos.NodeStatusTrash:
		return nil
	default:
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
}
