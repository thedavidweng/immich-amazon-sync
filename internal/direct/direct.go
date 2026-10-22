// Package direct resolves an Immich-reported absolute original path onto an
// operator-approved read-only local directory. It only ever opens regular files
// for reading: it never writes, creates, truncates or changes an mtime.
//
// The package separates "not eligible, use the Immich API" from "suspicious,
// fail closed" so callers can never silently read the wrong bytes:
//
//   - NotConfigured, OutsideRoot, RootUnavailable, Missing and Unreadable mean
//     the mounted copy cannot be used and the API stream remains authoritative.
//   - PathInvalid, PathEscape and PathNotRegular mean the reported path is
//     malformed or points at something other than a regular file. Those must
//     never fall back: the operator's mapping is wrong or hostile.
//
// Every open is confined with os.Root, so traversal and symlink escapes outside
// an approved root are impossible even when the path is attacker-controlled.
package direct

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Root maps one absolute Immich path prefix to a local directory that contains
// the same originals and is mounted read-only.
type Root struct {
	ImmichPath string
	HostPath   string
}

// Outcome is the finite result of resolving one reported original path.
type Outcome string

const (
	Opened          Outcome = "direct"
	NotConfigured   Outcome = "not_configured"
	OutsideRoot     Outcome = "outside_root"
	RootUnavailable Outcome = "root_unavailable"
	Missing         Outcome = "missing"
	Unreadable      Outcome = "unreadable"
	PathInvalid     Outcome = "path_invalid"
	PathEscape      Outcome = "path_escape"
	PathNotRegular  Outcome = "path_not_regular"
)

// FailClosed reports whether an outcome must stop the asset instead of falling
// back to the Immich API.
func (o Outcome) FailClosed() bool {
	switch o {
	case PathInvalid, PathEscape, PathNotRegular:
		return true
	}
	return false
}

// Code is the finite durable/error code for a fail-closed outcome.
func (o Outcome) Code() string {
	switch o {
	case PathInvalid:
		return "direct_path_invalid"
	case PathEscape:
		return "direct_path_escape"
	case PathNotRegular:
		return "direct_path_not_regular"
	}
	return ""
}

// ErrInvalidRoot is returned for a mapping that cannot be approved safely.
var ErrInvalidRoot = errors.New("direct_root_invalid")

type root struct {
	immich string
	host   string
}

// Set is an immutable, validated list of approved mappings. A nil Set is
// disabled and always reports NotConfigured.
type Set struct {
	roots []root
}

// New validates mappings and orders them so the longest approved prefix wins.
func New(roots []Root) (*Set, error) {
	s := &Set{}
	seen := make(map[string]bool, len(roots))
	for _, r := range roots {
		if !absolute(r.ImmichPath) || !absolute(r.HostPath) || seen[r.ImmichPath] {
			return nil, ErrInvalidRoot
		}
		seen[r.ImmichPath] = true
		s.roots = append(s.roots, root{immich: r.ImmichPath, host: r.HostPath})
	}
	sort.SliceStable(s.roots, func(i, j int) bool { return len(s.roots[i].immich) > len(s.roots[j].immich) })
	return s, nil
}

// Configured reports whether any root is approved.
func (s *Set) Configured() bool { return s != nil && len(s.roots) > 0 }

// Check reports approved roots and how many are currently openable. Root
// identities are never returned, so diagnostics stay path-free.
func (s *Set) Check() (configured, available int) {
	if s == nil {
		return 0, 0
	}
	for _, r := range s.roots {
		root, err := os.OpenRoot(r.host)
		if err != nil {
			continue
		}
		available++
		_ = root.Close()
	}
	return len(s.roots), available
}

// File is an opened regular original. Stable must be called after the caller
// has read it to completion and before the bytes are recorded as evidence.
type File struct {
	*os.File
	before fs.FileInfo
}

// Stable reports that size, mode and mtime did not change while the file was
// open. A false result means the bytes just read are not a stable original.
func (f *File) Stable() bool {
	after, err := f.Stat()
	if err != nil {
		return false
	}
	return after.Size() == f.before.Size() && after.Mode() == f.before.Mode() &&
		after.ModTime().Equal(f.before.ModTime())
}

func absolute(p string) bool {
	return p != "" && filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsRune(p, 0)
}

// Open resolves one Immich-reported path. A nil File means the caller must use
// the Immich API stream; only a fail-closed outcome may stop the asset.
func (s *Set) Open(sourcePath string) (*File, Outcome) {
	if !s.Configured() {
		return nil, NotConfigured
	}
	if sourcePath == "" {
		// The source did not report a path; nothing can be approved for it.
		return nil, OutsideRoot
	}
	if !absolute(sourcePath) {
		return nil, PathInvalid
	}
	matched := s.match(sourcePath)
	if matched == nil {
		return nil, OutsideRoot
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(sourcePath, matched.immich), "/")
	if rel == "" || !filepath.IsLocal(rel) || strings.ContainsRune(rel, 0) {
		return nil, PathInvalid
	}
	root, err := os.OpenRoot(matched.host)
	if err != nil {
		return nil, RootUnavailable
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		return nil, classify(err)
	}
	if !info.Mode().IsRegular() {
		return nil, PathNotRegular
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, classify(err)
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, Unreadable
	}
	if !opened.Mode().IsRegular() {
		_ = f.Close()
		return nil, PathNotRegular
	}
	return &File{File: f, before: opened}, Opened
}

func (s *Set) match(p string) *root {
	for i := range s.roots {
		r := &s.roots[i]
		if p == r.immich || strings.HasPrefix(p, r.immich+"/") {
			return r
		}
	}
	return nil
}

// classify maps an open or stat failure. Traversal and escapes are already
// excluded by validation, so an unexplained failure is treated as a rejected
// path rather than a mount that may be retried later.
func classify(err error) Outcome {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Missing
	case errors.Is(err, fs.ErrPermission):
		return Unreadable
	}
	return PathEscape
}
