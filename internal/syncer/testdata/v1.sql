CREATE TABLE accounts (
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
	PRAGMA user_version=1;
