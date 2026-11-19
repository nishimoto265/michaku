package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/michaku/internal/ledger"
)

// TestRebuildScale guards the v0.1.1 fix: Rebuild used to issue one
// autocommit (fsync) per event and appeared to hang once a ledger passed
// ~10k events on storage with millisecond fsync latency. A single
// transaction keeps the cost bounded. 12,000 events must finish well
// inside 30 seconds even on a slow filesystem.
func TestRebuildScale(t *testing.T) {
	const n = 12000
	const limit = 30 * time.Second

	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.FixedZone("JST", 9*3600))
	events := make([]ledger.Event, n)
	for i := 0; i < n; i++ {
		e := ledger.NewEvent("activity.seen", "agent:scale", "job:scale", "", "scale-test", nil)
		e.TS = now.Add(time.Duration(i) * time.Second)
		events[i] = e
	}

	db := filepath.Join(t.TempDir(), "scale.db")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	start := time.Now()
	st, err := s.Rebuild(events)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Rebuild: %v (elapsed %s)", err, elapsed)
	}
	if elapsed > limit {
		t.Fatalf("Rebuild of %d events took %s, want <= %s", n, elapsed, limit)
	}
	count, err := s.Count("events")
	if err != nil || count != n {
		t.Fatalf("events count = %d, err %v (want %d)", count, err, n)
	}
	if st == nil {
		t.Fatal("Rebuild returned nil state")
	}
	t.Logf("Rebuild of %d events finished in %s", n, elapsed)
}
