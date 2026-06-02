package snapshot

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentReadWrite hammers the store with concurrent readers and writers.
// The store uses a single serialized connection (MaxOpenConns=1) so access is
// queued, never torn — this test asserts that under -race: no "database is
// locked", no data races in our code, and reads always see consistent state.
func TestConcurrentReadWrite(t *testing.T) {
	db, _ := openTemp(t)
	repoID, err := db.EnrollRepo("alpha", "/repos/alpha", "", 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// Seed one snapshot so readers always have something to read.
	if _, err := db.SaveSnapshot(repoID, "s", "seed", "/repos/alpha", makeIR("h0", "Seed")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const writers, readers, iters = 4, 8, 25
	var wg sync.WaitGroup
	errCh := make(chan error, (writers+readers)*iters)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				hash := fmt.Sprintf("h-%d-%d", w, i)
				if _, err := db.SaveSnapshot(repoID, "s", "w", "/repos/alpha", makeIR(hash, hash)); err != nil {
					errCh <- fmt.Errorf("write: %w", err)
					return
				}
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				metas, err := db.List(repoID, 5)
				if err != nil {
					errCh <- fmt.Errorf("list: %w", err)
					return
				}
				for _, m := range metas {
					if m.RepoID != repoID {
						errCh <- fmt.Errorf("torn read: repo_id=%d want %d", m.RepoID, repoID)
						return
					}
				}
				if _, err := db.Health(); err != nil {
					errCh <- fmt.Errorf("health: %w", err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// Final invariant: every committed write persisted, none lost.
	count, err := db.CountSnapshots(repoID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if want := 1 + writers*iters; count != want {
		t.Fatalf("snapshot count = %d, want %d (no lost/torn writes)", count, want)
	}
}
