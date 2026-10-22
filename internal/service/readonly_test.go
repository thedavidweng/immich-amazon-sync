package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/cli"
	"github.com/thedavidweng/immich-amazon-sync/internal/service"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// `status` is a report, not a service start: it must read the ledger while
// another process holds the writer lock, without migrating or repairing it.
func TestStatusRunsReadOnlyBesideARunningWriter(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	seedLedger(t, state, false)
	cfg := metricsConfig(t, dir, "127.0.0.1:0", false)
	writer, err := syncer.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	runtime, err := service.OpenReadOnly(cfg, io.Discard)
	if err != nil {
		t.Fatalf("read-only service open beside a writer: %v", err)
	}
	report, err := runtime.StatusReport(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if len(report.Accounts) != 1 || report.Accounts[0].Code != "ok" ||
		report.Accounts[0].Status.Confirmed != 11 {
		t.Fatalf("read-only report diverged: %+v", report)
	}

	path := filepath.Join(dir, "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := cli.Execute(context.Background(), []string{"--config", path, "status", "--json"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("cli status beside a writer exit=%d stderr=%s", code, errOut.String())
	}
	var cliReport service.Report
	if err := json.Unmarshal(out.Bytes(), &cliReport); err != nil {
		t.Fatal(err)
	}
	if len(cliReport.Accounts) != 1 || cliReport.Accounts[0].Status.Confirmed != 11 {
		t.Fatalf("cli read-only report diverged: %+v", cliReport)
	}
}
