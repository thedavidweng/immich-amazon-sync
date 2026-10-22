package syncer

import (
	"context"
	"fmt"
	"testing"
)

// The deletion guard and the owned-membership removal guard are baseline safety
// bounds. These tests pin the exact arithmetic: the limit is inclusive and a
// fraction over 10% (or more than 500) is refused. The external deletion tests
// cover the behaviour; these cover the thresholds without uploading thousands of
// originals.
func TestDeletionGuardThresholds(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		confirmed, pending, failed int
		blocked                    bool
	}{
		{"exactly-500-of-large-library", 5000, 500, 0, false},
		{"501-pending", 5000, 501, 0, true},
		{"exactly-ten-percent", 900, 100, 0, false},
		{"over-ten-percent", 900, 101, 0, true},
		{"unmapped-failures-do-not-count", 900, 100, 500, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := openGuardStore(t)
			seedGuardAccount(t, store)
			seedGuardAssets(t, store, "confirmed", tt.confirmed)
			seedGuardAssets(t, store, "delete_pending", tt.pending)
			seedGuardAssets(t, store, "failed", tt.failed)

			e := &Engine{store: store, opts: Options{AccountID: "account"}}
			plan, err := e.PlanDeletions(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if plan.Pending != tt.pending {
				t.Fatalf("pending = %d, want %d", plan.Pending, tt.pending)
			}
			if plan.Synced != tt.confirmed+tt.pending {
				t.Fatalf("synced = %d, want %d", plan.Synced, tt.confirmed+tt.pending)
			}
			if plan.Blocked != tt.blocked {
				t.Fatalf("blocked = %v, want %v (%+v)", plan.Blocked, tt.blocked, plan)
			}
			if tt.blocked && plan.Reason != "deletion_guard" {
				t.Fatalf("reason = %q", plan.Reason)
			}
			if !tt.blocked && plan.Reason != "" {
				t.Fatalf("unexpected block reason: %q", plan.Reason)
			}
		})
	}
}

func TestMembershipRemovalGuardThresholds(t *testing.T) {
	for _, tt := range []struct {
		name               string
		baseline, removals int
		removeNonOwned     bool
		blocked            bool
	}{
		{"exactly-500-of-large-album", 6000, 500, false, false},
		{"501-of-large-album", 6000, 501, false, true},
		{"exactly-ten-percent", 1000, 100, false, false},
		{"over-ten-percent", 1000, 101, false, true},
		{"single-removal-small-album", 3, 1, false, false},
		{"two-removals-small-album", 3, 2, false, true},
		{"empty-baseline", 0, 1, false, true},
		{"non-owned-does-not-count", 3, 1, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := openGuardStore(t)
			seedGuardAccount(t, store)
			if _, err := store.db.Exec(`INSERT INTO rules(account_id,scope,album_id) VALUES('account','album:scope','scope')`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`INSERT INTO albums(account_id,scope,name) VALUES('account','album:scope','Album')`); err != nil {
				t.Fatal(err)
			}
			owned := make([]string, 0, tt.removals)
			for i := range tt.removals {
				id := fmt.Sprintf("node-%05d", i)
				if _, err := store.db.Exec(`INSERT INTO memberships(account_id,scope,node_id,owned,state)
					VALUES('account','album:scope',?,1,'present')`, id); err != nil {
					t.Fatal(err)
				}
				owned = append(owned, id)
			}
			if tt.removeNonOwned {
				if _, err := store.db.Exec(`INSERT INTO memberships(account_id,scope,node_id,owned,state)
					VALUES('account','album:scope','manual-node',0,'present')`); err != nil {
					t.Fatal(err)
				}
			}
			actual := make(map[string]bool, len(owned))
			for _, id := range owned {
				actual[id] = true
			}
			e := &Engine{store: store, opts: Options{AccountID: "account"}}
			job := &albumJob{scope: "album:scope", complete: true, baseline: tt.baseline}
			err := e.planMembers(context.Background(), job, []string{"keep-node"}, actual)
			if (err != nil) != tt.blocked {
				t.Fatalf("planMembers error = %v, want blocked %v", err, tt.blocked)
			}
			if tt.blocked && err.Error() != "membership_guard" {
				t.Fatalf("guard code = %q", err)
			}
			for _, id := range owned {
				var state string
				if err := store.db.QueryRow(`SELECT state FROM memberships
					WHERE account_id='account' AND scope='album:scope' AND node_id=?`, id).Scan(&state); err != nil {
					t.Fatal(err)
				}
				want := "present"
				if !tt.blocked {
					want = "remove"
				}
				if state != want {
					t.Fatalf("owned %s state = %q, want %q", id, state, want)
				}
			}
			if tt.removeNonOwned {
				var state string
				if err := store.db.QueryRow(`SELECT state FROM memberships
					WHERE account_id='account' AND scope='album:scope' AND node_id='manual-node'`).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if state != "present" {
					t.Fatalf("non-owned membership was modified: %q", state)
				}
			}
		})
	}
}

func openGuardStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func seedGuardAccount(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO accounts(id) VALUES('account')`); err != nil {
		t.Fatal(err)
	}
}

func seedGuardAssets(t *testing.T, store *Store, state string, count int) {
	t.Helper()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO assets(account_id,id,metadata,state) VALUES('account',?,'{}',?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range count {
		if _, err := stmt.Exec(fmt.Sprintf("%s-%05d", state, i), state); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
