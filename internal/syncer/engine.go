package syncer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type Options struct {
	AccountID       string
	AlbumIDs        []string
	Library         bool
	IncludeVideos   bool
	MirrorAlbums    bool
	Workers         int
	MaxStagingBytes int64
	MaxStagingFiles int
	BytesPerSecond  int64
}

type Status struct {
	Pending     int            `json:"pending"`
	Staged      int            `json:"staged"`
	Running     int            `json:"running"`
	Unconfirmed int            `json:"unconfirmed"`
	Confirmed   int            `json:"confirmed"`
	Failed      int            `json:"failed"`
	Blocked     int            `json:"blocked"`
	Skipped     int            `json:"skipped"`
	Scanning    int            `json:"scanning"`
	Paused      bool           `json:"paused"`
	PauseReason string         `json:"pause_reason,omitempty"`
	Errors      map[string]int `json:"errors"`
	LastSuccess *time.Time     `json:"last_success"`
}

type Engine struct {
	store     *Store
	source    bridge.Source
	target    bridge.Target
	opts      Options
	rateMu    sync.Mutex
	rateAt    time.Time
	contentMu sync.Mutex
	contents  map[string]*contentLock
}

func New(store *Store, source bridge.Source, target bridge.Target, opts Options) (*Engine, error) {
	if store == nil || source == nil || target == nil || opts.AccountID == "" ||
		(!opts.Library && len(opts.AlbumIDs) == 0) || (opts.Library && len(opts.AlbumIDs) != 0) ||
		opts.Workers < 0 || opts.Workers > 32 || opts.MaxStagingBytes < 0 ||
		opts.MaxStagingFiles < 0 || opts.BytesPerSecond < 0 {
		return nil, errors.New("invalid_options")
	}
	for _, id := range opts.AlbumIDs {
		if id == "" {
			return nil, errors.New("invalid_options")
		}
	}
	if opts.Workers == 0 {
		opts.Workers = 2
	}
	if opts.MaxStagingBytes == 0 {
		opts.MaxStagingBytes = 10 << 30
	}
	if opts.MaxStagingFiles == 0 {
		opts.MaxStagingFiles = 2
	}
	opts.AlbumIDs = append([]string(nil), opts.AlbumIDs...)
	if _, err := store.db.Exec("INSERT INTO accounts(id) VALUES(?) ON CONFLICT DO NOTHING", opts.AccountID); err != nil {
		return nil, errors.New("storage_unavailable")
	}
	return &Engine{store: store, source: source, target: target, opts: opts,
		contents: make(map[string]*contentLock)}, nil
}

func (e *Engine) Pause(ctx context.Context, paused bool) error {
	reason := ""
	if paused {
		reason = "manual"
	}
	_, err := e.store.db.ExecContext(ctx, "UPDATE accounts SET pause_reason=? WHERE id=?", reason, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) Retry(ctx context.Context) error {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE assets SET state=CASE WHEN state='unconfirmed' THEN state
		WHEN stage_name!='' THEN 'staged' ELSE 'pending' END,attempts=0,next_at=0,
		error_code=CASE WHEN state='unconfirmed' THEN error_code ELSE '' END
		WHERE account_id=? AND state IN ('failed','blocked','unconfirmed') AND error_code!='source_changed'`, e.opts.AccountID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET pause_reason=CASE WHEN pause_reason='auth' THEN '' ELSE pause_reason END,
			error_code='' WHERE id=?`, e.opts.AccountID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) Status(ctx context.Context) (Status, error) {
	st := Status{Errors: map[string]int{}}
	var reason, code string
	var last *string
	err := e.store.db.QueryRowContext(ctx, "SELECT pause_reason,last_success,error_code FROM accounts WHERE id=?",
		e.opts.AccountID).Scan(&reason, &last, &code)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	st.Paused = reason != ""
	st.PauseReason = reason
	if last != nil {
		t, err := time.Parse(time.RFC3339Nano, *last)
		if err != nil {
			return st, errors.New("storage_unavailable")
		}
		st.LastSuccess = &t
	}
	if code != "" {
		st.Errors[code]++
	}
	rows, err := e.store.db.QueryContext(ctx, "SELECT state,error_code,count(*) FROM assets WHERE account_id=? GROUP BY state,error_code", e.opts.AccountID)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	for rows.Next() {
		var state, code string
		var count int
		if err = rows.Scan(&state, &code, &count); err != nil {
			break
		}
		switch state {
		case "pending":
			st.Pending += count
		case "staged":
			st.Staged += count
		case "running":
			st.Running += count
		case "unconfirmed":
			st.Unconfirmed += count
		case "confirmed":
			st.Confirmed += count
		case "failed":
			st.Failed += count
		case "blocked":
			st.Blocked += count
		case "skipped":
			st.Skipped += count
		}
		if code != "" && state != "skipped" {
			st.Errors[code] += count
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	err = e.store.db.QueryRowContext(ctx, "SELECT count(*) FROM rules WHERE account_id=? AND active=1 AND complete=0",
		e.opts.AccountID).Scan(&st.Scanning)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	return st, nil
}

func (e *Engine) Once(ctx context.Context) error {
	run, _ := e.store.runs.LoadOrStore(e.opts.AccountID, &sync.Mutex{})
	if !run.(*sync.Mutex).TryLock() {
		return errors.New("engine_busy")
	}
	defer run.(*sync.Mutex).Unlock()
	st, err := e.Status(ctx)
	if err != nil {
		return err
	}
	if st.PauseReason == "manual" {
		return errors.New("account_paused")
	}
	if err = e.identify(ctx); err != nil {
		return e.accountError(ctx, err)
	}
	if err = e.configureRules(ctx); err != nil {
		return errors.New("storage_unavailable")
	}
	// A new invocation retries a failed page, retaining its last committed cursor.
	if _, err = e.store.db.ExecContext(ctx, "UPDATE rules SET error_code='' WHERE account_id=?", e.opts.AccountID); err != nil {
		return errors.New("storage_unavailable")
	}
	workErr := e.drain(ctx)
	for page := 0; page < 8; page++ {
		if ctx.Err() != nil {
			return errors.New("canceled")
		}
		st, err = e.Status(ctx)
		if err != nil {
			return err
		}
		if st.Paused {
			return errors.New("account_paused")
		}
		more, scanErr := e.scanPage(ctx)
		if scanErr != nil {
			return e.accountError(ctx, scanErr)
		}
		if !more {
			break
		}
		if err := e.drain(ctx); err != nil {
			workErr = err
		}
	}
	if err := e.finishScan(ctx); err != nil {
		return errors.New("storage_unavailable")
	}
	st, err = e.Status(ctx)
	if err != nil {
		return err
	}
	if st.Errors["source_changed"] > 0 {
		return errors.New("source_changed")
	}
	return workErr
}

func (e *Engine) identify(ctx context.Context) error {
	source, err := readRetry(ctx, func() (string, error) { return e.source.Identity(ctx) })
	if err != nil {
		return err
	}
	target, err := readRetry(ctx, func() (bridge.Capabilities, error) { return e.target.Probe(ctx) })
	if err != nil {
		return err
	}
	if source == "" || target.Identity == "" {
		return errors.New("identity_unavailable")
	}
	result, err := e.store.db.ExecContext(ctx, `UPDATE accounts SET source_identity=?,target_identity=?,
		pause_reason=CASE WHEN pause_reason='auth' THEN '' ELSE pause_reason END,error_code=''
		WHERE id=? AND (source_identity='' OR source_identity=?) AND (target_identity='' OR target_identity=?)`,
		source, target.Identity, e.opts.AccountID, source, target.Identity)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return errors.New("storage_unavailable")
	}
	if n != 1 {
		return errors.New("identity_mismatch")
	}
	_, err = e.store.db.ExecContext(ctx, `UPDATE assets SET state=CASE WHEN stage_name='' THEN 'pending' ELSE 'staged' END,
		error_code='',attempts=0,next_at=0 WHERE account_id=? AND state='failed' AND error_code IN (?,?)`,
		e.opts.AccountID, bridge.Authentication, bridge.Permission)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func errorCode(err error) string {
	var external *bridge.Error
	code := err.Error()
	if errors.As(err, &external) {
		code = external.Code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	switch code {
	case bridge.Authentication, bridge.Permission, bridge.RateLimit, bridge.Unavailable,
		bridge.NotFound, bridge.Conflict, bridge.Unconfirmed, bridge.Protocol, bridge.Unsupported,
		"identity_unavailable", "identity_mismatch", "storage_unavailable", "staging_quota",
		"checksum_mismatch", "size_mismatch", "staging_corrupt", "unsafe_filename", "source_changed", "canceled":
		return code
	}
	return "remote_error"
}

func (e *Engine) accountError(ctx context.Context, err error) error {
	code := errorCode(err)
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	_, dbErr := e.store.db.ExecContext(ctx, `UPDATE accounts SET error_code=?,
		pause_reason=CASE WHEN ? IN (?,?) AND pause_reason!='manual' THEN 'auth' ELSE pause_reason END WHERE id=?`,
		code, code, bridge.Authentication, bridge.Permission, e.opts.AccountID)
	if dbErr != nil {
		return errors.New("storage_unavailable")
	}
	return errors.New(code)
}
