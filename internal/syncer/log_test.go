package syncer_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func TestJobProgressUsesAnonymousIDsAndFiniteErrors(t *testing.T) {
	store, err := syncer.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := photo("private-asset")
	a.Name = "../private-filename.jpg"
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{a}}}}
	var logs bytes.Buffer
	opts := options()
	opts.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	e := engine(t, store, s, &targetFake{identity: "target"}, opts)
	if err := e.Once(t.Context()); err == nil {
		t.Fatal("invalid filename not rejected")
	}
	text := logs.String()
	for _, field := range []string{`"account"`, `"rule"`, `"job_id"`, `"stage"`, `"counts"`, `"elapsed_ms"`, `"error_code":"unsafe_filename"`, `"last_success"`} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing %s: %s", field, text)
		}
	}
	for _, private := range []string{"private-asset", "private-filename", `"album"`, `"account":"account"`} {
		if strings.Contains(text, private) {
			t.Fatalf("private data logged: %s", text)
		}
	}
}
