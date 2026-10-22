package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// favoriteBatch bounds one favorite pass so an explicit run stays bounded even
// when a whole library becomes favorite at once. Remaining work persists.
const favoriteBatch = 64

// favoriteAuditBatch bounds the periodic drift re-read. Each settled node is
// re-read at most once per favoriteAuditInterval, oldest first, so an outage or
// restart converges without a retry storm and without reading the whole library
// on every pass.
const favoriteAuditBatch = 16

// favoriteAuditInterval spaces those re-reads. It is the slowest loop that can
// notice a manual change on the Amazon side.
const favoriteAuditInterval = 6 * time.Hour

const (
	favoriteNone      = "none"
	favoriteUnknown   = "unknown"
	favoritePending   = "pending"
	favoriteApplied   = "applied"
	favoritePreserved = "preserved"
	favoriteConflict  = "conflict"
	favoriteFailed    = "failed"
	favoriteUncertain = "uncertain"
)

// sharedTargetConflict is the finite code for contradictory favorite intent
// recorded by two local Accounts that resolve to the same Amazon identity. Both
// accounts stop writing until one of them changes its source wish.
const sharedTargetConflict = "shared_target_conflict"

// favoriteIntentSQL selects rows that carry an explicit source decision. A
// withdrawn intent (favorites off) or an absent source field is not an opinion
// and cannot conflict with a peer account.
const favoriteIntentSQL = `state NOT IN ('none','unknown','preserved')`

// favoriteEligibleSQL gates per-node favorite work on at least one mapping in a
// settled media state. Trashed (delete_pending), Deleted and Unconfirmed
// mappings never authorize a favorite write or a drift read.
const favoriteEligibleSQL = `EXISTS(SELECT 1 FROM assets a WHERE a.account_id=favorites.account_id
	AND a.node_id=favorites.node_id AND a.state IN ('confirmed','existing'))`

func (e *Engine) favoritesOn() bool {
	return e.opts.Favorites == "add_only" || e.opts.Favorites == "mirror"
}

// favoriteMirror reports whether the source is authoritative enough to clear a
// star. add_only never issues an unstar.
func (e *Engine) favoriteMirror() bool { return e.opts.Favorites == "mirror" }

// favoriteMode is the effective setting reported in status.
func (e *Engine) favoriteMode() string {
	if e.favoritesOn() {
		return e.opts.Favorites
	}
	return "off"
}

// favoriteAuthority explains who may clear a star: add-only never does, mirror
// clears only stars this service applied, and takeover also clears manual ones.
func (e *Engine) favoriteAuthority() string {
	switch {
	case e.opts.Favorites == "add_only":
		return "star_only"
	case e.favoriteMirror() && e.opts.FavoriteTakeover:
		return "takeover"
	case e.favoriteMirror():
		return "source_applied"
	}
	return ""
}

// refreshFavorites derives one durable desired state per verified node from the
// explicit source favorite presence. Several assets or albums sharing a node
// collapse to a single row and therefore one write and one outcome. Absent
// source presence is unknown and never authorizes an unstar. A star this
// service did not apply is preserved unless takeover is recorded.
func (e *Engine) refreshFavorites(ctx context.Context) error {
	now := e.opts.Now()
	if !e.favoritesOn() {
		// Disabling stops new writes without unstaring. The ownership and
		// observation history stays, so re-enabling can still clear only stars
		// this service applied.
		_, err := e.store.db.ExecContext(ctx, `UPDATE favorites SET desired=0,state='none',
			attempts=0,next_at=0,error_code='',updated_at=? WHERE account_id=?`,
			now.UTC().Format(time.RFC3339Nano), e.opts.AccountID)
		if err != nil {
			return errors.New("storage_unavailable")
		}
		return nil
	}
	mirror := e.favoriteMirror()
	// add_only has nothing to do for an explicitly non-favorite node. mirror
	// performs one bounded baseline read so status can show the remote value
	// before it decides whether the star belongs to this service.
	settled := favoriteNone
	if mirror {
		settled = favoritePending
	}
	_, err := e.store.db.ExecContext(ctx, `INSERT INTO favorites(account_id,node_id,desired,state,updated_at)
		SELECT account_id,node_id,
			CASE WHEN max(favorite)=1 THEN 1 ELSE 0 END,
			CASE WHEN max(favorite)=1 THEN 'pending'
				WHEN min(favorite)=0 THEN ?
				ELSE 'unknown' END,
			?
		FROM assets WHERE account_id=? AND node_id!='' AND state IN ('confirmed','existing')
		GROUP BY account_id,node_id
		ON CONFLICT(account_id,node_id) DO UPDATE SET
			desired=excluded.desired,
			state=CASE
				WHEN excluded.state='unknown' THEN 'unknown'
				WHEN favorites.state IN ('pending','failed','uncertain') THEN favorites.state
				WHEN excluded.desired=1 THEN CASE WHEN favorites.observed=1 THEN 'applied' ELSE 'pending' END
				WHEN favorites.observed=0 THEN 'none'
				WHEN favorites.observed=1 THEN CASE WHEN ? OR (? AND favorites.applied=1)
					THEN 'pending' ELSE 'preserved' END
				WHEN ? THEN 'pending'
				ELSE 'none' END,
			updated_at=excluded.updated_at`,
		settled, now.UTC().Format(time.RFC3339Nano), e.opts.AccountID,
		e.opts.FavoriteTakeover, mirror, mirror)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	// Settled rows carry no retry state; their next read is the audit schedule.
	_, err = e.store.db.ExecContext(ctx, `UPDATE favorites SET attempts=0,next_at=0,error_code=''
		WHERE account_id=? AND state IN ('applied','none','preserved','unknown')`, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return e.markFavoriteConflicts(ctx)
}

// markFavoriteConflicts blocks writes for a node two local Accounts both claim
// with contradictory intent. The conflict is derived, never sticky: a withdrawn
// peer intent returns the row to normal work on the next pass.
func (e *Engine) markFavoriteConflicts(ctx context.Context) error {
	now := e.opts.Now().UTC().Format(time.RFC3339Nano)
	// Only a live peer mapping gives that account a writable intent; a stale
	// row whose media became ineligible can never write and must not block this
	// account's legitimate work.
	const peer = `SELECT 1 FROM favorites other
		JOIN accounts mine ON mine.id=favorites.account_id
		JOIN accounts theirs ON theirs.id=other.account_id
		WHERE other.account_id<>favorites.account_id AND other.node_id=favorites.node_id
		AND other.state NOT IN ('none','unknown','preserved') AND other.desired<>favorites.desired
		AND mine.target_identity!='' AND theirs.target_identity=mine.target_identity
		AND EXISTS(SELECT 1 FROM assets peer WHERE peer.account_id=other.account_id
			AND peer.node_id=other.node_id AND peer.state IN ('confirmed','existing'))`
	_, err := e.store.db.ExecContext(ctx, `UPDATE favorites SET state='conflict',
		error_code='shared_target_conflict',attempts=0,next_at=0,updated_at=?
		WHERE account_id=? AND `+favoriteIntentSQL+` AND `+favoriteEligibleSQL+`
		AND EXISTS (`+peer+`)`, now, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	// The peer no longer contradicts this row: re-derive the settled state so
	// the row is either due again or visibly preserved.
	_, err = e.store.db.ExecContext(ctx, `UPDATE favorites SET error_code='',attempts=0,next_at=0,
		state=CASE WHEN desired=1 THEN CASE WHEN observed=1 THEN 'applied' ELSE 'pending' END
			WHEN observed=0 THEN 'none'
			WHEN observed=1 AND NOT (?) AND applied=0 THEN 'preserved'
			ELSE 'pending' END,
		updated_at=?
		WHERE account_id=? AND state='conflict' AND NOT EXISTS (`+peer+`)`,
		e.opts.FavoriteTakeover, now, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	// A row that left the conflict through the source refresh must not keep the
	// blocking marker, or its fresh intent could never be applied.
	_, err = e.store.db.ExecContext(ctx, `UPDATE favorites SET error_code=''
		WHERE account_id=? AND error_code='shared_target_conflict' AND state<>'conflict'`,
		e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

// sweepFavorites refreshes desired state and settles a bounded batch of due
// nodes plus a bounded drift audit. Every attempt begins with an authoritative
// remote read, so a lost or timed-out mutation is retried only after the node
// has been re-read. Media is never uploaded or mutated here.
func (e *Engine) sweepFavorites(ctx context.Context) error {
	if err := e.refreshFavorites(ctx); err != nil {
		return err
	}
	if !e.favoritesOn() {
		return nil
	}
	due, err := e.dueFavorites(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	conflicts, err := e.favoriteConflicts(ctx)
	if err != nil {
		return err
	}
	if conflicts > 0 {
		firstErr = errors.New(sharedTargetConflict)
	}
	if err := e.auditFavorites(ctx); err != nil {
		return err
	}
	for _, item := range due {
		if ctx.Err() != nil {
			return errors.New("canceled")
		}
		if err := e.applyFavorite(ctx, item); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type pendingFavorite struct {
	nodeID   string
	desired  bool
	applied  bool
	attempts int
}

// dueFavorites returns the bounded set of rows with a read or write pending.
// Shared-target conflicts are never due and therefore never write.
func (e *Engine) dueFavorites(ctx context.Context) ([]pendingFavorite, error) {
	rows, err := e.store.db.QueryContext(ctx, `SELECT node_id,desired,applied,attempts FROM favorites
		WHERE account_id=? AND state IN ('pending','failed','uncertain') AND `+e.attemptLimit("")+`
		AND next_at<=? AND error_code<>'shared_target_conflict' AND `+favoriteEligibleSQL+`
		ORDER BY next_at,node_id LIMIT ?`, e.opts.AccountID, e.opts.Now().UnixMilli(), favoriteBatch)
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	due, err := scanFavorites(rows)
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	return due, nil
}

// auditFavorites re-reads a bounded, oldest-first slice of settled nodes. A
// star the source still wants is repaired; takeover also re-checks nodes the
// source cleared, because a manual star reappearing there is drift too.
func (e *Engine) auditFavorites(ctx context.Context) error {
	before := e.opts.Now().Add(-favoriteAuditInterval).UnixMilli()
	rows, err := e.store.db.QueryContext(ctx, `SELECT node_id,desired,applied,0 FROM favorites
		WHERE account_id=? AND state IN ('applied','none') AND checked_at<=? AND (desired=1 OR ?)
		AND error_code<>'shared_target_conflict' AND `+favoriteEligibleSQL+`
		ORDER BY checked_at,node_id LIMIT ?`,
		e.opts.AccountID, before, e.opts.FavoriteTakeover, favoriteAuditBatch)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	due, err := scanFavorites(rows)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	var firstErr error
	for _, item := range due {
		if ctx.Err() != nil {
			return errors.New("canceled")
		}
		if err := e.applyFavorite(ctx, item); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func scanFavorites(rows *sql.Rows) ([]pendingFavorite, error) {
	var due []pendingFavorite
	for rows.Next() {
		var item pendingFavorite
		if err := rows.Scan(&item.nodeID, &item.desired, &item.applied, &item.attempts); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, item)
	}
	err := errors.Join(rows.Err(), rows.Close())
	return due, err
}

// favoriteConflicts reports how many rows are blocked by a shared-target
// conflict.
func (e *Engine) favoriteConflicts(ctx context.Context) (int, error) {
	var count int
	if err := e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM favorites
		WHERE account_id=? AND error_code='shared_target_conflict'`, e.opts.AccountID).Scan(&count); err != nil {
		return 0, errors.New("storage_unavailable")
	}
	return count, nil
}

// applyFavorite reconciles one node from an authoritative read. Authority for
// an unstar is durable: the star must have been applied by this service, or the
// account must have recorded takeover. A fresh explicit-true read is always
// required before the mutation, so a stale belief cannot clear a star.
func (e *Engine) applyFavorite(ctx context.Context, item pendingFavorite) error {
	content, ok, err := e.favoriteContent(ctx, item.nodeID)
	if err != nil || !ok {
		return err
	}
	observed, err := readRetry(ctx, func() (bool, error) {
		return e.target.ReadFavorite(ctx, content, item.nodeID)
	})
	if err != nil {
		return e.favoriteFailed(ctx, item.nodeID, item.attempts, err)
	}
	if observed == item.desired {
		if item.desired {
			// Ownership is never promoted by a read: a manually starred node
			// stays outside this service's unstar authority.
			return e.settleFavorite(ctx, item.nodeID, true, item.applied, favoriteApplied)
		}
		return e.settleFavorite(ctx, item.nodeID, false, false, favoriteNone)
	}
	if !item.desired {
		if !e.opts.FavoriteTakeover && !item.applied {
			return e.settleFavorite(ctx, item.nodeID, true, false, favoritePreserved)
		}
		if err := e.target.SetFavorite(ctx, content, item.nodeID, false); err != nil {
			return e.favoriteFailed(ctx, item.nodeID, item.attempts, err)
		}
		return e.settleFavorite(ctx, item.nodeID, false, false, favoriteNone)
	}
	if err := e.target.SetFavorite(ctx, content, item.nodeID, true); err != nil {
		return e.favoriteFailed(ctx, item.nodeID, item.attempts, err)
	}
	return e.settleFavorite(ctx, item.nodeID, true, true, favoriteApplied)
}

// favoriteContent builds the verification evidence from one settled mapping of
// the shared node. Media identity, not the mapping that happened to be scanned
// first, authorizes the write.
func (e *Engine) favoriteContent(ctx context.Context, nodeID string) (bridge.Content, bool, error) {
	var digest, metadata string
	var size int64
	err := e.store.db.QueryRowContext(ctx, `SELECT md5,size,metadata FROM assets
		WHERE account_id=? AND node_id=? AND state IN ('confirmed','existing')
		ORDER BY (state='confirmed') DESC,id LIMIT 1`, e.opts.AccountID, nodeID).
		Scan(&digest, &size, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return bridge.Content{}, false, nil
	}
	if err != nil {
		return bridge.Content{}, false, errors.New("storage_unavailable")
	}
	var asset bridge.Asset
	if err := json.Unmarshal([]byte(metadata), &asset); err != nil {
		return bridge.Content{}, false, errors.New("storage_unavailable")
	}
	return bridge.Content{Name: asset.Name, MIME: asset.MIME, MD5: digest, Size: size, TakenAt: asset.TakenAt}, true, nil
}

func (e *Engine) settleFavorite(ctx context.Context, nodeID string, observed, applied bool, state string) error {
	_, err := e.store.db.ExecContext(ctx, `UPDATE favorites SET state=?,applied=?,observed=?,
		attempts=0,next_at=0,error_code='',checked_at=?,updated_at=? WHERE account_id=? AND node_id=?`,
		state, boolInt(applied), boolInt(observed), e.opts.Now().UnixMilli(),
		e.opts.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID, nodeID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

// favoriteFailed records an independent per-node retry. A mutation or read
// whose committed state is unknown stays explicitly uncertain instead of
// becoming a settled failure; other codes keep their finite value.
func (e *Engine) favoriteFailed(ctx context.Context, nodeID string, attempts int, err error) error {
	code := errorCode(err)
	state := favoriteFailed
	switch code {
	case bridge.FavoriteUnconfirmed, bridge.Unconfirmed, "canceled":
		state, code = favoriteUncertain, bridge.FavoriteUnconfirmed
	}
	attempts++
	_, dbErr := e.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE favorites SET state=?,
		error_code=?,attempts=?,next_at=?,updated_at=? WHERE account_id=? AND node_id=?`,
		state, code, attempts, e.opts.Now().Add(e.retryDelay(attempts-1, err)).UnixMilli(),
		e.opts.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID, nodeID)
	if dbErr != nil {
		return errors.New("storage_unavailable")
	}
	if code == bridge.Authentication || code == bridge.Permission {
		return e.accountError(ctx, err)
	}
	return errors.New(code)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
