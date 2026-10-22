package syncer_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/filter"
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
	if version != 7 || favorite != -1 || rows != 0 {
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

// A version-three store contains the legacy video toggle as a skip code. The
// new schema must keep that exclusion durable and able to requeue.
func TestVersionThreeStoreReevaluatesLegacyVideoSkip(t *testing.T) {
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
	video := filterAsset("video", "video.mp4")
	video.Kind, video.MIME = "VIDEO", "video/mp4"
	metadata, err := json.Marshal(video)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity) VALUES('account','source','target');
		INSERT INTO rules(account_id,scope,album_id,complete,expected,generation,name) VALUES('account','album:album','album',1,-1,1,'Album');
		INSERT INTO assets(account_id,id,metadata,state,error_code) VALUES('account','video',?,'skipped','video_disabled');
		INSERT INTO scope_assets(account_id,scope,asset_id,generation) VALUES('account','album:album','video',1)`,
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
	s := &sourceFake{identity: "source", data: map[string]string{"video": "world"}}
	d := &targetFake{identity: "target"}
	opts := options()
	opts.Filters.MediaTypes = []string{"image", "video"}
	e := engine(t, store, s, d, opts)
	if st := status(t, e); st.Exclusions[filter.MediaType] != 1 || st.Skipped != 1 {
		t.Fatalf("legacy skip was not migrated into a reevaluable exclusion: %+v", st)
	}
	if err = e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e); st.Confirmed != 1 || len(st.Exclusions) != 0 || len(d.bodies) != 1 {
		t.Fatalf("migrated exclusion did not requeue: %+v uploads=%d", st, len(d.bodies))
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	var state, exclusion string
	if err = db.QueryRow("SELECT state,exclusion FROM assets WHERE account_id='account' AND id='video'").
		Scan(&state, &exclusion); err != nil || state != "confirmed" || exclusion != "" {
		t.Fatalf("migrated row = %q/%q, %v", state, exclusion, err)
	}
}

// A version 5 store already owns the workflow table; the favorites step must
// still run and must leave that ownership, and every mapping, untouched.
func TestVersionFiveStoreAddsFavoritesAndKeepsWorkflowOwnership(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/v5.sql")
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
		INSERT INTO scope_assets(account_id,scope,asset_id,generation) VALUES('account','album:album','one',1);
		INSERT INTO workflows(account_id,workflow_id,contract,state,desired_hash)
		VALUES('account','44444444-4444-4444-8444-444444444444','contract','active','hash')`,
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
	record, err := store.WorkflowRecord(t.Context(), "account")
	if err != nil || record.WorkflowID != "44444444-4444-4444-8444-444444444444" || record.State != "active" {
		t.Fatalf("migration lost workflow ownership: %+v %v", record, err)
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
	if version != 7 || favorite != -1 || rows != 0 {
		t.Fatalf("favorites migration widened scope: version=%d favorite=%d rows=%d", version, favorite, rows)
	}

	// Off by default: the migrated account performs no favorite write.
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
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.FavoritesMode != "" && st.FavoritesMode != "off" {
		t.Fatalf("migrated account gained favorite scope: %q", st.FavoritesMode)
	}
	if len(d.favWrites) != 0 || st.FavoritesApplied != 0 || st.Confirmed != 1 || len(d.bodies) != 0 {
		t.Fatalf("migrated account wrote favorites: %+v writes=%v uploads=%d", st, d.favWrites, len(d.bodies))
	}
}

// A version 6 store already owns favorites. The filters step must still run and
// must adopt the legacy video skip without touching settled favorites rows.
func TestVersionSixStoreAddsFiltersAndKeepsFavorites(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/v6.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	video := filterAsset("video", "video.mp4")
	video.Kind, video.MIME = "VIDEO", "video/mp4"
	metadata, err := json.Marshal(video)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity) VALUES('account','source','target');
		INSERT INTO rules(account_id,scope,album_id,complete,expected) VALUES('account','album:album','album',1,-1);
		INSERT INTO assets(account_id,id,metadata,state,error_code,favorite) VALUES('account','video',?,'skipped','video_disabled',1);
		INSERT INTO scope_assets(account_id,scope,asset_id,generation) VALUES('account','album:album','video',1);
		INSERT INTO favorites(account_id,node_id,desired,applied,observed,state)
		VALUES('account','node','1','1','1','applied')`,
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
	defer db.Close()
	var version, favorite, applied int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	if err = db.QueryRow("SELECT favorite FROM assets WHERE account_id='account' AND id='video'").Scan(&favorite); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT applied FROM favorites WHERE account_id='account' AND node_id='node'").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	var exclusion string
	if err = db.QueryRow("SELECT exclusion FROM assets WHERE account_id='account' AND id='video'").Scan(&exclusion); err != nil ||
		exclusion != filter.MediaType {
		t.Fatalf("legacy skip exclusion = %q, %v", exclusion, err)
	}
	if favorite != 1 || applied != 1 {
		t.Fatalf("filters migration disturbed favorites: favorite=%d applied=%d", favorite, applied)
	}
}
