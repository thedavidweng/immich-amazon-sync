// Package filter defines the validated per-account eligibility policy. It
// decides whether a discovered Asset may be uploaded. It never decides that
// media may be deleted or that a Membership may be removed.
package filter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// Finite exclusion reasons. They are durable, reevaluable and never deletion
// evidence.
const (
	MediaType      = "media_type_excluded"
	RAW            = "raw_excluded"
	TooLarge       = "size_excluded"
	SizeUnknown    = "size_unknown"
	Archived       = "archived_excluded"
	Tag            = "tag_excluded"
	TagUnavailable = "tag_provenance_unavailable"
)

var reasons = []string{MediaType, RAW, TooLarge, SizeUnknown, Archived, Tag, TagUnavailable}

// IsReason reports whether code is a filter-managed exclusion reason.
func IsReason(code string) bool { return slices.Contains(reasons, code) }

// AllReasons returns the finite filter exclusion reasons.
func AllReasons() []string { return append([]string(nil), reasons...) }

// rawExtensions is the documented skip-RAW list. It is matched against the
// lowercased original filename extension, never against an assumed MIME type.
var rawExtensions = map[string]bool{
	".3fr": true, ".ari": true, ".arw": true, ".bay": true, ".cr2": true,
	".cr3": true, ".crw": true, ".dcr": true, ".dng": true, ".erf": true,
	".fff": true, ".gpr": true, ".iiq": true, ".k25": true, ".kdc": true,
	".mef": true, ".mos": true, ".mrw": true, ".nef": true, ".nrw": true,
	".orf": true, ".pef": true, ".raf": true, ".raw": true, ".rw2": true,
	".rwl": true, ".sr2": true, ".srf": true, ".srw": true, ".x3f": true,
}

// Policy is the effective per-account eligibility policy.
//
// MediaTypes defaults to images only, preserving the historical default.
// IncludeArchived defaults to true because archive was already eligible before
// filters existed; nil distinguishes "unset" from an explicit false.
type Policy struct {
	MediaTypes       []string `json:"media_types,omitempty"`
	MaxOriginalBytes int64    `json:"max_original_bytes,omitempty"`
	SkipRAW          bool     `json:"skip_raw,omitempty"`
	IncludeArchived  *bool    `json:"include_archived,omitempty"`
	ExcludedTags     []string `json:"excluded_tags,omitempty"`
}

func (p Policy) ArchivedIncluded() bool { return p.IncludeArchived == nil || *p.IncludeArchived }

// Normalize fills defaults and canonicalizes ordering so equivalent policies
// compare and fingerprint identically.
func (p Policy) Normalize() Policy {
	n := p
	if len(n.MediaTypes) == 0 {
		n.MediaTypes = []string{"image"}
	}
	types := make([]string, 0, len(n.MediaTypes))
	for _, media := range n.MediaTypes {
		types = append(types, strings.ToLower(strings.TrimSpace(media)))
	}
	slices.Sort(types)
	n.MediaTypes = slices.Compact(types)
	tags := make([]string, 0, len(p.ExcludedTags))
	for _, id := range p.ExcludedTags {
		tags = append(tags, strings.ToLower(strings.TrimSpace(id)))
	}
	slices.Sort(tags)
	n.ExcludedTags = slices.Compact(tags)
	included := p.ArchivedIncluded()
	n.IncludeArchived = &included
	return n
}

func (p Policy) Validate() error {
	if len(p.MediaTypes) == 0 {
		return errors.New("filter_media_type_invalid")
	}
	seen := map[string]bool{}
	for _, media := range p.MediaTypes {
		media = strings.ToLower(strings.TrimSpace(media))
		if (media != "image" && media != "video") || seen[media] {
			return errors.New("filter_media_type_invalid")
		}
		seen[media] = true
	}
	if p.MaxOriginalBytes < 0 {
		return errors.New("filter_max_original_bytes_invalid")
	}
	tags := map[string]bool{}
	for _, id := range p.ExcludedTags {
		if !stableID(id) || tags[id] {
			return errors.New("filter_excluded_tags_invalid")
		}
		tags[id] = true
	}
	return nil
}

// Fingerprint identifies the effective policy so the engine can requeue
// previously filtered Assets only when the rules actually change.
func (p Policy) Fingerprint() string {
	normalized := p.Normalize()
	data, _ := json.Marshal(normalized) // struct of scalar fields cannot fail
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Exclude returns the first applicable finite exclusion reason, or "" when the
// Asset is eligible. Order is stable so counts do not drift between runs.
func (p Policy) Exclude(a bridge.Asset) string {
	if a.ExcludedReason != "" {
		return ""
	}
	kind := mediaKind(a)
	if kind == "IMAGE" && !p.Includes("image") || kind == "VIDEO" && !p.Includes("video") {
		return MediaType
	}
	if p.SkipRAW && kind == "IMAGE" && rawExtensions[strings.ToLower(filepath.Ext(a.Name))] {
		return RAW
	}
	if !p.ArchivedIncluded() && a.Archived {
		return Archived
	}
	if p.MaxOriginalBytes > 0 {
		if a.Size < 0 {
			return SizeUnknown
		}
		if a.Size > p.MaxOriginalBytes {
			return TooLarge
		}
	}
	if len(p.ExcludedTags) > 0 {
		if a.Tags == nil {
			return TagUnavailable
		}
		for _, tag := range a.Tags {
			if slices.Contains(p.ExcludedTags, tag.ID) {
				return Tag
			}
		}
	}
	return ""
}

// Includes reports whether the normalized media type is included.
func (p Policy) Includes(media string) bool {
	for _, value := range p.MediaTypes {
		if strings.EqualFold(value, media) {
			return true
		}
	}
	return false
}

func mediaKind(a bridge.Asset) string {
	if kind := strings.ToUpper(a.Kind); kind != "" {
		return kind
	}
	switch {
	case strings.HasPrefix(a.MIME, "image/"):
		return "IMAGE"
	case strings.HasPrefix(a.MIME, "video/"):
		return "VIDEO"
	}
	return ""
}

// stableID accepts the UUID shape the pinned Immich TagResponseDto uses for
// stable tag identity. Anything else is rejected rather than matched loosely.
func stableID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
