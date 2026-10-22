package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

func (e *Engine) beginIncremental(ctx context.Context) error {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "INSERT INTO incremental(account_id) VALUES(?) ON CONFLICT DO NOTHING", e.opts.AccountID); err != nil {
		return errors.New("storage_unavailable")
	}
	var watermark, started string
	var active bool
	if err = tx.QueryRowContext(ctx, "SELECT watermark,active FROM incremental WHERE account_id=?", e.opts.AccountID).
		Scan(&watermark, &active); err != nil {
		return errors.New("storage_unavailable")
	}
	if active {
		_, err = tx.ExecContext(ctx, "UPDATE incremental SET error_code='' WHERE account_id=?", e.opts.AccountID)
	} else {
		if watermark == "" {
			if err = tx.QueryRowContext(ctx, "SELECT min(started_at) FROM rules WHERE account_id=? AND active=1", e.opts.AccountID).
				Scan(&started); err != nil {
				return errors.New("storage_unavailable")
			}
			watermark = started
		}
		t, parseErr := time.Parse(time.RFC3339Nano, watermark)
		if parseErr != nil {
			return errors.New("storage_unavailable")
		}
		_, err = tx.ExecContext(ctx, `UPDATE incremental SET active=1,since=?,started_at=?,cursor='',error_code='' WHERE account_id=?`,
			t.Add(-2*time.Minute).UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID)
		if err != nil {
			return errors.New("storage_unavailable")
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM incremental_seen WHERE account_id=?", e.opts.AccountID); err != nil {
			return errors.New("storage_unavailable")
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM incremental_tokens WHERE account_id=?", e.opts.AccountID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) incrementalPage(ctx context.Context) (result error) {
	if !e.opts.Library {
		return nil
	}
	var cursor, since string
	err := e.store.db.QueryRowContext(ctx, `SELECT cursor,since FROM incremental
		WHERE account_id=? AND active=1 AND error_code=''`, e.opts.AccountID).Scan(&cursor, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() {
		if result != nil {
			if _, err := e.store.db.ExecContext(context.WithoutCancel(ctx), "UPDATE incremental SET error_code=? WHERE account_id=?",
				errorCode(result), e.opts.AccountID); err != nil {
				result = errors.New("storage_unavailable")
			}
		}
	}()
	updated, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	p, err := readRetry(ctx, func() (bridge.Page, error) {
		return e.source.Assets(ctx, bridge.Scope{Library: true, UpdatedAfter: updated, IncludeTrashed: e.opts.Deletions}, cursor)
	})
	if err != nil {
		return err
	}
	if len(p.Assets) > 1000 || len(p.Next) > 4096 || (p.Next != "" && p.Next == cursor) ||
		(len(p.Assets) == 0 && (p.Next != "" || cursor != "")) {
		return errors.New(bridge.Protocol)
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range p.Assets {
		if a.ID == "" || a.Name == "" || a.Size < -1 {
			return errors.New(bridge.Protocol)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO incremental_seen(account_id,asset_id) VALUES(?,?)",
			e.opts.AccountID, a.ID); err != nil {
			return errors.New(bridge.Protocol)
		}
		metadata, err := json.Marshal(a)
		if err != nil {
			return errors.New(bridge.Protocol)
		}
		if err = e.discover(ctx, tx, a, string(metadata)); err != nil {
			return errors.New("storage_unavailable")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO signals(account_id,scope,asset_id,generation)
			SELECT account_id,scope,?,generation FROM rules WHERE account_id=? AND scope='library' AND active=1
			ON CONFLICT(account_id,scope,asset_id) DO UPDATE SET generation=excluded.generation`, a.ID, e.opts.AccountID); err != nil {
			return errors.New("storage_unavailable")
		}
		if _, err = tx.ExecContext(ctx, "UPDATE assets SET priority=max(priority,5) WHERE account_id=? AND id=?",
			e.opts.AccountID, a.ID); err != nil {
			return errors.New("storage_unavailable")
		}
	}
	if p.Next != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO incremental_tokens(account_id,token) VALUES(?,?)", e.opts.AccountID, p.Next); err != nil {
			return errors.New(bridge.Protocol)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE incremental SET cursor=?,active=?,
		watermark=CASE WHEN ? THEN started_at ELSE watermark END WHERE account_id=?`, p.Next, p.Next != "", p.Next == "", e.opts.AccountID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}
