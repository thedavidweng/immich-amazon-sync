package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

func eventID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// Enqueue persists a hint without reading credentials or calling either remote.
func (s *Store) Enqueue(ctx context.Context, accountID, assetID string, priority int) error {
	if !eventID(accountID) || !eventID(assetID) || priority < 0 || priority > 100 {
		return errors.New("invalid_event")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "INSERT INTO accounts(id) VALUES(?) ON CONFLICT DO NOTHING", accountID); err != nil {
		return errors.New("storage_unavailable")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hints(account_id,asset_id,priority) VALUES(?,?,?)
		ON CONFLICT(account_id,asset_id) DO UPDATE SET priority=max(priority,excluded.priority),
		version=version+1,attempts=0,next_at=0,error_code=''`, accountID, assetID, priority)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) Enqueue(ctx context.Context, assetID string, priority int) error {
	return e.store.Enqueue(ctx, e.opts.AccountID, assetID, priority)
}

func (e *Engine) processHints(ctx context.Context, limit int) error {
	for range limit {
		var id string
		var priority, version, attempts int
		err := e.store.db.QueryRowContext(ctx, `SELECT asset_id,priority,version,attempts FROM hints WHERE account_id=?
			AND `+e.attemptLimit("")+` AND next_at<=? ORDER BY priority DESC,asset_id LIMIT 1`,
			e.opts.AccountID, e.opts.Now().UnixMilli()).Scan(&id, &priority, &version, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return errors.New("storage_unavailable")
		}
		if err := e.resolveHint(ctx, id, priority, version); err != nil {
			_, dbErr := e.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE hints SET attempts=attempts+1,
				error_code=?,next_at=? WHERE account_id=? AND asset_id=? AND version=?`,
				errorCode(err), e.opts.Now().Add(e.retryDelay(attempts, err)).UnixMilli(), e.opts.AccountID, id, version)
			if dbErr != nil {
				return errors.New("storage_unavailable")
			}
			return e.accountError(ctx, err)
		}
	}
	return nil
}

func (e *Engine) resolveHint(ctx context.Context, id string, priority, version int) error {
	type found struct {
		scope string
		asset bridge.Asset
	}
	var matches []found
	scopes := e.opts.AlbumIDs
	if e.opts.Library {
		scopes = []string{""}
	}
	for _, album := range scopes {
		var assets []bridge.Asset
		scope := "album:" + album
		if e.opts.Library {
			scope = "library"
			a, err := readRetry(ctx, func() (bridge.Asset, error) { return e.source.Asset(ctx, id) })
			if err != nil {
				if errorCode(err) == bridge.NotFound {
					continue
				}
				return err
			}
			assets = []bridge.Asset{a}
		} else {
			p, err := readRetry(ctx, func() (bridge.Page, error) {
				return e.source.Assets(ctx, bridge.Scope{AlbumID: album, AssetID: id, IncludeTrashed: e.opts.Deletions}, "")
			})
			if err != nil {
				return err
			}
			if p.Next != "" || len(p.Assets) > 1 {
				return errors.New(bridge.Protocol)
			}
			assets = p.Assets
		}
		for _, a := range assets {
			if a.ID != id || a.Name == "" || a.Size < -1 {
				return errors.New(bridge.Protocol)
			}
			matches = append(matches, found{scope, a})
		}
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	for _, f := range matches {
		metadata, err := json.Marshal(f.asset)
		if err != nil {
			return errors.New(bridge.Protocol)
		}
		if err = e.discover(ctx, tx, f.asset, string(metadata)); err != nil {
			return errors.New("storage_unavailable")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO signals(account_id,scope,asset_id,generation)
			SELECT account_id,scope,?,generation FROM rules WHERE account_id=? AND scope=? AND active=1
			ON CONFLICT(account_id,scope,asset_id) DO UPDATE SET generation=excluded.generation`,
			id, e.opts.AccountID, f.scope)
		if err != nil {
			return errors.New("storage_unavailable")
		}
		if _, err = tx.ExecContext(ctx, "UPDATE assets SET priority=max(priority,?) WHERE account_id=? AND id=?",
			priority, e.opts.AccountID, id); err != nil {
			return errors.New("storage_unavailable")
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM hints WHERE account_id=? AND asset_id=? AND version=?",
		e.opts.AccountID, id, version); err != nil {
		return errors.New("storage_unavailable")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}
