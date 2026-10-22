// Package bridge defines the two external boundaries used by the sync use case.
package bridge

import (
	"context"
	"io"
	"time"
)

type Album struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// AlbumSyncMode is the configured album mirroring scope. Off performs no
// album writes. Selected mirrors exactly the explicitly configured albums.
// AllAccessible mirrors every album the caller can read, discovered by its
// stable source UUID; it never adopts a destination album by name.
type AlbumSyncMode string

const (
	AlbumSyncOff           AlbumSyncMode = "off"
	AlbumSyncSelected      AlbumSyncMode = "selected"
	AlbumSyncAllAccessible AlbumSyncMode = "all_accessible"
)

func (m AlbumSyncMode) Valid() bool {
	return m == AlbumSyncOff || m == AlbumSyncSelected || m == AlbumSyncAllAccessible
}

type Asset struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	MIME           string    `json:"mime"`
	Kind           string    `json:"kind"`
	Size           int64     `json:"size"` // -1 means the source did not report a byte size.
	SHA1           string    `json:"sha1"` // Base64 content SHA-1; empty when provenance is unknown.
	TakenAt        time.Time `json:"taken_at"`
	FileModifiedAt time.Time `json:"file_modified_at"`
	LivePhotoVideo bool      `json:"live_photo_video"`
	ExcludedReason string    `json:"excluded_reason,omitempty"`
	// OriginalPath is the absolute path the source reports for this original.
	// It is transient by design: it is never marshalled into the durable asset
	// metadata, never logged and only used to resolve one approved read-only
	// mount. Callers must re-read it instead of trusting a stored copy.
	OriginalPath string `json:"-"`
}

type Scope struct {
	AlbumID      string
	AssetID      string
	Library      bool
	UpdatedAfter time.Time
	// IncludeTrashed asks the source to return trashed assets as well, so the
	// caller can observe a source deletion. Off unless deletion propagation is
	// explicitly enabled.
	IncludeTrashed bool
}

type Page struct {
	Assets []Asset
	Next   string
}

type Source interface {
	Identity(context.Context) (string, error)
	Version(context.Context) (string, error)
	Albums(context.Context) ([]Album, error)
	Album(context.Context, string) (Album, error)
	Assets(context.Context, Scope, string) (Page, error)
	Asset(context.Context, string) (Asset, error)
	Original(context.Context, string) (io.ReadCloser, error)
}

type Content struct {
	Name     string
	MIME     string
	Size     int64
	MD5      string
	TakenAt  time.Time
	ParentID string // Empty identifies the authenticated root.
}

// Placement is the verified location of a media node relative to the requested
// target parent. It is derived by re-reading the node and its parents, never
// from a lookup hit, filename or HTTP status.
type Placement string

const (
	PlacementTarget Placement = "target" // In the requested target folder.
	PlacementFolder Placement = "folder" // In another owned folder.
	PlacementAlbum  Placement = "album"  // Only reachable through owned visual collections.
)

// Node is media whose content, status, ownership and placement were verified.
// ParentID is the verified folder parent; empty identifies the authenticated
// root unless AlbumOnly is set.
type Node struct {
	ID        string
	Name      string
	MD5       string
	Size      int64
	ParentID  string
	AlbumOnly bool
}

// Placement reports the node's verified location relative to target.
// target empty identifies the authenticated root.
func (n Node) Placement(target string) Placement {
	switch {
	case n.AlbumOnly:
		return PlacementAlbum
	case n.ParentID == target:
		return PlacementTarget
	default:
		return PlacementFolder
	}
}

// Target deliberately has no media deletion operation.
type Target interface {
	Probe(context.Context) (Capabilities, error)
	// Find is the strict target-scoped lookup. It may only return verified
	// nodes already placed at the requested parent, and must prove a complete
	// zero-or-one page.
	Find(context.Context, Content) ([]Node, error)
	// FindExisting performs the controlled account-wide MD5 lookup. A zero ID
	// means the complete enumeration proved that no candidate exists. Multiple
	// matches, mismatched metadata, unproven ownership and incomplete
	// pagination are errors, never absence and never success.
	FindExisting(context.Context, Content) (Node, error)
	Upload(context.Context, Content, io.Reader) (Node, error)
	// GetNode re-reads one known node and verifies its content, ownership and
	// placement. It never adopts a node by name.
	GetNode(context.Context, string) (Node, error)
	// Trash moves one verified, owned node to the remote trash. It re-reads the
	// node and its ownership first and must never permanently delete. Callers
	// supply the durable node mapping and the content evidence it was verified
	// with; a mismatch is an error, not a best-effort delete.
	Trash(context.Context, Content, string) error
	CreateAlbum(context.Context, string) (string, error)
	RenameAlbum(context.Context, string, string) error
	AlbumMembers(context.Context, string) ([]string, error)
	AddMembers(context.Context, string, []string) error
	RemoveMembers(context.Context, string, []string) error
}

type Capabilities struct {
	Region   string `json:"region"`
	Evidence string `json:"evidence"`
	Identity string `json:"identity"`
}

type Error struct {
	Code       string
	RetryAfter time.Duration
}

// Error messages are finite codes: external error bodies and URLs are private.
func (e *Error) Error() string { return e.Code }

const (
	Authentication = "authentication_required"
	Permission     = "permission_denied"
	RateLimit      = "rate_limited"
	Unavailable    = "remote_unavailable"
	NotFound       = "not_found"
	Conflict       = "content_conflict"
	Unconfirmed    = "upload_unconfirmed"
	Protocol       = "remote_protocol"
	Unsupported    = "unsupported_media"
)
