#!/bin/sh
set -eu
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
    printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
    exit 1
fi
go mod tidy -diff
go vet ./...
go build ./...
go test ./...
go test -race ./...
"${GOLANGCI_LINT:-golangci-lint}" run
"${JACTIONLINT:-jactionlint}" --no-online --strict-exit
