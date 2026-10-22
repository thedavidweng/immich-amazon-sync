package syncer

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// discoverAlbums reads the complete accessible album listing and reconciles
// per-album rules against it. A failed read changes nothing: an unavailable
// source is never evidence that an album disappeared. A successful read that
// no longer contains an album deactivates its mirroring but preserves the
// owned destination album, node mappings and memberships; removal from the
// listing is not deletion evidence and authorizes no membership change.
//
// Discovery happens before configureRules, which then activates only the
// albums that are present. Rules are keyed by the stable source UUID, so a
// rename updates the label without changing identity.
func (e *Engine) discoverAlbums(ctx context.Context) ([]bridge.Album, error) {
	albums, err := readRetry(ctx, func() ([]bridge.Album, error) { return e.source.Albums(ctx) })
	if err != nil {
		return nil, err
	}
	if albums == nil {
		return nil, errors.New(bridge.Protocol)
	}
	seen := make(map[string]bool, len(albums))
	for _, album := range albums {
		if album.ID == "" || album.Count < 0 || seen[album.ID] {
			return nil, errors.New(bridge.Protocol)
		}
		seen[album.ID] = true
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT album_id FROM rules
		WHERE account_id=? AND origin='discovered'`, e.opts.AccountID)
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	var missing []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, errors.New("storage_unavailable")
	}
	for _, id := range missing {
		if _, err = tx.ExecContext(ctx, `UPDATE rules SET active=0,discovered=0,error_code='album_inaccessible'
			WHERE account_id=? AND scope=?`, e.opts.AccountID, "album:"+id); err != nil {
			return nil, errors.New("storage_unavailable")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, album := range albums {
		if err := e.discoverAlbum(ctx, tx, album, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, errors.New("storage_unavailable")
	}
	return albums, nil
}

func (e *Engine) discoverAlbum(ctx context.Context, tx *sql.Tx, album bridge.Album, now string) error {
	scope := "album:" + album.ID
	expected := album.Count
	if e.opts.Deletions {
		// An Immich album count excludes trashed assets, while a scan that
		// includes them returns both. Do not pin a count that cannot be met.
		expected = -1
	}
	var priorExpected, generation int
	var complete bool
	err := tx.QueryRowContext(ctx, `SELECT expected,generation,complete FROM rules
		WHERE account_id=? AND scope=?`, e.opts.AccountID, scope).
		Scan(&priorExpected, &generation, &complete)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.New("storage_unavailable")
	}
	if err == nil && !complete && priorExpected >= 0 && priorExpected != album.Count {
		// The album grew or shrank between cycles. Restart the generation so
		// the next complete scan is authoritative for membership.
		if _, err = tx.ExecContext(ctx, `UPDATE rules SET generation=generation+1,cursor='',started_at=?
			WHERE account_id=? AND scope=?`, now, e.opts.AccountID, scope); err != nil {
			return errors.New("storage_unavailable")
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM scan_cursors WHERE account_id=? AND scope=?",
			e.opts.AccountID, scope); err != nil {
			return errors.New("storage_unavailable")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO rules(account_id,scope,album_id,origin,discovered,discovered_at,name,expected,started_at)
		VALUES(?,?,?,'discovered',1,?,?,?,?)
		ON CONFLICT(account_id,scope) DO UPDATE SET origin='discovered',discovered=1,
		discovered_at=excluded.discovered_at,name=excluded.name,expected=excluded.expected,
		error_code=CASE WHEN rules.error_code='album_inaccessible' THEN '' ELSE rules.error_code END`,
		e.opts.AccountID, scope, album.ID, now, album.Name, expected, now)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}
