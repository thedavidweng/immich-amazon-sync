package syncer_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func TestVersionOneStoreMigratesWithoutLosingConfirmedContent(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/v1.sql")
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
	const lastSuccess = "2026-01-01T00:00:00Z"
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity,last_success)
		VALUES('account','source','target',?);
		INSERT INTO rules(account_id,scope,album_id,complete,expected) VALUES('account','album:album','album',1,1);
		INSERT INTO assets(account_id,id,metadata,state,md5,sha1,size,node_id)
		VALUES('account','one',?,'confirmed','5d41402abc4b2a76b9719d911017c592','qvTGHdzF6KLavt4PO0gs2a6pQ00=',5,'existing');
		INSERT INTO scope_assets(account_id,scope,asset_id,generation) VALUES('account','album:album','one',1)`,
		lastSuccess, string(metadata)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &sourceFake{identity: "source", data: map[string]string{"two": "hello"},
		pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{photo("two")}}}}
	d := &targetFake{identity: "target", nodes: []bridge.Node{{
		ID: "existing", Name: "one.jpg", MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5,
	}}}
	e := engine(t, store, s, d, options())
	if st := status(t, e); st.Confirmed != 1 || st.LastSuccess == nil || st.LastSuccess.Format(time.RFC3339) != lastSuccess {
		t.Fatalf("migration lost prior completion: %+v", st)
	}
	if err = store.Enqueue(t.Context(), "account", "two", 10); err != nil {
		t.Fatal(err)
	}
	if err = e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 2 || st.EventsPending != 0 || st.LastSuccess == nil || len(d.bodies) != 0 {
		t.Fatalf("migrated content was not reusable by durable events: %+v uploads=%v", st, d.bodies)
	}
}
