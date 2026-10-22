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

const (
	favoriteNone      = "none"
	favoriteUnknown   = "unknown"
	favoritePending   = "pending"
	favoriteApplied   = "applied"
	favoriteFailed    = "failed"
	favoriteUncertain = "uncertain"
)

// favoriteEligibleSQL gates per-node favorite work on at least one mapping in a
// settled media state. Trashed (delete_pending), Deleted and Unconfirmed
// mappings never authorize a favorite write.
const favoriteEligibleSQL = `EXISTS(SELECT 1 FROM assets a WHERE a.account_id=favorites.account_id
	AND a.node_id=favorites.node_id AND a.state IN ('confirmed','existing'))`

func (e *Engine) favoritesOn() bool { return e.opts.Favorites == "add_only" }

// favoriteMode is the effective setting reported in status.
func (e *Engine) favoriteMode() string {
	if e.favoritesOn() {
		return "add_only"
	}
	return "off"
}

// refreshFavorites derives one durable desired state per verified node from the
// explicit source favorite presence. Several assets or albums sharing a node
// collapse to a single row and therefore one write and one outcome. Add-only
// never downgrades a verified star or discards an uncertain write.
func (e *Engine) refreshFavorites(ctx context.Context) error {
	if !e.favoritesOn() {
		// Disabling stops new writes without unstaring. Unsettled intents are
		// withdrawn; a possibly-committed mutation stays visibly uncertain.
		_, err := e.store.db.ExecContext(ctx, `UPDATE favorites SET desired=0,
			state=CASE WHEN state IN ('applied','uncertain') THEN state ELSE 'none' END,
			attempts=CASE WHEN state IN ('applied','uncertain') THEN attempts ELSE 0 END,
			next_at=CASE WHEN state IN ('applied','uncertain') THEN next_at ELSE 0 END,
			error_code=CASE WHEN state IN ('applied','uncertain') THEN error_code ELSE '' END
			WHERE account_id=?`, e.opts.AccountID)
		if err != nil {
			return errors.New("storage_unavailable")
		}
		return nil
	}
	_, err := e.store.db.ExecContext(ctx, `INSERT INTO favorites(account_id,node_id,desired,state,updated_at)
		SELECT account_id,node_id,
			CASE WHEN max(favorite)=1 THEN 1 ELSE 0 END,
			CASE WHEN max(favorite)=1 THEN 'pending' WHEN max(favorite)=-1 THEN 'unknown' ELSE 'none' END,
			?
		FROM assets WHERE account_id=? AND node_id!='' AND state IN ('confirmed','existing')
		GROUP BY account_id,node_id
		ON CONFLICT(account_id,node_id) DO UPDATE SET
			desired=excluded.desired,
			state=CASE
				WHEN favorites.state='applied' THEN 'applied'
				WHEN favorites.state='uncertain' THEN 'uncertain'
				WHEN excluded.desired=1 AND favorites.state IN ('pending','failed') THEN favorites.state
				ELSE excluded.state END,
			updated_at=excluded.updated_at`,
		time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

// sweepFavorites refreshes desired state and settles a bounded batch of due
// nodes. Every attempt begins with an authoritative remote read, so a lost or
// timed-out mutation is retried only after the node has been re-read. Media is
// never uploaded or mutated here.
func (e *Engine) sweepFavorites(ctx context.Context) error {
	if err := e.refreshFavorites(ctx); err != nil {
		return err
	}
	if !e.favoritesOn() {
		return nil
	}
	rows, err := e.store.db.QueryContext(ctx, `SELECT node_id,state,desired,attempts FROM favorites
		WHERE account_id=? AND state IN ('pending','failed','uncertain')
		AND attempts<5 AND next_at<=unixepoch('subsec')*1000 AND `+favoriteEligibleSQL+`
		ORDER BY node_id LIMIT ?`, e.opts.AccountID, favoriteBatch)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	type pendingFavorite struct {
		nodeID   string
		state    string
		desired  bool
		attempts int
	}
	var due []pendingFavorite
	for rows.Next() {
		var item pendingFavorite
		if err = rows.Scan(&item.nodeID, &item.state, &item.desired, &item.attempts); err != nil {
			break
		}
		due = append(due, item)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return errors.New("storage_unavailable")
	}
	var firstErr error
	for _, item := range due {
		if ctx.Err() != nil {
			return errors.New("canceled")
		}
		if err := e.applyFavorite(ctx, item.nodeID, item.desired, item.attempts); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (e *Engine) applyFavorite(ctx context.Context, nodeID string, desired bool, attempts int) error {
	content, ok, err := e.favoriteContent(ctx, nodeID)
	if err != nil || !ok {
		return err
	}
	observed, err := readRetry(ctx, func() (bool, error) {
		return e.target.ReadFavorite(ctx, content, nodeID)
	})
	if err != nil {
		return e.favoriteFailed(ctx, nodeID, attempts, err, nil)
	}
	if observed {
		return e.settleFavorite(ctx, nodeID, true, favoriteApplied)
	}
	if !desired {
		// The source no longer asks for a star and add-only never unstars.
		return e.settleFavorite(ctx, nodeID, false, favoriteNone)
	}
	// The read proved an explicit false immediately before this single write.
	if err := e.target.SetFavorite(ctx, content, nodeID); err != nil {
		return e.favoriteFailed(ctx, nodeID, attempts, err, &observed)
	}
	return e.settleFavorite(ctx, nodeID, true, favoriteApplied)
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

func (e *Engine) settleFavorite(ctx context.Context, nodeID string, observed bool, state string) error {
	applied := 0
	if state == favoriteApplied {
		applied = 1
	}
	_, err := e.store.db.ExecContext(ctx, `UPDATE favorites SET state=?,applied=?,observed=?,
		attempts=0,next_at=0,error_code='',updated_at=? WHERE account_id=? AND node_id=?`,
		state, applied, boolInt(observed), time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID, nodeID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

// favoriteFailed records an independent per-node retry. A mutation or read
// whose committed state is unknown stays explicitly uncertain instead of
// becoming a settled failure; other codes keep their finite value.
func (e *Engine) favoriteFailed(ctx context.Context, nodeID string, attempts int, err error, observed *bool) error {
	code := errorCode(err)
	state := favoriteFailed
	switch code {
	case bridge.FavoriteUnconfirmed, bridge.Unconfirmed, "canceled":
		state, code = favoriteUncertain, bridge.FavoriteUnconfirmed
	}
	attempts++
	value := -1
	if observed != nil {
		value = boolInt(*observed)
	}
	_, dbErr := e.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE favorites SET state=?,observed=?,
		error_code=?,attempts=?,next_at=?,updated_at=? WHERE account_id=? AND node_id=?`,
		state, value, code, attempts, time.Now().Add(retryDelay(attempts-1, err)).UnixMilli(),
		time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID, nodeID)
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
