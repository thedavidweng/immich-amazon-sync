package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// Hard limits for one deletion pass. They cannot be disabled: an Immich
// database restored from an old backup must not be able to empty the target
// account. The fraction is measured against every asset this account has a
// durable node mapping for.
const (
	deletionMaxAbsolute = 500
	deletionMaxFraction = 0.10
)

type DeletionPlan struct {
	Pending int      `json:"pending"`
	Synced  int      `json:"synced"`
	Blocked bool     `json:"blocked"`
	Reason  string   `json:"reason,omitempty"`
	Assets  []string `json:"assets"`
}

// PlanDeletions is the read-only manifest of the next deletion pass. It never
// mutates remote state.
func (e *Engine) PlanDeletions(ctx context.Context) (DeletionPlan, error) {
	plan := DeletionPlan{}
	err := e.store.db.QueryRowContext(ctx, `SELECT
		coalesce(sum(state='delete_pending'),0),
		coalesce(sum(state IN ('confirmed','existing','deleted','delete_pending')),0)
		FROM assets WHERE account_id=?`, e.opts.AccountID).Scan(&plan.Pending, &plan.Synced)
	if err != nil {
		return plan, errors.New("storage_unavailable")
	}
	if plan.Pending == 0 {
		return plan, nil
	}
	rows, err := e.store.db.QueryContext(ctx, `SELECT id FROM assets
		WHERE account_id=? AND state='delete_pending' ORDER BY id`, e.opts.AccountID)
	if err != nil {
		return plan, errors.New("storage_unavailable")
	}
	var assets []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		assets = append(assets, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return plan, errors.New("storage_unavailable")
	}
	plan.Assets = assets
	if plan.Pending > deletionMaxAbsolute || float64(plan.Pending)/float64(plan.Synced) > deletionMaxFraction {
		plan.Blocked = true
		plan.Reason = "deletion_guard"
	}
	return plan, nil
}

// sweepDeletions moves verified source-trashed assets to the Amazon trash. The
// whole pass is refused when it exceeds the hard limits. Each row is trashed
// only after the remote node is re-read and matched against the durable
// content evidence; nothing is ever permanently deleted.
func (e *Engine) sweepDeletions(ctx context.Context) error {
	if !e.opts.Deletions {
		return nil
	}
	plan, err := e.PlanDeletions(ctx)
	if err != nil {
		return err
	}
	if plan.Pending == 0 {
		return nil
	}
	if plan.Blocked {
		// The guard is deterministic until the source changes; do not hot-loop
		// on it. Pending rows keep their state and are visible in the plan.
		_, dbErr := e.store.db.ExecContext(ctx, `UPDATE assets SET next_at=?
			WHERE account_id=? AND state='delete_pending' AND next_at<=?`,
			e.opts.Now().Add(15*time.Minute).UnixMilli(), e.opts.AccountID, e.opts.Now().UnixMilli())
		if dbErr != nil {
			return errors.New("storage_unavailable")
		}
		return errors.New("deletion_guard")
	}
	var failed error
	for _, id := range plan.Assets {
		if ctx.Err() != nil {
			return errors.New("canceled")
		}
		if err := e.trashAsset(ctx, id); err != nil {
			failed = err
		}
	}
	return failed
}

// withdrawDeletions cancels every pending intent when propagation is disabled.
// The durable mapping returns to the state it had before the source trash.
func (e *Engine) withdrawDeletions(ctx context.Context) error {
	_, err := e.store.db.ExecContext(ctx, `UPDATE assets SET state=prior_state,prior_state='',
		error_code='',attempts=0,next_at=0 WHERE account_id=? AND state='delete_pending'`,
		e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) trashAsset(ctx context.Context, id string) error {
	var metadata, md5, node string
	var size int64
	var attempts int
	err := e.store.db.QueryRowContext(ctx, `SELECT metadata,md5,size,node_id,attempts FROM assets
		WHERE account_id=? AND id=? AND state='delete_pending' AND next_at<=?`,
		e.opts.AccountID, id, e.opts.Now().UnixMilli()).Scan(&metadata, &md5, &size, &node, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	var asset bridge.Asset
	if err := json.Unmarshal([]byte(metadata), &asset); err != nil {
		return errors.New("storage_unavailable")
	}
	content := bridge.Content{
		Name: asset.Name, MIME: asset.MIME, Size: size, MD5: md5, TakenAt: asset.TakenAt,
	}
	err = e.target.Trash(ctx, content, node)
	if err == nil {
		_, dbErr := e.store.db.ExecContext(ctx, `UPDATE assets SET state='deleted',prior_state='',
			error_code='',attempts=0,next_at=0 WHERE account_id=? AND id=?`, e.opts.AccountID, id)
		if dbErr != nil {
			return errors.New("storage_unavailable")
		}
		return nil
	}
	code := errorCode(err)
	attempts++
	next := e.opts.Now().Add(e.retryDelay(attempts, err)).UnixMilli()
	if _, dbErr := e.store.db.ExecContext(ctx, `UPDATE assets SET error_code=?,attempts=?,next_at=?
		WHERE account_id=? AND id=?`, code, attempts, next, e.opts.AccountID, id); dbErr != nil {
		return errors.New("storage_unavailable")
	}
	return errors.New(code)
}
