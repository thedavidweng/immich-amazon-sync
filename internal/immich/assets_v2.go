package immich

import (
	"context"
	"strings"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

func (c *Client) assetsV2(ctx context.Context, scope bridge.Scope, cursor string) (bridge.Page, error) {
	const archivePrefix = "archive:"
	if archiveCursor, ok := strings.CutPrefix(cursor, archivePrefix); ok {
		if archiveCursor == "" {
			return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
		}
		page, err := c.assetsPage(ctx, scope, archiveCursor, "archive")
		if page.Next != "" {
			page.Next = archivePrefix + page.Next
		}
		return page, err
	}
	page, err := c.assetsPage(ctx, scope, cursor, "timeline")
	if err != nil || page.Next != "" {
		return page, err
	}
	// v2 has no union visibility filter. Join the transition page so an empty
	// timeline does not produce an empty continuation before archived originals.
	archive, err := c.assetsPage(ctx, scope, "", "archive")
	if err != nil {
		return bridge.Page{}, err
	}
	seen := make(map[string]bool, len(page.Assets))
	for _, a := range page.Assets {
		seen[a.ID] = true
	}
	for _, a := range archive.Assets {
		if seen[a.ID] {
			return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
		}
	}
	page.Assets = append(page.Assets, archive.Assets...)
	if archive.Next != "" {
		page.Next = archivePrefix + archive.Next
	}
	return page, nil
}
