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

// remoteNode reads one node through the SDK with the metadata deadline and
// without inferring anything about its placement.
func (c *Client) remoteNode(ctx context.Context, id string) (*aphotos.Node, error) {
	n, err := c.sdk.GetNode(ctx, id)
	if err != nil {
		return nil, classify(err)
	}
	if n == nil || n.ID != id {
		return nil, &bridge.Error{Code: bridge.Unconfirmed}
	}
	return n, nil
}

// explicitBytes requires the remote size to be present rather than an SDK zero default.
func explicitBytes(n *aphotos.Node) (int64, error) {
	if n == nil || n.ContentProperties == nil {
		return 0, &bridge.Error{Code: bridge.Unconfirmed}
	}
	var raw struct {
		ContentProperties struct {
			Size *json.RawMessage `json:"size"`
		} `json:"contentProperties"`
	}
	if json.Unmarshal(n.Raw, &raw) != nil || raw.ContentProperties.Size == nil {
		return 0, &bridge.Error{Code: bridge.Unconfirmed}
	}
	var size json.Number
	if json.Unmarshal(*raw.ContentProperties.Size, &size) != nil {
		return 0, &bridge.Error{Code: bridge.Unconfirmed}
	}
	value, err := size.Int64()
	if err != nil || value != n.ContentProperties.Size {
		return 0, &bridge.Error{Code: bridge.Unconfirmed}
	}
	return value, nil
}

// verifiedMedia accepts a FILE whose content and ownership are authoritative.
func verifiedMedia(n, root *aphotos.Node) (bridge.Node, error) {
	if n == nil || n.ID == "" || n.Kind != aphotos.NodeKindFile || n.Status != aphotos.NodeStatusAvailable ||
		n.ContentProperties == nil || !validMD5(n.ContentProperties.MD5) || n.ContentProperties.Size < 0 ||
		root.OwnerID == "" || n.OwnerID != root.OwnerID {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	size, err := explicitBytes(n)
	if err != nil {
		return bridge.Node{}, err
	}
	return bridge.Node{ID: n.ID, Name: n.Name, MD5: strings.ToLower(n.ContentProperties.MD5), Size: size}, nil
}

// placed classifies a verified media node against its parents. target is the
// requested destination folder (the authenticated root when empty). The root
// and target folders were already verified by the caller; every other parent
// must be re-read to prove kind, status and ownership. A node with no folder
// parent but at least one owned album is an album-only copy.
func (c *Client) placed(ctx context.Context, media bridge.Node, n, root *aphotos.Node, target string) (bridge.Node, error) {
	if target == "" {
		target = root.ID
	}
	var folders, albums []string
	seen := map[string]bool{}
	for _, id := range n.Parents {
		if id == "" || seen[id] {
			return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
		}
		seen[id] = true
		// The root and an explicit target were verified by destination().
		kind := aphotos.NodeKindFolder
		if id != root.ID && id != target {
			parent, err := c.remoteNode(ctx, id)
			if err != nil {
				return bridge.Node{}, err
			}
			if parent.Status != aphotos.NodeStatusAvailable || parent.OwnerID != root.OwnerID {
				return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
			}
			kind = parent.Kind
		}
		switch kind {
		case aphotos.NodeKindFolder:
			folders = append(folders, id)
		case aphotos.NodeKindVisualCollection:
			albums = append(albums, id)
		default:
			return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
		}
	}
	node := media
	switch {
	case slices.Contains(folders, target):
		// The bridge represents the authenticated root as an empty parent.
		if target != root.ID {
			node.ParentID = target
		}
		return node, nil
	case len(folders) == 1:
		node.ParentID = folders[0]
		return node, nil
	case len(folders) == 0 && len(albums) > 0:
		node.ParentID, node.AlbumOnly = "", true
		return node, nil
	}
	return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
}

func (c *Client) verifiedNode(ctx context.Context, n, root *aphotos.Node, target string) (bridge.Node, error) {
	media, err := verifiedMedia(n, root)
	if err != nil {
		return bridge.Node{}, err
	}
	return c.placed(ctx, media, n, root, target)
}

// uniquePage accepts only one complete page proving zero or one match.
func uniquePage(page *aphotos.Page[aphotos.Node]) bool {
	return page != nil && page.Complete && !page.HasMore && !page.TruncatedByOffsetLimit &&
		page.TotalCount == len(page.Items) && len(page.Items) <= 1 && page.Offset == 0 &&
		page.Limit == 200 && page.NextOffset == 200 && len(page.Warnings) == 0
}

// Find is the strict target-scoped lookup. Its result may only describe nodes
// already at the requested parent.
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
	// Only a complete zero or unique match can authorize an absence decision.
	if !uniquePage(page) {
		return nil, &bridge.Error{Code: bridge.Unconfirmed}
	}
	target := parent
	if parent == root.ID {
		// The bridge represents the authenticated root as an empty parent.
		target = ""
	}
	nodes := make([]bridge.Node, 0, len(page.Items))
	for i := range page.Items {
		if !slices.Contains(page.Items[i].Parents, parent) {
			return nil, &bridge.Error{Code: bridge.Unconfirmed}
		}
		node, err := c.verifiedNode(ctx, &page.Items[i], root, target)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(node.MD5, content.MD5) || node.Size != content.Size ||
			node.ParentID != target || node.AlbumOnly {
			return nil, &bridge.Error{Code: bridge.Unconfirmed}
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// FindExisting performs the controlled account-wide MD5 lookup. A zero ID
// means the complete enumeration proved that no candidate exists. A single
// AVAILABLE, owned node is re-read and its placement verified; multiple
// matches and incomplete pagination remain unconfirmed.
func (c *Client) FindExisting(ctx context.Context, content bridge.Content) (bridge.Node, error) {
	if !validMD5(content.MD5) || content.Size < 0 {
		return bridge.Node{}, &bridge.Error{Code: bridge.Protocol}
	}
	root, _, err := c.destination(ctx, content.ParentID)
	if err != nil {
		return bridge.Node{}, err
	}
	page, err := c.sdk.QueryNodes(ctx, aphotos.NodeQueryOptions{
		Filter: aphotos.NewFilter().Kind(aphotos.NodeKindFile).Status(aphotos.NodeStatusAvailable).
			MD5(content.MD5),
		Limit: 200,
	})
	if err != nil {
		return bridge.Node{}, classify(err)
	}
	if !uniquePage(page) {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	if len(page.Items) == 0 {
		return bridge.Node{}, nil
	}
	id := page.Items[0].ID
	if id == "" {
		return bridge.Node{}, &bridge.Error{Code: bridge.Protocol}
	}
	// A search hit is a pointer. Re-read the node and its parents before reuse.
	n, err := c.remoteNode(ctx, id)
	if err != nil {
		return bridge.Node{}, err
	}
	node, err := c.verifiedNode(ctx, n, root, content.ParentID)
	if err != nil {
		return bridge.Node{}, err
	}
	if node.ID != id || node.Size != content.Size || !strings.EqualFold(node.MD5, content.MD5) {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	return node, nil
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
	remote, err := c.remoteNode(ctx, result.NodeID)
	if err != nil {
		return bridge.Node{}, unconfirmed(err)
	}
	target := parent
	if parent == root.ID {
		target = ""
	}
	node, err := c.verifiedNode(ctx, remote, root, target)
	if err != nil {
		return bridge.Node{}, unconfirmed(err)
	}
	if node.ID != result.NodeID || node.Size != content.Size ||
		!strings.EqualFold(node.MD5, content.MD5) || node.ParentID != target || node.AlbumOnly {
		return bridge.Node{}, &bridge.Error{Code: bridge.Unconfirmed}
	}
	return node, nil
}

// GetNode re-reads one known node against the root target and verifies its
// content, ownership and placement.
func (c *Client) GetNode(ctx context.Context, id string) (bridge.Node, error) {
	root, err := c.root(ctx)
	if err != nil {
		return bridge.Node{}, err
	}
	n, err := c.remoteNode(ctx, id)
	if err != nil {
		return bridge.Node{}, err
	}
	return c.verifiedNode(ctx, n, root, "")
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
