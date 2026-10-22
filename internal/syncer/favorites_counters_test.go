package syncer

import (
	"context"
	"testing"
)

// Backlog counters describe work that can actually run. A favorite row whose
// node has no settled media mapping can never dispatch, so it must not inflate
// pending, failed or uncertain counts.
func TestFavoriteBacklogCountersExcludeIneligibleNodes(t *testing.T) {
	store := openGuardStore(t)
	seedGuardAccount(t, store)
	for _, state := range []string{"pending", "failed", "uncertain"} {
		settled := "node-settled-" + state
		stranded := "node-stranded-" + state
		if _, err := store.db.Exec(`INSERT INTO assets(account_id,id,metadata,state,node_id) VALUES
			('account',?,'{}','confirmed',?),
			('account',?,'{}','delete_pending',?)`,
			"settled-"+state, settled, "stranded-"+state, stranded); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO favorites(account_id,node_id,desired,state) VALUES
			('account',?,1,?),('account',?,1,?)`, settled, state, stranded, state); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{store: store, opts: Options{AccountID: "account"}}
	st, err := e.summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.FavoritesPending != 1 || st.FavoritesFailed != 1 || st.FavoritesUncertain != 1 {
		t.Fatalf("ineligible favorite rows inflated backlog counters: %+v", st)
	}
	if st.FavoritesDesired != 6 {
		t.Fatalf("desired intent was dropped: %+v", st)
	}
}

// A node that regains a settled mapping is eligible again in the same report.
func TestFavoriteBacklogCounterFollowsMediaEligibility(t *testing.T) {
	store := openGuardStore(t)
	seedGuardAccount(t, store)
	if _, err := store.db.Exec(`INSERT INTO assets(account_id,id,metadata,state,node_id)
		VALUES('account','one','{}','skipped','node-one');
		INSERT INTO favorites(account_id,node_id,desired,state) VALUES('account','node-one',1,'pending')`); err != nil {
		t.Fatal(err)
	}
	e := &Engine{store: store, opts: Options{AccountID: "account"}}
	if st, err := e.summary(context.Background()); err != nil || st.FavoritesPending != 0 {
		t.Fatalf("ineligible row counted: %+v %v", st, err)
	}
	if _, err := store.db.Exec("UPDATE assets SET state='confirmed' WHERE account_id='account' AND id='one'"); err != nil {
		t.Fatal(err)
	}
	if st, err := e.summary(context.Background()); err != nil || st.FavoritesPending != 1 {
		t.Fatalf("eligible row not counted: %+v %v", st, err)
	}
}
