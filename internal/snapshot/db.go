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
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) setPragmas() error {
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.conn.Exec(p); err != nil {
			return fmt.Errorf("pragma %q: %w", p, err)
		}
	}
	return nil
}

func (db *DB) migrate() error {
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

	for _, s := range stmts {
		if _, err := db.conn.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Close closes the underlying connection.
func (db *DB) Close() error {
	return db.conn.Close()
}
