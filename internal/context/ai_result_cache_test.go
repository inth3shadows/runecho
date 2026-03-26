package context

import (
	"os"
	"testing"
	"time"
)

func TestAIResultCache_PutGet(t *testing.T) {
	f, err := os.CreateTemp("", "ai_result_cache_*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	c, err := OpenAIResultCache(f.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()

	const (
		irHash     = "abc123"
		promptHash = "def456"
		model      = "sonnet"
		taskID     = "42"
		result     = `{"output":"hello"}`
	)

	// Miss before put.
	if _, ok := c.Get(irHash, promptHash, model, taskID); ok {
		t.Fatal("expected cache miss before Put")
	}

	// Put then hit.
	if err := c.Put(irHash, promptHash, model, taskID, result); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := c.Get(irHash, promptHash, model, taskID)
	if !ok {
		t.Fatal("expected cache hit after Put")
	}
	if got != result {
		t.Fatalf("result mismatch: got %q, want %q", got, result)
	}
}

func TestAIResultCache_KeyIsolation(t *testing.T) {
	f, err := os.CreateTemp("", "ai_result_cache_*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	c, err := OpenAIResultCache(f.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()

	if err := c.Put("ir1", "p1", "sonnet", "t1", "result-A"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("ir1", "p1", "haiku", "t1", "result-B"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("ir1", "p1", "sonnet", "t2", "result-C"); err != nil {
		t.Fatal(err)
	}

	// Different model — must not collide.
	got, ok := c.Get("ir1", "p1", "haiku", "t1")
	if !ok || got != "result-B" {
		t.Fatalf("model isolation failed: got %q ok=%v", got, ok)
	}

	// Different task_id — must not collide.
	got, ok = c.Get("ir1", "p1", "sonnet", "t2")
	if !ok || got != "result-C" {
		t.Fatalf("task_id isolation failed: got %q ok=%v", got, ok)
	}

	// Different ir_hash — must miss.
	if _, ok := c.Get("ir2", "p1", "sonnet", "t1"); ok {
		t.Fatal("ir_hash isolation failed: expected miss")
	}
}

func TestAIResultCache_Invalidate(t *testing.T) {
	f, err := os.CreateTemp("", "ai_result_cache_*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	c, err := OpenAIResultCache(f.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()

	if err := c.Put("ir1", "p1", "sonnet", "task-99", "result"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("ir1", "p2", "sonnet", "task-99", "result2"); err != nil {
		t.Fatal(err)
	}
	// Different task — should survive invalidation.
	if err := c.Put("ir1", "p1", "sonnet", "task-00", "survivor"); err != nil {
		t.Fatal(err)
	}

	if err := c.Invalidate("task-99"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	if _, ok := c.Get("ir1", "p1", "sonnet", "task-99"); ok {
		t.Fatal("expected miss after Invalidate")
	}
	if _, ok := c.Get("ir1", "p2", "sonnet", "task-99"); ok {
		t.Fatal("expected miss after Invalidate (p2)")
	}
	// task-00 must survive.
	if got, ok := c.Get("ir1", "p1", "sonnet", "task-00"); !ok || got != "survivor" {
		t.Fatalf("unrelated task was invalidated: got %q ok=%v", got, ok)
	}
}

func TestAIResultCache_TTLExpiry(t *testing.T) {
	f, err := os.CreateTemp("", "ai_result_cache_*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	c, err := OpenAIResultCache(f.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()

	// Insert an entry with an old timestamp (25h ago — beyond 24h TTL).
	stale := time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339)
	_, err = c.conn.Exec(
		`INSERT INTO ai_result_cache (ir_hash, prompt_hash, model, task_id, result, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"ir-stale", "p-stale", "sonnet", "t-stale", "stale-result", stale,
	)
	if err != nil {
		t.Fatalf("manual insert: %v", err)
	}

	// Should miss — past TTL cutoff.
	if _, ok := c.Get("ir-stale", "p-stale", "sonnet", "t-stale"); ok {
		t.Fatal("expected TTL miss for 25h-old entry")
	}
}

func TestAIResultCache_Upsert(t *testing.T) {
	f, err := os.CreateTemp("", "ai_result_cache_*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	c, err := OpenAIResultCache(f.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()

	if err := c.Put("ir1", "p1", "sonnet", "t1", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("ir1", "p1", "sonnet", "t1", "v2"); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("ir1", "p1", "sonnet", "t1")
	if !ok || got != "v2" {
		t.Fatalf("upsert failed: got %q ok=%v", got, ok)
	}
}
