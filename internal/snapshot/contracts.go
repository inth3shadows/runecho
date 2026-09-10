package snapshot

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// migrateV9 adds the contracts table: which edit-scope contract is active for a
// (repo, session), and the content hash of the contract file at the moment it
// was activated.
//
// The contract itself lives in the repo as a reviewable file — only the BINDING
// lives here. Storing the hash is what makes an ask reproducible: without it, a
// contract edited mid-session silently changes what the guard was enforcing and
// the decision log can no longer be replayed against the text that produced it.
//
// Purely additive (CREATE TABLE + index), never a rebuild. A table-rebuild
// migration in this codebase is a data-loss trap — SQLite ignores
// `PRAGMA foreign_keys=OFF` inside a transaction, which every migration here
// runs in, so a rebuild's DROP TABLE fires cascades and commits clean (issue
// #13, PR #196).
func migrateV9(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS contracts (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			repo_id       INTEGER NOT NULL REFERENCES repos(id),
			session_id    TEXT NOT NULL,
			name          TEXT NOT NULL,
			path          TEXT NOT NULL,
			content_hash  TEXT NOT NULL,
			activated_at  TEXT NOT NULL
		)`,
		// One active contract per (repo, session): activating a second replaces
		// the first rather than stacking, so "what is in scope" always has a
		// single answer. Enforced by the schema, not by convention.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_contracts_repo_session
			ON contracts(repo_id, session_id)`,
	}
	return execAll(tx, stmts)
}

// ActiveContract is the stored binding for one (repo, session).
type ActiveContract struct {
	Name        string
	Path        string
	ContentHash string
	ActivatedAt time.Time
}

// ErrNoActiveContract is returned when a (repo, session) has no contract bound.
// Callers MUST treat this as "abstain entirely", never as "nothing is in scope"
// — the check exists only for users who explicitly declared a scope, and a
// missing contract means they did not.
var ErrNoActiveContract = errors.New("no active contract")

// ActivateContract binds a contract to a (repo, session), replacing any prior
// binding for that pair.
func (db *DB) ActivateContract(repoID int64, sessionID, name, path, contentHash string) error {
	if sessionID == "" {
		return fmt.Errorf("activate contract: empty session id")
	}
	_, err := db.conn.Exec(`
		INSERT INTO contracts (repo_id, session_id, name, path, content_hash, activated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, session_id) DO UPDATE SET
			name = excluded.name,
			path = excluded.path,
			content_hash = excluded.content_hash,
			activated_at = excluded.activated_at`,
		repoID, sessionID, name, path, contentHash, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("activate contract %q: %w", name, err)
	}
	return nil
}

// GetActiveContract returns the binding for a (repo, session), or
// ErrNoActiveContract.
func (db *DB) GetActiveContract(repoID int64, sessionID string) (ActiveContract, error) {
	var c ActiveContract
	var at string
	err := db.conn.QueryRow(
		`SELECT name, path, content_hash, activated_at
		 FROM contracts WHERE repo_id = ? AND session_id = ?`,
		repoID, sessionID,
	).Scan(&c.Name, &c.Path, &c.ContentHash, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return ActiveContract{}, ErrNoActiveContract
	}
	if err != nil {
		return ActiveContract{}, fmt.Errorf("get active contract: %w", err)
	}
	// A stored timestamp that will not parse is not worth failing the lookup
	// over — the binding is still valid, only its display time is unknown.
	c.ActivatedAt, _ = time.Parse(time.RFC3339, at)
	return c, nil
}

// DeactivateContract removes the binding for a (repo, session). Removing a
// binding that does not exist is not an error — deactivating twice should be
// safe, and the caller's intent ("no contract active") is satisfied either way.
func (db *DB) DeactivateContract(repoID int64, sessionID string) error {
	if _, err := db.conn.Exec(
		`DELETE FROM contracts WHERE repo_id = ? AND session_id = ?`,
		repoID, sessionID,
	); err != nil {
		return fmt.Errorf("deactivate contract: %w", err)
	}
	return nil
}

// SiblingContract is a contract bound to a DIFFERENT repo id that shares this
// one's git-common-dir — i.e. another worktree of the same repository.
type SiblingContract struct {
	RepoID   int64
	RepoName string
	Name     string
	Path     string
}

// FindSiblingContract answers "this (repo, session) has no contract — does a
// sibling worktree of the same repository have one?" (#385).
//
// The failure it exists to expose: a contract is bound by repo id, but since
// #382 a given cwd's id depends on which sibling directories happen to exist at
// resolution time. Delete an unrelated sibling and the same cwd can resolve to a
// different id, at which point GetActiveContract returns ErrNoActiveContract and
// the guard abstains — silently, because "no contract" and "your contract is
// filed under the other id" are the same answer to that query. That is the #369
// shape: reporting success while doing less than claimed.
//
// The common-dir is what is actually stable across worktrees, so it is the join
// key here. Rows with no recorded common-dir (pre-V4 enrolments) are excluded
// rather than treated as matching each other — an empty string is missing data,
// not a repository they all share.
//
// Returns ErrNoActiveContract when there is no such sibling, so a caller can
// treat "nothing anywhere" with one comparison. Newest binding wins if several
// siblings have one; the query is bounded to a single row because the caller
// only needs to know THAT scope is unenforced, not to enumerate every place it
// might have been declared.
func (db *DB) FindSiblingContract(repoID int64, sessionID string) (SiblingContract, error) {
	var s SiblingContract
	err := db.conn.QueryRow(
		`SELECT c.repo_id, r.name, c.name, c.path
		   FROM contracts c
		   JOIN repos r ON r.id = c.repo_id
		  WHERE c.session_id = ?
		    AND c.repo_id != ?
		    AND r.common_dir IS NOT NULL
		    AND r.common_dir != ''
		    AND r.common_dir = (SELECT common_dir FROM repos WHERE id = ?)
		  ORDER BY c.activated_at DESC
		  LIMIT 1`,
		sessionID, repoID, repoID,
	).Scan(&s.RepoID, &s.RepoName, &s.Name, &s.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return SiblingContract{}, ErrNoActiveContract
	}
	if err != nil {
		return SiblingContract{}, fmt.Errorf("find sibling contract: %w", err)
	}
	return s, nil
}
