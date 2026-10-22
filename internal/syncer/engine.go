package syncer

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/direct"
	"github.com/thedavidweng/immich-amazon-sync/internal/filter"
)

type Options struct {
	AccountID     string
	AlbumIDs      []string
	Library       bool
	Filters       filter.Policy
	AlbumSyncMode bridge.AlbumSyncMode
	// LivePhotoMode is still_only (default) or original_pair. OriginalPair
	// resolves each still's companion and backs it up as a separate original.
	LivePhotoMode bridge.LivePhotoMode
	Deletions     bool
	Favorites     string
	// VideoUsageThresholdBytes is an explicit user budget for observed
	// billable video bytes. Zero disables the advisory.
	VideoUsageThresholdBytes int64
	// HoldVideosAtThreshold holds new video uploads at or above the observed
	// threshold. Photos and metadata work continue.
	HoldVideosAtThreshold bool
	// FavoriteTakeover records that the source is authoritative over stars a
	// user applied manually on the Amazon side. It is only legal with mirror.
	FavoriteTakeover bool
	Workers          int
	MaxStagingBytes  int64
	MaxStagingFiles  int
	BytesPerSecond   int64
	// CaptureDate is the finite capture-date write capability declared by the
	// target adapter. Empty is treated as metadata_not_supported: absent proof
	// must never imply success.
	CaptureDate string
	// Window is the live optional transfer window shared with the service. A
	// nil holder or nil current window allows continuous transfers.
	Window *WindowState
	// Now is the clock used for transfer-window decisions. The default is time.Now.
	Now func() time.Time
	// DirectRoots are operator-approved read-only mappings. Empty keeps every
	// original on the Immich API.
	DirectRoots []direct.Root
	Logger      *slog.Logger
}

// WindowStatus is the optional transfer-window view in account status.
type WindowStatus struct {
	Timezone    string     `json:"timezone"`
	Start       string     `json:"start"`
	End         string     `json:"end"`
	Open        bool       `json:"open"`
	NextOpening *time.Time `json:"next_opening,omitempty"`
}

type Status struct {
	Pending                 int            `json:"pending"`
	Staged                  int            `json:"staged"`
	Running                 int            `json:"running"`
	Unconfirmed             int            `json:"unconfirmed"`
	Confirmed               int            `json:"confirmed"`
	Failed                  int            `json:"failed"`
	Blocked                 int            `json:"blocked"`
	Skipped                 int            `json:"skipped"`
	Exclusions              map[string]int `json:"exclusions"`
	Deferred                int            `json:"deferred"`
	Existing                int            `json:"existing"`
	DeletePending           int            `json:"deletion_pending"`
	Deleted                 int            `json:"deleted"`
	Scanning                int            `json:"scanning"`
	Paused                  bool           `json:"paused"`
	PauseReason             string         `json:"pause_reason,omitempty"`
	Errors                  map[string]int `json:"errors"`
	LastSuccess             *time.Time     `json:"last_success"`
	AlbumsPending           int            `json:"albums_pending"`
	AlbumsUnconfirmed       int            `json:"albums_unconfirmed"`
	AlbumsFailed            int            `json:"albums_failed"`
	AlbumSyncMode           string         `json:"album_sync_mode"`
	AlbumsActive            int            `json:"albums_active"`
	AlbumsInaccessible      int            `json:"albums_inaccessible"`
	AssociationsPending     int            `json:"associations_pending"`
	AssociationsFailed      int            `json:"associations_failed"`
	AssociationsUnconfirmed int            `json:"associations_unconfirmed"`
	EventsPending           int            `json:"events_pending"`
	EventsFailed            int            `json:"events_failed"`
	Workflow                string         `json:"workflow,omitempty"`
	WorkflowCode            string         `json:"workflow_code,omitempty"`
	IncrementalScanning     bool           `json:"incremental_scanning"`
	LastIncremental         *time.Time     `json:"last_incremental"`
	FavoritesMode           string         `json:"favorites_mode"`
	LivePhotoMode           string         `json:"live_photo_mode"`
	PairsComplete           int            `json:"pairs_complete"`
	PairsIncomplete         int            `json:"pairs_incomplete"`
	FavoritesAuthority      string         `json:"favorites_authority,omitempty"`
	FavoritesDesired        int            `json:"favorites_desired"`
	FavoritesObserved       int            `json:"favorites_observed"`
	FavoritesPending        int            `json:"favorites_pending"`
	FavoritesApplied        int            `json:"favorites_applied"`
	FavoritesPreserved      int            `json:"favorites_preserved"`
	FavoritesFailed         int            `json:"favorites_failed"`
	FavoritesUncertain      int            `json:"favorites_uncertain"`
	FavoritesUnknown        int            `json:"favorites_unknown"`
	FavoritesConflict       int            `json:"favorites_conflict"`
	WorkRemaining           bool           `json:"work_remaining"`
	NextAttempt             *time.Time     `json:"next_attempt,omitempty"`
	Usage                   UsageStatus    `json:"usage"`
	CaptureDate             string         `json:"capture_date"`
	DeferralReason          string         `json:"deferral_reason,omitempty"`
	TransferWindow          *WindowStatus  `json:"transfer_window,omitempty"`
	BytesPerSecond          int64          `json:"bytes_per_second"`
	Direct                  DirectCounters `json:"direct"`
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
	videoHold atomic.Bool
	quotaHold atomic.Bool
	direct    *direct.Set
}

func New(store *Store, source bridge.Source, target bridge.Target, opts Options) (*Engine, error) {
	mode := opts.AlbumSyncMode
	if mode == "" {
		mode = bridge.AlbumSyncOff
	}
	if store == nil || source == nil || target == nil || opts.AccountID == "" ||
		(!opts.Library && len(opts.AlbumIDs) == 0) || (opts.Library && len(opts.AlbumIDs) != 0) ||
		!mode.Valid() || (mode == bridge.AlbumSyncSelected && (opts.Library || len(opts.AlbumIDs) == 0)) ||
		(opts.Favorites != "" && opts.Favorites != "off" && opts.Favorites != "add_only" &&
			opts.Favorites != "mirror") ||
		(opts.FavoriteTakeover && opts.Favorites != "mirror") ||
		opts.Workers < 0 || opts.Workers > 32 || opts.MaxStagingBytes < 0 ||
		opts.MaxStagingFiles < 0 || opts.BytesPerSecond < 0 ||
		opts.VideoUsageThresholdBytes < 0 ||
		(opts.HoldVideosAtThreshold && opts.VideoUsageThresholdBytes == 0) {
		return nil, errors.New("invalid_options")
	}
	opts.AlbumSyncMode = mode
	for _, id := range opts.AlbumIDs {
		if id == "" {
			return nil, errors.New("invalid_options")
		}
	}
	// Absent a declared capability the honest answer is that the target cannot
	// set a capture date. Anything outside the finite vocabulary is refused
	// rather than rendered as success.
	if opts.CaptureDate == "" {
		opts.CaptureDate = bridge.MetadataNotSupported
	}
	if opts.CaptureDate != bridge.MetadataSupported && opts.CaptureDate != bridge.MetadataNotSupported {
		return nil, errors.New("invalid_options")
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
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.AlbumIDs = append([]string(nil), opts.AlbumIDs...)
	opts.Filters = opts.Filters.Normalize()
	if err := opts.Filters.Validate(); err != nil {
		return nil, errors.New("invalid_options")
	}
	if opts.LivePhotoMode == "" {
		opts.LivePhotoMode = bridge.LivePhotoStillOnly
	}
	if !opts.LivePhotoMode.Valid() {
		return nil, errors.New("invalid_options")
	}
	directSet, err := direct.New(opts.DirectRoots)
	if err != nil {
		return nil, err
	}
	if _, err := store.db.Exec("INSERT INTO accounts(id) VALUES(?) ON CONFLICT DO NOTHING", opts.AccountID); err != nil {
		return nil, errors.New("storage_unavailable")
	}
	return &Engine{store: store, source: source, target: target, opts: opts,
		contents: make(map[string]*contentLock), direct: directSet}, nil
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
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE rules SET generation=generation+1,cursor='',expected=-1,
		started_at=?,attempts=0,next_at=0 WHERE account_id=? AND complete=0 AND error_code!=''`,
		time.Now().UTC().Format(time.RFC3339Nano), e.opts.AccountID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM scan_cursors WHERE account_id=? AND scope IN
			(SELECT scope FROM rules WHERE account_id=? AND complete=0 AND error_code!='')`, e.opts.AccountID, e.opts.AccountID)
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	_, err = tx.ExecContext(ctx, `UPDATE assets SET state=CASE WHEN state='unconfirmed' THEN state
		WHEN stage_name!='' THEN 'staged' ELSE 'pending' END,attempts=0,next_at=0,
		error_code=CASE WHEN state='unconfirmed' THEN error_code ELSE '' END
		WHERE account_id=? AND state IN ('failed','blocked','unconfirmed') AND error_code!='source_changed'`, e.opts.AccountID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE hints SET attempts=0,next_at=0,error_code='' WHERE account_id=?`, e.opts.AccountID)
	}
	if err == nil {
		// Every favorite attempt re-reads the node before writing, so an
		// uncertain favorite mutation is safe to retry instead of a blind write.
		// Settled rows are audited again after an explicit retry, so a user
		// repair also re-reads drift. Conflicts are re-derived by the next pass.
		_, err = tx.ExecContext(ctx, `UPDATE favorites SET attempts=0,next_at=0,
			error_code=CASE WHEN state IN ('pending','failed','uncertain') THEN '' ELSE error_code END,
			checked_at=CASE WHEN state IN ('applied','none','preserved') THEN 0 ELSE checked_at END
			WHERE account_id=? AND state IN ('pending','failed','uncertain','applied','none','preserved')`,
			e.opts.AccountID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE albums SET attempts=0,next_at=0,error_code='' WHERE account_id=?`, e.opts.AccountID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE memberships SET attempts=0,next_at=0,error_code='' WHERE account_id=?`, e.opts.AccountID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE rules SET attempts=0,next_at=0,error_code='' WHERE account_id=?`, e.opts.AccountID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE incremental SET error_code='' WHERE account_id=?`, e.opts.AccountID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET pause_reason=CASE WHEN pause_reason='auth' THEN '' ELSE pause_reason END,
			error_code='' WHERE id=?`, e.opts.AccountID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE usage SET hold=0 WHERE account_id=?`, e.opts.AccountID)
	}
	if err == nil {
		// An incomplete pair is retried only when the operator asks; the
		// companion relationship itself is never re-derived by a retry.
		_, err = tx.ExecContext(ctx, `UPDATE live_pairs SET attempts=0,next_at=0
			WHERE account_id=? AND state!='complete'`, e.opts.AccountID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) summary(ctx context.Context) (Status, error) {
	st := Status{Errors: map[string]int{}, Exclusions: map[string]int{}, BytesPerSecond: e.opts.BytesPerSecond}
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
		case "deferred":
			st.Deferred += count
		case "existing":
			st.Existing += count
		case "delete_pending":
			st.DeletePending += count
		case "deleted":
			st.Deleted += count
		}
		if code != "" && state != "skipped" {
			st.Errors[code] += count
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return st, errors.New("storage_unavailable")
	}
	st.FavoritesMode = e.favoriteMode()
	st.FavoritesAuthority = e.favoriteAuthority()
	err = e.store.db.QueryRowContext(ctx, `SELECT
		coalesce(sum(desired=1),0),
		coalesce(sum(observed=1),0),
		coalesce(sum(state='pending'),0),
		coalesce(sum(state='applied'),0),
		coalesce(sum(state='preserved'),0),
		coalesce(sum(state='failed'),0),
		coalesce(sum(state='uncertain'),0),
		coalesce(sum(state='unknown'),0),
		coalesce(sum(state='conflict'),0)
		FROM favorites WHERE account_id=?`, e.opts.AccountID).
		Scan(&st.FavoritesDesired, &st.FavoritesObserved, &st.FavoritesPending, &st.FavoritesApplied,
			&st.FavoritesPreserved, &st.FavoritesFailed, &st.FavoritesUncertain, &st.FavoritesUnknown,
			&st.FavoritesConflict)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	rows, err = e.store.db.QueryContext(ctx, `SELECT exclusion,count(*) FROM assets
		WHERE account_id=? AND state='skipped' AND exclusion!='' GROUP BY exclusion`, e.opts.AccountID)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	for rows.Next() {
		var reason string
		var count int
		if err = rows.Scan(&reason, &count); err != nil {
			break
		}
		st.Exclusions[reason] += count
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return st, errors.New("storage_unavailable")
	}
	return st, nil
}

func (e *Engine) mirrorsAlbums() bool {
	return e.opts.AlbumSyncMode != bridge.AlbumSyncOff
}

func (e *Engine) paused(ctx context.Context) (bool, error) {
	var reason string
	if err := e.store.db.QueryRowContext(ctx, "SELECT pause_reason FROM accounts WHERE id=?", e.opts.AccountID).Scan(&reason); err != nil {
		return false, errors.New("storage_unavailable")
	}
	return reason != "", nil
}

func (e *Engine) Status(ctx context.Context) (Status, error) {
	st, err := e.summary(ctx)
	if err != nil {
		return st, err
	}
	usage, err := e.usageStatus(ctx)
	if err != nil {
		return st, err
	}
	videoHold := usage.VideoHold
	st.CaptureDate = e.opts.CaptureDate
	st.Direct = e.store.directCounters(e.opts.AccountID)
	st.LivePhotoMode = string(e.opts.LivePhotoMode)
	if e.pairsOn() {
		err = e.store.db.QueryRowContext(ctx, `SELECT coalesce(sum(state='complete'),0),
			coalesce(sum(state='incomplete'),0) FROM live_pairs WHERE account_id=?`,
			e.opts.AccountID).Scan(&st.PairsComplete, &st.PairsIncomplete)
		if err != nil {
			return st, errors.New("storage_unavailable")
		}
		if st.PairsIncomplete > 0 {
			// An incomplete pair is not a full Live Photo backup. It is a
			// reported condition, not a media failure and not retryable work.
			st.Errors[pairIncomplete] = st.PairsIncomplete
		}
	}
	err = e.store.db.QueryRowContext(ctx, "SELECT count(*) FROM rules WHERE account_id=? AND active=1 AND complete=0",
		e.opts.AccountID).Scan(&st.Scanning)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	st.AlbumSyncMode = string(e.opts.AlbumSyncMode)
	if e.mirrorsAlbums() {
		err = e.store.db.QueryRowContext(ctx, `SELECT
			coalesce(sum(a.target_id IS NULL OR a.target_id='' OR a.complete_generation!=r.generation OR a.applied_name!=r.name),0),
			coalesce(sum(a.state IN ('creating','unconfirmed')),0),
			coalesce(sum(a.error_code!=''),0)
			FROM rules r LEFT JOIN albums a ON r.account_id=a.account_id AND r.scope=a.scope
			WHERE r.account_id=? AND r.active=1 AND r.album_id!=''`, e.opts.AccountID).
			Scan(&st.AlbumsPending, &st.AlbumsUnconfirmed, &st.AlbumsFailed)
		if err != nil {
			return st, errors.New("storage_unavailable")
		}
		err = e.store.db.QueryRowContext(ctx, `SELECT coalesce(sum(m.state NOT IN ('present','absent')),0),
			coalesce(sum(m.state IN ('unconfirmed_add','unconfirmed_remove')),0),
			coalesce(sum(m.error_code!=''),0)
			FROM memberships m JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
			WHERE m.account_id=? AND r.active=1`, e.opts.AccountID).
			Scan(&st.AssociationsPending, &st.AssociationsUnconfirmed, &st.AssociationsFailed)
		if err != nil {
			return st, errors.New("storage_unavailable")
		}
		err = e.store.db.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM rules WHERE account_id=? AND active=1 AND album_id!=''),
			(SELECT count(*) FROM rules WHERE account_id=? AND album_id!='' AND discovered=0)`,
			e.opts.AccountID, e.opts.AccountID).Scan(&st.AlbumsActive, &st.AlbumsInaccessible)
		if err != nil {
			return st, errors.New("storage_unavailable")
		}
	}
	if err = e.store.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(error_code!=''),0) FROM hints WHERE account_id=?`,
		e.opts.AccountID).Scan(&st.EventsPending, &st.EventsFailed); err != nil {
		return st, errors.New("storage_unavailable")
	}
	err = e.store.db.QueryRowContext(ctx, "SELECT state,error_code FROM workflows WHERE account_id=?",
		e.opts.AccountID).Scan(&st.Workflow, &st.WorkflowCode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, errors.New("storage_unavailable")
	}
	var watermark string
	err = e.store.db.QueryRowContext(ctx, "SELECT active,watermark FROM incremental WHERE account_id=?", e.opts.AccountID).
		Scan(&st.IncrementalScanning, &watermark)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, errors.New("storage_unavailable")
	}
	if watermark != "" {
		t, err := time.Parse(time.RFC3339Nano, watermark)
		if err != nil {
			return st, errors.New("storage_unavailable")
		}
		st.LastIncremental = &t
	}
	rows, err := e.store.db.QueryContext(ctx, `SELECT error_code,count(*) FROM (
		SELECT error_code FROM rules WHERE account_id=? AND active=1
		UNION ALL SELECT error_code FROM hints WHERE account_id=?
		UNION ALL SELECT error_code FROM incremental WHERE account_id=? AND ?
		UNION ALL SELECT a.error_code FROM albums a JOIN rules r ON r.account_id=a.account_id AND r.scope=a.scope
			WHERE a.account_id=? AND r.active=1 AND ?
		UNION ALL SELECT m.error_code FROM memberships m JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
			WHERE m.account_id=? AND r.active=1 AND ?
		UNION ALL SELECT error_code FROM rules WHERE account_id=? AND album_id!='' AND discovered=0
		UNION ALL SELECT error_code FROM favorites WHERE account_id=?) WHERE error_code!='' GROUP BY error_code`,
		e.opts.AccountID, e.opts.AccountID, e.opts.AccountID, e.opts.Library,
		e.opts.AccountID, e.mirrorsAlbums(), e.opts.AccountID, e.mirrorsAlbums(), e.opts.AccountID,
		e.opts.AccountID)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	for rows.Next() {
		var code string
		var count int
		if err = rows.Scan(&code, &count); err != nil {
			break
		}
		st.Errors[code] += count
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return st, errors.New("storage_unavailable")
	}
	now := e.opts.Now()
	nowMillis := now.UnixMilli()
	window := e.currentWindow()
	windowOpen := window == nil || window.Open(now)
	var uploadDue, reconcileDue int
	err = e.store.db.QueryRowContext(ctx, `SELECT
		coalesce(sum(state IN ('pending','staged','failed')
			AND (?=0 OR coalesce(json_extract(metadata,'$.kind'),'')!='VIDEO')),0),
		coalesce(sum(state='unconfirmed'),0)
		FROM assets a WHERE account_id=? AND attempts<5 AND next_at<=?
		AND error_code!='source_changed' AND `+e.eligibleSQL(),
		videoHold, e.opts.AccountID, nowMillis).Scan(&uploadDue, &reconcileDue)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	var otherDue int
	err = e.store.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM rules WHERE account_id=? AND active=1 AND complete=0 AND (error_code='' OR (attempts<5 AND next_at<=?)))+
		(SELECT count(*) FROM hints WHERE account_id=? AND attempts<5 AND next_at<=?)+
		(SELECT count(*) FROM incremental WHERE account_id=? AND active=1 AND error_code='')+
		(SELECT count(*) FROM assets WHERE account_id=? AND state='delete_pending' AND next_at<=?)+
		(SELECT count(*) FROM albums a JOIN rules r ON r.account_id=a.account_id AND r.scope=a.scope
		 WHERE a.account_id=? AND ? AND r.active=1 AND r.complete=1 AND a.state NOT IN ('creating','unconfirmed')
		 AND a.error_code!='' AND a.attempts<5 AND a.next_at<=?)+
		(SELECT count(*) FROM favorites WHERE account_id=? AND ? AND state IN ('pending','failed','uncertain')
		 AND attempts<5 AND next_at<=? AND `+favoriteEligibleSQL+`)`,
		e.opts.AccountID, nowMillis, e.opts.AccountID, nowMillis, e.opts.AccountID,
		e.opts.AccountID, nowMillis,
		e.opts.AccountID, e.mirrorsAlbums(), nowMillis,
		e.opts.AccountID, e.favoritesOn(), nowMillis).Scan(&otherDue)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	due := otherDue + reconcileDue
	if windowOpen {
		due += uploadDue
	} else {
		// Upload work blocked only by the window is deferral, not runnable work.
		st.Deferred += uploadDue
	}
	st.WorkRemaining = due > 0 && !st.Paused
	var next *int64
	err = e.store.db.QueryRowContext(ctx, `SELECT min(next_at) FROM (
		SELECT next_at FROM assets a WHERE account_id=? AND state IN ('failed','unconfirmed')
			AND attempts<5 AND error_code!='source_changed'
			AND (?=0 OR coalesce(json_extract(a.metadata,'$.kind'),'')!='VIDEO') AND `+e.eligibleSQL()+`
		UNION ALL SELECT next_at FROM hints WHERE account_id=? AND attempts<5
		UNION ALL SELECT next_at FROM assets WHERE account_id=? AND state='delete_pending'
		UNION ALL SELECT next_at FROM rules WHERE account_id=? AND active=1 AND complete=0 AND attempts<5
		UNION ALL SELECT a.next_at FROM albums a JOIN rules r ON r.account_id=a.account_id AND r.scope=a.scope
			WHERE a.account_id=? AND ? AND r.active=1 AND a.attempts<5 AND a.error_code!=''
			AND a.state NOT IN ('creating','unconfirmed')
		UNION ALL SELECT next_at FROM favorites WHERE account_id=? AND ? AND state IN ('pending','failed','uncertain')
			AND attempts<5 AND `+favoriteEligibleSQL+`) WHERE next_at>?`,
		e.opts.AccountID, videoHold, e.opts.AccountID, e.opts.AccountID, e.opts.AccountID,
		e.opts.AccountID, e.mirrorsAlbums(), e.opts.AccountID, e.favoritesOn(), nowMillis).Scan(&next)
	if err != nil {
		return st, errors.New("storage_unavailable")
	}
	if next != nil && !st.Paused {
		t := time.UnixMilli(*next)
		st.NextAttempt = &t
	}
	st.Usage = usage
	if usage.QuotaHold {
		// No upload can dispatch until a later full pass clears the hold.
		st.WorkRemaining = false
		st.NextAttempt = nil
	}
	if window != nil {
		view := &WindowStatus{Timezone: window.Timezone, Start: window.Start, End: window.End, Open: windowOpen}
		if !windowOpen {
			opening := window.NextOpening(now)
			view.NextOpening = &opening
		}
		st.TransferWindow = view
	}
	if st.Deferred > 0 && !windowOpen {
		st.DeferralReason = "transfer_window"
		if !st.Paused {
			opening := *st.TransferWindow.NextOpening
			if st.NextAttempt == nil || opening.Before(*st.NextAttempt) {
				st.NextAttempt = &opening
			}
		}
	}
	return st, nil
}

// currentWindow returns the live window snapshot for this account.
func (e *Engine) currentWindow() *Window {
	if e.opts.Window == nil {
		return nil
	}
	return e.opts.Window.Current()
}

// windowOpen reports whether new upload dispatch is allowed at now.
func (e *Engine) windowOpen(now time.Time) bool {
	window := e.currentWindow()
	return window == nil || window.Open(now)
}

func (e *Engine) Once(ctx context.Context) error {
	return e.run(ctx, true, false)
}

// Step continues durable work without starting another completed full scan.
func (e *Engine) Step(ctx context.Context) error {
	return e.run(ctx, false, false)
}

// Reconcile starts an overlapping library delta and performs daily full verification.
func (e *Engine) Reconcile(ctx context.Context) error {
	return e.run(ctx, false, true)
}

// reconcileFilterPolicy requeues Assets excluded by a previous policy when the
// effective rules change. It reads only the durable ledger; no source rescan is
// required, and confirmed or Existing mappings are never touched.
func (e *Engine) reconcileFilterPolicy(ctx context.Context) error {
	fingerprint := e.opts.Filters.Fingerprint()
	var stored string
	if err := e.store.db.QueryRowContext(ctx, "SELECT filter_fingerprint FROM accounts WHERE id=?",
		e.opts.AccountID).Scan(&stored); err != nil {
		return errors.New("storage_unavailable")
	}
	if stored == fingerprint {
		return nil
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	reasons := filter.AllReasons()
	args := []any{e.opts.AccountID}
	for _, reason := range reasons {
		args = append(args, reason)
	}
	_, err = tx.ExecContext(ctx, `UPDATE assets SET state='pending',exclusion='',error_code='',
		attempts=0,next_at=0 WHERE account_id=? AND state='skipped' AND exclusion IN (?`+
		strings.Repeat(",?", len(reasons)-1)+`)`, args...)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE accounts SET filter_fingerprint=? WHERE id=?",
		fingerprint, e.opts.AccountID); err != nil {
		return errors.New("storage_unavailable")
	}
	if err = tx.Commit(); err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) run(ctx context.Context, full, incremental bool) error {
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
	// Observed usage gates only the video advisory hold; a failed usage read is
	// recorded as stale/unavailable and never blocks other media work.
	if err = e.refreshUsage(ctx); err != nil {
		return err
	}
	// A quota hold survives continuation steps. Only an explicit full pass or
	// periodic reconcile clears it and re-attempts the durable original.
	if full || incremental {
		if err = e.clearUsageHold(ctx); err != nil {
			return err
		}
	}
	usage, err := e.usageStatus(ctx)
	if err != nil {
		return err
	}
	e.videoHold.Store(usage.VideoHold)
	e.quotaHold.Store(usage.QuotaHold)
	// A full pass or a periodic reconcile reads the authoritative accessible
	// album listing; bounded continuations keep the discovered set stable so an
	// in-flight album scan is not interrupted. A failed listing changes nothing.
	if e.opts.AlbumSyncMode == bridge.AlbumSyncAllAccessible && (full || incremental) {
		if _, err = e.discoverAlbums(ctx); err != nil {
			return e.accountError(ctx, err)
		}
	}
	if err = e.configureRules(ctx, full); err != nil {
		return errors.New("storage_unavailable")
	}
	if err = e.reconcileFilterPolicy(ctx); err != nil {
		return err
	}
	if err = e.reconcileLivePhotoMode(ctx); err != nil {
		return err
	}
	if !e.opts.Deletions {
		if err = e.withdrawDeletions(ctx); err != nil {
			return err
		}
	}
	// Explicit runs retry immediately; continuations honor persisted scan backoff.
	// An inaccessible album stays visibly failed until discovery returns it.
	if _, err = e.store.db.ExecContext(ctx, `UPDATE rules SET error_code='' WHERE account_id=?
		AND active=1 AND error_code!='album_inaccessible'
		AND (? OR (attempts<5 AND next_at<=?))`, e.opts.AccountID, full, e.opts.Now().UnixMilli()); err != nil {
		return errors.New("storage_unavailable")
	}
	if incremental {
		var due int
		if err = e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM rules WHERE account_id=? AND active=1
			AND complete=1 AND completed_at<?`, e.opts.AccountID, time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339Nano)).
			Scan(&due); err != nil {
			return errors.New("storage_unavailable")
		}
		if !e.opts.Library || due > 0 {
			if err := e.configureRules(ctx, true); err != nil {
				return errors.New("storage_unavailable")
			}
		}
		if e.opts.Library {
			if err := e.beginIncremental(ctx); err != nil {
				return err
			}
		}
	}
	if err := e.validateScopes(ctx); err != nil {
		return e.accountError(ctx, err)
	}
	workErr := e.processHints(ctx, 8)
	attempted := make(map[string]bool)
	if err := e.drain(ctx, attempted); err != nil {
		workErr = err
	}
	for page := 0; page < 8; page++ {
		if ctx.Err() != nil {
			return errors.New("canceled")
		}
		paused, err := e.paused(ctx)
		if err != nil {
			return errors.New("storage_unavailable")
		}
		if paused {
			return errors.New("account_paused")
		}
		if err := e.incrementalPage(ctx); err != nil {
			return e.accountError(ctx, err)
		}
		more, scanErr := e.scanPage(ctx)
		if scanErr != nil {
			return e.accountError(ctx, scanErr)
		}
		if err := e.drain(ctx, attempted); err != nil {
			workErr = err
		}
		if !more && !st.IncrementalScanning {
			break
		}
	}
	if err := e.sweepFavorites(ctx); err != nil {
		workErr = err
	}
	// Pair resolution registers companions that the pages did not enumerate;
	// the second drain uploads them, and the second settle reports the pair.
	if err := e.sweepPairs(ctx); err != nil {
		workErr = err
	}
	if err := e.drain(ctx, attempted); err != nil {
		workErr = err
	}
	if err := e.sweepPairs(ctx); err != nil {
		workErr = err
	}
	if err := e.sweepDeletions(ctx); err != nil {
		workErr = err
		if errorCode(err) == "deletion_guard" {
			workErr = e.accountError(ctx, err)
		}
	}
	if err := e.mirror(ctx); err != nil {
		workErr = err
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
	case bridge.Authentication, bridge.Permission, bridge.RateLimit, bridge.Quota, bridge.Unavailable,
		bridge.NotFound, bridge.Conflict, bridge.Unconfirmed, bridge.FavoriteUnconfirmed,
		bridge.Protocol, bridge.Unsupported,
		"identity_unavailable", "identity_mismatch", "storage_unavailable", "staging_quota",
		"checksum_mismatch", "size_mismatch", "staging_corrupt", "unsafe_filename", "source_changed", "canceled",
		"direct_path_invalid", "direct_path_escape", "direct_path_not_regular",
		"invalid_event", "album_unconfirmed", "membership_guard", "deletion_guard", "album_inaccessible",
		"shared_target_conflict":
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
