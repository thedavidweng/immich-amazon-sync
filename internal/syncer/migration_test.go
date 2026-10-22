package syncer_test

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		VALUES('account','source','target',?)`, lastSuccess); err != nil {
		t.Fatal(err)
	}
	// One Exec per parameterized statement: a multi-statement Exec rebinds its
	// argument list per statement, which would store the timestamp as metadata.
	if _, err = db.Exec(`INSERT INTO rules(account_id,scope,album_id,complete,expected)
		VALUES('account','album:album','album',1,1);
		INSERT INTO scope_assets(account_id,scope,asset_id,generation) VALUES('account','album:album','one',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO assets(account_id,id,metadata,state,md5,sha1,size,node_id)
		VALUES('account','one',?,'confirmed','5d41402abc4b2a76b9719d911017c592','qvTGHdzF6KLavt4PO0gs2a6pQ00=',5,'existing')`,
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
	var version, favorite, rows, checked int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT favorite FROM assets WHERE account_id='account' AND id='one'").Scan(&favorite); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM favorites").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('favorites') WHERE name='checked_at'").Scan(&checked); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if version != 10 || favorite != -1 || rows != 0 || checked != 1 {
		t.Fatalf("migration widened scope: version=%d favorite=%d rows=%d checked=%d", version, favorite, rows, checked)
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
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
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
	var version, favorite, rows, checked int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT favorite FROM assets WHERE account_id='account' AND id='one'").Scan(&favorite); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM favorites").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('favorites') WHERE name='checked_at'").Scan(&checked); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if version != 10 || favorite != -1 || rows != 0 || checked != 1 {
		t.Fatalf("favorites migration widened scope: version=%d favorite=%d rows=%d checked=%d", version, favorite, rows, checked)
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
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
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

// A version 7 store already has explicit filters and favorites; the usage step
// must add only the observation table and leave every durable row untouched.
func TestVersionSevenStoreAddsUsageWithoutTouchingFiltersOrFavorites(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/v7.sql")
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
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity,filter_fingerprint)
		VALUES('account','source','target','fingerprint');
		INSERT INTO rules(account_id,scope,album_id,complete,expected) VALUES('account','album:album','album',1,-1);
		INSERT INTO assets(account_id,id,metadata,state,exclusion,favorite)
		VALUES('account','video',?,'skipped','media_type_excluded',1);
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
	var fingerprint string
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	if err = db.QueryRow("SELECT filter_fingerprint FROM accounts WHERE id='account'").Scan(&fingerprint); err != nil ||
		fingerprint != "fingerprint" {
		t.Fatalf("filters migration disturbed the account fingerprint: %q %v", fingerprint, err)
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
		t.Fatalf("usage migration disturbed exclusions: %q %v", exclusion, err)
	}
	if favorite != 1 || applied != 1 {
		t.Fatalf("usage migration disturbed favorites: favorite=%d applied=%d", favorite, applied)
	}
	var rows int
	if err = db.QueryRow("SELECT count(*) FROM usage").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("usage migration invented observations: rows=%d %v", rows, err)
	}
	var checked int
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('favorites') WHERE name='checked_at'").Scan(&checked); err != nil ||
		checked != 1 {
		t.Fatalf("drift-audit column missing after the version 7 path: %d %v", checked, err)
	}
}

// A version 8 store already has the usage observation. The drift-audit step
// must add only the checked_at column and leave every durable row untouched.
func TestVersionEightStoreAddsFavoriteAuditWithoutTouchingUsage(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/v8.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity,filter_fingerprint)
		VALUES('account','source','target','fingerprint');
		INSERT INTO favorites(account_id,node_id,desired,applied,observed,state)
		VALUES('account','node','1','1','1','applied');
		INSERT INTO usage(account_id,observed_at,last_calculated,categories,hold)
		VALUES('account','2026-10-22T00:00:00Z','2026-10-21T00:00:00Z','[{"type":"photo"}]',1)`); err != nil {
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
	var version, checked int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('favorites') WHERE name='checked_at'").Scan(&checked); err != nil ||
		checked != 1 {
		t.Fatalf("drift-audit column missing: %d %v", checked, err)
	}
	var desired, applied, observed, rowChecked int
	var state string
	if err = db.QueryRow(`SELECT desired,applied,observed,state,checked_at FROM favorites
		WHERE account_id='account' AND node_id='node'`).Scan(&desired, &applied, &observed, &state, &rowChecked); err != nil {
		t.Fatal(err)
	}
	if desired != 1 || applied != 1 || observed != 1 || state != "applied" || rowChecked != 0 {
		t.Fatalf("migration disturbed the favorite row: desired=%d applied=%d observed=%d state=%s checked=%d",
			desired, applied, observed, state, rowChecked)
	}
	var observedAt, lastCalculated, categories string
	var hold int
	if err = db.QueryRow(`SELECT observed_at,last_calculated,categories,hold FROM usage
		WHERE account_id='account'`).Scan(&observedAt, &lastCalculated, &categories, &hold); err != nil {
		t.Fatal(err)
	}
	if observedAt != "2026-10-22T00:00:00Z" || lastCalculated != "2026-10-21T00:00:00Z" ||
		categories != `[{"type":"photo"}]` || hold != 1 {
		t.Fatalf("migration disturbed the usage row: %q %q %q %d", observedAt, lastCalculated, categories, hold)
	}
}

// A newer schema fails closed instead of being treated as current, and a store
// already at this version reopens without running any step.
func TestFutureSchemaFailsClosedAndCurrentSchemaReopens(t *testing.T) {
	dir := t.TempDir()
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatalf("current schema did not reopen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	if _, err = db.Exec("PRAGMA user_version=11;"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = syncer.Open(dir); err == nil {
		t.Fatal("a future schema version was accepted")
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 11 {
		t.Fatalf("refused open rewrote the version: %d, %v", version, err)
	}
}

// A version 7 store gains the Live Photo pair ledger without widening scope:
// every existing account stays still_only, and confirmed mappings, filters and
// favorites are untouched.
func TestVersionSevenStoreAddsLivePhotoPairsWithoutWideningScope(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/v7.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(liveStill("still", "companion"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity) VALUES('account','source','target');
		INSERT INTO rules(account_id,scope,album_id,complete,expected) VALUES('account','album:album','album',1,1);
		INSERT INTO scope_assets(account_id,scope,asset_id,generation) VALUES('account','album:album','still',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO assets(account_id,id,metadata,state,md5,sha1,size,node_id)
		VALUES('account','still',?,'confirmed','5d41402abc4b2a76b9719d911017c592','qvTGHdzF6KLavt4PO0gs2a6pQ00=',5,'node')`,
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
	defer store.Close()
	s := &sourceFake{identity: "source", pages: map[string]bridge.Page{"": {Assets: []bridge.Asset{}}}}
	d := &targetFake{identity: "target", nodes: []bridge.Node{{
		ID: "node", Name: "still.heic", MD5: "5d41402abc4b2a76b9719d911017c592", Size: 5,
	}}}
	e := engine(t, store, s, d, options())
	st := status(t, e)
	if st.Confirmed != 1 || st.LivePhotoMode != "still_only" || st.PairsComplete != 0 || st.PairsIncomplete != 0 {
		t.Fatalf("migration changed the baseline: %+v", st)
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	var mode string
	if err = db.QueryRow("SELECT live_photo_mode FROM accounts WHERE id='account'").Scan(&mode); err != nil || mode != "" {
		t.Fatalf("existing account gained a pair mode: %q, %v", mode, err)
	}
	var pairs int
	if err = db.QueryRow("SELECT count(*) FROM live_pairs").Scan(&pairs); err != nil || pairs != 0 {
		t.Fatalf("migration created pair work: %d, %v", pairs, err)
	}
}

// A version 9 store is one step from current: the pair ledger is added, the
// drift-audit column stays, and every favorite and usage row is untouched.
// Reopening the resulting version 10 store runs no step at all.
func TestVersionNineStoreAddsLivePhotoPairsWithoutTouchingFavoritesOrUsage(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/v9.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity,filter_fingerprint)
		VALUES('account','source','target','fingerprint');
		INSERT INTO favorites(account_id,node_id,desired,applied,observed,state,checked_at)
		VALUES('account','node','1','1','1','applied',7);
		INSERT INTO usage(account_id,observed_at,last_calculated,categories,hold)
		VALUES('account','2026-10-22T00:00:00Z','2026-10-21T00:00:00Z','[{"type":"photo"}]',1)`); err != nil {
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
	// A second open is the version 10 "already current" path: no step runs, so
	// a re-run would fail on the duplicate live_pairs table.
	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatalf("version 10 store did not reopen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	var mode string
	if err = db.QueryRow("SELECT live_photo_mode FROM accounts WHERE id='account'").Scan(&mode); err != nil || mode != "" {
		t.Fatalf("existing account gained a pair mode: %q, %v", mode, err)
	}
	var pairs int
	if err = db.QueryRow("SELECT count(*) FROM live_pairs").Scan(&pairs); err != nil || pairs != 0 {
		t.Fatalf("migration created pair work: %d, %v", pairs, err)
	}
	var desired, applied, observed, checked int
	var state string
	if err = db.QueryRow(`SELECT desired,applied,observed,state,checked_at FROM favorites
		WHERE account_id='account' AND node_id='node'`).Scan(&desired, &applied, &observed, &state, &checked); err != nil {
		t.Fatal(err)
	}
	if desired != 1 || applied != 1 || observed != 1 || state != "applied" || checked != 7 {
		t.Fatalf("migration disturbed the favorite row: desired=%d applied=%d observed=%d state=%s checked=%d",
			desired, applied, observed, state, checked)
	}
	var observedAt, lastCalculated, categories string
	var hold int
	if err = db.QueryRow(`SELECT observed_at,last_calculated,categories,hold FROM usage
		WHERE account_id='account'`).Scan(&observedAt, &lastCalculated, &categories, &hold); err != nil {
		t.Fatal(err)
	}
	if observedAt != "2026-10-22T00:00:00Z" || lastCalculated != "2026-10-21T00:00:00Z" ||
		categories != `[{"type":"photo"}]` || hold != 1 {
		t.Fatalf("migration disturbed the usage row: %q %q %q %d", observedAt, lastCalculated, categories, hold)
	}
}

// One live version-3 ledger exercises the whole release upgrade path: every
// durable category the gate promises survives to the current head, no new
// mutating feature gains scope, and the migrated store reopens without a step.
// Version 3 is the schema of the deployed release at issue #16's baseline.
func TestVersionThreeLiveSchemaUpgradePreservesEveryDurableState(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	const staged = "original-staged"
	if err := os.WriteFile(filepath.Join(staging, staged), []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "original-orphan"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity,pause_reason,last_success,error_code)
		VALUES('account','source-identity','target-identity','paused by operator','2026-01-01T00:00:00Z','remote_unavailable');
		INSERT INTO rules(account_id,scope,album_id,active,generation,cursor,complete,expected,error_code,name,completed_at,started_at,attempts,next_at)
		VALUES('account','album:album-a','album-a',1,2,'album-cursor',1,3,'','Family','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z',2,77),
		       ('account','library','',1,1,'library-cursor',0,-1,'','Library','','',0,0);
		INSERT INTO albums(account_id,scope,target_id,name,applied_name,state,error_code,attempts,next_at,baseline,complete_generation)
		VALUES('account','album:album-a','owned-album','Family','Family','confirmed','',0,0,2,2);
		INSERT INTO memberships(account_id,scope,node_id,owned,state,error_code,attempts,next_at)
		VALUES('account','album:album-a','node-one',1,'present','',0,0),
		       ('account','album:album-a','node-two',0,'present','',0,0);
		INSERT INTO scan_cursors(account_id,scope,token) VALUES('account','album:album-a','page-7');
		INSERT INTO incremental(account_id,watermark,since,started_at,cursor,active,error_code)
		VALUES('account','watermark-9','2026-01-01T00:00:00Z','2026-01-01T00:01:00Z','cursor-9',1,'');
		INSERT INTO incremental_tokens(account_id,token) VALUES('account','token-9');
		INSERT INTO incremental_seen(account_id,asset_id) VALUES('account','one');
		INSERT INTO hints(account_id,asset_id,priority,version,attempts,next_at,error_code)
		VALUES('account','one',10,3,2,1234,'');
		INSERT INTO signals(account_id,scope,asset_id,generation) VALUES('account','album:album-a','one',2);
		INSERT INTO scope_assets(account_id,scope,asset_id,generation)
		VALUES('account','album:album-a','one',2),
		       ('account','album:album-a','two',2),
		       ('account','library','three',1)`); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(photo("one"))
	if err != nil {
		t.Fatal(err)
	}
	seedAsset := func(id, state, code string, attempts int, nextAt int64, stage, md5, sha1 string,
		size int64, node, prior string, priority int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO assets(account_id,id,metadata,state,error_code,attempts,next_at,stage_name,md5,sha1,size,node_id,priority,prior_state)
			VALUES('account',?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, string(metadata), state, code, attempts, nextAt, stage, md5, sha1, size, node, priority, prior); err != nil {
			t.Fatal(err)
		}
	}
	seedAsset("one", "confirmed", "", 0, 0, "", "md5-one", "sha1-one", 5, "node-one", "", 10)
	seedAsset("two", "existing", "", 0, 0, "", "md5-two", "sha1-two", 5, "node-two", "", 0)
	seedAsset("three", "delete_pending", "", 0, 0, "", "md5-three", "sha1-three", 5, "node-three", "confirmed", 0)
	seedAsset("four", "deleted", "", 0, 0, "", "md5-four", "sha1-four", 5, "node-four", "confirmed", 0)
	seedAsset("five", "unconfirmed", "upload_unconfirmed", 2, 1234, staged, "md5-five", "sha1-five", 7, "", "", 0)
	seedAsset("video", "skipped", "video_disabled", 0, 0, "", "", "", 0, "", "", 0)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatalf("live version-3 ledger refused the upgrade: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	var sourceIdentity, targetIdentity, pauseReason, lastSuccess, accountError, fingerprint, mode string
	if err := raw.QueryRow(`SELECT source_identity,target_identity,pause_reason,last_success,error_code,
		filter_fingerprint,live_photo_mode FROM accounts WHERE id='account'`).
		Scan(&sourceIdentity, &targetIdentity, &pauseReason, &lastSuccess, &accountError, &fingerprint, &mode); err != nil {
		t.Fatal(err)
	}
	if sourceIdentity != "source-identity" || targetIdentity != "target-identity" ||
		pauseReason != "paused by operator" || lastSuccess != "2026-01-01T00:00:00Z" ||
		accountError != "remote_unavailable" || fingerprint != "" || mode != "" {
		t.Fatalf("account identity or safe defaults changed: %q %q %q %q %q %q %q",
			sourceIdentity, targetIdentity, pauseReason, lastSuccess, accountError, fingerprint, mode)
	}

	for _, want := range []struct {
		id, state, code, stage, md5, sha1, node, prior, exclusion string
		attempts, nextAt, size, priority, favorite                int64
	}{
		{id: "one", state: "confirmed", md5: "md5-one", sha1: "sha1-one", size: 5, node: "node-one", priority: 10, favorite: -1},
		{id: "two", state: "existing", md5: "md5-two", sha1: "sha1-two", size: 5, node: "node-two", favorite: -1},
		{id: "three", state: "delete_pending", md5: "md5-three", sha1: "sha1-three", size: 5, node: "node-three",
			prior: "confirmed", favorite: -1},
		{id: "four", state: "deleted", md5: "md5-four", sha1: "sha1-four", size: 5, node: "node-four",
			prior: "confirmed", favorite: -1},
		{id: "five", state: "unconfirmed", code: "upload_unconfirmed", stage: staged, md5: "md5-five",
			sha1: "sha1-five", size: 7, attempts: 2, nextAt: 1234, favorite: -1},
		{id: "video", state: "skipped", exclusion: filter.MediaType, favorite: -1},
	} {
		var gotState, gotCode, gotStage, gotMD5, gotSHA1, gotNode, gotPrior, gotExclusion string
		var gotAttempts, gotNext, gotSize, gotPriority, gotFavorite int64
		if err := raw.QueryRow(`SELECT state,error_code,stage_name,md5,sha1,size,node_id,priority,prior_state,exclusion,favorite,attempts,next_at
			FROM assets WHERE account_id='account' AND id=?`, want.id).
			Scan(&gotState, &gotCode, &gotStage, &gotMD5, &gotSHA1, &gotSize, &gotNode, &gotPriority,
				&gotPrior, &gotExclusion, &gotFavorite, &gotAttempts, &gotNext); err != nil {
			t.Fatal(err)
		}
		if gotState != want.state || gotCode != want.code || gotStage != want.stage || gotMD5 != want.md5 ||
			gotSHA1 != want.sha1 || gotSize != want.size || gotNode != want.node || gotPriority != want.priority ||
			gotPrior != want.prior || gotExclusion != want.exclusion || gotFavorite != want.favorite ||
			gotAttempts != want.attempts || gotNext != want.nextAt {
			t.Fatalf("asset %s changed across the upgrade: state=%q code=%q stage=%q md5=%q sha1=%q size=%d node=%q priority=%d prior=%q exclusion=%q favorite=%d attempts=%d next=%d",
				want.id, gotState, gotCode, gotStage, gotMD5, gotSHA1, gotSize, gotNode, gotPriority,
				gotPrior, gotExclusion, gotFavorite, gotAttempts, gotNext)
		}
	}

	for _, want := range []struct {
		scope, albumID, cursor, origin string
		generation, complete, expected int64
	}{
		{scope: "album:album-a", albumID: "album-a", cursor: "album-cursor", origin: "explicit",
			generation: 2, complete: 1, expected: 3},
		{scope: "library", albumID: "", cursor: "library-cursor", origin: "explicit",
			generation: 1, complete: 0, expected: -1},
	} {
		var albumID, cursor, origin, name string
		var generation, complete, expected, discovered int64
		if err := raw.QueryRow(`SELECT album_id,cursor,origin,discovered,generation,complete,expected,name
			FROM rules WHERE account_id='account' AND scope=?`, want.scope).
			Scan(&albumID, &cursor, &origin, &discovered, &generation, &complete, &expected, &name); err != nil {
			t.Fatal(err)
		}
		if albumID != want.albumID || cursor != want.cursor || origin != want.origin || discovered != 1 ||
			generation != want.generation || complete != want.complete || expected != want.expected {
			t.Fatalf("rule %s changed across the upgrade: album=%q cursor=%q origin=%q discovered=%d generation=%d complete=%d expected=%d",
				want.scope, albumID, cursor, origin, discovered, generation, complete, expected)
		}
	}
	var targetID, appliedName, albumState string
	var baseline, completeGeneration int64
	if err := raw.QueryRow(`SELECT target_id,applied_name,state,baseline,complete_generation
		FROM albums WHERE account_id='account' AND scope='album:album-a'`).
		Scan(&targetID, &appliedName, &albumState, &baseline, &completeGeneration); err != nil {
		t.Fatal(err)
	}
	if targetID != "owned-album" || appliedName != "Family" || albumState != "confirmed" ||
		baseline != 2 || completeGeneration != 2 {
		t.Fatalf("album ownership changed: target=%q applied=%q state=%q baseline=%d generation=%d",
			targetID, appliedName, albumState, baseline, completeGeneration)
	}
	var owned, unowned int
	if err := raw.QueryRow(`SELECT sum(owned),count(*)-sum(owned) FROM memberships
		WHERE account_id='account' AND scope='album:album-a'`).Scan(&owned, &unowned); err != nil {
		t.Fatal(err)
	}
	if owned != 1 || unowned != 1 {
		t.Fatalf("owned membership state changed: owned=%d unowned=%d", owned, unowned)
	}
	var cursorToken, watermark, since, incrementalCursor string
	var active int
	if err := raw.QueryRow(`SELECT token FROM scan_cursors WHERE account_id='account' AND scope='album:album-a'`).
		Scan(&cursorToken); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT watermark,since,cursor,active FROM incremental WHERE account_id='account'`).
		Scan(&watermark, &since, &incrementalCursor, &active); err != nil {
		t.Fatal(err)
	}
	var tokens, seen, hints, signals, scoped int
	if err := raw.QueryRow(`SELECT count(*) FROM incremental_tokens WHERE account_id='account'`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM incremental_seen WHERE account_id='account'`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM hints WHERE account_id='account'`).Scan(&hints); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM signals WHERE account_id='account'`).Scan(&signals); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM scope_assets WHERE account_id='account'`).Scan(&scoped); err != nil {
		t.Fatal(err)
	}
	if cursorToken != "page-7" || watermark != "watermark-9" || since != "2026-01-01T00:00:00Z" ||
		incrementalCursor != "cursor-9" || active != 1 || tokens != 1 || seen != 1 || hints != 1 ||
		signals != 1 || scoped != 3 {
		t.Fatalf("event cursor or discovery state changed: token=%q watermark=%q since=%q cursor=%q active=%d tokens=%d seen=%d hints=%d signals=%d scope_assets=%d",
			cursorToken, watermark, since, incrementalCursor, active, tokens, seen, hints, signals, scoped)
	}

	for _, table := range []string{"favorites", "live_pairs", "workflows", "usage"} {
		var rows int
		if err := raw.QueryRow("SELECT count(*) FROM " + table).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("migration invented %s work: rows=%d %v", table, rows, err)
		}
	}
	if _, err := os.Stat(filepath.Join(staging, staged)); err != nil {
		t.Fatalf("migration released a staged original still referenced by an intent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "original-orphan")); !os.IsNotExist(err) {
		t.Fatalf("migration kept an unreferenced staging file: %v", err)
	}

	store, err = syncer.Open(dir)
	if err != nil {
		t.Fatalf("upgraded version-10 store did not reopen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// Every checked-in fixture is a real starting point for the upgrade path, not
// just the one step it was introduced for. Each must reach the current head and
// reopen without running a migration step twice.
func TestEveryCheckedInFixtureUpgradesToHeadAndReopens(t *testing.T) {
	for _, fixture := range []string{"v1.sql", "v3.sql", "v4.sql", "v5.sql", "v6.sql", "v7.sql", "v8.sql", "v9.sql"} {
		t.Run(fixture, func(t *testing.T) {
			dir := t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			schema, err := os.ReadFile(filepath.Join("testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(string(schema)); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				store, err := syncer.Open(dir)
				if err != nil {
					t.Fatalf("%s did not upgrade or reopen: %v", fixture, err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var version, livePairs, favoriteAudit, exclusion, liveMode, usage int
			if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
				t.Fatalf("schema version = %d, %v", version, err)
			}
			for query, target := range map[string]*int{
				"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='live_pairs'":     &livePairs,
				"SELECT count(*) FROM pragma_table_info('favorites') WHERE name='checked_at'":     &favoriteAudit,
				"SELECT count(*) FROM pragma_table_info('assets') WHERE name='exclusion'":         &exclusion,
				"SELECT count(*) FROM pragma_table_info('accounts') WHERE name='live_photo_mode'": &liveMode,
				"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='usage'":          &usage,
			} {
				if err := raw.QueryRow(query).Scan(target); err != nil {
					t.Fatal(err)
				}
			}
			if livePairs != 1 || favoriteAudit != 1 || exclusion != 1 || liveMode != 1 || usage != 1 {
				t.Fatalf("%s upgraded to an incomplete head schema: live_pairs=%d checked_at=%d exclusion=%d live_photo_mode=%d usage=%d",
					fixture, livePairs, favoriteAudit, exclusion, liveMode, usage)
			}
		})
	}
}

// An unknown schema version fails closed with the finite schema code instead of
// being treated as current, rewritten, or reaching the migration dispatch and
// panicking. Both a future version and an out-of-range negative header value are
// unknown; the database keeps its version and its contents.
func TestUnknownSchemaVersionsFailClosedWithTheSchemaCode(t *testing.T) {
	for _, version := range []int{11, 999, -1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dir := t.TempDir()
			store, err := syncer.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO accounts(id) VALUES('account')`); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d;", version)); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = syncer.Open(dir); err == nil || err.Error() != "unsupported_schema" {
				t.Fatalf("version %d was not refused with unsupported_schema: %v", version, err)
			}
			db, err = sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var got, accounts int
			if err := db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != version {
				t.Fatalf("refused open rewrote the version: %d, %v", got, err)
			}
			if err := db.QueryRow("SELECT count(*) FROM accounts").Scan(&accounts); err != nil || accounts != 1 {
				t.Fatalf("refused open changed the ledger: accounts=%d %v", accounts, err)
			}
		})
	}
}

// A ledger upgraded from the live schema keeps today's write surface when the
// operator changes nothing: stock options do not star, pair, mirror an album or
// trash anything, even when the source already reports those inputs.
func TestMigratedLiveAccountWithDefaultOptionsStaysOnBaselineBehaviour(t *testing.T) {
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
	if _, err = db.Exec(`INSERT INTO accounts(id,source_identity,target_identity) VALUES('account','source','target');
		INSERT INTO rules(account_id,scope,album_id,complete,expected,name) VALUES('account','album:album','album',1,-1,'Family')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	favorite := true
	still := liveStill("still", "mov")
	still.Favorite = &favorite
	companion := liveCompanion("mov")
	s := &sourceFake{identity: "source",
		albums: []bridge.Album{{ID: "album", Name: "Family", Count: 2}},
		pages:  map[string]bridge.Page{"": {Assets: []bridge.Asset{still, companion}}},
		data:   map[string]string{"still": "hello"},
		assets: map[string]bridge.Asset{"mov": companion}}
	d := newAlbumTarget()
	e := engine(t, store, s, d, options())
	if err := e.Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := status(t, e)
	if st.FavoritesMode != "off" || st.FavoritesApplied != 0 || len(d.favWrites) != 0 {
		t.Fatalf("upgraded account gained favorite scope: %+v writes=%v", st, d.favWrites)
	}
	if st.LivePhotoMode != "still_only" || st.PairsComplete != 0 || st.PairsIncomplete != 0 {
		t.Fatalf("upgraded account gained Live Photo pair scope: %+v", st)
	}
	if st.Confirmed != 1 || st.Deleted != 0 || st.DeletePending != 0 || len(d.trashed) != 0 {
		t.Fatalf("baseline media state or deletion scope changed: %+v trashed=%v", st, d.trashed)
	}
	if d.creates != 0 || len(d.albums) != 0 {
		t.Fatalf("upgraded account gained album mirroring: creates=%d albums=%v", d.creates, d.albums)
	}
	if st.Exclusions["hidden"] != 1 {
		t.Fatalf("hidden companion was not excluded by the default mode: %+v", st.Exclusions)
	}
	if st.TransferWindow != nil || st.DeferralReason != "" {
		t.Fatalf("upgraded account gained a transfer window: %+v", st)
	}
	if st.Usage.VideoHold || st.Usage.QuotaHold || st.Usage.VideoThresholdBytes != 0 {
		t.Fatalf("upgraded account gained a usage hold: %+v", st.Usage)
	}
	if st.Direct.Reads != 0 || len(st.Direct.Fallbacks) != 0 {
		t.Fatalf("upgraded account gained direct-read scope: %+v", st.Direct)
	}
}

// The container smoke mounts this checked-in version-3 ledger, so it must be a
// genuine live-schema database whose mapping survives the upgrade. It is also
// the seeded prior fixture the release gate starts from on both architectures.
func TestContainerLegacyFixtureUpgradesAndKeepsItsMapping(t *testing.T) {
	archive, err := os.Open(filepath.Join("..", "..", "scripts", "testdata", "legacy-v3.sqlite.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	reader, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.sqlite")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(file, reader); err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(reader.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	store, err := syncer.Open(dir)
	if err != nil {
		t.Fatalf("container fixture did not upgrade: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, confirmed, rules, favorites int
	var node string
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("fixture version = %d, %v", version, err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM assets WHERE account_id='legacy' AND state='confirmed'`).
		Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT node_id FROM assets WHERE account_id='legacy'`).Scan(&node); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM rules WHERE account_id='legacy'`).Scan(&rules); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM favorites`).Scan(&favorites); err != nil {
		t.Fatal(err)
	}
	if confirmed != 1 || node != "legacy-node" || rules != 1 || favorites != 0 {
		t.Fatalf("container fixture migrated incorrectly: confirmed=%d node=%q rules=%d favorites=%d",
			confirmed, node, rules, favorites)
	}
}
