package amazon

import (
	"context"
	"strings"

	"github.com/thedavidweng/amazon-photos/pkg/aphotos"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

var _ bridge.Target = (*Client)(nil)

func ownedAlbum(n, root *aphotos.Node, id string) bool {
	return n != nil && n.ID != "" && n.ID == id && n.Kind == aphotos.NodeKindVisualCollection &&
		n.Status == aphotos.NodeStatusAvailable && root.OwnerID != "" && n.OwnerID == root.OwnerID
}

func (c *Client) album(ctx context.Context, id string) (*aphotos.Node, error) {
	if strings.TrimSpace(id) == "" {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	root, err := c.root(ctx)
	if err != nil {
		return nil, err
	}
	n, err := c.sdk.GetNode(ctx, id)
	if err != nil {
		return nil, classify(err)
	}
	if !ownedAlbum(n, root, id) {
		return nil, &bridge.Error{Code: bridge.Unconfirmed}
	}
	return n, nil
}

func (c *Client) CreateAlbum(ctx context.Context, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", &bridge.Error{Code: bridge.Protocol}
	}
	root, err := c.root(ctx)
	if err != nil {
		return "", err
	}
	if root.OwnerID == "" {
		return "", &bridge.Error{Code: bridge.Unconfirmed}
	}
	// No name lookup or initial members: a lost response cannot authorize adoption.
	created, err := c.sdk.CreateAlbum(ctx, name)
	if err != nil {
		return "", uncertainWrite(err, true)
	}
	if created == nil || created.ID == "" {
		return "", &bridge.Error{Code: bridge.Unconfirmed}
	}
	n, err := c.sdk.GetNode(ctx, created.ID)
	if err != nil {
		return "", unconfirmed(err)
	}
	if !ownedAlbum(n, root, created.ID) || n.Name != strings.TrimSpace(name) {
		return "", &bridge.Error{Code: bridge.Unconfirmed}
	}
	return n.ID, nil
}

func (c *Client) RenameAlbum(ctx context.Context, id, name string) error {
	if strings.TrimSpace(name) == "" {
		return &bridge.Error{Code: bridge.Protocol}
	}
	if _, err := c.album(ctx, id); err != nil {
		return err
	}
	if _, err := c.sdk.RenameNode(ctx, id, name); err != nil {
		return uncertainWrite(err, false)
	}
	n, err := c.album(ctx, id)
	if err != nil {
		return unconfirmed(err)
	}
	if n.Name != strings.TrimSpace(name) {
		return &bridge.Error{Code: bridge.Unconfirmed}
	}
	return nil
}

func (c *Client) AlbumMembers(ctx context.Context, id string) ([]string, error) {
	album, err := c.album(ctx, id)
	if err != nil {
		return nil, err
	}
	members := []string{}
	seen := map[string]bool{}
	total := -1
	for offset := 0; ; {
		page, err := c.sdk.ListChildren(ctx, id, aphotos.ChildrenOptions{Limit: 200, Offset: offset})
		if err != nil {
			return nil, classify(err)
		}
		if page == nil || !page.Complete || page.TruncatedByOffsetLimit || len(page.Warnings) != 0 ||
			page.Offset != offset || page.Limit != 200 || page.NextOffset != offset+page.Limit ||
			len(page.Items) > page.Limit || page.TotalCount < offset+len(page.Items) ||
			(total >= 0 && page.TotalCount != total) {
			return nil, &bridge.Error{Code: bridge.Unconfirmed}
		}
		total = page.TotalCount
		for _, node := range page.Items {
			if node.ID == "" || seen[node.ID] || node.Kind != aphotos.NodeKindFile ||
				node.Status != aphotos.NodeStatusAvailable || node.OwnerID != album.OwnerID {
				return nil, &bridge.Error{Code: bridge.Unconfirmed}
			}
			seen[node.ID] = true
			members = append(members, node.ID)
		}
		if !page.HasMore {
			if len(members) != total {
				return nil, &bridge.Error{Code: bridge.Unconfirmed}
			}
			return members, nil
		}
		if len(page.Items) != page.Limit || page.NextOffset >= total {
			return nil, &bridge.Error{Code: bridge.Unconfirmed}
		}
		offset = page.NextOffset
	}
}

func (c *Client) AddMembers(ctx context.Context, id string, members []string) error {
	return c.changeMembers(ctx, id, members, true)
}

func (c *Client) RemoveMembers(ctx context.Context, id string, members []string) error {
	return c.changeMembers(ctx, id, members, false)
}

func (c *Client) changeMembers(ctx context.Context, id string, members []string, add bool) error {
	if _, err := c.album(ctx, id); err != nil {
		return err
	}
	for _, member := range members {
		if _, err := c.GetNode(ctx, member); err != nil {
			return err
		}
	}
	if len(members) == 0 {
		return nil
	}
	var err error
	if add {
		err = c.sdk.AddAlbumMembers(ctx, id, members)
	} else {
		err = c.sdk.RemoveAlbumMembers(ctx, id, members)
	}
	return uncertainWrite(err, false)
}
