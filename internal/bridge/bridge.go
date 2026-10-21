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

type Asset struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	MIME           string    `json:"mime"`
	Kind           string    `json:"kind"`
	Size           int64     `json:"size"` // -1 means the source did not report a byte size.
	SHA1           string    `json:"sha1"` // Base64 content SHA-1; empty when provenance is unknown.
	TakenAt        time.Time `json:"taken_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LivePhotoVideo bool      `json:"live_photo_video"`
	ExcludedReason string    `json:"excluded_reason,omitempty"`
}

type Scope struct {
	AlbumID      string
	Library      bool
	UpdatedAfter time.Time
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

type Node struct {
	ID       string
	Name     string
	MD5      string
	Size     int64
	ParentID string // Empty identifies the authenticated root, verified by the adapter.
}

type Capabilities struct {
	Region   string `json:"region"`
	Evidence string `json:"evidence"`
	Identity string `json:"identity"`
}

// Target deliberately has no media deletion operation.
type Target interface {
	Probe(context.Context) (Capabilities, error)
	Find(context.Context, Content) ([]Node, error)
	Upload(context.Context, Content, io.Reader) (Node, error)
	GetNode(context.Context, string) (Node, error)
	CreateAlbum(context.Context, string) (string, error)
	RenameAlbum(context.Context, string, string) error
	AlbumMembers(context.Context, string) ([]string, error)
	AddMembers(context.Context, string, []string) error
	RemoveMembers(context.Context, string, []string) error
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
