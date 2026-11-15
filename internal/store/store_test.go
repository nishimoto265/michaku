package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/opsledger/internal/ledger"
)

func sampleEvents() []ledger.Event {
	now := time.Now()
	mk := func(typ, actor, subj, corr string, data map[string]any) ledger.Event {
		e := ledger.NewEvent(typ, actor, subj, corr, "test", data)
		e.TS = now
		return e
	}
	return []ledger.Event{
		mk("actor.registered", "agent:a", "agent:a", "", map[string]any{"kind": "personal-agent"}),
		mk("delegation.created", "agent:a", "job:x", "delegation:1", map[string]any{"task": "t", "due": now.Add(time.Hour).Format(time.RFC3339)}),
		mk("approval.requested", "agent:a", "thing:y", "approval:1", map[string]any{"what": "deploy"}),
	}
}

func TestProjection(t *testing.T) {
	st := Project(sampleEvents())
	if len(st.Delegations) != 1 || len(st.Approvals) != 1 || len(st.Actors) != 1 {
		t.Fatalf("bad projection: %+v", st)
	}
}

func TestRebuildConsistency(t *testing.T) {
	events := sampleEvents()
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Rebuild(events)
	if err != nil {
		t.Fatal(err)
	}
	want := Project(events)
	if len(st.Delegations) != len(want.Delegations) || len(st.Approvals) != len(want.Approvals) || len(st.Actors) != len(want.Actors) {
		t.Fatalf("rebuild mismatch: %+v vs %+v", st, want)
	}
	n, err := s.Count("events")
	if err != nil || n != len(events) {
		t.Fatalf("events count %d err %v", n, err)
	}
	n, _ = s.Count("open_delegations")
	if n != 1 {
		t.Fatalf("open_delegations %d", n)
	}
	// rebuild again must be idempotent
	if _, err := s.Rebuild(events); err != nil {
		t.Fatal(err)
	}
	n, _ = s.Count("events")
	if n != len(events) {
		t.Fatalf("after second rebuild events %d", n)
	}
}
