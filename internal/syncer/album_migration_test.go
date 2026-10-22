package syncer_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
	_ "modernc.org/sqlite"
)

func TestVersionThreeStoreAddsAlbumScopeColumnsWithoutWideningIt(t *testing.T) {
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
		INSERT INTO rules(account_id,scope,album_id,complete,expected,name)
		VALUES('account','album:11111111-1111-4111-8111-111111111111','11111111-1111-4111-8111-111111111111',1,1,'Family');
		INSERT INTO albums(account_id,scope,target_id,name,applied_name,state,baseline,complete_generation)
		VALUES('account','album:11111111-1111-4111-8111-111111111111','owned-album','Family','Family','confirmed',1,1);
		INSERT INTO memberships(account_id,scope,node_id,owned,state)
		VALUES('account','album:11111111-1111-4111-8111-111111111111','node-one',1,'present')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	check := func() {
		t.Helper()
		probe, err := sql.Open("sqlite", filepath.Join(dir, "sync.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Close()
		var version, active, discovered int
		var origin string
		if err := probe.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := probe.QueryRow(`SELECT active,origin,discovered FROM rules
			WHERE account_id='account' AND album_id!=''`).Scan(&active, &origin, &discovered); err != nil {
			t.Fatal(err)
		}
		if version != 4 || active != 1 || origin != "explicit" || discovered != 1 {
			t.Fatalf("migration widened or lost scope: version=%d active=%d origin=%s discovered=%d",
				version, active, origin, discovered)
		}
		var target string
		if err := probe.QueryRow("SELECT target_id FROM albums WHERE account_id='account'").Scan(&target); err != nil {
			t.Fatal(err)
		}
		var memberships, rules int
		if err := probe.QueryRow("SELECT count(*) FROM memberships WHERE account_id='account' AND owned=1").Scan(&memberships); err != nil {
			t.Fatal(err)
		}
		if err := probe.QueryRow("SELECT count(*) FROM rules WHERE account_id='account'").Scan(&rules); err != nil {
			t.Fatal(err)
		}
		if target != "owned-album" || memberships != 1 || rules != 1 {
			t.Fatalf("owned album state changed: target=%q memberships=%d rules=%d", target, memberships, rules)
		}
	}

	for range 2 {
		store, err := syncer.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		check()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
