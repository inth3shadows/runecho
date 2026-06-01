package snapshot

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection for snapshot storage.
type DB struct {
	conn *sql.DB
}

// Open opens (or creates) the snapshot DB at path, sets pragmas, and migrates schema.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}

	// Single writer — avoid "database is locked" on WAL.
	conn.SetMaxOpenConns(1)

	db := &DB{conn: conn}
	if err := db.setPragmas(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := db.integrityCheck(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

// integrityCheck runs PRAGMA quick_check (cheaper than integrity_check, sufficient
// for catching corruption on open). Durability guarantee: never serve a corrupt DB.
func (db *DB) integrityCheck() error {
	var result string
	if err := db.conn.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("quick_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check failed: %s", result)
	}
	return nil
}

// BackupTo writes an atomic, consistent single-file backup using VACUUM INTO.
// The destination must not already exist (SQLite requirement).
func (db *DB) BackupTo(path string) error {
	if _, err := db.conn.Exec("VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("vacuum into %q: %w", path, err)
	}
	return nil
}

func (db *DB) setPragmas() error {
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		// Wait up to 5s for a competing writer instead of failing immediately
		// with "database is locked" — MaxOpenConns(1) only serializes in-process,
		// so a second runecho process (CLI write vs. MCP first-run migrate) can
		// still contend for the write lock.
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.conn.Exec(p); err != nil {
			return fmt.Errorf("pragma %q: %w", p, err)
		}
	}
	return nil
}

// migration brings the schema from version N to N+1, where N is its index in the
// migrations slice. Each runs in its own transaction; user_version is bumped only
// on commit, so a partial upgrade can never leave a torn schema.
type migration func(*sql.Tx) error

var migrations = []migration{
	migrateV1, // 0 → 1: baseline snapshots/files/symbols
	migrateV2, // 1 → 2: central-store repos registry + snapshots.repo_id
}

// SchemaVersion is the latest schema version this binary understands.
var SchemaVersion = len(migrations)

func (db *DB) migrate() error {
	var current int
	if err := db.conn.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("db schema version %d is newer than this binary supports (%d); upgrade runecho", current, len(migrations))
	}
	for v := current; v < len(migrations); v++ {
		tx, err := db.conn.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", v+1, err)
		}
		if err := migrations[v](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate to v%d: %w", v+1, err)
		}
		// PRAGMA user_version cannot be parameterized; v+1 is a trusted loop index.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set user_version %d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", v+1, err)
		}
	}
	return nil
}

func migrateV1(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS snapshots (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id  TEXT NOT NULL,
			label       TEXT NOT NULL,
			timestamp   TEXT NOT NULL,
			root        TEXT NOT NULL,
			root_hash   TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_session ON snapshots(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_label   ON snapshots(label)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_ts      ON snapshots(timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_root    ON snapshots(root)`,

		`CREATE TABLE IF NOT EXISTS files (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_id  INTEGER NOT NULL REFERENCES snapshots(id),
			path         TEXT NOT NULL,
			content_hash TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_files_snapshot ON files(snapshot_id)`,

		`CREATE TABLE IF NOT EXISTS symbols (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			file_id INTEGER NOT NULL REFERENCES files(id),
			name    TEXT NOT NULL,
			kind    TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols(file_id)`,
		`CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name)`,
	}
	return execAll(tx, stmts)
}

// migrateV2 introduces the central-store repos registry. snapshots.repo_id is the
// stable identity spine: a repo keeps its id across path moves, and all reads scope
// by repo_id instead of the root path string. Existing rows get repo_id=NULL (the
// central store starts empty, so there are none in practice).
func migrateV2(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS repos (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT NOT NULL UNIQUE,
			path         TEXT NOT NULL UNIQUE,
			file_cap     INTEGER NOT NULL DEFAULT 0,
			enrolled_at  TEXT NOT NULL,
			last_indexed TEXT,
			parse_errors INTEGER NOT NULL DEFAULT 0
		)`,
		`ALTER TABLE snapshots ADD COLUMN repo_id INTEGER REFERENCES repos(id)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_repo ON snapshots(repo_id)`,
	}
	return execAll(tx, stmts)
}

func execAll(tx *sql.Tx, stmts []string) error {
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("exec %q: %w", s, err)
		}
	}
	return nil
}

// HealthInfo summarizes store-wide health (self-observing guarantee).
type HealthInfo struct {
	SchemaVersion int    `json:"schema_version"`
	Integrity     string `json:"integrity"` // "ok" or the failure detail
	RepoCount     int    `json:"repo_count"`
}

// Health reports schema version, integrity (live quick_check), and repo count.
func (db *DB) Health() (HealthInfo, error) {
	var h HealthInfo
	if err := db.conn.QueryRow("PRAGMA user_version").Scan(&h.SchemaVersion); err != nil {
		return h, fmt.Errorf("read user_version: %w", err)
	}
	var qc string
	if err := db.conn.QueryRow("PRAGMA quick_check").Scan(&qc); err != nil {
		return h, fmt.Errorf("quick_check: %w", err)
	}
	h.Integrity = qc
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM repos").Scan(&h.RepoCount); err != nil {
		return h, fmt.Errorf("count repos: %w", err)
	}
	return h, nil
}

// CountSnapshots returns the number of snapshots stored for repoID.
func (db *DB) CountSnapshots(repoID int64) (int, error) {
	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE repo_id = ?`, repoID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count snapshots: %w", err)
	}
	return n, nil
}

// SymbolsForLatestSnapshot returns the set of symbol names recorded in the most
// recent snapshot for repoID. Returns an empty map (no error) when there are no
// snapshots yet.
func (db *DB) SymbolsForLatestSnapshot(repoID int64) (map[string]struct{}, error) {
	rows, err := db.conn.Query(`
		SELECT DISTINCT s.name
		FROM symbols s
		JOIN files f ON s.file_id = f.id
		WHERE f.snapshot_id = (
			SELECT id FROM snapshots WHERE repo_id = ? ORDER BY timestamp DESC, id DESC LIMIT 1
		)`, repoID)
	if err != nil {
		return nil, fmt.Errorf("symbols for latest snapshot: %w", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// Close closes the underlying connection.
func (db *DB) Close() error {
	return db.conn.Close()
}
