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

// A version 3 store gains the favorite column and per-node table without
// widening scope: every historical mapping is favorite-unknown until the
// account explicitly enables add-only.
func TestVersionThreeStoreMigratesFavoritesWithoutWideningScope(t *testing.T) {
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
		INSERT INTO rules(account_id,scope,album_id,complete,expected) VALUES('account','album:album','album',1,1);
		INSERT INTO assets(account_id,id,metadata,state,md5,sha1,size,node_id)
		VALUES('account','one',?,'confirmed','5d41402abc4b2a76b9719d911017c592','qvTGHdzF6KLavt4PO0gs2a6pQ00=',5,'existing');
		INSERT INTO scope_assets(account_id,scope,asset_id,generation) VALUES('account','album:album','one',1)`,
		string(metadata)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var version, favorite, rows int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT favorite FROM assets WHERE account_id='account' AND id='one'").Scan(&favorite); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM favorites").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if version != 5 || favorite != -1 || rows != 0 {
		t.Fatalf("migration widened scope: version=%d favorite=%d rows=%d", version, favorite, rows)
	}

	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{
		"": {Assets: []bridge.Asset{favoritePhoto("one", true)}},
	}, data: map[string]string{"one": "hello"}}
	d := &targetFake{identity: "target", nodes: []bridge.Node{{
		ID: "existing", Name: "one.jpg", MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5,
	}}}
	opts := options()
	opts.Favorites = "add_only"
	e := engine(t, store, s, d, opts)
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.Confirmed != 1 || st.FavoritesApplied != 1 || len(d.favWrites) != 1 || len(d.bodies) != 0 {
		t.Fatalf("migrated mapping did not star once: %+v writes=%v uploads=%d", st, d.favWrites, len(d.bodies))
	}
}
