package service

import (
	"context"
	"errors"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// Code is an allowlist, never a renderer for external error messages.
func Code(err error) string {
	if err == nil {
		return "ok"
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var selected error
		for _, child := range joined.Unwrap() {
			if selected == nil || ExitCode(child) > ExitCode(selected) {
				selected = child
			}
		}
		return Code(selected)
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var remote *bridge.Error
	if errors.As(err, &remote) {
		return knownCode(remote.Code)
	}
	return knownCode(err.Error())
}

func knownCode(code string) string {
	switch code {
	case bridge.Authentication, bridge.Permission, bridge.RateLimit, bridge.Unavailable,
		bridge.NotFound, bridge.Conflict, bridge.Unconfirmed, bridge.Protocol, bridge.Unsupported,
		"storage_unavailable", "writer_locked", "locking_unsupported", "unsupported_schema",
		"engine_busy", "account_paused", "identity_unavailable", "identity_mismatch",
		"unsafe_filename", "size_mismatch", "checksum_mismatch", "staging_quota",
		"source_changed", "remote_error",
		"canceled", "deadline_exceeded", "restart_required",
		"listen_unavailable", "http_unavailable", "output_unavailable",
		"config_unreadable", "config_invalid_json", "data_directory_required",
		"listen_address_invalid", "scan_interval_invalid", "resource_limits_invalid",
		"log_level_invalid", "accounts_required", "account_required", "account_not_found",
		"account_id_invalid", "immich_url_invalid", "amazon_region_required",
		"credentials_required", "credential_sources_conflict", "source_scope_required",
		"source_scope_conflict", "album_id_invalid", "secret_unreadable", "secret_invalid",
		"invalid_options", "include_videos_invalid", "config_write_failed":
		return code
	}
	return "operation_failed"
}

// ExitCode is stable across commands; aggregate failures use the highest
// precedence: cancellation, local storage, authentication, configuration, remote.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		best := 0
		for _, child := range joined.Unwrap() {
			best = max(best, ExitCode(child))
		}
		return best
	}
	switch Code(err) {
	case "canceled":
		return 130
	case "storage_unavailable", "writer_locked", "locking_unsupported", "unsupported_schema", "staging_quota":
		return 4
	case bridge.Authentication, bridge.Permission, "credentials_required", "credential_sources_conflict", "secret_unreadable", "secret_invalid":
		return 3
	case "config_unreadable", "config_invalid_json", "data_directory_required",
		"listen_address_invalid", "scan_interval_invalid", "resource_limits_invalid",
		"log_level_invalid", "accounts_required", "account_required", "account_not_found",
		"account_id_invalid", "immich_url_invalid", "amazon_region_required",
		"source_scope_required", "source_scope_conflict", "album_id_invalid", "invalid_options", "include_videos_invalid":
		return 2
	}
	return 1
}
