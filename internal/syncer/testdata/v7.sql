-- Schema version 7, after migrateP6() makes filter exclusions explicit and reevaluable.
CREATE TABLE accounts (
		id TEXT PRIMARY KEY,
		source_identity TEXT NOT NULL DEFAULT '',
		target_identity TEXT NOT NULL DEFAULT '',
		pause_reason TEXT NOT NULL DEFAULT '',
		last_success TEXT,
		error_code TEXT NOT NULL DEFAULT ''
	, filter_fingerprint TEXT NOT NULL DEFAULT '');
CREATE TABLE rules (
		account_id TEXT NOT NULL REFERENCES accounts(id),
		scope TEXT NOT NULL,
		album_id TEXT NOT NULL,
		active INTEGER NOT NULL DEFAULT 1,
		generation INTEGER NOT NULL DEFAULT 1,
		cursor TEXT NOT NULL DEFAULT '',
		complete INTEGER NOT NULL DEFAULT 0,
		expected INTEGER NOT NULL DEFAULT -1,
		error_code TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '', completed_at TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0, next_at INTEGER NOT NULL DEFAULT 0, origin TEXT NOT NULL DEFAULT 'explicit', discovered INTEGER NOT NULL DEFAULT 1, discovered_at TEXT NOT NULL DEFAULT '',
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
		priority INTEGER NOT NULL DEFAULT 0, prior_state TEXT NOT NULL DEFAULT '', favorite INTEGER NOT NULL DEFAULT -1, exclusion TEXT NOT NULL DEFAULT '',
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
CREATE TABLE workflows (
		account_id TEXT PRIMARY KEY REFERENCES accounts(id),
		workflow_id TEXT NOT NULL DEFAULT '',
		contract TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT '',
		error_code TEXT NOT NULL DEFAULT '',
		desired_hash TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT '');
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
CREATE INDEX asset_state_errors ON assets(account_id,state,error_code);

PRAGMA user_version=7;
