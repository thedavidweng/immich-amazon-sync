package immich

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

var _ bridge.Source = (*Client)(nil)

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type albumResponse struct {
	ID     string `json:"id"`
	Name   string `json:"albumName"`
	Count  *int   `json:"assetCount"`
	Shared *bool  `json:"shared"`
}

func (a albumResponse) album() (bridge.Album, error) {
	if !uuid.MatchString(a.ID) || a.Count == nil || *a.Count < 0 {
		return bridge.Album{}, &bridge.Error{Code: bridge.Protocol}
	}
	return bridge.Album{ID: a.ID, Name: a.Name, Count: *a.Count}, nil
}

func (c *Client) Albums(ctx context.Context) ([]bridge.Album, error) {
	var response []albumResponse
	if err := c.json(ctx, http.MethodGet, "/albums", nil, &response); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	albums := make([]bridge.Album, 0, len(response))
	for _, a := range response {
		album, err := a.album()
		if err != nil {
			return nil, err
		}
		albums = append(albums, album)
	}
	return albums, nil
}

func (c *Client) Album(ctx context.Context, id string) (bridge.Album, error) {
	if !uuid.MatchString(id) {
		return bridge.Album{}, &bridge.Error{Code: bridge.Protocol}
	}
	version, err := c.Version(ctx)
	if err != nil {
		return bridge.Album{}, err
	}
	a, err := c.albumMetadata(ctx, id, version)
	if err != nil {
		return bridge.Album{}, err
	}
	return a.album()
}

func (c *Client) albumMetadata(ctx context.Context, id, version string) (albumResponse, error) {
	path := "/albums/" + id
	if strings.HasPrefix(version, "2.") {
		path += "?withoutAssets=true"
	}
	var a albumResponse
	if err := c.json(ctx, http.MethodGet, path, nil, &a); err != nil {
		return albumResponse{}, err
	}
	if a.ID != id {
		return albumResponse{}, &bridge.Error{Code: bridge.Protocol}
	}
	return a, nil
}

type tagResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type assetResponse struct {
	ID               string          `json:"id"`
	Name             string          `json:"originalFileName"`
	MIME             string          `json:"originalMimeType"`
	Kind             string          `json:"type"`
	Checksum         string          `json:"checksum"`
	TakenAt          time.Time       `json:"fileCreatedAt"`
	FileModifiedAt   time.Time       `json:"fileModifiedAt"`
	LivePhotoVideoID string          `json:"livePhotoVideoId"`
	LibraryID        json.RawMessage `json:"libraryId"`
	Visibility       string          `json:"visibility"`
	Archived         *bool           `json:"isArchived"`
	Tags             json.RawMessage `json:"tags"`
	Offline          *bool           `json:"isOffline"`
	Trashed          *bool           `json:"isTrashed"`
	Exif             *struct {
		Size *int64 `json:"fileSizeInByte"`
	} `json:"exifInfo"`
}

func (a assetResponse) asset() (bridge.Asset, error) {
	if !uuid.MatchString(a.ID) || a.Name == "" || a.Offline == nil || a.Trashed == nil {
		return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
	}
	reason := ""
	switch a.Visibility {
	case "timeline", "archive":
	case "hidden", "locked":
		reason = a.Visibility
	default:
		return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
	}
	if *a.Trashed {
		reason = "trashed"
	} else if *a.Offline {
		reason = "offline"
	}
	switch a.Kind {
	case "IMAGE", "VIDEO":
	case "AUDIO", "OTHER":
		reason = "unsupported_type"
	default:
		return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
	}
	sha1 := ""
	if len(a.LibraryID) != 0 {
		var library *string
		if err := json.Unmarshal(a.LibraryID, &library); err != nil {
			return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
		}
		if library != nil {
			if !uuid.MatchString(*library) {
				return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
			}
		} else {
			checksum, err := base64.StdEncoding.DecodeString(a.Checksum)
			if err != nil || len(checksum) != 20 {
				return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
			}
			sha1 = a.Checksum
		}
	}
	size := int64(-1)
	if a.Exif != nil && a.Exif.Size != nil {
		size = *a.Exif.Size
		if size < 0 {
			return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
		}
	}
	archived := a.Visibility == "archive"
	if a.Archived != nil && *a.Archived {
		archived = true
	}
	// The pinned contract makes tags optional. Absence is unknown provenance,
	// never an authoritative empty list; a present array (including []) is.
	var tags []bridge.Tag
	if len(a.Tags) != 0 {
		var raw []tagResponse
		if string(a.Tags) == "null" || json.Unmarshal(a.Tags, &raw) != nil {
			return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
		}
		tags = make([]bridge.Tag, 0, len(raw))
		for _, tag := range raw {
			if !uuid.MatchString(tag.ID) {
				return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
			}
			tags = append(tags, bridge.Tag{ID: tag.ID, Name: tag.Name, Value: tag.Value})
		}
	}
	return bridge.Asset{
		ID: a.ID, Name: a.Name, MIME: a.MIME, Kind: a.Kind, Size: size,
		SHA1: sha1, TakenAt: a.TakenAt, FileModifiedAt: a.FileModifiedAt,
		LivePhotoVideo: a.Kind == "VIDEO" && a.Visibility == "hidden",
		Archived:       archived, Tags: tags, ExcludedReason: reason,
	}, nil
}

func (c *Client) Assets(ctx context.Context, scope bridge.Scope, cursor string) (bridge.Page, error) {
	if scope.Library == (scope.AlbumID != "") || (scope.AlbumID != "" && !uuid.MatchString(scope.AlbumID)) ||
		(scope.AssetID != "" && !uuid.MatchString(scope.AssetID)) {
		return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
	}
	version, err := c.Version(ctx)
	if err != nil {
		return bridge.Page{}, err
	}
	if strings.HasPrefix(version, "2.") && scope.AlbumID != "" {
		album, err := c.albumMetadata(ctx, scope.AlbumID, version)
		if err != nil {
			return bridge.Page{}, err
		}
		if _, err := album.album(); err != nil || album.Shared == nil {
			return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
		}
		if *album.Shared {
			return bridge.Page{}, &bridge.Error{Code: bridge.Unsupported}
		}
	} else if !strings.HasPrefix(version, "2.") && !strings.HasPrefix(version, "3.") {
		return bridge.Page{}, &bridge.Error{Code: bridge.Unsupported}
	}
	if strings.HasPrefix(version, "2.") {
		return c.assetsV2(ctx, scope, cursor)
	}
	return c.assetsPage(ctx, scope, cursor, "")
}

func (c *Client) assetsPage(ctx context.Context, scope bridge.Scope, cursor, visibility string) (bridge.Page, error) {
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 || strconv.Itoa(n) != cursor {
			return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
		}
		page = n
	}
	payload := map[string]any{"page": page, "size": 250, "withExif": true, "order": "asc"}
	if scope.IncludeTrashed {
		payload["withDeleted"] = true
	}
	if visibility != "" {
		payload["visibility"] = visibility
	}
	if scope.AlbumID != "" {
		payload["albumIds"] = []string{scope.AlbumID}
	}
	if scope.AssetID != "" {
		payload["id"] = scope.AssetID
	}
	if !scope.UpdatedAfter.IsZero() {
		payload["updatedAfter"] = scope.UpdatedAfter.UTC().Format(time.RFC3339Nano)
	}
	var response struct {
		Assets *struct {
			Count    *int             `json:"count"`
			Items    *[]assetResponse `json:"items"`
			NextPage json.RawMessage  `json:"nextPage"`
		} `json:"assets"`
	}
	if err := c.json(ctx, http.MethodPost, "/search/metadata", payload, &response); err != nil {
		return bridge.Page{}, err
	}
	a := response.Assets
	if a == nil || a.Count == nil || a.Items == nil || *a.Count != len(*a.Items) || len(*a.Items) > 250 || len(a.NextPage) == 0 {
		return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
	}
	var next *string
	if err := json.Unmarshal(a.NextPage, &next); err != nil {
		return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
	}
	result := bridge.Page{Assets: make([]bridge.Asset, 0, len(*a.Items))}
	if next != nil {
		if *next != strconv.Itoa(page+1) || len(*a.Items) == 0 {
			return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
		}
		result.Next = *next
	}
	if page > 1 && len(*a.Items) == 0 {
		return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
	}
	seen := make(map[string]bool, len(*a.Items))
	for _, raw := range *a.Items {
		asset, err := raw.asset()
		if err != nil {
			return bridge.Page{}, err
		}
		if seen[asset.ID] {
			return bridge.Page{}, &bridge.Error{Code: bridge.Protocol}
		}
		seen[asset.ID] = true
		result.Assets = append(result.Assets, asset)
	}
	return result, nil
}

func (c *Client) Asset(ctx context.Context, id string) (bridge.Asset, error) {
	if !uuid.MatchString(id) {
		return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
	}
	var a assetResponse
	if err := c.json(ctx, http.MethodGet, "/assets/"+id, nil, &a); err != nil {
		return bridge.Asset{}, err
	}
	if a.ID != id {
		return bridge.Asset{}, &bridge.Error{Code: bridge.Protocol}
	}
	return a.asset()
}

func (c *Client) Original(ctx context.Context, id string) (io.ReadCloser, error) {
	if !uuid.MatchString(id) {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	resp, err := c.request(ctx, http.MethodGet, "/assets/"+id+"/original", nil)
	if err != nil {
		return nil, err
	}
	return &originalStream{ReadCloser: resp.Body, ctx: ctx}, nil
}

type originalStream struct {
	io.ReadCloser
	ctx context.Context
}

func (s *originalStream) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		return n, readError(s.ctx)
	}
	return n, err
}

func (s *originalStream) Close() error {
	if err := s.ReadCloser.Close(); err != nil {
		return readError(s.ctx)
	}
	return nil
}
