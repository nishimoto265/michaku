package detect

import (
	"testing"
	"time"

	"github.com/nishimoto265/opsledger/internal/config"
	"github.com/nishimoto265/opsledger/internal/ledger"
	"github.com/nishimoto265/opsledger/internal/store"
)

func ev(typ, actor, subj, corr string, ts time.Time, data map[string]any) ledger.Event {
	e := ledger.NewEvent(typ, actor, subj, corr, "test", data)
	e.TS = ts
	return e
}
func hasRule(cands []Candidate, rule string) bool {
	for _, c := range cands {
		if c.Rule == rule {
			return true
		}
	}
	return false
}

func TestMissingRunCron(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local)
	cfg := config.Default()
	// declared long ago, daily 09:00, no runs -> missing
	st := store.Project([]ledger.Event{ev("schedule.declared", "ops", "job:daily", "", now.Add(-48*time.Hour), map[string]any{"expr": "0 9 * * *", "grace": "5m"})})
	if !hasRule(Evaluate(st, cfg, now), "run.missing") {
		t.Fatal("expected run.missing")
	}
	// run today at 09:01 -> no missing
	st2 := store.Project([]ledger.Event{
		ev("schedule.declared", "ops", "job:daily", "", now.Add(-48*time.Hour), map[string]any{"expr": "0 9 * * *", "grace": "5m"}),
		ev("run.finished", "ops", "job:daily", "run:1", now.Add(-3*time.Hour+time.Minute), map[string]any{"status": "ok"}),
	})
	if hasRule(Evaluate(st2, cfg, now), "run.missing") {
		t.Fatal("unexpected run.missing when run happened")
	}
	// within grace: now 09:03, expected 09:00 grace 5m -> no finding
	nowGrace := time.Date(2026, 11, 15, 9, 3, 0, 0, time.Local)
	if hasRule(Evaluate(st, cfg, nowGrace), "run.missing") {
		t.Fatal("unexpected missing within grace")
	}
}

func TestMissingRunOverdue(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	st := store.Project([]ledger.Event{ev("run.started", "ops", "job:slow", "run:9", now.Add(-3*time.Hour), map[string]any{"job": "job:slow"})})
	if !hasRule(Evaluate(st, cfg, now), "run.overdue") {
		t.Fatal("expected run.overdue")
	}
}

func TestReportMissingSeverityBands(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	mk := func(due time.Time) *store.State {
		return store.Project([]ledger.Event{ev("delegation.created", "a", "task:1", "delegation:1", now.Add(-72*time.Hour), map[string]any{"task": "x", "due": due.Format(time.RFC3339)})})
	}
	if hasRule(Evaluate(mk(now.Add(time.Hour)), cfg, now), "delegation.unreported") {
		t.Fatal("not yet due")
	}
	c := Evaluate(mk(now.Add(-time.Hour)), cfg, now)
	if !hasRule(c, "delegation.unreported") {
		t.Fatal("expected unreported warn")
	}
	for _, cand := range c {
		if cand.Rule == "delegation.unreported" && cand.Severity != "warn" {
			t.Fatalf("expected warn, got %s", cand.Severity)
		}
	}
	c2 := Evaluate(mk(now.Add(-48*time.Hour)), cfg, now)
	found := false
	for _, cand := range c2 {
		if cand.Rule == "delegation.unreported" && cand.Severity == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected critical after critical_after band")
	}
	// reported -> resolved state, no finding
	st := store.Project([]ledger.Event{
		ev("delegation.created", "a", "task:1", "delegation:1", now.Add(-72*time.Hour), map[string]any{"task": "x", "due": now.Add(-time.Hour).Format(time.RFC3339)}),
		ev("delegation.reported", "a", "task:1", "delegation:1", now.Add(-30*time.Minute), map[string]any{"outcome": "completed"}),
	})
	if hasRule(Evaluate(st, cfg, now), "delegation.unreported") {
		t.Fatal("reported delegation must not raise")
	}
}

func TestApprovalStale(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	st := store.Project([]ledger.Event{ev("approval.requested", "a", "thing:1", "approval:1", now.Add(-25*time.Hour), map[string]any{"what": "x"})})
	if !hasRule(Evaluate(st, cfg, now), "approval.pending_stale") {
		t.Fatal("expected approval stale")
	}
	st2 := store.Project([]ledger.Event{ev("approval.requested", "a", "thing:1", "approval:1", now.Add(-time.Hour), map[string]any{"what": "x"})})
	if hasRule(Evaluate(st2, cfg, now), "approval.pending_stale") {
		t.Fatal("fresh approval must not be stale")
	}
}

func TestHeartbeatStale(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	st := store.Project([]ledger.Event{ev("actor.heartbeat", "agent:quiet", "agent:quiet", "", now.Add(-2*time.Hour), nil)})
	if !hasRule(Evaluate(st, cfg, now), "actor.silent") {
		t.Fatal("expected actor.silent")
	}
	st2 := store.Project([]ledger.Event{ev("actor.heartbeat", "agent:active", "agent:active", "", now.Add(-5*time.Minute), nil)})
	if hasRule(Evaluate(st2, cfg, now), "actor.silent") {
		t.Fatal("active actor must not be silent")
	}
}

func TestRepeatedFailure(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	var evs []ledger.Event
	for i := 0; i < 3; i++ {
		evs = append(evs, ev("run.finished", "ops", "job:flap", "run:x", now.Add(-time.Hour), map[string]any{"status": "failed"}))
	}
	if !hasRule(Evaluate(store.Project(evs), cfg, now), "run.flapping") {
		t.Fatal("expected flapping")
	}
	if hasRule(Evaluate(store.Project(evs[:2]), cfg, now), "run.flapping") {
		t.Fatal("2 failures below threshold must not flap")
	}
}

func TestFindingEpisodeDedup(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	// isolate approval rule: actor "a" would also legitimately be silent
	if r := cfg.Rules["heartbeat_stale"]; true {
		r.Enabled = false
		cfg.Rules["heartbeat_stale"] = r
	}
	st := store.Project([]ledger.Event{
		ev("approval.requested", "a", "thing:1", "approval:1", now.Add(-25*time.Hour), map[string]any{"what": "x"}),
		ev("finding.raised", "opsledger:check", "thing:1", "", now.Add(-time.Hour), map[string]any{"rule": "approval.pending_stale", "finding_key": "approval.pending_stale|thing:1|approval:1", "severity": "warn", "detail": "d"}),
	})
	cands := Evaluate(st, cfg, now)
	raised, _, _ := Diff(st.Findings, cands)
	if len(raised) != 0 {
		t.Fatalf("episode must raise only once, got %v", raised)
	}
}
