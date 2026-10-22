package syncer_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

// A reporting open takes no writer lock and performs none of startup's repair
// work: a crash marker stays running and an orphaned staged original stays on
// disk. Reporting beside a live writer is therefore safe and truthful.
func TestReadOnlyLedgerOpenNeedsNoWriterLockAndDoesNotMutate(t *testing.T) {
	dir := t.TempDir()
	writer, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(photo("one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id) VALUES('account');
		INSERT INTO assets(account_id,id,metadata,state) VALUES('account','one',?,'running')`,
		string(metadata)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(dir, "staging")
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(staging, "original-orphan")
	if err := os.WriteFile(orphan, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}

	store, err := syncer.OpenReadOnly(dir)
	if err != nil {
		t.Fatalf("read-only open beside a writer failed: %v", err)
	}
	defer store.Close()
	e := engine(t, store, &sourceFake{identity: "source"}, &targetFake{identity: "target"}, options())
	st := status(t, e)
	if st.Running != 1 || st.Unconfirmed != 0 {
		t.Fatalf("read-only status rewrote a crash marker: %+v", st)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("read-only status cleaned staging: %v", err)
	}
}

// A report never migrates and never presents another schema as current.
func TestReadOnlyLedgerRefusesNonCurrentSchema(t *testing.T) {
	for _, test := range []struct {
		version int
		code    string
	}{
		{0, "schema_migration_required"},
		{3, "schema_migration_required"},
		{99, "unsupported_schema"},
	} {
		t.Run(fmt.Sprint(test.version), func(t *testing.T) {
			dir := t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", test.version)); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := syncer.OpenReadOnly(dir); err == nil || err.Error() != test.code {
				t.Fatalf("schema %d: got %v want %s", test.version, err, test.code)
			}
			db, err = sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var version int
			if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != test.version {
				t.Fatalf("read-only open migrated the ledger to %d", version)
			}
			if _, err := os.Stat(filepath.Join(dir, "writer.lock")); err == nil {
				t.Fatal("read-only open created a writer lock")
			}
		})
	}
	if _, err := syncer.OpenReadOnly(t.TempDir()); err == nil || err.Error() != "ledger_absent" {
		t.Fatalf("absent ledger: got %v", err)
	}
}

// Status matches the durable counters while a writer keeps the lock.
func TestReadOnlyStatusMatchesTheLiveLedger(t *testing.T) {
	dir := t.TempDir()
	writer, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	seed := engine(t, writer, &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{photo("one")}},
	}, data: map[string]string{"one": "hello"}}, &targetFake{identity: "target"}, options())
	if err := seed.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err := syncer.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine(t, store, &sourceFake{identity: "source"}, &targetFake{identity: "target"}, options())
	if st := status(t, e); st.Confirmed != 1 || st.Pending != 0 {
		t.Fatalf("read-only status diverged from the ledger: %+v", st)
	}
}

// A cleanly stopped service checkpoints its WAL and removes the shared-memory
// file. A report must still open that ledger.
func TestReadOnlyLedgerOpensAfterACleanClose(t *testing.T) {
	dir := t.TempDir()
	writer, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seed := engine(t, writer, &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{photo("one")}},
	}, data: map[string]string{"one": "hello"}}, &targetFake{identity: "target"}, options())
	if err := seed.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := syncer.OpenReadOnly(dir)
	if err != nil {
		t.Fatalf("read-only open after clean close: %v", err)
	}
	defer store.Close()
	e := engine(t, store, &sourceFake{identity: "source"}, &targetFake{identity: "target"}, options())
	if st := status(t, e); st.Confirmed != 1 {
		t.Fatalf("read-only status after clean close: %+v", st)
	}
}
