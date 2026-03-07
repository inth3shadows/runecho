package context

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const cacheTTL = 30 * time.Minute

// ResultCache caches compiled context strings in a local SQLite DB.
type ResultCache struct {
	conn *sql.DB
}

// OpenResultCache opens (or creates) the context result cache at path.
func OpenResultCache(path string) (*ResultCache, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open cache db: %w", err)
	}
	conn.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := conn.Exec(pragma); err != nil {
			conn.Close()
			return nil, fmt.Errorf("cache pragma: %w", err)
		}
	}

	_, err = conn.Exec(`
		CREATE TABLE IF NOT EXISTS result_cache (
			state_hash  TEXT NOT NULL,
			prompt_hash TEXT NOT NULL,
			providers   TEXT NOT NULL,
			budget      INTEGER NOT NULL,
			result      TEXT NOT NULL,
			created_at  TEXT NOT NULL,
			PRIMARY KEY (state_hash, prompt_hash, providers, budget)
		)
	`)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("cache migrate: %w", err)
	}

	c := &ResultCache{conn: conn}
	c.evictExpired()
	return c, nil
}

// Get returns the cached result if present and within the TTL window.
func (c *ResultCache) Get(stateHash, promptHash, providers string, budget int) (string, bool) {
	cutoff := time.Now().UTC().Add(-cacheTTL).Format(time.RFC3339)
	var result string
	err := c.conn.QueryRow(
		`SELECT result FROM result_cache
		 WHERE state_hash = ? AND prompt_hash = ? AND providers = ? AND budget = ?
		   AND created_at > ?`,
		stateHash, promptHash, providers, budget, cutoff,
	).Scan(&result)
	if err != nil {
		return "", false
	}
	return result, true
}

// Put stores a compiled context result.
func (c *ResultCache) Put(stateHash, promptHash, providers, result string, budget int) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	_, err := c.conn.Exec(
		`INSERT OR REPLACE INTO result_cache
			(state_hash, prompt_hash, providers, budget, result, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		stateHash, promptHash, providers, budget, result, ts,
	)
	return err
}

// Close closes the underlying connection.
func (c *ResultCache) Close() error {
	return c.conn.Close()
}

// evictExpired removes entries older than the TTL.
func (c *ResultCache) evictExpired() {
	cutoff := time.Now().UTC().Add(-cacheTTL).Format(time.RFC3339)
	c.conn.Exec(`DELETE FROM result_cache WHERE created_at <= ?`, cutoff)
}
