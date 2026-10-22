package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// pairBatch bounds one pair-resolution pass so an explicit run stays bounded
// even when a whole library of Live Photos is adopted at once.
const pairBatch = 16

// pairIncomplete is the finite report code for a still whose companion is
// missing or ineligible. It never claims a complete Live Photo backup.
const pairIncomplete = "pair_incomplete"

func (e *Engine) pairsOn() bool { return e.opts.LivePhotoMode == bridge.LivePhotoOriginalPair }

// companionEligibleSQL anchors a resolved companion to a still that belongs to
// an active scope. It lets a companion Immich does not enumerate be uploaded
// without inventing album membership or widening the configured scope.
const companionEligibleSQL = `(EXISTS(SELECT 1 FROM live_pairs p
		JOIN assets s ON s.account_id=p.account_id AND s.id=p.still_id
		JOIN scope_assets m ON m.account_id=s.account_id AND m.asset_id=s.id
		JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
		WHERE p.account_id=a.account_id AND p.companion_id=a.id
			AND r.active=1 AND m.generation=r.generation AND r.error_code='')
	OR EXISTS(SELECT 1 FROM live_pairs p
		JOIN assets s ON s.account_id=p.account_id AND s.id=p.still_id
		JOIN signals m ON m.account_id=s.account_id AND m.asset_id=s.id
		JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
		WHERE p.account_id=a.account_id AND p.companion_id=a.id
			AND r.active=1 AND m.generation=r.generation AND r.error_code=''))`

// eligibleSQL adds companion eligibility only where the mode asks for it.
func (e *Engine) eligibleSQL() string {
	if e.pairsOn() {
		return "(" + eligibleSQL + " OR " + companionEligibleSQL + ")"
	}
	return eligibleSQL
}

// companionAsset reports whether assetID is the resolved companion of a still.
// The durable relationship is the only thing that makes a hidden video
// eligible, so unrelated hidden media keeps its exclusion.
func (e *Engine) companionAsset(ctx context.Context, assetID string) (bool, error) {
	if !e.pairsOn() {
		return false, nil
	}
	var count int
	err := e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM live_pairs
		WHERE account_id=? AND companion_id=?`,
		e.opts.AccountID, assetID).Scan(&count)
	if err != nil {
		return false, errors.New("storage_unavailable")
	}
	return count != 0, nil
}

// companionExclusion applies the policy to a resolved companion. Its hidden
// visibility is the pair mechanism, not an exclusion; every other finite
// reason still applies.
func (e *Engine) companionExclusion(a bridge.Asset) string {
	a.ExcludedReason = ""
	return e.opts.Filters.ExcludeCompanion(a)
}

// discoverPair keeps the durable Live Photo relationship in step with the
// source. An eligible still that references a companion owns one pair row; an
// excluded still or a removed link withdraws it. Withdrawing bookkeeping never
// downgrades a confirmed Node, and nothing here deletes remote media.
func (e *Engine) discoverPair(ctx context.Context, tx *sql.Tx, a bridge.Asset, exclusion string) error {
	if !e.pairsOn() {
		return nil
	}
	if a.LivePhotoID == "" || exclusion != "" {
		_, err := tx.ExecContext(ctx, "DELETE FROM live_pairs WHERE account_id=? AND still_id=?",
			e.opts.AccountID, a.ID)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO live_pairs(account_id,still_id,companion_id) VALUES(?,?,?)
		ON CONFLICT(account_id,still_id) DO UPDATE SET
			companion_id=excluded.companion_id,
			state=CASE WHEN live_pairs.companion_id=excluded.companion_id THEN live_pairs.state ELSE 'pending' END,
			error_code=CASE WHEN live_pairs.companion_id=excluded.companion_id THEN live_pairs.error_code ELSE '' END,
			attempts=CASE WHEN live_pairs.companion_id=excluded.companion_id THEN live_pairs.attempts ELSE 0 END,
			next_at=CASE WHEN live_pairs.companion_id=excluded.companion_id THEN live_pairs.next_at ELSE 0 END`,
		e.opts.AccountID, a.ID, a.LivePhotoID)
	if err != nil {
		return err
	}
	// The companion may have been discovered before its still existed.
	_, err = tx.ExecContext(ctx, `UPDATE assets SET state='pending',exclusion='',error_code='',attempts=0,next_at=0
		WHERE account_id=? AND id=? AND state='skipped' AND exclusion IN ('hidden','live_photo_companion')`,
		e.opts.AccountID, a.LivePhotoID)
	return err
}

// reconcileLivePhotoMode applies an explicit mode change without touching
// remote media. Enabling original_pair seeds the durable link from stored
// source metadata, so already confirmed stills are adopted without a rescan;
// disabling it withdraws pair bookkeeping but keeps every Node.
func (e *Engine) reconcileLivePhotoMode(ctx context.Context) error {
	var stored string
	if err := e.store.db.QueryRowContext(ctx, "SELECT live_photo_mode FROM accounts WHERE id=?",
		e.opts.AccountID).Scan(&stored); err != nil {
		return errors.New("storage_unavailable")
	}
	mode := string(e.opts.LivePhotoMode)
	if stored == mode {
		return nil
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	if e.pairsOn() {
		rows, err := tx.QueryContext(ctx, `SELECT id,metadata FROM assets
			WHERE account_id=? AND state NOT IN ('skipped','deleted') AND metadata LIKE '%live_photo_id%'`,
			e.opts.AccountID)
		if err != nil {
			return errors.New("storage_unavailable")
		}
		type pair struct{ still, companion string }
		var found []pair
		for rows.Next() {
			var id, metadata string
			if err = rows.Scan(&id, &metadata); err != nil {
				break
			}
			var a bridge.Asset
			if err = json.Unmarshal([]byte(metadata), &a); err != nil {
				break
			}
			if a.LivePhotoID != "" {
				found = append(found, pair{still: id, companion: a.LivePhotoID})
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return errors.New("storage_unavailable")
		}
		for _, p := range found {
			if _, err = tx.ExecContext(ctx, `INSERT INTO live_pairs(account_id,still_id,companion_id) VALUES(?,?,?)
				ON CONFLICT(account_id,still_id) DO UPDATE SET companion_id=excluded.companion_id,state='pending',
					error_code='',attempts=0,next_at=0`, e.opts.AccountID, p.still, p.companion); err != nil {
				return errors.New("storage_unavailable")
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE assets SET state='pending',exclusion='',error_code='',attempts=0,next_at=0
			WHERE account_id=? AND state='skipped' AND exclusion IN ('hidden','live_photo_companion')
			AND EXISTS(SELECT 1 FROM live_pairs p WHERE p.account_id=assets.account_id AND p.companion_id=assets.id)`,
			e.opts.AccountID); err != nil {
			return errors.New("storage_unavailable")
		}
	} else if _, err = tx.ExecContext(ctx, "DELETE FROM live_pairs WHERE account_id=?", e.opts.AccountID); err != nil {
		return errors.New("storage_unavailable")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE accounts SET live_photo_mode=? WHERE id=?", mode, e.opts.AccountID); err != nil {
		return errors.New("storage_unavailable")
	}
	if err = tx.Commit(); err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

// sweepPairs settles pair completeness and resolves companions that the
// current pages did not enumerate. It is bounded and never performs a remote
// deletion.
func (e *Engine) sweepPairs(ctx context.Context) error {
	if !e.pairsOn() {
		return nil
	}
	if err := e.settlePairs(ctx); err != nil {
		return err
	}
	rows, err := e.store.db.QueryContext(ctx, `SELECT still_id,companion_id,attempts FROM live_pairs
		WHERE account_id=? AND state!='complete' AND attempts<5 AND next_at<=?
		ORDER BY still_id LIMIT ?`, e.opts.AccountID, e.opts.Now().UnixMilli(), pairBatch)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	type pending struct {
		still, companion string
		attempts         int
	}
	var due []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.still, &p.companion, &p.attempts); err != nil {
			break
		}
		due = append(due, p)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return errors.New("storage_unavailable")
	}
	for _, p := range due {
		if err := e.resolvePair(ctx, p.still, p.companion, p.attempts); err != nil {
			return err
		}
	}
	return nil
}

// settlePairs derives completeness from the durable media states: both the
// still and its companion must be verified Nodes with their own identity. A
// pair that can no longer complete is reported incomplete.
func (e *Engine) settlePairs(ctx context.Context) error {
	// An incomplete pair returns to complete as soon as both originals have
	// their own verified Node again, so a recovered companion is never reported
	// stale.
	_, err := e.store.db.ExecContext(ctx, `UPDATE live_pairs SET state='complete',error_code=''
		WHERE account_id=? AND state IN ('pending','incomplete')
		AND EXISTS(SELECT 1 FROM assets s, assets c
			WHERE s.account_id=live_pairs.account_id AND s.id=live_pairs.still_id
			AND s.state IN ('confirmed','existing') AND s.node_id!=''
			AND c.account_id=live_pairs.account_id AND c.id=live_pairs.companion_id
			AND c.state IN ('confirmed','existing') AND c.node_id!=s.node_id)`, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	_, err = e.store.db.ExecContext(ctx, `UPDATE live_pairs SET state='incomplete',error_code=?
		WHERE account_id=? AND state='pending'
		AND (EXISTS(SELECT 1 FROM assets c WHERE c.account_id=live_pairs.account_id
				AND c.id=live_pairs.companion_id AND c.state IN ('skipped','deleted'))
			OR EXISTS(SELECT 1 FROM assets s WHERE s.account_id=live_pairs.account_id
				AND s.id=live_pairs.still_id AND s.state IN ('skipped','deleted')))`,
		pairIncomplete, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

// resolvePair makes one companion uploadable, or records why it cannot be. A
// fetched companion is registered as its own Asset with its own bytes; no
// derived media or hash is ever produced.
func (e *Engine) resolvePair(ctx context.Context, stillID, companionID string, attempts int) error {
	var state, exclusion string
	err := e.store.db.QueryRowContext(ctx, `SELECT state,exclusion FROM assets WHERE account_id=? AND id=?`,
		e.opts.AccountID, companionID).Scan(&state, &exclusion)
	switch {
	case err == nil && (state == "confirmed" || state == "existing"):
		// settlePairs owns completeness; a still still in flight keeps the
		// pair pending without another source read.
		return nil
	case err == nil && state == "skipped" && (exclusion == "hidden" || exclusion == "live_photo_companion"):
		_, err = e.store.db.ExecContext(ctx, `UPDATE assets SET state='pending',exclusion='',error_code='',
			attempts=0,next_at=0 WHERE account_id=? AND id=?`, e.opts.AccountID, companionID)
		if err != nil {
			return errors.New("storage_unavailable")
		}
		return e.clearPairRetry(ctx, stillID)
	case err == nil && (state == "skipped" || state == "deleted"):
		return e.failPair(ctx, stillID, attempts, nil)
	case err == nil:
		// A settled-or-in-flight companion needs no source read yet.
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return errors.New("storage_unavailable")
	}
	a, err := readRetry(ctx, func() (bridge.Asset, error) { return e.source.Asset(ctx, companionID) })
	if err != nil {
		if errorCode(err) == bridge.NotFound {
			return e.failPair(ctx, stillID, attempts, err)
		}
		return err
	}
	if a.ID != companionID || !validCompanion(a) {
		return e.failPair(ctx, stillID, attempts, nil)
	}
	metadata, err := json.Marshal(a)
	if err != nil {
		return errors.New(bridge.Protocol)
	}
	reason := e.companionExclusion(a)
	state = "pending"
	if reason != "" {
		state = "skipped"
	}
	if _, err = e.store.db.ExecContext(ctx, `INSERT INTO assets(account_id,id,metadata,state,exclusion,favorite)
		VALUES(?,?,?,?,?,?) ON CONFLICT(account_id,id) DO NOTHING`,
		e.opts.AccountID, a.ID, string(metadata), state, reason, favoriteValue(a)); err != nil {
		return errors.New("storage_unavailable")
	}
	if reason != "" {
		return e.failPair(ctx, stillID, attempts, nil)
	}
	return e.clearPairRetry(ctx, stillID)
}

// failPair records an incomplete pair with bounded retry: a companion can
// appear later, but a permanently missing one must not be re-read every cycle.
func (e *Engine) failPair(ctx context.Context, stillID string, attempts int, cause error) error {
	_, err := e.store.db.ExecContext(ctx, `UPDATE live_pairs SET state='incomplete',error_code=?,
		attempts=attempts+1,next_at=? WHERE account_id=? AND still_id=?`,
		pairIncomplete, e.opts.Now().Add(retryDelay(attempts, cause)).UnixMilli(), e.opts.AccountID, stillID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) clearPairRetry(ctx context.Context, stillID string) error {
	_, err := e.store.db.ExecContext(ctx, `UPDATE live_pairs SET state='pending',error_code='',attempts=0,next_at=0
		WHERE account_id=? AND still_id=?`, e.opts.AccountID, stillID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

// validCompanion accepts only what Immich reports as a hidden video companion.
func validCompanion(a bridge.Asset) bool {
	if !a.LivePhotoVideo || !strings.EqualFold(a.Kind, "VIDEO") {
		return false
	}
	return a.ExcludedReason == "" || a.ExcludedReason == "hidden"
}

func favoriteValue(a bridge.Asset) int {
	if a.Favorite == nil {
		return -1
	}
	if *a.Favorite {
		return 1
	}
	return 0
}
