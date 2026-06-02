package snapshot

import (
	"database/sql"
	"fmt"
	"os"
	"time"
)

// Repo is an enrolled repository — the stable identity the oracle scopes all
// snapshot history by. The repos table is the single registry (no external
// manifest), foreign-keyed to snapshots so identity and history share one
// integrity boundary.
type Repo struct {
	ID          int64
	Name        string
	Path        string
	FileCap     int // 0 = unlimited; honest-coverage cap for huge repos
	EnrolledAt  time.Time
	LastIndexed time.Time // zero if never indexed
	ParseErrors int
}

// EnrollRepo registers a repo and returns its id. Strict: returns an error if the
// name or path is already taken (callers wanting auto-disambiguation handle that
// at their layer). enrolledAt is taken from the caller-free clock at insert time.
func (db *DB) EnrollRepo(name, path string, fileCap int) (int64, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	res, err := db.conn.Exec(
		`INSERT INTO repos (name, path, file_cap, enrolled_at) VALUES (?, ?, ?, ?)`,
		name, path, fileCap, ts,
	)
	if err != nil {
		return 0, fmt.Errorf("enroll repo %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

// GetRepoByPath returns the repo enrolled at path, or nil, nil if none.
func (db *DB) GetRepoByPath(path string) (*Repo, error) {
	return scanRepo(db.conn.QueryRow(
		`SELECT id, name, path, file_cap, enrolled_at, last_indexed, parse_errors
		 FROM repos WHERE path = ?`, path))
}

// GetRepoByName returns the repo with the given name, or nil, nil if none.
func (db *DB) GetRepoByName(name string) (*Repo, error) {
	return scanRepo(db.conn.QueryRow(
		`SELECT id, name, path, file_cap, enrolled_at, last_indexed, parse_errors
		 FROM repos WHERE name = ?`, name))
}

// ListRepos returns all enrolled repos, ordered by name.
func (db *DB) ListRepos() ([]Repo, error) {
	rows, err := db.conn.Query(
		`SELECT id, name, path, file_cap, enrolled_at, last_indexed, parse_errors
		 FROM repos ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer rows.Close()

	var repos []Repo
	for rows.Next() {
		r, err := scanRepoRows(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, *r)
	}
	return repos, rows.Err()
}

// RemoveRepo deletes a repo by id. Returns an error if the repo has any
// snapshots — use PurgeRepo to remove the repo and its full history atomically.
func (db *DB) RemoveRepo(id int64) error {
	n, err := db.CountSnapshots(id)
	if err != nil {
		return fmt.Errorf("count snapshots before remove: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("repo %d has %d snapshot(s); use PurgeRepo to delete them along with the repo", id, n)
	}
	if _, err := db.conn.Exec(`DELETE FROM repos WHERE id = ?`, id); err != nil {
		return fmt.Errorf("remove repo %d: %w", id, err)
	}
	return nil
}

// PurgeRepo deletes a repo and its entire snapshot history (symbols, files,
// snapshots, then the repo row) in one transaction — no orphaned rows.
func (db *DB) PurgeRepo(id int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin purge: %w", err)
	}
	defer tx.Rollback()
	stmts := []struct {
		sql string
	}{
		{`DELETE FROM symbols WHERE file_id IN (
			SELECT f.id FROM files f JOIN snapshots s ON f.snapshot_id = s.id WHERE s.repo_id = ?)`},
		{`DELETE FROM files WHERE snapshot_id IN (SELECT id FROM snapshots WHERE repo_id = ?)`},
		{`DELETE FROM snapshots WHERE repo_id = ?`},
		{`DELETE FROM repos WHERE id = ?`},
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s.sql, id); err != nil {
			return fmt.Errorf("purge repo %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// TouchRepo records indexing state after a (re)index: when it last ran and how
// many parse errors were seen (self-observing / honest-coverage guarantees).
func (db *DB) TouchRepo(id int64, lastIndexed time.Time, parseErrors int) error {
	_, err := db.conn.Exec(
		`UPDATE repos SET last_indexed = ?, parse_errors = ? WHERE id = ?`,
		lastIndexed.UTC().Format(time.RFC3339), parseErrors, id,
	)
	if err != nil {
		return fmt.Errorf("touch repo %d: %w", id, err)
	}
	return nil
}

func scanRepo(row *sql.Row) (*Repo, error) {
	r, err := scanRepoCols(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func scanRepoRows(rows *sql.Rows) (*Repo, error) {
	return scanRepoCols(rows.Scan)
}

// scanRepoCols scans the standard repo column order via the given Scan closure,
// shared by *sql.Row and *sql.Rows. last_indexed is nullable.
func scanRepoCols(scan func(...any) error) (*Repo, error) {
	var r Repo
	var enrolled string
	var lastIndexed sql.NullString
	if err := scan(&r.ID, &r.Name, &r.Path, &r.FileCap, &enrolled, &lastIndexed, &r.ParseErrors); err != nil {
		return nil, err
	}
	var parseErr error
	r.EnrolledAt, parseErr = time.Parse(time.RFC3339, enrolled)
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "runecho: warning: repo %d enrolled_at %q: %v\n", r.ID, enrolled, parseErr)
	}
	if lastIndexed.Valid {
		r.LastIndexed, parseErr = time.Parse(time.RFC3339, lastIndexed.String)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "runecho: warning: repo %d last_indexed %q: %v\n", r.ID, lastIndexed.String, parseErr)
			r.LastIndexed = time.Time{}
		}
	}
	return &r, nil
}
