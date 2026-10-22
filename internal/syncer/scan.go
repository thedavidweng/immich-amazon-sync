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
	name       string
}

func (e *Engine) validateScopes(ctx context.Context) error {
	for _, id := range e.opts.AlbumIDs {
		scope := "album:" + id
		a, err := readRetry(ctx, func() (bridge.Album, error) { return e.source.Album(ctx, id) })
		if err == nil && (a.ID != id || a.Count < 0) {
			err = errors.New(bridge.Protocol)
		}
		if err != nil {
			return e.scanError(ctx, rule{scope: scope}, err)
		}
		tx, err := e.store.db.BeginTx(ctx, nil)
		if err != nil {
			return errors.New("storage_unavailable")
		}
		var expected int
		var complete bool
		err = tx.QueryRowContext(ctx, "SELECT expected,complete FROM rules WHERE account_id=? AND scope=?",
			e.opts.AccountID, scope).Scan(&expected, &complete)
		if err == nil && !complete && expected >= 0 && expected != a.Count {
			_, err = tx.ExecContext(ctx, `UPDATE rules SET generation=generation+1,cursor='',started_at=?
				WHERE account_id=? AND scope=?`, time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID, scope)
			if err == nil {
				_, err = tx.ExecContext(ctx, "DELETE FROM scan_cursors WHERE account_id=? AND scope=?", e.opts.AccountID, scope)
			}
		}
		if err == nil {
			// Immich album counts exclude trashed assets, but a scan that
			// includes them returns both. Do not pin an expected count that
			// cannot be met while deletion propagation is enabled.
			expected := a.Count
			if e.opts.Deletions {
				expected = -1
			}
			_, err = tx.ExecContext(ctx, `UPDATE rules SET name=?,expected=? WHERE account_id=? AND scope=?`,
				a.Name, expected, e.opts.AccountID, scope)
		}
		if err == nil {
			err = tx.Commit()
		}
		_ = tx.Rollback()
		if err != nil {
			return errors.New("storage_unavailable")
		}
	}
	return nil
}

// configureRules activates exactly the scopes implied by the current mode.
// discovered reports whether this cycle read the complete accessible album
// listing. A bounded continuation keeps previously discovered rules active
// instead of stopping an in-flight album scan.
func (e *Engine) configureRules(ctx context.Context, restart bool) error {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id := e.opts.AccountID
	if !e.opts.Library {
		if _, err = tx.ExecContext(ctx, "UPDATE incremental SET active=0,error_code='' WHERE account_id=?", id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE rules SET active=0 WHERE account_id=?", id); err != nil {
		return err
	}
	if e.opts.Library {
		if _, err = tx.ExecContext(ctx, `INSERT INTO rules(account_id,scope,album_id) VALUES(?,'library','')
			ON CONFLICT(account_id,scope) DO UPDATE SET active=1`, id); err != nil {
			return err
		}
	}
	for _, album := range e.opts.AlbumIDs {
		// An album that comes back into reach is retried instead of keeping a
		// stale inaccessibility error; the explicit scope is never widened.
		if _, err = tx.ExecContext(ctx, `INSERT INTO rules(account_id,scope,album_id,discovered) VALUES(?,?,?,1)
			ON CONFLICT(account_id,scope) DO UPDATE SET active=1,discovered=1,
			error_code=CASE WHEN rules.error_code='album_inaccessible' THEN '' ELSE rules.error_code END`,
			id, "album:"+album, album); err != nil {
			return err
		}
	}
	if e.opts.AlbumSyncMode == bridge.AlbumSyncAllAccessible {
		if _, err = tx.ExecContext(ctx, `UPDATE rules SET active=1
			WHERE account_id=? AND origin='discovered' AND discovered=1`, id); err != nil {
			return err
		}
	}
	var incomplete int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM rules WHERE account_id=? AND active=1 AND complete=0", id).Scan(&incomplete); err != nil {
		return err
	}
	if incomplete == 0 && restart {
		_, err = tx.ExecContext(ctx, `UPDATE rules SET complete=0,generation=generation+1,cursor='',expected=-1,error_code=''
			WHERE account_id=? AND active=1`, id)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE rules SET started_at=? WHERE account_id=? AND active=1",
			time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM scan_cursors WHERE account_id=?", id)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM signals WHERE account_id=?", id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE rules SET started_at=? WHERE account_id=? AND active=1 AND started_at=''",
		time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *Engine) scanPage(ctx context.Context) (bool, error) {
	var r rule
	err := e.store.db.QueryRowContext(ctx, `SELECT scope,album_id,cursor,generation,expected,name FROM rules
		WHERE account_id=? AND active=1 AND complete=0 AND error_code='' ORDER BY scope LIMIT 1`,
		e.opts.AccountID).Scan(&r.scope, &r.album, &r.cursor, &r.generation, &r.expected, &r.name)
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
		if !e.opts.Deletions {
			r.expected = album.Count
		}
		r.name = album.Name
	}
	p, err := readRetry(ctx, func() (bridge.Page, error) {
		return e.source.Assets(ctx, bridge.Scope{
			AlbumID: r.album, Library: r.album == "", IncludeTrashed: e.opts.Deletions,
		}, r.cursor)
	})
	if err != nil {
		return false, e.scanError(ctx, r, err)
	}
	if len(p.Assets) > 1000 || len(p.Next) > 4096 || (len(p.Assets) == 0 && (p.Next != "" || r.cursor != "")) ||
		(p.Next != "" && p.Next == r.cursor) {
		return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range p.Assets {
		if a.ID == "" || a.Name == "" || a.Size < -1 {
			_ = tx.Rollback()
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
			_ = tx.Rollback()
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
			_ = tx.Rollback()
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
			_ = tx.Rollback()
			return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
		}
	} else if r.album == "" && p.Next == "" && len(p.Assets) == 0 && r.cursor == "" {
		var prior int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM scope_assets WHERE account_id=? AND scope=?",
			e.opts.AccountID, r.scope).Scan(&prior); err != nil {
			return false, err
		}
		if prior > 0 {
			_ = tx.Rollback()
			return false, e.scanError(ctx, r, errors.New(bridge.Protocol))
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE rules SET cursor=?,expected=?,complete=?,name=?,
		completed_at=CASE WHEN ? THEN ? ELSE completed_at END WHERE account_id=? AND scope=?`,
		p.Next, r.expected, p.Next == "", r.name, p.Next == "", time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID, r.scope)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (e *Engine) discover(ctx context.Context, tx *sql.Tx, a bridge.Asset, metadata string) error {
	var oldMetadata, digest, checksum, node, state, prior, code, stage string
	var size int64
	var attempts int
	favorite := -1
	if a.Favorite != nil {
		favorite = 0
		if *a.Favorite {
			favorite = 1
		}
	}
	err := tx.QueryRowContext(ctx, "SELECT metadata,md5,sha1,size,state,node_id,prior_state,error_code,attempts,stage_name FROM assets WHERE account_id=? AND id=?",
		e.opts.AccountID, a.ID).Scan(&oldMetadata, &digest, &checksum, &size, &state, &node, &prior, &code, &attempts, &stage)
	if errors.Is(err, sql.ErrNoRows) {
		exclusion := e.exclusion(a)
		target := "pending"
		if exclusion != "" {
			target = "skipped"
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO assets(account_id,id,metadata,state,exclusion,favorite) VALUES(?,?,?,?,?,?)",
			e.opts.AccountID, a.ID, metadata, target, exclusion, favorite)
		return err
	}
	if err != nil {
		return err
	}
	if e.opts.Deletions {
		trashed := a.ExcludedReason == "trashed"
		switch {
		case trashed && node != "" && (state == "confirmed" || state == "existing"):
			// Record a durable intent; the sweep re-verifies the remote node
			// before moving it to trash.
			_, err = tx.ExecContext(ctx, `UPDATE assets SET prior_state=?,state='delete_pending',error_code='',
				attempts=0,next_at=0 WHERE account_id=? AND id=?`, state, e.opts.AccountID, a.ID)
			if err != nil {
				return err
			}
			prior, state = state, "delete_pending"
		case !trashed && state == "delete_pending":
			// The source was restored before the sweep ran; withdraw the intent
			// instead of trashing a live asset.
			_, err = tx.ExecContext(ctx, `UPDATE assets SET state=?,prior_state='',error_code='',
				attempts=0,next_at=0 WHERE account_id=? AND id=?`, prior, e.opts.AccountID, a.ID)
			if err != nil {
				return err
			}
			state = prior
		}
	}
	var old bridge.Asset
	if err := json.Unmarshal([]byte(oldMetadata), &old); err != nil {
		return err
	}
	changed := digest != "" && ((a.SHA1 != "" && checksum != a.SHA1) ||
		(a.Size >= 0 && a.Size != size) ||
		(a.SHA1 == "" && !old.FileModifiedAt.Equal(a.FileModifiedAt)))
	safeSkip := a.ExcludedReason != "" && state != "confirmed" && state != "existing" &&
		state != "running" && state != "unconfirmed" && state != "delete_pending" && state != "deleted"
	if changed && !safeSkip {
		// Preserve the original metadata and confirmed/uncertain content evidence.
		_, err = tx.ExecContext(ctx, "UPDATE assets SET error_code='source_changed' WHERE account_id=? AND id=?",
			e.opts.AccountID, a.ID)
		return err
	}
	// Filters only manage unsettled work. A confirmed or Existing mapping is
	// never downgraded, so a policy change cannot remove media or Membership.
	protected := state == "confirmed" || state == "existing" || state == "running" ||
		state == "unconfirmed" || state == "delete_pending" || state == "deleted"
	exclusion, target := "", state
	metadataChanged := false
	if !protected {
		exclusion = e.exclusion(a)
		metadataChanged = metadata != oldMetadata && digest == ""
		switch {
		case exclusion != "":
			// A staged original is released by transfer's skip path, which owns
			// the staging file. Marking it skipped here would orphan that file.
			if stage == "" {
				target = "skipped"
			}
		case state == "skipped" || metadataChanged:
			target = "pending"
		}
	}
	reset := !protected && (exclusion != "" || state == "skipped" || metadataChanged)
	if reset {
		code, attempts = "", 0
	}
	_, err = tx.ExecContext(ctx, `UPDATE assets SET metadata=?,favorite=?,state=?,exclusion=?,error_code=?,attempts=?,
		next_at=CASE WHEN ? THEN 0 ELSE next_at END WHERE account_id=? AND id=?`,
		metadata, favorite, target, exclusion, code, attempts, reset, e.opts.AccountID, a.ID)
	return err
}

// exclusion classifies an Asset for durable, non-destructive exclusion. Source
// conditions win over policy rules so counts name the actual reason.
func (e *Engine) exclusion(a bridge.Asset) string {
	if a.ExcludedReason != "" {
		return sourceExclusion(a.ExcludedReason)
	}
	return e.opts.Filters.Exclude(a)
}

func sourceExclusion(reason string) string {
	switch reason {
	case "hidden", "locked", "offline", "trashed", "unsupported_type",
		"external_library_checksum", "unknown_checksum_provenance":
		return reason
	}
	return "source_excluded"
}

func (e *Engine) scanError(ctx context.Context, r rule, err error) error {
	code := errorCode(err)
	_, dbErr := e.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE rules SET error_code=?,attempts=attempts+1,
		next_at=? WHERE account_id=? AND scope=?`, code,
		e.opts.Now().Add(retryDelay(4, err)).UnixMilli(), e.opts.AccountID, r.scope)
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
		AND (state NOT IN ('confirmed','existing','skipped','deleted') OR error_code='source_changed') AND `+eligibleSQL,
		e.opts.AccountID).Scan(&outstanding)
	if err != nil || unfinished != 0 || outstanding != 0 {
		return err
	}
	if err = e.store.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM hints WHERE account_id=?)+
		(SELECT count(*) FROM incremental WHERE account_id=? AND (active=1 OR error_code!=''))`,
		e.opts.AccountID, e.opts.AccountID).Scan(&outstanding); err != nil || outstanding != 0 {
		return err
	}
	if e.mirrorsAlbums() {
		err = e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM rules r LEFT JOIN albums a
			ON a.account_id=r.account_id AND a.scope=r.scope WHERE r.account_id=? AND r.active=1 AND r.album_id!=''
			AND r.error_code=''
			AND (a.target_id IS NULL OR a.target_id='' OR a.complete_generation!=r.generation OR a.error_code!='')`,
			e.opts.AccountID).Scan(&outstanding)
		if err != nil || outstanding != 0 {
			return err
		}
	}
	if e.favoritesOn() {
		err = e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM favorites
			WHERE account_id=? AND state IN ('pending','failed','uncertain') AND `+favoriteEligibleSQL,
			e.opts.AccountID).Scan(&outstanding)
		if err != nil || outstanding != 0 {
			return err
		}
	}
	_, err = e.store.db.ExecContext(ctx, "UPDATE accounts SET last_success=?,error_code='' WHERE id=?",
		time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID)
	return err
}
