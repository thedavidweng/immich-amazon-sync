#!/bin/sh
# Print the single Go compiler pin from mise.toml. CI and the Docker builder
# both consume this script so the project has one compiler source.
set -eu

config=${1:-mise.toml}
version=$(sed -n 's/^go = "\([0-9][0-9.]*\)"$/\1/p' "$config")
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || {
    printf 'go-version: %s must pin an exact Go major.minor.patch version, got %s\n' "$config" "$version" >&2
    exit 1
}
printf '%s\n' "$version"
