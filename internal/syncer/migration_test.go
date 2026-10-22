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
