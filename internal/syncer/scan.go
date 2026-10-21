package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type rule struct {
	scope      string
	album      string
	cursor     string
	generation int
	expected   int
}

func (e *Engine) configureRules(ctx context.Context) error {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := e.opts.AccountID
	if _, err = tx.ExecContext(ctx, "UPDATE rules SET active=0 WHERE account_id=?", id); err != nil {
		return err
	}
	albums := e.opts.AlbumIDs
	if e.opts.Library {
		albums = []string{""}
	}
	for _, album := range albums {
		scope := "album:" + album
		if e.opts.Library {
			scope = "library"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO rules(account_id,scope,album_id) VALUES(?,?,?)
			ON CONFLICT(account_id,scope) DO UPDATE SET active=1`, id, scope, album)
		if err != nil {
			return err
		}
	}
	var incomplete int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM rules WHERE account_id=? AND active=1 AND complete=0", id).Scan(&incomplete); err != nil {
		return err
	}
	if incomplete == 0 {
		_, err = tx.ExecContext(ctx, `UPDATE rules SET complete=0,generation=generation+1,cursor='',expected=-1,error_code=''
			WHERE account_id=? AND active=1`, id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM scan_cursors WHERE account_id=?", id)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (e *Engine) scanPage(ctx context.Context) (bool, error) {
	var r rule
	err := e.store.db.QueryRowContext(ctx, `SELECT scope,album_id,cursor,generation,expected FROM rules
		WHERE account_id=? AND active=1 AND complete=0 AND error_code='' ORDER BY scope LIMIT 1`,
		e.opts.AccountID).Scan(&r.scope, &r.album, &r.cursor, &r.generation, &r.expected)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.album != "" && r.expected == -1 {
		album, err := readRetry(ctx, func() (bridge.Album, error) { return e.source.Album(ctx, r.album) })
		if err != nil {
			return false, e.scanError(ctx, r, err)
		}
		if album.ID != r.album || album.Count < 0 {
			return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
		}
		r.expected = album.Count
	}
	p, err := readRetry(ctx, func() (bridge.Page, error) {
		return e.source.Assets(ctx, bridge.Scope{AlbumID: r.album, Library: r.album == ""}, r.cursor)
	})
	if err != nil {
		return false, e.scanError(ctx, r, err)
	}
	if len(p.Assets) > 1000 || len(p.Next) > 4096 || (len(p.Assets) == 0 && p.Next != "") ||
		(p.Next != "" && p.Next == r.cursor) {
		return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, a := range p.Assets {
		if a.ID == "" || a.Name == "" || a.Size < -1 {
			tx.Rollback()
			return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
		}
		var seen int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM scope_assets
			WHERE account_id=? AND scope=? AND asset_id=? AND generation=?`,
			e.opts.AccountID, r.scope, a.ID, r.generation).Scan(&seen)
		if err != nil {
			return false, err
		}
		if seen != 0 {
			tx.Rollback()
			return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
		}
		metadata, err := json.Marshal(a)
		if err != nil {
			return false, err
		}
		if err = e.discover(ctx, tx, a, string(metadata)); err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO scope_assets(account_id,scope,asset_id,generation) VALUES(?,?,?,?)
			ON CONFLICT(account_id,scope,asset_id) DO UPDATE SET generation=excluded.generation`,
			e.opts.AccountID, r.scope, a.ID, r.generation)
		if err != nil {
			return false, err
		}
	}
	if p.Next != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO scan_cursors(account_id,scope,token) VALUES(?,?,?)",
			e.opts.AccountID, r.scope, p.Next); err != nil {
			tx.Rollback()
			return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
		}
	} else if r.expected >= 0 {
		var total int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM scope_assets
			WHERE account_id=? AND scope=? AND generation=?`, e.opts.AccountID, r.scope, r.generation).Scan(&total)
		if err != nil {
			return false, err
		}
		if total != r.expected {
			tx.Rollback()
			return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE rules SET cursor=?,expected=?,complete=? WHERE account_id=? AND scope=?`,
		p.Next, r.expected, p.Next == "", e.opts.AccountID, r.scope)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (e *Engine) discover(ctx context.Context, tx *sql.Tx, a bridge.Asset, metadata string) error {
	var oldMetadata, digest, checksum string
	var size int64
	err := tx.QueryRowContext(ctx, "SELECT metadata,md5,sha1,size FROM assets WHERE account_id=? AND id=?",
		e.opts.AccountID, a.ID).Scan(&oldMetadata, &digest, &checksum, &size)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, "INSERT INTO assets(account_id,id,metadata) VALUES(?,?,?)", e.opts.AccountID, a.ID, metadata)
		return err
	}
	if err != nil {
		return err
	}
	var old bridge.Asset
	if err := json.Unmarshal([]byte(oldMetadata), &old); err != nil {
		return err
	}
	changed := digest != "" && ((a.SHA1 != "" && checksum != a.SHA1) ||
		(a.Size >= 0 && a.Size != size) ||
		(a.SHA1 == "" && !old.UpdatedAt.Equal(a.UpdatedAt)))
	if changed {
		// Preserve the original metadata and confirmed/uncertain content evidence.
		_, err = tx.ExecContext(ctx, "UPDATE assets SET error_code='source_changed' WHERE account_id=? AND id=?",
			e.opts.AccountID, a.ID)
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE assets SET metadata=?,
		state=CASE WHEN state='skipped' OR (md5='' AND metadata!=?) THEN 'pending' ELSE state END,
		error_code=CASE WHEN state='skipped' OR error_code='source_changed' OR (md5='' AND metadata!=?) THEN '' ELSE error_code END,
		attempts=CASE WHEN state='skipped' OR (md5='' AND metadata!=?) THEN 0 ELSE attempts END
		WHERE account_id=? AND id=?`, metadata, metadata, metadata, metadata, e.opts.AccountID, a.ID)
	return err
}

func (e *Engine) scanError(ctx context.Context, r rule, err error) error {
	code := errorCode(err)
	_, dbErr := e.store.db.ExecContext(ctx, "UPDATE rules SET error_code=? WHERE account_id=? AND scope=?",
		code, e.opts.AccountID, r.scope)
	if dbErr != nil {
		return dbErr
	}
	return errors.New(code)
}

func (e *Engine) finishScan(ctx context.Context) error {
	var unfinished, outstanding int
	err := e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM rules WHERE account_id=? AND active=1 AND complete=0`,
		e.opts.AccountID).Scan(&unfinished)
	if err != nil {
		return err
	}
	err = e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM assets a WHERE account_id=?
		AND (state NOT IN ('confirmed','skipped') OR error_code='source_changed') AND EXISTS(SELECT 1 FROM scope_assets m JOIN rules r
		ON r.account_id=m.account_id AND r.scope=m.scope WHERE m.account_id=a.account_id AND m.asset_id=a.id AND r.active=1 AND m.generation=r.generation)`,
		e.opts.AccountID).Scan(&outstanding)
	if err != nil || unfinished != 0 || outstanding != 0 {
		return err
	}
	_, err = e.store.db.ExecContext(ctx, "UPDATE accounts SET last_success=?,error_code='' WHERE id=?",
		time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID)
	return err
}
