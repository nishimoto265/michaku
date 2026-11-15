package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/michaku/internal/ledger"
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

// Only registered / heartbeat actors are tracked (SPEC §3.5); actors that
// merely appear on other events are not recorded as seen actors at all.
func TestProjectionTrackedActorsOnly(t *testing.T) {
	now := time.Now()
	mk := func(typ, actor, subj string, data map[string]any) ledger.Event {
		e := ledger.NewEvent(typ, actor, subj, "", "test", data)
		e.TS = now
		return e
	}
	st := Project([]ledger.Event{
		mk("actor.registered", "agent:reg", "agent:reg", map[string]any{"kind": "personal-agent"}),
		mk("actor.heartbeat", "agent:hb", "agent:hb", nil),
		mk("run.finished", "agent:cron-runner", "job:x", map[string]any{"status": "ok"}),
		mk("activity.seen", "agent:seen-only", "job:x", map[string]any{"source": "log"}),
		mk("finding.raised", "michaku:check", "job:x", map[string]any{"rule": "run.missing"}),
	})
	if len(st.Actors) != 2 || len(st.TrackedActors) != 2 {
		t.Fatalf("expected exactly the registered+heartbeat actors tracked, got actors=%v tracked=%v", st.Actors, st.TrackedActors)
	}
	if !st.TrackedActors["agent:reg"] || !st.TrackedActors["agent:hb"] {
		t.Fatalf("wrong tracked set: %v", st.TrackedActors)
	}
}

// schedule.declared: enabled defaults to true, enabled:false is honoured,
// grace_sec is the alias for grace, and a grace string wins over grace_sec.
func TestScheduleDeclaredEnabledAndGrace(t *testing.T) {
	now := time.Now()
	mk := func(subj string, data map[string]any) ledger.Event {
		e := ledger.NewEvent("schedule.declared", "system:cron", subj, "", "test", data)
		e.TS = now
		return e
	}
	st := Project([]ledger.Event{
		mk("job:a", map[string]any{"expr": "runonce@2026-11-16T08:00:00", "grace_sec": float64(900), "enabled": false}),
		mk("job:b", map[string]any{"expr": "0 9 * * *"}),
		mk("job:c", map[string]any{"expr": "0 9 * * *", "grace": "5m", "grace_sec": float64(900)}),
	})
	if sc := st.Schedules["job:a"]; sc == nil || sc.Enabled || sc.Grace != 15*time.Minute {
		t.Fatalf("job:a: want enabled=false grace=15m, got %+v", sc)
	}
	if sc := st.Schedules["job:b"]; sc == nil || !sc.Enabled || sc.Grace != 0 {
		t.Fatalf("job:b: want enabled=true grace=0, got %+v", sc)
	}
	if sc := st.Schedules["job:c"]; sc == nil || sc.Grace != 5*time.Minute {
		t.Fatalf("job:c: grace string must win over grace_sec, got %+v", sc)
	}
}
