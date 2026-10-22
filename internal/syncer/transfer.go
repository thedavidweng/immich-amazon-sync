package syncer

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

type job struct {
	id       string
	asset    bridge.Asset
	state    string
	stage    string
	md5      string
	sha1     string
	size     int64
	node     string
	attempts int
	next     int64
	read     string // "direct" or "api" once an original is opened
}

const eligibleSQL = `(EXISTS(SELECT 1 FROM scope_assets m JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
	WHERE m.account_id=a.account_id AND m.asset_id=a.id AND r.active=1 AND m.generation=r.generation AND r.error_code='')
	OR EXISTS(SELECT 1 FROM signals m JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
	WHERE m.account_id=a.account_id AND m.asset_id=a.id AND r.active=1 AND m.generation=r.generation AND r.error_code=''))`

// errWindowClosed marks a job deferred by the transfer window. It is never
// persisted as a failure and never consumes a retry.
var errWindowClosed = errors.New("transfer_window_closed")

func (e *Engine) drain(ctx context.Context, attempted map[string]bool) error {
	var selectMu sync.Mutex
	var windowClosed atomic.Bool
	results := make(chan error, 64+e.opts.Workers)
	var workers sync.WaitGroup
	for range min(e.opts.Workers, e.opts.MaxStagingFiles) {
		workers.Go(func() {
			for {
				if ctx.Err() != nil || windowClosed.Load() {
					return
				}
				paused, err := e.paused(ctx)
				if err != nil || paused {
					return
				}
				selectMu.Lock()
				err = e.processHints(ctx, 1)
				var j *job
				if err == nil && len(attempted) < 64 {
					j, err = e.nextJob(ctx, attempted)
				}
				selectMu.Unlock()
				if err != nil {
					results <- err
				}
				if j == nil {
					return
				}
				err = e.transfer(ctx, j)
				if errors.Is(err, errWindowClosed) {
					// Stop selecting new uploads but let sibling workers finish
					// any upload already streaming.
					windowClosed.Store(true)
					return
				}
				results <- err
			}
		})
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) nextJob(ctx context.Context, attempted map[string]bool) (*job, error) {
	now := e.opts.Now()
	deferAllowed := 0
	if e.windowOpen(now) {
		deferAllowed = 1
	}
	rows, err := e.store.db.QueryContext(ctx, `SELECT id,metadata,state,stage_name,md5,sha1,size,node_id,attempts FROM assets a
		WHERE account_id=? AND state IN ('pending','staged','deferred','unconfirmed','failed') AND next_at<=? AND attempts<5
		AND error_code!='source_changed' AND (state!='deferred' OR ?)
		AND `+eligibleSQL+` ORDER BY priority DESC,id LIMIT 64`, e.opts.AccountID, now.UnixMilli(), deferAllowed)
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	var selected *job
	for rows.Next() {
		var j job
		var metadata string
		if err = rows.Scan(&j.id, &metadata, &j.state, &j.stage, &j.md5, &j.sha1, &j.size, &j.node, &j.attempts); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(metadata), &j.asset); err != nil {
			break
		}
		if !attempted[j.id] {
			attempted[j.id] = true
			selected = &j
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	return selected, nil
}

func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) &&
		!strings.ContainsAny(name, `/\:`) && !strings.ContainsFunc(name, unicode.IsControl)
}

func (e *Engine) transfer(ctx context.Context, j *job) (result error) {
	started := time.Now()
	var unlock func()
	defer func() {
		// Persist the outcome before allowing another worker to use this content.
		if result != nil && !errors.Is(result, errWindowClosed) {
			result = e.fail(ctx, *j, result)
		}
		if unlock != nil {
			unlock()
		}
		e.logJob(context.WithoutCancel(ctx), j, started, result)
	}()
	if j.state != "unconfirmed" {
		if j.asset.ExcludedReason != "" {
			return e.skip(ctx, j, sourceExclusion(j.asset.ExcludedReason))
		}
		if !safeName(j.asset.Name) {
			return errors.New("unsafe_filename")
		}
		kind := strings.ToUpper(j.asset.Kind)
		if kind == "" {
			switch {
			case strings.HasPrefix(j.asset.MIME, "image/"):
				kind = "IMAGE"
			case strings.HasPrefix(j.asset.MIME, "video/"):
				kind = "VIDEO"
			}
		}
		if j.asset.LivePhotoVideo {
			return e.skip(ctx, j, "live_photo_companion")
		}
		if kind != "IMAGE" && kind != "VIDEO" {
			return e.skip(ctx, j, "unsupported_type")
		}
		// The policy is authoritative at the write boundary: a stale pending
		// row or a policy change cannot authorize an excluded upload.
		if reason := e.opts.Filters.Exclude(j.asset); reason != "" {
			return e.skip(ctx, j, reason)
		}
		if j.asset.MIME == "" {
			j.asset.MIME = mime.TypeByExtension(filepath.Ext(j.asset.Name))
		}
		if j.stage == "" {
			// A closed window defers the upload before downloading source bytes so
			// deferral cannot consume staging quota or retries.
			if !e.windowOpen(e.opts.Now()) {
				return e.deferUpload(ctx, j)
			}
			if err := e.stage(ctx, j); err != nil {
				return err
			}
		}
	}
	unlock = e.lockContent(j)
	content := bridge.Content{Name: j.asset.Name, MIME: j.asset.MIME, MD5: j.md5, Size: j.size, TakenAt: j.asset.TakenAt}
	// A crash or lost response after dispatch is never permission to POST again.
	uncertain := j.state == "unconfirmed"
	var known string
	err := e.store.db.QueryRowContext(ctx, `SELECT node_id FROM assets
		WHERE account_id=? AND md5=? AND size=? AND state IN ('confirmed','existing','running','unconfirmed')
		AND node_id!='' ORDER BY (state='confirmed') DESC,(state='existing') DESC LIMIT 1`,
		e.opts.AccountID, j.md5, j.size).Scan(&known)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.New("storage_unavailable")
	}
	if err == nil {
		node, err := readRetry(ctx, func() (bridge.Node, error) { return e.target.GetNode(ctx, known) })
		if err != nil {
			return err
		}
		if node.ID != known || !sameContent(node, content) {
			j.state = "unconfirmed"
			return errors.New(bridge.Unconfirmed)
		}
		return e.reuse(ctx, j, node, content.ParentID)
	}
	nodes, err := readRetry(ctx, func() ([]bridge.Node, error) { return e.target.Find(ctx, content) })
	if err != nil {
		return err
	}
	if len(nodes) > 1 {
		// The strict lookup proves zero or one match; anything else is unusable.
		j.state = "unconfirmed"
		return errors.New(bridge.Unconfirmed)
	}
	if len(nodes) == 1 {
		n := nodes[0]
		if n.ID == "" || n.MD5 == "" || n.Size < 0 {
			j.state = "unconfirmed"
			return errors.New(bridge.Unconfirmed)
		}
		if !sameContent(n, content) {
			// A same-name target node with different content is an explicit
			// conflict; anything else remains unverifiable.
			if n.Name == content.Name && n.Placement(content.ParentID) == bridge.PlacementTarget {
				return errors.New(bridge.Conflict)
			}
			j.state = "unconfirmed"
			return errors.New(bridge.Unconfirmed)
		}
		verified, err := readRetry(ctx, func() (bridge.Node, error) { return e.target.GetNode(ctx, n.ID) })
		if err != nil {
			return err
		}
		if !sameContent(verified, content) || verified.ID != n.ID ||
			verified.Placement(content.ParentID) != bridge.PlacementTarget {
			j.state = "unconfirmed"
			return errors.New(bridge.Unconfirmed)
		}
		return e.reuse(ctx, j, verified, content.ParentID)
	}
	// The target parent is strict. Only an authoritative account-wide lookup may
	// prove that a copy already exists somewhere else in this account.
	existing, err := readRetry(ctx, func() (bridge.Node, error) { return e.target.FindExisting(ctx, content) })
	if err != nil {
		if uncertain {
			j.state = "unconfirmed"
		}
		return err
	}
	if existing.ID != "" {
		if !sameContent(existing, content) {
			j.state = "unconfirmed"
			return errors.New(bridge.Unconfirmed)
		}
		return e.reuse(ctx, j, existing, content.ParentID)
	}
	if uncertain {
		// An incomplete or zero match cannot prove a prior write did not commit.
		j.state = "unconfirmed"
		return errors.New(bridge.Unconfirmed)
	}
	// Metadata reads above stay available outside the window; only the first
	// remote write is gated. An upload already streaming never rechecks.
	if !e.windowOpen(e.opts.Now()) {
		return e.deferUpload(ctx, j)
	}
	if filepath.Base(j.stage) != j.stage || !strings.HasPrefix(j.stage, "original-") {
		return errors.New("staging_corrupt")
	}
	f, err := os.Open(filepath.Join(e.store.dir, "staging", j.stage))
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer f.Close()
	digest := md5.New()
	checksum := sha1.New()
	n, err := io.CopyBuffer(io.MultiWriter(digest, checksum), f, make([]byte, 32<<10))
	if err != nil {
		return errors.New("storage_unavailable")
	}
	if n != j.size || hex.EncodeToString(digest.Sum(nil)) != j.md5 ||
		base64.StdEncoding.EncodeToString(checksum.Sum(nil)) != j.sha1 {
		return errors.New("staging_corrupt")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errors.New("storage_unavailable")
	}
	// This commit is the point after which a crash requires remote reconciliation.
	if err := e.setState(ctx, j, "running", "", ""); err != nil {
		return err
	}
	node, uploadErr := e.target.Upload(ctx, content, &rateReader{e: e, ctx: ctx, reader: f})
	j.state = "unconfirmed"
	if uploadErr != nil {
		var rejection *bridge.Error
		if errors.As(uploadErr, &rejection) && (rejection.Code == bridge.Authentication ||
			rejection.Code == bridge.Permission || rejection.Code == bridge.RateLimit) {
			j.state = "staged"
			return uploadErr
		}
		// The write may have committed, or Amazon's global dedupe may have
		// rejected a copy that already exists elsewhere. Reconcile only codes
		// where the remote state is genuinely unknown.
		switch errorCode(uploadErr) {
		case bridge.Conflict, bridge.Unconfirmed, bridge.Unavailable, bridge.Protocol, "canceled":
			return e.reconcile(ctx, j, content, uploadErr)
		}
		return uploadErr
	}
	j.node = node.ID
	if err := e.setState(context.WithoutCancel(ctx), j, "unconfirmed", bridge.Unconfirmed, ""); err != nil {
		return err
	}
	if !sameContent(node, content) || node.Placement(content.ParentID) != bridge.PlacementTarget {
		return errors.New(bridge.Unconfirmed)
	}
	node, err = readRetry(ctx, func() (bridge.Node, error) { return e.target.GetNode(ctx, node.ID) })
	if err != nil {
		return err
	}
	if !sameContent(node, content) || node.ID != j.node || node.Placement(content.ParentID) != bridge.PlacementTarget {
		return errors.New(bridge.Unconfirmed)
	}
	return e.confirm(ctx, j)
}

func anonymousID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

func (e *Engine) logJob(ctx context.Context, j *job, started time.Time, result error) {
	if e.opts.Logger == nil {
		return
	}
	scope := ""
	_ = e.store.db.QueryRowContext(ctx, `SELECT min(scope) FROM (
		SELECT m.scope FROM scope_assets m JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
		WHERE m.account_id=? AND m.asset_id=? AND r.active=1 AND m.generation=r.generation
		UNION SELECT m.scope FROM signals m JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
		WHERE m.account_id=? AND m.asset_id=? AND r.active=1 AND m.generation=r.generation)`,
		e.opts.AccountID, j.id, e.opts.AccountID, j.id).Scan(&scope)
	st, _ := e.summary(ctx)
	code := "ok"
	stage := j.state
	switch {
	case errors.Is(result, errWindowClosed):
		stage, code = "deferred", "transfer_window_closed"
	case result != nil:
		code = errorCode(result)
		stage = "failed"
		if j.state == "unconfirmed" || j.state == "running" {
			stage = "unconfirmed"
		}
	}
	args := []any{"account", anonymousID(e.opts.AccountID),
		"rule", anonymousID(e.opts.AccountID + "\x00" + scope),
		"job_id", anonymousID(e.opts.AccountID + "\x00" + j.id), "stage", stage,
		"counts", st, "elapsed_ms", time.Since(started).Milliseconds(), "error_code", code,
		"last_success", st.LastSuccess}
	if j.read != "" {
		args = append(args, "read_source", j.read)
	}
	e.opts.Logger.InfoContext(ctx, "job_progress", args...)
}

type contentLock struct {
	mu   sync.Mutex
	refs int
}

func (e *Engine) lockContent(j *job) func() {
	key := j.md5 + ":" + strconv.FormatInt(j.size, 10)
	e.contentMu.Lock()
	lock := e.contents[key]
	if lock == nil {
		lock = &contentLock{}
		e.contents[key] = lock
	}
	lock.refs++
	e.contentMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		e.contentMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(e.contents, key)
		}
		e.contentMu.Unlock()
	}
}

func sameContent(n bridge.Node, c bridge.Content) bool {
	return n.ID != "" && n.MD5 != "" && strings.EqualFold(n.MD5, c.MD5) && n.Size == c.Size
}

func (e *Engine) setState(ctx context.Context, j *job, state, code, exclusion string) error {
	_, err := e.store.db.ExecContext(ctx, `UPDATE assets SET state=?,error_code=?,exclusion=?,stage_name=?,md5=?,sha1=?,size=?,node_id=?,attempts=?,next_at=?
		WHERE account_id=? AND id=?`, state, code, exclusion, j.stage, j.md5, j.sha1, j.size, j.node,
		j.attempts, j.next, e.opts.AccountID, j.id)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	j.state = state
	return nil
}

// deferUpload persists a job as deferred without consuming a retry. The window
// state, not a stored timestamp, decides when it becomes dispatchable again, so
// an edited window applies to the next dispatch.
func (e *Engine) deferUpload(ctx context.Context, j *job) error {
	if e.currentWindow() == nil {
		return nil
	}
	j.next = 0
	if err := e.setState(ctx, j, "deferred", "", ""); err != nil {
		return err
	}
	return errWindowClosed
}

func (e *Engine) confirm(ctx context.Context, j *job) error {
	stage := j.stage
	j.stage = ""
	j.attempts, j.next = 0, 0
	if err := e.setState(ctx, j, "confirmed", "", ""); err != nil {
		return err
	}
	e.cleanupStage(ctx, stage)
	return nil
}

// reconcile resolves an uncertain write by authoritative remote evidence. A
// unique verified match clears the job. A complete lookup that proves no copy
// exists leaves a real name conflict explicit; every other outcome keeps the
// uncertainty and the staged original.
func (e *Engine) reconcile(ctx context.Context, j *job, content bridge.Content, writeErr error) error {
	existing, err := readRetry(ctx, func() (bridge.Node, error) { return e.target.FindExisting(ctx, content) })
	if err != nil {
		j.state = "unconfirmed"
		return err
	}
	if existing.ID == "" {
		j.state = "unconfirmed"
		if errorCode(writeErr) == bridge.Conflict {
			return errors.New(bridge.Conflict)
		}
		return errors.New(bridge.Unconfirmed)
	}
	if !sameContent(existing, content) {
		j.state = "unconfirmed"
		return errors.New(bridge.Unconfirmed)
	}
	return e.reuse(ctx, j, existing, content.ParentID)
}

// reuse durably maps a verified remote node instead of uploading it. A node in
// the target folder is a normal confirmation; a verified copy elsewhere in the
// account is recorded as existing so membership can still be applied later.
// Either way the staged original is no longer needed.
func (e *Engine) reuse(ctx context.Context, j *job, n bridge.Node, target string) error {
	stage, node, attempts, next := j.stage, j.node, j.attempts, j.next
	j.node, j.stage = n.ID, ""
	j.attempts, j.next = 0, 0
	state := "confirmed"
	if n.Placement(target) != bridge.PlacementTarget {
		state = "existing"
	}
	if err := e.setState(ctx, j, state, "", ""); err != nil {
		// Restore the job so the failure path persists the original staging
		// reference and backoff instead of orphaning the staged file.
		j.node, j.stage, j.attempts, j.next = node, stage, attempts, next
		return err
	}
	e.cleanupStage(ctx, stage)
	return nil
}

func (e *Engine) skip(ctx context.Context, j *job, code string) error {
	stage := j.stage
	j.stage, j.md5, j.sha1, j.size = "", "", "", 0
	if err := e.setState(ctx, j, "skipped", "", code); err != nil {
		return err
	}
	e.cleanupStage(ctx, stage)
	return nil
}

// cleanupStage removes a staged file after its durable state no longer references it.
// A failed unlink must not downgrade a confirmed or skipped asset; the orphan is
// removed by cleanStaging at the next startup.
func (e *Engine) cleanupStage(ctx context.Context, stage string) {
	if err := e.store.removeStage(stage); err != nil && e.opts.Logger != nil {
		e.opts.Logger.WarnContext(ctx, "staging_cleanup_failed", "error_code", "storage_unavailable")
	}
}

func (e *Engine) fail(ctx context.Context, j job, err error) error {
	ctx = context.WithoutCancel(ctx)
	code := errorCode(err)
	j.attempts++
	j.next = e.opts.Now().Add(retryDelay(j.attempts-1, err)).UnixMilli()
	state := "blocked"
	if j.state == "unconfirmed" || j.state == "running" {
		state = "unconfirmed"
	} else if code == bridge.Authentication || code == bridge.Permission || code == bridge.RateLimit ||
		code == bridge.Unavailable || code == "staging_quota" || code == "storage_unavailable" || code == "canceled" {
		state = "failed"
	}
	if state == "failed" && j.attempts >= maxAttempts {
		state = "blocked"
	}
	if dbErr := e.setState(ctx, &j, state, code, ""); dbErr != nil {
		return dbErr
	}
	if code == bridge.Authentication || code == bridge.Permission {
		return e.accountError(ctx, err)
	}
	return errors.New(code)
}

func (e *Engine) stage(ctx context.Context, j *job) (result error) {
	dir := filepath.Join(e.store.dir, "staging")
	e.store.staging.Lock()
	if e.store.stageFiles >= e.opts.MaxStagingFiles || j.asset.Size > e.opts.MaxStagingBytes-e.store.stageBytes {
		e.store.staging.Unlock()
		return errors.New("staging_quota")
	}
	f, err := os.CreateTemp(dir, "original-")
	if err == nil {
		e.store.stageFiles++
	}
	e.store.staging.Unlock()
	if err != nil {
		return errors.New("storage_unavailable")
	}
	path := f.Name()
	defer func() {
		f.Close()
		if j.stage == "" {
			result = errors.Join(result, e.store.removeStage(filepath.Base(path)))
		}
	}()
	original, err := e.openOriginal(ctx, j)
	if err != nil {
		return err
	}
	defer original.Close()
	m, s := md5.New(), sha1.New()
	n, err := io.CopyBuffer(io.MultiWriter(&quotaWriter{e: e, file: f}, m, s),
		&rateReader{e: e, ctx: ctx, reader: original}, make([]byte, 32<<10))
	if err != nil {
		return err
	}
	digest, checksum := hex.EncodeToString(m.Sum(nil)), base64.StdEncoding.EncodeToString(s.Sum(nil))
	if original.direct {
		// A mounted original must still be the bytes the source recorded. A
		// mutation during the read, or a stale mapping, is a changed source:
		// the digest is never recorded from an unstable or mismatched read.
		if !original.stable() || (j.asset.Size >= 0 && n != j.asset.Size) ||
			(j.asset.SHA1 != "" && checksum != j.asset.SHA1) {
			return errors.New("source_changed")
		}
	} else if j.asset.Size >= 0 && n != j.asset.Size {
		return errors.New("size_mismatch")
	} else if j.asset.SHA1 != "" && checksum != j.asset.SHA1 {
		return errors.New("checksum_mismatch")
	}
	if err = f.Sync(); err != nil {
		return errors.New("storage_unavailable")
	}
	if err = f.Close(); err != nil {
		return errors.New("storage_unavailable")
	}
	d, err := os.Open(dir)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	err = errors.Join(d.Sync(), d.Close())
	if err != nil {
		return errors.New("storage_unavailable")
	}
	j.stage, j.md5, j.size = filepath.Base(path), digest, n
	j.sha1 = checksum
	if err := e.setState(ctx, j, "staged", "", ""); err != nil {
		j.stage = ""
		return err
	}
	if original.direct {
		e.store.recordDirect(e.opts.AccountID, n)
	}
	return nil
}

// original is one readable original and whether it came from an approved
// read-only mount. stable is nil for API streams, whose length and checksum are
// already negotiated by the source contract.
type original struct {
	io.ReadCloser
	direct bool
	stable func() bool
}

// openOriginal returns the bytes for one asset. An approved, eligible mount is
// read in place and the Immich original is never requested; an absent or
// ineligible mount falls back to the API stream. A suspicious path or changed
// source identity fails closed instead of downloading other bytes.
func (e *Engine) openOriginal(ctx context.Context, j *job) (*original, error) {
	fromAPI := func() (*original, error) {
		r, err := readRetry(ctx, func() (io.ReadCloser, error) { return e.source.Original(ctx, j.id) })
		if err != nil {
			return nil, err
		}
		j.read = "api"
		return &original{ReadCloser: r}, nil
	}
	if !e.direct.Configured() {
		return fromAPI()
	}
	a, err := readRetry(ctx, func() (bridge.Asset, error) { return e.source.Asset(ctx, j.id) })
	if err != nil {
		return nil, err
	}
	if a.ID != j.id {
		return nil, errors.New(bridge.Protocol)
	}
	if (j.asset.Size >= 0 && a.Size >= 0 && a.Size != j.asset.Size) ||
		(j.asset.SHA1 != "" && a.SHA1 != "" && a.SHA1 != j.asset.SHA1) {
		return nil, errors.New("source_changed")
	}
	file, outcome := e.direct.Open(a.OriginalPath)
	if file == nil {
		if outcome.FailClosed() {
			e.store.recordDirectFailure(e.opts.AccountID, outcome)
			return nil, errors.New(outcome.Code())
		}
		e.store.recordDirectFallback(e.opts.AccountID, outcome)
		return fromAPI()
	}
	j.read = "direct"
	return &original{ReadCloser: file, direct: true, stable: file.Stable}, nil
}

type rateReader struct {
	e      *Engine
	ctx    context.Context
	reader io.Reader
}

func (r *rateReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	p = p[:min(len(p), 32<<10)]
	n, err := r.reader.Read(p)
	if n == 0 || r.e.opts.BytesPerSecond == 0 {
		return n, err
	}
	r.e.rateMu.Lock()
	start := time.Now()
	if r.e.rateAt.After(start) {
		start = r.e.rateAt
	}
	r.e.rateAt = start.Add(time.Duration(float64(n) / float64(r.e.opts.BytesPerSecond) * float64(time.Second)))
	wait := time.Until(r.e.rateAt)
	r.e.rateMu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	case <-timer.C:
		return n, err
	}
}

type quotaWriter struct {
	e    *Engine
	file *os.File
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	s := w.e.store
	s.staging.Lock()
	if int64(len(p)) > w.e.opts.MaxStagingBytes-s.stageBytes {
		s.staging.Unlock()
		return 0, errors.New("staging_quota")
	}
	s.stageBytes += int64(len(p))
	s.staging.Unlock()
	n, err := w.file.Write(p)
	s.staging.Lock()
	s.stageBytes -= int64(len(p) - n)
	s.staging.Unlock()
	if err != nil {
		return n, errors.New("storage_unavailable")
	}
	return n, nil
}
