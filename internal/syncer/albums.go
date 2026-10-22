package syncer

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type albumJob struct {
	scope, target, name, applied, state string
	generation, baseline, attempts      int
	complete                            bool
}

func (e *Engine) mirror(ctx context.Context) error {
	if !e.mirrorsAlbums() {
		return nil
	}
	_, err := e.store.db.ExecContext(ctx, `INSERT INTO albums(account_id,scope,name)
		SELECT account_id,scope,name FROM rules r WHERE account_id=? AND active=1 AND album_id!='' AND error_code=''
		ON CONFLICT(account_id,scope) DO UPDATE SET name=excluded.name`, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	rows, err := e.store.db.QueryContext(ctx, `SELECT a.scope,target_id,a.name,applied_name,state,r.generation,baseline,a.attempts,r.complete
		FROM albums a JOIN rules r ON r.account_id=a.account_id AND r.scope=a.scope
		WHERE a.account_id=? AND r.active=1 AND r.error_code=''
		AND `+e.attemptLimit("a.")+` AND a.next_at<=?`, e.opts.AccountID, e.opts.Now().UnixMilli())
	if err != nil {
		return errors.New("storage_unavailable")
	}
	var jobs []albumJob
	for rows.Next() {
		var j albumJob
		if err = rows.Scan(&j.scope, &j.target, &j.name, &j.applied, &j.state, &j.generation, &j.baseline, &j.attempts, &j.complete); err != nil {
			break
		}
		jobs = append(jobs, j)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return errors.New("storage_unavailable")
	}
	var result error
	for _, j := range jobs {
		if err := e.mirrorAlbum(ctx, &j); err != nil {
			code := errorCode(err)
			j.attempts++
			_, dbErr := e.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE albums SET state=?,error_code=?,attempts=?,next_at=?
				WHERE account_id=? AND scope=?`, j.state, code, j.attempts,
				e.opts.Now().Add(e.retryDelay(j.attempts-1, err)).UnixMilli(), e.opts.AccountID, j.scope)
			if dbErr != nil {
				return errors.New("storage_unavailable")
			}
			result = e.accountError(ctx, err)
			if code == bridge.Authentication || code == bridge.Permission || ctx.Err() != nil {
				return result
			}
		}
	}
	return result
}

func explicitRejection(err error) bool {
	var remote *bridge.Error
	return errors.As(err, &remote) && (remote.Code == bridge.Authentication ||
		remote.Code == bridge.Permission || remote.Code == bridge.RateLimit)
}

func (e *Engine) mirrorAlbum(ctx context.Context, j *albumJob) error {
	if j.state == "creating" || j.state == "unconfirmed" {
		j.state = "unconfirmed"
		return errors.New("album_unconfirmed")
	}
	if j.target == "" {
		if _, err := e.store.db.ExecContext(ctx, "UPDATE albums SET state='creating' WHERE account_id=? AND scope=?",
			e.opts.AccountID, j.scope); err != nil {
			return errors.New("storage_unavailable")
		}
		j.state = "unconfirmed"
		id, err := e.target.CreateAlbum(ctx, j.name)
		if err != nil {
			if explicitRejection(err) {
				j.state = "pending"
				return err
			}
			return errors.New("album_unconfirmed")
		}
		if id == "" {
			return errors.New("album_unconfirmed")
		}
		// Only the ID returned from our own creation can establish ownership.
		if _, err = e.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE albums SET target_id=?,applied_name=?,state='confirmed'
			WHERE account_id=? AND scope=?`, id, j.name, e.opts.AccountID, j.scope); err != nil {
			return errors.New("storage_unavailable")
		}
		j.target, j.applied, j.state = id, j.name, "confirmed"
	}
	if j.name != j.applied {
		if err := e.target.RenameAlbum(ctx, j.target, j.name); err != nil {
			return err
		}
		if _, err := e.store.db.ExecContext(ctx, "UPDATE albums SET applied_name=? WHERE account_id=? AND scope=?",
			j.name, e.opts.AccountID, j.scope); err != nil {
			return errors.New("storage_unavailable")
		}
	}
	var outstanding int
	err := e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM scope_assets m JOIN assets a ON a.account_id=m.account_id AND a.id=m.asset_id
		WHERE m.account_id=? AND m.scope=? AND m.generation=? AND (a.state NOT IN ('confirmed','existing','skipped','deleted') OR a.error_code='source_changed')`,
		e.opts.AccountID, j.scope, j.generation).Scan(&outstanding)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	j.complete = j.complete && outstanding == 0
	desired, err := e.desiredMembers(ctx, j)
	if err != nil {
		return err
	}
	actual, err := readRetry(ctx, func() ([]string, error) { return e.target.AlbumMembers(ctx, j.target) })
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range actual {
		if id == "" || seen[id] {
			return errors.New(bridge.Protocol)
		}
		seen[id] = true
	}
	if err := e.planMembers(ctx, j, desired, seen); err != nil {
		return err
	}
	if err := e.applyMembers(ctx, j, seen); err != nil {
		return err
	}
	if !j.complete {
		return nil
	}
	_, err = e.store.db.ExecContext(ctx, `UPDATE albums SET baseline=?,complete_generation=?,attempts=0,next_at=0,error_code=''
		WHERE account_id=? AND scope=?`, len(desired), j.generation, e.opts.AccountID, j.scope)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) desiredMembers(ctx context.Context, j *albumJob) ([]string, error) {
	rows, err := e.store.db.QueryContext(ctx, `SELECT DISTINCT a.node_id FROM (
		SELECT account_id,scope,asset_id,generation FROM scope_assets
		UNION SELECT account_id,scope,asset_id,generation FROM signals) m JOIN assets a
		ON a.account_id=m.account_id AND a.id=m.asset_id WHERE m.account_id=? AND m.scope=? AND m.generation=?
		AND a.state IN ('confirmed','existing') AND a.error_code='' ORDER BY a.node_id`, e.opts.AccountID, j.scope, j.generation)
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	return ids, nil
}

func (e *Engine) planMembers(ctx context.Context, j *albumJob, desired []string, actual map[string]bool) error {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "SELECT node_id FROM memberships WHERE account_id=? AND scope=? AND owned=1",
		e.opts.AccountID, j.scope)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	var removed []string
	planned := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		if j.complete && !slices.Contains(desired, id) {
			removed = append(removed, id)
			if actual[id] {
				planned++
			}
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return errors.New("storage_unavailable")
	}
	// Empty scans never remove members; small albums permit one removal, larger ones at most 10% or 500.
	if planned > 0 && (j.baseline == 0 || len(desired) == 0 ||
		planned > min(500, max(1, j.baseline/10))) {
		return errors.New("membership_guard")
	}
	for _, id := range removed {
		if _, err = tx.ExecContext(ctx, `UPDATE memberships SET state=CASE WHEN state='unconfirmed_remove' THEN state ELSE 'remove' END
			WHERE account_id=? AND scope=? AND node_id=?`, e.opts.AccountID, j.scope, id); err != nil {
			return errors.New("storage_unavailable")
		}
	}
	for _, id := range desired {
		state := "add"
		if actual[id] {
			state = "present"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO memberships(account_id,scope,node_id,state) VALUES(?,?,?,?)
			ON CONFLICT(account_id,scope,node_id) DO UPDATE SET state=CASE
			WHEN memberships.state IN ('remove','unconfirmed_remove','absent') THEN excluded.state ELSE memberships.state END`,
			e.opts.AccountID, j.scope, id, state)
		if err != nil {
			return errors.New("storage_unavailable")
		}
	}
	if err = tx.Commit(); err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) applyMembers(ctx context.Context, j *albumJob, actual map[string]bool) error {
	rows, err := e.store.db.QueryContext(ctx, `SELECT node_id,state,owned FROM memberships
		WHERE account_id=? AND scope=? ORDER BY node_id`, e.opts.AccountID, j.scope)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	type member struct {
		id, state string
		owned     bool
	}
	var members []member
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.id, &m.state, &m.owned); err != nil {
			break
		}
		members = append(members, m)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return errors.New("storage_unavailable")
	}
	for _, m := range members {
		remove := m.state == "remove" || m.state == "unconfirmed_remove" || m.state == "absent"
		if remove && !j.complete {
			continue
		}
		if remove && !m.owned {
			continue
		}
		state := "present"
		if remove {
			state = "absent"
		}
		if actual[m.id] == remove {
			intent := "unconfirmed_add"
			if remove {
				intent = "unconfirmed_remove"
			}
			if _, err = e.store.db.ExecContext(ctx, `UPDATE memberships SET state=?,owned=1 WHERE account_id=? AND scope=? AND node_id=?`,
				intent, e.opts.AccountID, j.scope, m.id); err != nil {
				return errors.New("storage_unavailable")
			}
			var mutationErr error
			if remove {
				mutationErr = e.target.RemoveMembers(ctx, j.target, []string{m.id})
			} else {
				mutationErr = e.target.AddMembers(ctx, j.target, []string{m.id})
			}
			if mutationErr != nil {
				_, dbErr := e.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE memberships SET error_code=?
					WHERE account_id=? AND scope=? AND node_id=?`, errorCode(mutationErr), e.opts.AccountID, j.scope, m.id)
				if dbErr != nil {
					return errors.New("storage_unavailable")
				}
				if mutationErrCode := errorCode(mutationErr); mutationErrCode == bridge.Authentication ||
					mutationErrCode == bridge.Permission {
					return mutationErr
				}
			}
			if settleErr := e.membershipSettled(ctx, j.target, m.id, !remove); settleErr != nil {
				if mutationErr != nil {
					return mutationErr
				}
				_, dbErr := e.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE memberships SET error_code=?
					WHERE account_id=? AND scope=? AND node_id=?`, errorCode(settleErr), e.opts.AccountID, j.scope, m.id)
				if dbErr != nil {
					return errors.New("storage_unavailable")
				}
				return settleErr
			}
		}
		if _, err = e.store.db.ExecContext(ctx, `UPDATE memberships SET state=?,error_code=''
			WHERE account_id=? AND scope=? AND node_id=?`, state, e.opts.AccountID, j.scope, m.id); err != nil {
			return errors.New("storage_unavailable")
		}
	}
	return nil
}

// Album membership reads can lag the accepted mutation. Verify with a bounded
// poll so one accepted member does not take one whole sync run to settle.
func (e *Engine) membershipSettled(ctx context.Context, album, node string, want bool) error {
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		members, err := e.target.AlbumMembers(ctx, album)
		if err == nil && slices.Contains(members, node) == want {
			return nil
		}
		last = err
		if err != nil {
			code := errorCode(err)
			if code != bridge.Unconfirmed && code != bridge.Unavailable && code != bridge.RateLimit {
				return err
			}
		}
		if attempt == 4 {
			break
		}
		delay := 100 * time.Millisecond << attempt
		var remote *bridge.Error
		if errors.As(err, &remote) && remote.RetryAfter > delay {
			delay = remote.RetryAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if last != nil {
		return last
	}
	return errors.New(bridge.Unconfirmed)
}
