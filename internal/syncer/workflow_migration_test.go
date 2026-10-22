package syncer_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
	"github.com/thedavidweng/immich-amazon-sync/internal/workflow"
)

// A v3 ledger gains only the workflow table; every mapping, cursor, deletion
// state and staging reference must survive unchanged.
func TestVersionThreeStoreMigratesWithoutTouchingExistingState(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/v3.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(photo("one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity) VALUES('account','source','target');
		INSERT INTO rules(account_id,scope,album_id,name,complete,expected) VALUES('account','album:album','album','album',1,1);
		INSERT INTO assets(account_id,id,metadata,state,md5,sha1,size,node_id,prior_state)
		VALUES('account','one',?,'confirmed','5d41402abc4b2a76b9719d911017c592','qvTGHdzF6KLavt4PO0gs2a6pQ00=',5,'node-one',''),
		       ('account','two',?,'delete_pending','5d41402abc4b2a76b9719d911017c592','qvTGHdzF6KLavt4PO0gs2a6pQ00=',5,'node-two','confirmed');
		INSERT INTO albums(account_id,scope,target_id,name,applied_name,state,baseline,complete_generation)
		VALUES('account','album:album','album-target','album','album','confirmed',1,1);
		INSERT INTO memberships(account_id,scope,node_id,owned,state) VALUES('account','album:album','node-one',1,'present');
		INSERT INTO hints(account_id,asset_id,priority) VALUES('account','one',10);`,
		string(metadata), string(metadata)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.WorkflowRecord(t.Context(), "account")
	if err != nil || record.WorkflowID != "" || record.State != "" {
		t.Fatalf("migration invented workflow ownership: %+v %v", record, err)
	}
	saved := workflow.Record{
		AccountID: "account", WorkflowID: "44444444-4444-4444-8444-444444444444",
		Contract: workflow.Contract, State: workflow.StateActive, DesiredHash: "hash",
	}
	if err := store.SaveWorkflowRecord(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	record, err = reopened.WorkflowRecord(t.Context(), "account")
	if err != nil || record.WorkflowID != saved.WorkflowID || record.Contract != workflow.Contract || record.State != workflow.StateActive {
		t.Fatalf("workflow ownership lost across restart: %+v %v", record, err)
	}

	raw, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	var state, prior string
	if err := raw.QueryRow("SELECT state,prior_state FROM assets WHERE account_id='account' AND id='two'").Scan(&state, &prior); err != nil ||
		state != "delete_pending" || prior != "confirmed" {
		t.Fatalf("deletion state lost: %q %q %v", state, prior, err)
	}
	var target string
	if err := raw.QueryRow("SELECT target_id FROM albums WHERE account_id='account'").Scan(&target); err != nil || target != "album-target" {
		t.Fatalf("album ownership lost: %q %v", target, err)
	}
}
