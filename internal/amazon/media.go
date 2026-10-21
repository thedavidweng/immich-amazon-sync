package amazon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

func validMD5(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16
}

func (c *Client) destination(ctx context.Context, parent string) (*aphotos.Node, string, error) {
	root, err := c.root(ctx)
	if err != nil {
		return nil, "", err
	}
	if parent == "" {
		parent = root.ID
	}
	if parent != root.ID {
		n, err := c.sdk.GetNode(ctx, parent)
		if err != nil {
			return nil, "", classify(err)
		}
		if n.ID != parent || n.Kind != aphotos.NodeKindFolder || n.Status != aphotos.NodeStatusAvailable ||
			root.OwnerID == "" || n.OwnerID != root.OwnerID {
			return nil, "", &bridge.Error{Code: bridge.Unconfirmed}
		}
	}
	return root, parent, nil
}

func confirmed(n *aphotos.Node, root *aphotos.Node, parent string) (bridge.Node, error) {
	if n == nil || n.ID == "" || n.Kind != aphotos.NodeKindFile || n.Status != aphotos.NodeStatusAvailable ||
		n.ContentProperties == nil || !validMD5(n.ContentProperties.MD5) || n.ContentProperties.Size < 0 ||
		!slices.Contains(n.Parents, parent) || (root.OwnerID != "" && n.OwnerID != root.OwnerID) {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	// SDK size defaults to zero; require explicit remote evidence, including for empty files.
	var raw struct {
		ContentProperties struct {
			Size *json.RawMessage `json:"size"`
		} `json:"contentProperties"`
	}
	if json.Unmarshal(n.Raw, &raw) != nil || raw.ContentProperties.Size == nil {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	var size json.Number
	if json.Unmarshal(*raw.ContentProperties.Size, &size) != nil {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	explicitSize, err := size.Int64()
	if err != nil || explicitSize != n.ContentProperties.Size {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	// The bridge represents the authenticated root as an empty parent.
	if parent == root.ID {
		parent = ""
	}
	return bridge.Node{
		ID: n.ID, Name: n.Name, ParentID: parent,
		MD5: strings.ToLower(n.ContentProperties.MD5), Size: n.ContentProperties.Size,
	}, nil
}

func (c *Client) Find(ctx context.Context, content bridge.Content) ([]bridge.Node, error) {
	if !validMD5(content.MD5) || content.Size < 0 {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	root, parent, err := c.destination(ctx, content.ParentID)
	if err != nil {
		return nil, err
	}
	page, err := c.sdk.QueryNodes(ctx, aphotos.NodeQueryOptions{
		Filter: aphotos.NewFilter().Kind(aphotos.NodeKindFile).Status(aphotos.NodeStatusAvailable).
			MD5(content.MD5).ParentID(parent),
		Limit: 200,
	})
	if err != nil {
		return nil, classify(err)
	}
	// Only a complete zero or unique match can authorize an absence/adoption decision.
	if page == nil || !page.Complete || page.HasMore || page.TruncatedByOffsetLimit ||
		page.TotalCount != len(page.Items) || len(page.Items) > 1 || page.Offset != 0 ||
		page.Limit != 200 || page.NextOffset != 200 || len(page.Warnings) != 0 {
		return nil, &bridge.Error{Code: bridge.Unconfirmed}
	}
	nodes := make([]bridge.Node, 0, len(page.Items))
	for i := range page.Items {
		node, err := confirmed(&page.Items[i], root, parent)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(node.MD5, content.MD5) || node.Size != content.Size {
			return nil, &bridge.Error{Code: bridge.Unconfirmed}
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (c *Client) Upload(ctx context.Context, content bridge.Content, body io.Reader) (bridge.Node, error) {
	if !validMD5(content.MD5) || content.Size < 0 || body == nil ||
		strings.TrimSpace(content.Name) == "" || content.Name == "." || content.Name == ".." ||
		strings.ContainsAny(content.Name, "/\\\r\n") {
		return bridge.Node{}, &bridge.Error{Code: bridge.Protocol}
	}
	root, parent, err := c.destination(ctx, content.ParentID)
	if err != nil {
		return bridge.Node{}, err
	}
	result, err := c.sdk.Upload(ctx, aphotos.UploadRequest{
		Name: content.Name, ParentNodeID: parent, Body: body, Size: content.Size,
		ContentType: content.MIME, ConflictPolicy: aphotos.ConflictFail,
	})
	if err != nil {
		return bridge.Node{}, uncertainWrite(err, false)
	}
	if result == nil || result.Status != aphotos.TransferConfirmed || result.NodeID == "" ||
		result.Bytes != content.Size || !strings.EqualFold(result.MD5, content.MD5) {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	remote, err := c.sdk.GetNode(ctx, result.NodeID)
	if err != nil {
		return bridge.Node{}, unconfirmed(err)
	}
	node, err := confirmed(remote, root, parent)
	if err != nil {
		return bridge.Node{}, err
	}
	if node.ID != result.NodeID || node.Size != content.Size || !strings.EqualFold(node.MD5, content.MD5) {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	return node, nil
}

func (c *Client) GetNode(ctx context.Context, id string) (bridge.Node, error) {
	root, err := c.root(ctx)
	if err != nil {
		return bridge.Node{}, err
	}
	n, err := c.sdk.GetNode(ctx, id)
	if err != nil {
		return bridge.Node{}, classify(err)
	}
	if n.ID != id {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	parent := root.ID
	if !slices.Contains(n.Parents, parent) {
		if len(n.Parents) != 1 {
			return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
		}
		_, parent, err = c.destination(ctx, n.Parents[0])
		if err != nil {
			return bridge.Node{}, err
		}
	}
	return confirmed(n, root, parent)
}

func uncertainWrite(err error, albumCreate bool) error {
	// Cancellation after dispatch cannot prove whether a mutation committed.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
	mapped := classify(err)
	var remote *bridge.Error
	if errors.As(mapped, &remote) && (remote.Code == bridge.Unavailable || remote.Code == bridge.Protocol ||
		(albumCreate && remote.Code == bridge.Conflict)) {
		return &bridge.Error{Code: bridge.Unconfirmed, RetryAfter: remote.RetryAfter}
	}
	return mapped
}

// Verification errors occur after a mutation may already have committed.
func unconfirmed(err error) error {
	result := &bridge.Error{Code: bridge.Unconfirmed}
	var remote *bridge.Error
	if errors.As(err, &remote) || errors.As(classify(err), &remote) {
		result.RetryAfter = remote.RetryAfter
	}
	return result
}
