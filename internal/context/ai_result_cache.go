package context

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const aiResultCacheTTL = 24 * time.Hour

// AIResultCache caches AI pipeline execution results in a local SQLite DB.
// Key: (ir_hash, prompt_hash, model, task_id).
// Distinct from ResultCache, which caches context compilation strings.
type AIResultCache struct {
	conn *sql.DB
}

// OpenAIResultCache opens (or creates) the AI result cache at path.
func OpenAIResultCache(path string) (*AIResultCache, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open ai result cache: %w", err)
	}
	conn.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := conn.Exec(pragma); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ai result cache pragma: %w", err)
		}
	}

	_, err = conn.Exec(`
		CREATE TABLE IF NOT EXISTS ai_result_cache (
			ir_hash     TEXT NOT NULL,
			prompt_hash TEXT NOT NULL,
			model       TEXT NOT NULL,
			task_id     TEXT NOT NULL,
			result      TEXT NOT NULL,
			created_at  TEXT NOT NULL,
			PRIMARY KEY (ir_hash, prompt_hash, model, task_id)
		)
	`)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ai result cache migrate: %w", err)
	}

	c := &AIResultCache{conn: conn}
	c.evictExpired()
	return c, nil
}

// Get returns the cached result for the given key if present and within TTL.
func (c *AIResultCache) Get(irHash, promptHash, model, taskID string) (string, bool) {
	cutoff := time.Now().UTC().Add(-aiResultCacheTTL).Format(time.RFC3339)
	var result string
	err := c.conn.QueryRow(
		`SELECT result FROM ai_result_cache
		 WHERE ir_hash = ? AND prompt_hash = ? AND model = ? AND task_id = ?
		   AND created_at > ?`,
		irHash, promptHash, model, taskID, cutoff,
	).Scan(&result)
	if err != nil {
		return "", false
	}
	return result, true
}

// Put stores an AI result for the given key.
func (c *AIResultCache) Put(irHash, promptHash, model, taskID, result string) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	_, err := c.conn.Exec(
		`INSERT OR REPLACE INTO ai_result_cache
			(ir_hash, prompt_hash, model, task_id, result, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		irHash, promptHash, model, taskID, result, ts,
	)
	return err
}

// Invalidate removes all cache entries for a given task_id.
// Call this when a task's verify passes (IR has advanced).
func (c *AIResultCache) Invalidate(taskID string) error {
	_, err := c.conn.Exec(`DELETE FROM ai_result_cache WHERE task_id = ?`, taskID)
	return err
}

// Close closes the underlying connection.
func (c *AIResultCache) Close() error {
	return c.conn.Close()
}

// evictExpired removes entries older than the TTL.
func (c *AIResultCache) evictExpired() {
	cutoff := time.Now().UTC().Add(-aiResultCacheTTL).Format(time.RFC3339)
	c.conn.Exec(`DELETE FROM ai_result_cache WHERE created_at <= ?`, cutoff)
}
