package syncer

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/thedavidweng/immich-amazon-sync/internal/direct"
	_ "modernc.org/sqlite"
)

type Store struct {
	db         *sql.DB
	dir        string
	lock       *os.File
	readOnly   bool
	runs       sync.Map
	staging    sync.Mutex
	stageBytes int64
	stageFiles int
	directMu   sync.Mutex
	direct     map[string]*DirectCounters
}

// DirectCounters reports read-only mount diagnostics for one account. Counters
// are process-local: they describe this process and reset on restart. No path,
// filename or remote payload is ever recorded. BytesAvoided counts originals
// read from an approved mount; APIBytes counts originals downloaded from the
// Immich API.
type DirectCounters struct {
	Reads        int            `json:"reads"`
	BytesAvoided int64          `json:"bytes_avoided"`
	APIBytes     int64          `json:"api_bytes"`
	Fallbacks    map[string]int `json:"fallbacks,omitempty"`
	Failures     map[string]int `json:"failures,omitempty"`
}

func (s *Store) directFor(account string) *DirectCounters {
	c := s.direct[account]
	if c == nil {
		c = &DirectCounters{Fallbacks: map[string]int{}, Failures: map[string]int{}}
		s.direct[account] = c
	}
	return c
}

func (s *Store) recordDirect(account string, n int64) {
	s.directMu.Lock()
	defer s.directMu.Unlock()
	c := s.directFor(account)
	c.Reads++
	c.BytesAvoided += n
}

func (s *Store) recordAPI(account string, n int64) {
	s.directMu.Lock()
	defer s.directMu.Unlock()
	s.directFor(account).APIBytes += n
}

func (s *Store) recordDirectFallback(account string, outcome direct.Outcome) {
	s.directMu.Lock()
	defer s.directMu.Unlock()
	s.directFor(account).Fallbacks[string(outcome)]++
}

func (s *Store) recordDirectFailure(account string, outcome direct.Outcome) {
	s.directMu.Lock()
	defer s.directMu.Unlock()
	s.directFor(account).Failures[string(outcome)]++
}

func (s *Store) directCounters(account string) DirectCounters {
	s.directMu.Lock()
	defer s.directMu.Unlock()
	recorded := s.directFor(account)
	counters := DirectCounters{Reads: recorded.Reads, BytesAvoided: recorded.BytesAvoided,
		APIBytes:  recorded.APIBytes,
		Fallbacks: make(map[string]int, len(recorded.Fallbacks)),
		Failures:  make(map[string]int, len(recorded.Failures))}
	for outcome, count := range recorded.Fallbacks {
		counters.Fallbacks[outcome] = count
	}
	for outcome, count := range recorded.Failures {
		counters.Failures[outcome] = count
	}
	return counters
}

// schemaVersion is the highest ledger schema this binary understands. An
// unknown version — newer than this head, or an out-of-range header value —
// fails closed instead of being migrated, treated as current or rewritten.
const schemaVersion = 10

// errUnsupportedSchema is the finite failure for an unknown ledger schema. The
// CLI and service code classify it as a local-storage error, so the operator
// sees why an older binary refused a newer volume.
var errUnsupportedSchema = errors.New("unsupported_schema")

// errSchemaMigrationRequired is the finite failure for a read-only report of a
// ledger this binary could read only after migrating it. A status command never
// migrates and never reports counters from another schema version as current.
var errSchemaMigrationRequired = errors.New("schema_migration_required")

// errLedgerAbsent is the finite failure for a report before any ledger exists.
// A read-only open never creates one, so the operator learns that a sync pass
// has not run yet instead of being shown a fabricated empty store.
var errLedgerAbsent = errors.New("ledger_absent")

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, errors.New("storage_unavailable")
	}
	lock, err := lockDirectory(filepath.Join(dataDir, "writer.lock"))
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dataDir, lock: lock, direct: make(map[string]*DirectCounters)}
	dbPath := filepath.Join(dataDir, "sync.sqlite")
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		s.Close()
		return nil, errors.New("storage_unavailable")
	}
	f.Close()
	s.db, err = sql.Open("sqlite", dbPath)
	if err == nil {
		s.db.SetMaxOpenConns(1)
		_, err = s.db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
			PRAGMA synchronous=FULL;`)
	}
	if err == nil {
		err = s.migrate()
	}
	if err == nil {
		_, err = s.db.Exec("CREATE INDEX IF NOT EXISTS asset_state_errors ON assets(account_id,state,error_code)")
	}
	if err == nil {
		_, err = s.db.Exec("UPDATE assets SET state='unconfirmed',error_code='upload_unconfirmed' WHERE state='running'")
	}
	if err == nil {
		err = s.cleanStaging()
	}
	if err != nil {
		s.Close()
		if errors.Is(err, errUnsupportedSchema) {
			return nil, err
		}
		return nil, errors.New("storage_unavailable")
	}
	return s, nil
}

// OpenReadOnly opens the existing durable ledger for reporting only. It takes
// no writer lock, performs no migration, never rewrites a crash marker and
// leaves staging untouched, so a report works beside a running service. A
// ledger that is absent, older than this binary or newer than it fails with a
// finite code instead of silently reporting counters from another schema.
func OpenReadOnly(dataDir string) (*Store, error) {
	dbPath := filepath.Join(dataDir, "sync.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errLedgerAbsent
		}
		return nil, errors.New("storage_unavailable")
	}
	s := &Store{dir: dataDir, readOnly: true, direct: make(map[string]*DirectCounters)}
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}).String()
	var err error
	s.db, err = sql.Open("sqlite", dsn)
	if err == nil {
		s.db.SetMaxOpenConns(1)
		_, err = s.db.Exec(`PRAGMA query_only=ON; PRAGMA busy_timeout=5000;`)
	}
	var version int
	if err == nil {
		err = s.db.QueryRow("PRAGMA user_version").Scan(&version)
	}
	if err != nil {
		s.Close()
		return nil, errors.New("storage_unavailable")
	}
	if version != schemaVersion {
		s.Close()
		if version < 0 || version > schemaVersion {
			return nil, errUnsupportedSchema
		}
		return nil, errSchemaMigrationRequired
	}
	return s, nil
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (s *Store) cleanStaging() error {
	dir := filepath.Join(s.dir, "staging")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	rows, err := s.db.Query("SELECT stage_name FROM assets WHERE stage_name!=''")
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		used[name] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range files {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("storage_unavailable")
		}
		if strings.HasPrefix(entry.Name(), "original-") && !used[entry.Name()] {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
			continue
		}
		s.stageBytes += info.Size()
		s.stageFiles++
	}
	return nil
}

func (s *Store) removeStage(name string) error {
	if name == "" {
		return nil
	}
	if filepath.Base(name) != name || !strings.HasPrefix(name, "original-") {
		return errors.New("storage_unavailable")
	}
	s.staging.Lock()
	defer s.staging.Unlock()
	path := filepath.Join(s.dir, "staging", name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	if err = os.Remove(path); err != nil {
		return errors.New("storage_unavailable")
	}
	s.stageBytes -= info.Size()
	s.stageFiles--
	return nil
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > schemaVersion {
		return errUnsupportedSchema
	}
	steps := []func() error{s.migrateP1, s.migrateP2, s.migrateP3, s.migrateP4,
		s.migrateP5, s.migrateP6, s.migrateP7, s.migrateP8, s.migrateP9}
	for step := version; step < len(steps)+1 && version != 0; step++ {
		if err := steps[step-1](); err != nil {
			return err
		}
	}
	if version != 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TABLE accounts (
		id TEXT PRIMARY KEY,
		source_identity TEXT NOT NULL DEFAULT '',
		target_identity TEXT NOT NULL DEFAULT '',
		pause_reason TEXT NOT NULL DEFAULT '',
		last_success TEXT,
		error_code TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE rules (
		account_id TEXT NOT NULL REFERENCES accounts(id),
		scope TEXT NOT NULL,
		album_id TEXT NOT NULL,
		active INTEGER NOT NULL DEFAULT 1,
		generation INTEGER NOT NULL DEFAULT 1,
		cursor TEXT NOT NULL DEFAULT '',
		complete INTEGER NOT NULL DEFAULT 0,
		expected INTEGER NOT NULL DEFAULT -1,
		error_code TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(account_id,scope)
	);
	CREATE TABLE assets (
		account_id TEXT NOT NULL REFERENCES accounts(id),
		id TEXT NOT NULL,
		metadata TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'pending',
		error_code TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,
		next_at INTEGER NOT NULL DEFAULT 0,
		stage_name TEXT NOT NULL DEFAULT '',
		md5 TEXT NOT NULL DEFAULT '',
		sha1 TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		node_id TEXT NOT NULL DEFAULT '',
		priority INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(account_id,id)
	);
	CREATE TABLE scope_assets (
		account_id TEXT NOT NULL,
		scope TEXT NOT NULL,
		asset_id TEXT NOT NULL,
		generation INTEGER NOT NULL,
		PRIMARY KEY(account_id,scope,asset_id),
		FOREIGN KEY(account_id,scope) REFERENCES rules(account_id,scope),
		FOREIGN KEY(account_id,asset_id) REFERENCES assets(account_id,id)
	);
	CREATE TABLE scan_cursors (
		account_id TEXT NOT NULL,
		scope TEXT NOT NULL,
		token TEXT NOT NULL,
		PRIMARY KEY(account_id,scope,token),
		FOREIGN KEY(account_id,scope) REFERENCES rules(account_id,scope)
	);
	CREATE INDEX work_queue ON assets(account_id,state,next_at,priority);
	PRAGMA user_version=1;`)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	applied := []func() error{s.migrateP1, s.migrateP2, s.migrateP3, s.migrateP4,
		s.migrateP5, s.migrateP6, s.migrateP7, s.migrateP8, s.migrateP9}
	for _, step := range applied {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// migrateP3 records how each album rule came into scope. Explicit rules are
// configured; discovered rules follow the accessible album listing. Existing
// mappings, cursors and memberships are preserved and no rule is widened.
func (s *Store) migrateP3() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`ALTER TABLE rules ADD COLUMN origin TEXT NOT NULL DEFAULT 'explicit';
	ALTER TABLE rules ADD COLUMN discovered INTEGER NOT NULL DEFAULT 1;
	ALTER TABLE rules ADD COLUMN discovered_at TEXT NOT NULL DEFAULT '';
	PRAGMA user_version=4;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateP1() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`ALTER TABLE rules ADD COLUMN name TEXT NOT NULL DEFAULT '';
	ALTER TABLE rules ADD COLUMN completed_at TEXT NOT NULL DEFAULT '';
	ALTER TABLE rules ADD COLUMN started_at TEXT NOT NULL DEFAULT '';
	ALTER TABLE rules ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE rules ADD COLUMN next_at INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE hints (
		account_id TEXT NOT NULL REFERENCES accounts(id),asset_id TEXT NOT NULL,
		priority INTEGER NOT NULL,version INTEGER NOT NULL DEFAULT 1,
		attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,
		error_code TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_id,asset_id));
	CREATE TABLE signals (
		account_id TEXT NOT NULL,scope TEXT NOT NULL,asset_id TEXT NOT NULL,generation INTEGER NOT NULL,
		PRIMARY KEY(account_id,scope,asset_id),
		FOREIGN KEY(account_id,scope) REFERENCES rules(account_id,scope),
		FOREIGN KEY(account_id,asset_id) REFERENCES assets(account_id,id));
	CREATE TABLE albums (
		account_id TEXT NOT NULL,scope TEXT NOT NULL,target_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',applied_name TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'pending',error_code TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,
		baseline INTEGER NOT NULL DEFAULT 0,complete_generation INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(account_id,scope),
		FOREIGN KEY(account_id,scope) REFERENCES rules(account_id,scope));
	CREATE TABLE memberships (
		account_id TEXT NOT NULL,scope TEXT NOT NULL,node_id TEXT NOT NULL,
		owned INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'add',
		error_code TEXT NOT NULL DEFAULT '',attempts INTEGER NOT NULL DEFAULT 0,
		next_at INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(account_id,scope,node_id),
		FOREIGN KEY(account_id,scope) REFERENCES albums(account_id,scope));
	CREATE TABLE incremental (
		account_id TEXT PRIMARY KEY REFERENCES accounts(id),watermark TEXT NOT NULL DEFAULT '',
		since TEXT NOT NULL DEFAULT '',started_at TEXT NOT NULL DEFAULT '',cursor TEXT NOT NULL DEFAULT '',
		active INTEGER NOT NULL DEFAULT 0,error_code TEXT NOT NULL DEFAULT '');
	CREATE TABLE incremental_tokens (
		account_id TEXT NOT NULL REFERENCES incremental(account_id),token TEXT NOT NULL,
		PRIMARY KEY(account_id,token));
	CREATE TABLE incremental_seen (
		account_id TEXT NOT NULL REFERENCES incremental(account_id),asset_id TEXT NOT NULL,
		PRIMARY KEY(account_id,asset_id));
	PRAGMA user_version=2;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateP2() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`ALTER TABLE assets ADD COLUMN prior_state TEXT NOT NULL DEFAULT '';
	PRAGMA user_version=3;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// migrateP4 adds the single durable row that records the owned Immich Workflow.
// Purely additive: existing mappings, cursors, deletion states and staging
// references are untouched, and no account gains workflow scope by migration.
func (s *Store) migrateP4() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TABLE workflows (
		account_id TEXT PRIMARY KEY REFERENCES accounts(id),
		workflow_id TEXT NOT NULL DEFAULT '',
		contract TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT '',
		error_code TEXT NOT NULL DEFAULT '',
		desired_hash TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT '');
	PRAGMA user_version=5;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// migrateP5 adds the source favorite presence column and one durable favorite
// outcome per verified Amazon node. Existing accounts migrate with favorites
// unknown (-1) and no desired rows, so the migration never widens write scope.
func (s *Store) migrateP5() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`ALTER TABLE assets ADD COLUMN favorite INTEGER NOT NULL DEFAULT -1;
	CREATE TABLE favorites (
		account_id TEXT NOT NULL REFERENCES accounts(id),
		node_id TEXT NOT NULL,
		desired INTEGER NOT NULL DEFAULT 0,
		applied INTEGER NOT NULL DEFAULT 0,
		observed INTEGER NOT NULL DEFAULT -1,
		state TEXT NOT NULL DEFAULT 'pending',
		error_code TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,
		next_at INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(account_id,node_id)
	);
	CREATE INDEX favorite_queue ON favorites(account_id,state,next_at);
	PRAGMA user_version=6;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// migrateP6 makes filter exclusions explicit and reevaluable instead of
// overloading transient error codes. Legacy video-toggle skips adopt the new
// media-type reason so a later policy can requeue them.
func (s *Store) migrateP6() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`ALTER TABLE assets ADD COLUMN exclusion TEXT NOT NULL DEFAULT '';
	ALTER TABLE accounts ADD COLUMN filter_fingerprint TEXT NOT NULL DEFAULT '';
	UPDATE assets SET exclusion=error_code,error_code='' WHERE state='skipped' AND error_code!='';
	UPDATE assets SET exclusion='media_type_excluded' WHERE exclusion='video_disabled';
	PRAGMA user_version=7;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// migrateP7 stores only the normalized, categorized usage observation and the
// provider's own calculation timestamp. No raw remote payload or inferred
// capacity is retained. Existing mappings, cursors and deletion state are
// untouched.
func (s *Store) migrateP7() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TABLE usage (
		account_id TEXT PRIMARY KEY REFERENCES accounts(id),
		observed_at TEXT NOT NULL DEFAULT '',
		last_calculated TEXT NOT NULL DEFAULT '',
		categories TEXT NOT NULL DEFAULT '',
		error_code TEXT NOT NULL DEFAULT '',
		hold INTEGER NOT NULL DEFAULT 0
	);
	PRAGMA user_version=8;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// migrateP8 adds the drift-audit timestamp to the existing per-node favorite
// outcome. It is purely additive: desired, ownership, observations and retry
// state are untouched, and a settled row becomes auditable once instead of
// being rewritten or re-starred by the migration.
func (s *Store) migrateP8() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`ALTER TABLE favorites ADD COLUMN checked_at INTEGER NOT NULL DEFAULT 0;
	PRAGMA user_version=9;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// migrateP9 adds the Live Photo pair ledger and the applied per-account pair
// mode. Existing accounts keep the historical still-only behavior: the stored
// mode is empty, no pair exists, and every asset mapping, exclusion, favorite
// and staging reference is untouched.
func (s *Store) migrateP9() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`ALTER TABLE accounts ADD COLUMN live_photo_mode TEXT NOT NULL DEFAULT '';
	CREATE TABLE live_pairs (
		account_id TEXT NOT NULL REFERENCES accounts(id),
		still_id TEXT NOT NULL,
		companion_id TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'pending',
		error_code TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,
		next_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(account_id,still_id),
		FOREIGN KEY(account_id,still_id) REFERENCES assets(account_id,id)
	);
	CREATE INDEX live_pair_queue ON live_pairs(account_id,state,next_at);
	PRAGMA user_version=10;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		err = errors.Join(err, s.lock.Close())
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}
