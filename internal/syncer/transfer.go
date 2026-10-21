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
}

const eligibleSQL = `(EXISTS(SELECT 1 FROM scope_assets m JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
	WHERE m.account_id=a.account_id AND m.asset_id=a.id AND r.active=1 AND m.generation=r.generation AND r.error_code='')
	OR EXISTS(SELECT 1 FROM signals m JOIN rules r ON r.account_id=m.account_id AND r.scope=m.scope
	WHERE m.account_id=a.account_id AND m.asset_id=a.id AND r.active=1 AND m.generation=r.generation AND r.error_code=''))`

func (e *Engine) drain(ctx context.Context, attempted map[string]bool) error {
	var selectMu sync.Mutex
	results := make(chan error, 64+e.opts.Workers)
	var workers sync.WaitGroup
	for range min(e.opts.Workers, e.opts.MaxStagingFiles) {
		workers.Go(func() {
			for {
				if ctx.Err() != nil {
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
				results <- e.transfer(ctx, j)
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
	rows, err := e.store.db.QueryContext(ctx, `SELECT id,metadata,state,stage_name,md5,sha1,size,node_id,attempts FROM assets a
		WHERE account_id=? AND state IN ('pending','staged','unconfirmed','failed') AND next_at<=unixepoch('subsec')*1000 AND attempts<5
		AND error_code!='source_changed'
		AND `+eligibleSQL+` ORDER BY priority DESC,id LIMIT 64`, e.opts.AccountID)
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
		if result != nil {
			result = e.fail(ctx, *j, result)
		}
		if unlock != nil {
			unlock()
		}
		e.logJob(context.WithoutCancel(ctx), j, started, result)
	}()
	if j.state != "unconfirmed" {
		if j.asset.ExcludedReason != "" {
			code := j.asset.ExcludedReason
			switch code {
			case "hidden", "locked", "offline", "trashed", "unsupported_type", "external_library_checksum", "unknown_checksum_provenance":
			default:
				code = "source_excluded"
			}
			return e.skip(ctx, j, code)
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
		if kind == "VIDEO" && !e.opts.IncludeVideos {
			return e.skip(ctx, j, "video_disabled")
		}
		if kind != "IMAGE" && kind != "VIDEO" {
			return e.skip(ctx, j, "unsupported_type")
		}
		if j.asset.MIME == "" {
			j.asset.MIME = mime.TypeByExtension(filepath.Ext(j.asset.Name))
		}
		if j.stage == "" {
			if err := e.stage(ctx, j); err != nil {
				return err
			}
		}
	}
	unlock = e.lockContent(j)
	content := bridge.Content{Name: j.asset.Name, MIME: j.asset.MIME, MD5: j.md5, Size: j.size, TakenAt: j.asset.TakenAt}
	var known, state string
	err := e.store.db.QueryRowContext(ctx, `SELECT node_id,state FROM assets
		WHERE account_id=? AND md5=? AND size=? AND state IN ('confirmed','running','unconfirmed')
		ORDER BY (node_id!='') DESC,(state='confirmed') DESC LIMIT 1`,
		e.opts.AccountID, j.md5, j.size).Scan(&known, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.New("storage_unavailable")
	}
	if err == nil {
		j.state = "unconfirmed"
		if known != "" {
			j.node = known
			n, err := readRetry(ctx, func() (bridge.Node, error) { return e.target.GetNode(ctx, known) })
			if err != nil {
				return err
			}
			if n.ID != known || !matches(n, content) {
				return errors.New(bridge.Unconfirmed)
			}
			return e.confirm(ctx, j)
		}
	}
	nodes, err := readRetry(ctx, func() ([]bridge.Node, error) { return e.target.Find(ctx, content) })
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if matches(n, content) {
			verified, err := readRetry(ctx, func() (bridge.Node, error) { return e.target.GetNode(ctx, n.ID) })
			if err != nil {
				return err
			}
			if !matches(verified, content) || verified.ID != n.ID {
				j.state = "unconfirmed"
				return errors.New(bridge.Unconfirmed)
			}
			j.node = verified.ID
			return e.confirm(ctx, j)
		}
	}
	for _, n := range nodes {
		if n.ID == "" || n.MD5 == "" || n.Size < 0 || n.ParentID != content.ParentID {
			j.state = "unconfirmed"
			return errors.New(bridge.Unconfirmed)
		}
		if n.Name == content.Name && n.ParentID == content.ParentID {
			return errors.New(bridge.Conflict)
		}
	}
	if j.state == "unconfirmed" {
		return errors.New(bridge.Unconfirmed)
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
	if err := e.setState(ctx, j, "running", ""); err != nil {
		return err
	}
	node, uploadErr := e.target.Upload(ctx, content, &rateReader{e: e, ctx: ctx, reader: f})
	j.state = "unconfirmed"
	if uploadErr != nil {
		var rejection *bridge.Error
		if errors.As(uploadErr, &rejection) && (rejection.Code == bridge.Authentication ||
			rejection.Code == bridge.Permission || rejection.Code == bridge.RateLimit) {
			j.state = "staged"
		}
		return uploadErr
	}
	j.node = node.ID
	if err := e.setState(context.WithoutCancel(ctx), j, "unconfirmed", bridge.Unconfirmed); err != nil {
		return err
	}
	if !matches(node, content) {
		return errors.New(bridge.Unconfirmed)
	}
	node, err = readRetry(ctx, func() (bridge.Node, error) { return e.target.GetNode(ctx, node.ID) })
	if err != nil {
		return err
	}
	if !matches(node, content) || node.ID != j.node {
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
	if result != nil {
		code = errorCode(result)
	}
	stage := j.state
	if result != nil {
		stage = "failed"
		if j.state == "unconfirmed" || j.state == "running" {
			stage = "unconfirmed"
		}
	}
	e.opts.Logger.InfoContext(ctx, "job_progress", "account", anonymousID(e.opts.AccountID),
		"rule", anonymousID(e.opts.AccountID+"\x00"+scope),
		"job_id", anonymousID(e.opts.AccountID+"\x00"+j.id), "stage", stage,
		"counts", st, "elapsed_ms", time.Since(started).Milliseconds(), "error_code", code, "last_success", st.LastSuccess)
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

func matches(n bridge.Node, c bridge.Content) bool {
	return n.ID != "" && n.MD5 != "" && strings.EqualFold(n.MD5, c.MD5) && n.Size == c.Size && n.ParentID == c.ParentID
}

func (e *Engine) setState(ctx context.Context, j *job, state, code string) error {
	_, err := e.store.db.ExecContext(ctx, `UPDATE assets SET state=?,error_code=?,stage_name=?,md5=?,sha1=?,size=?,node_id=?,attempts=?,next_at=?
		WHERE account_id=? AND id=?`, state, code, j.stage, j.md5, j.sha1, j.size, j.node, j.attempts, j.next, e.opts.AccountID, j.id)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	j.state = state
	return nil
}

func (e *Engine) confirm(ctx context.Context, j *job) error {
	stage := j.stage
	j.stage = ""
	j.attempts, j.next = 0, 0
	if err := e.setState(ctx, j, "confirmed", ""); err != nil {
		return err
	}
	e.cleanupStage(ctx, stage)
	return nil
}

func (e *Engine) skip(ctx context.Context, j *job, code string) error {
	stage := j.stage
	j.stage, j.md5, j.sha1, j.size = "", "", "", 0
	if err := e.setState(ctx, j, "skipped", code); err != nil {
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
	j.next = time.Now().Add(retryDelay(j.attempts-1, err)).UnixMilli()
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
	if dbErr := e.setState(ctx, &j, state, code); dbErr != nil {
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
	r, err := readRetry(ctx, func() (io.ReadCloser, error) { return e.source.Original(ctx, j.id) })
	if err != nil {
		return err
	}
	defer r.Close()
	m, s := md5.New(), sha1.New()
	n, err := io.CopyBuffer(io.MultiWriter(&quotaWriter{e: e, file: f}, m, s),
		&rateReader{e: e, ctx: ctx, reader: r}, make([]byte, 32<<10))
	if err != nil {
		return err
	}
	if j.asset.Size >= 0 && n != j.asset.Size {
		return errors.New("size_mismatch")
	}
	if j.asset.SHA1 != "" && base64.StdEncoding.EncodeToString(s.Sum(nil)) != j.asset.SHA1 {
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
	j.stage, j.md5, j.size = filepath.Base(path), hex.EncodeToString(m.Sum(nil)), n
	j.sha1 = base64.StdEncoding.EncodeToString(s.Sum(nil))
	if err := e.setState(ctx, j, "staged", ""); err != nil {
		j.stage = ""
		return err
	}
	return nil
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
