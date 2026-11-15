package detect

import (
	"testing"
	"time"

	"github.com/nishimoto265/michaku/internal/config"
	"github.com/nishimoto265/michaku/internal/ledger"
	"github.com/nishimoto265/michaku/internal/store"
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
		ev("finding.raised", "michaku:check", "thing:1", "", now.Add(-time.Hour), map[string]any{"rule": "approval.pending_stale", "finding_key": "approval.pending_stale|thing:1|approval:1", "severity": "warn", "detail": "d"}),
	})
	cands := Evaluate(st, cfg, now)
	raised, _, _ := Diff(st.Findings, cands)
	if len(raised) != 0 {
		t.Fatalf("episode must raise only once, got %v", raised)
	}
}

func hasRuleFor(cands []Candidate, rule, subject string) bool {
	for _, c := range cands {
		if c.Rule == rule && c.Subject == subject {
			return true
		}
	}
	return false
}

// Phase A regression: actors that merely appear on events (schedule
// declarers, run executors, approval requesters, the checker itself)
// have no heartbeat expectation and must never raise actor.silent.
func TestHeartbeatStaleUntrackedActorsDoNotFire(t *testing.T) {
	now := time.Date(2026, 11, 16, 8, 16, 0, 0, time.Local)
	cfg := config.Default()
	old := now.Add(-48 * time.Hour)
	st := store.Project([]ledger.Event{
		ev("schedule.declared", "system:cron", "job:daily", "sched:1", old, map[string]any{"expr": "0 9 * * *", "grace": "5m"}),
		ev("run.started", "agent:cron-runner", "job:daily", "run:1", old, map[string]any{"job": "job:daily"}),
		ev("run.finished", "agent:cron-runner", "job:daily", "run:1", old.Add(time.Minute), map[string]any{"status": "ok"}),
		ev("approval.requested", "agent:coordinator", "approval:x", "appr:1", old, map[string]any{"what": "x"}),
		ev("delegation.created", "agent:coordinator", "task:1", "deleg:1", old, map[string]any{"task": "x"}),
		ev("activity.seen", "agent:seen-only", "task:1", "deleg:1", old, map[string]any{"source": "log"}),
		ev("finding.raised", "michaku:check", "job:daily", "", old, map[string]any{"rule": "run.missing", "finding_key": "run.missing|job:daily|x", "severity": "warn"}),
	})
	if cands := Evaluate(st, cfg, now); hasRule(cands, "actor.silent") {
		t.Fatalf("untracked actors must not raise actor.silent, got %v", cands)
	}
}

// A registered actor is tracked: its silence fires, but later activity
// by the same actor (any event) refreshes its last-seen time.
func TestHeartbeatStaleRegisteredActor(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	st := store.Project([]ledger.Event{
		ev("actor.registered", "agent:reg", "agent:reg", "", now.Add(-2*time.Hour), map[string]any{"kind": "personal-agent"}),
	})
	if !hasRuleFor(Evaluate(st, cfg, now), "actor.silent", "agent:reg") {
		t.Fatal("registered silent actor must raise actor.silent")
	}
	st2 := store.Project([]ledger.Event{
		ev("actor.registered", "agent:reg", "agent:reg", "", now.Add(-3*time.Hour), map[string]any{"kind": "personal-agent"}),
		ev("run.finished", "agent:reg", "job:x", "run:1", now.Add(-5*time.Minute), map[string]any{"status": "ok"}),
	})
	if hasRuleFor(Evaluate(st2, cfg, now), "actor.silent", "agent:reg") {
		t.Fatal("registered actor with recent activity must not be silent")
	}
}

// Phase A regression (proposal §4 criterion 3): one-shot schedules
// declared as runonce@<time> must be judged by missing_run.
func TestMissingRunRunonce(t *testing.T) {
	cfg := config.Default()
	declared := time.Date(2026, 11, 14, 21, 0, 0, 0, time.Local)
	expected := time.Date(2026, 11, 16, 8, 0, 0, 0, time.Local)
	// Dataset form: no timezone offset, grace given as grace_sec=900.
	decl := ev("schedule.declared", "system:cron", "job:once", "sched:once", declared, map[string]any{"expr": "runonce@2026-11-16T08:00:00", "grace_sec": 900})
	// At expected+grace+1m with no run -> missing (the Phase A scenario).
	now := expected.Add(16 * time.Minute)
	if !hasRuleFor(Evaluate(store.Project([]ledger.Event{decl}), cfg, now), "run.missing", "job:once") {
		t.Fatal("expected run.missing for unexecuted runonce job")
	}
	// Within grace_sec (15m) -> no finding. The rule default grace is
	// 5m, so this also proves grace_sec was honoured.
	if hasRuleFor(Evaluate(store.Project([]ledger.Event{decl}), cfg, expected.Add(10*time.Minute)), "run.missing", "job:once") {
		t.Fatal("runonce within grace_sec must not raise run.missing")
	}
	// A run at/after the scheduled time resolves it.
	stRun := store.Project([]ledger.Event{
		decl,
		ev("run.started", "agent:cron-runner", "job:once", "run:1", expected.Add(13*time.Second), map[string]any{"job": "job:once"}),
		ev("run.finished", "agent:cron-runner", "job:once", "run:1", expected.Add(2*time.Minute), map[string]any{"status": "ok"}),
	})
	if hasRuleFor(Evaluate(stRun, cfg, now), "run.missing", "job:once") {
		t.Fatal("executed runonce job must not raise run.missing")
	}
	// RFC3339 form with an explicit offset parses as an absolute time.
	declTZ := ev("schedule.declared", "system:cron", "job:once-tz", "sched:once-tz", declared, map[string]any{"expr": "runonce@2026-11-16T08:00:00+09:00", "grace_sec": 900})
	expTZ, _ := time.Parse(time.RFC3339, "2026-11-16T08:00:00+09:00")
	if !hasRuleFor(Evaluate(store.Project([]ledger.Event{declTZ}), cfg, expTZ.Add(16*time.Minute)), "run.missing", "job:once-tz") {
		t.Fatal("expected run.missing for runonce with timezone offset")
	}
}

func TestMissingRunDisabledSchedule(t *testing.T) {
	cfg := config.Default()
	declared := time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local)
	now := time.Date(2026, 11, 16, 8, 16, 0, 0, time.Local)
	// Disabled runonce schedule (Phase A povo case): never a target.
	st := store.Project([]ledger.Event{
		ev("schedule.declared", "system:cron", "job:disabled-once", "sched:d1", declared, map[string]any{"expr": "runonce@2026-11-16T09:00:00", "grace_sec": 900, "enabled": false}),
		ev("schedule.declared", "system:cron", "job:disabled-cron", "sched:d2", declared, map[string]any{"expr": "0 9 * * *", "grace": "5m", "enabled": false}),
	})
	cands := Evaluate(st, cfg, now)
	if hasRuleFor(cands, "run.missing", "job:disabled-once") || hasRuleFor(cands, "run.missing", "job:disabled-cron") {
		t.Fatalf("disabled schedules must not raise run.missing, got %v", cands)
	}
}

// grace_sec (numeric seconds) is an alias for grace on cron schedules too.
func TestMissingRunGraceSecAlias(t *testing.T) {
	cfg := config.Default()
	declared := time.Date(2026, 11, 13, 9, 0, 0, 0, time.Local)
	st := store.Project([]ledger.Event{
		ev("schedule.declared", "system:cron", "job:daily", "sched:g", declared, map[string]any{"expr": "0 9 * * *", "grace_sec": 900}),
	})
	// Expected 09:00, now 09:10: inside grace_sec=15m (rule default is 5m).
	within := time.Date(2026, 11, 15, 9, 10, 0, 0, time.Local)
	if hasRuleFor(Evaluate(st, cfg, within), "run.missing", "job:daily") {
		t.Fatal("grace_sec must extend the grace window")
	}
	// Now 09:16: past expected+grace_sec with no run -> missing.
	after := time.Date(2026, 11, 15, 9, 16, 0, 0, time.Local)
	if !hasRuleFor(Evaluate(st, cfg, after), "run.missing", "job:daily") {
		t.Fatal("expected run.missing after grace_sec elapsed")
	}
}
