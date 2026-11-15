package detect

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/nishimoto265/opsledger/internal/config"
	"github.com/nishimoto265/opsledger/internal/store"
)

type Candidate struct {
	Key      string // finding_key, episode identity: rule|subject|episodeRef
	Rule     string // e.g. run.missing
	Kind     string // config rule name
	Subject  string
	Severity string // warn | critical
	Detail   string
}

// prevCron returns the latest scheduled time <= now within lookback.
func prevCron(expr string, now time.Time, lookback time.Duration) (time.Time, bool) {
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := p.Parse(expr)
	if err != nil {
		// try with seconds optional parser from robfig standard
		s2, err2 := cron.ParseStandard(expr)
		if err2 != nil {
			return time.Time{}, false
		}
		sched = s2
	}
	t := now.Add(-lookback)
	var last time.Time
	found := false
	for i := 0; i < 200000; i++ {
		next := sched.Next(t)
		if next.After(now) {
			break
		}
		last = next
		found = true
		t = next
	}
	return last, found
}

// Evaluate returns candidates that should be open at `now`.
func Evaluate(st *store.State, cfg *config.Config, now time.Time) []Candidate {
	var out []Candidate
	// merge config schedules into view
	schedules := map[string]*store.Schedule{}
	for k, v := range st.Schedules {
		cp := *v
		schedules[k] = &cp
	}
	for _, s := range cfg.Schedules {
		if _, ok := schedules[s.Subject]; !ok {
			grace, _ := time.ParseDuration(s.Grace)
			schedules[s.Subject] = &store.Schedule{Subject: s.Subject, Expr: s.Expr, Every: s.Every, Grace: grace, Declared: now.Add(-24 * time.Hour)}
		}
	}

	// missing_run + overdue
	if r := cfg.Rule("missing_run"); r.Enabled {
		graceDef := r.Dur("grace", 5*time.Minute)
		maxDur := r.Dur("max_duration", 2*time.Hour)
		for _, sc := range schedules {
			grace := sc.Grace
			if grace == 0 {
				grace = graceDef
			}
			if sc.Expr != "" {
				expected, ok := prevCron(sc.Expr, now, 8*24*time.Hour)
				if !ok {
					continue
				}
				if now.Sub(expected) <= grace {
					continue
				}
				last, has := st.LastRun[sc.Subject]
				if !has || last.Before(expected) {
					out = append(out, Candidate{Key: "run.missing|" + sc.Subject + "|" + expected.Format(time.RFC3339), Rule: "run.missing", Kind: "missing_run", Subject: sc.Subject, Severity: "warn", Detail: fmt.Sprintf("expected run at %s, none recorded (grace %s)", expected.Format(time.RFC3339), grace)})
				}
			} else if sc.Every != "" {
				every, err := time.ParseDuration(sc.Every)
				if err != nil || every <= 0 {
					continue
				}
				last, has := st.LastRun[sc.Subject]
				anchor := sc.Declared
				if has {
					anchor = last
				}
				if now.Sub(anchor) > every+grace {
					ep := anchor.Add(every).Format(time.RFC3339)
					out = append(out, Candidate{Key: "run.missing|" + sc.Subject + "|" + ep, Rule: "run.missing", Kind: "missing_run", Subject: sc.Subject, Severity: "warn", Detail: fmt.Sprintf("no run within every=%s + grace %s (last anchor %s)", every, grace, anchor.Format(time.RFC3339))})
				}
			}
		}
		for k, run := range st.OpenRuns {
			if now.Sub(run.Started) > maxDur {
				out = append(out, Candidate{Key: "run.overdue|" + run.Subject + "|" + k, Rule: "run.overdue", Kind: "missing_run", Subject: run.Subject, Severity: "warn", Detail: fmt.Sprintf("run started %s still open after %s", run.Started.Format(time.RFC3339), maxDur)})
			}
		}
	}

	// report_missing
	if r := cfg.Rule("report_missing"); r.Enabled {
		grace := r.Dur("grace", 0)
		crit := r.Dur("critical_after", 24*time.Hour)
		for _, d := range st.Delegations {
			if d.Due == nil {
				continue
			}
			if now.After(d.Due.Add(grace)) {
				sev := "warn"
				if now.After(d.Due.Add(crit)) {
					sev = "critical"
				}
				out = append(out, Candidate{Key: "delegation.unreported|" + d.Subject + "|" + d.Key, Rule: "delegation.unreported", Kind: "report_missing", Subject: d.Subject, Severity: sev, Detail: fmt.Sprintf("delegation %s due %s unreported (task: %s)", d.Key, d.Due.Format(time.RFC3339), d.Task)})
			}
		}
	}

	// approval_stale
	if r := cfg.Rule("approval_stale"); r.Enabled {
		thr := r.Dur("threshold", 24*time.Hour)
		for _, a := range st.Approvals {
			if now.Sub(a.Requested) > thr {
				sev := "warn"
				if now.Sub(a.Requested) > 2*thr {
					sev = "critical"
				}
				out = append(out, Candidate{Key: "approval.pending_stale|" + a.Subject + "|" + a.Key, Rule: "approval.pending_stale", Kind: "approval_stale", Subject: a.Subject, Severity: sev, Detail: fmt.Sprintf("approval %s pending since %s (threshold %s): %s", a.Key, a.Requested.Format(time.RFC3339), thr, a.What)})
			}
		}
	}

	// heartbeat_stale
	if r := cfg.Rule("heartbeat_stale"); r.Enabled {
		thr := r.Dur("threshold", time.Hour)
		for actor, last := range st.Actors {
			if now.Sub(last) > thr {
				out = append(out, Candidate{Key: "actor.silent|" + actor + "|" + last.Format(time.RFC3339), Rule: "actor.silent", Kind: "heartbeat_stale", Subject: actor, Severity: "warn", Detail: fmt.Sprintf("actor %s last seen %s (threshold %s)", actor, last.Format(time.RFC3339), thr)})
			}
		}
	}

	// repeated_failure
	if r := cfg.Rule("repeated_failure"); r.Enabled {
		window := r.Dur("window", 24*time.Hour)
		n := r.Count
		if n <= 0 {
			n = 3
		}
		counts := map[string]int{}
		var latest map[string]time.Time = map[string]time.Time{}
		for _, fr := range st.Finished {
			if fr.Status == "failed" && now.Sub(fr.TS) <= window {
				counts[fr.Subject]++
				if fr.TS.After(latest[fr.Subject]) {
					latest[fr.Subject] = fr.TS
				}
			}
		}
		for subj, c := range counts {
			if c >= n {
				out = append(out, Candidate{Key: fmt.Sprintf("run.flapping|%s|%s", subj, latest[subj].Format(time.RFC3339)), Rule: "run.flapping", Kind: "repeated_failure", Subject: subj, Severity: "warn", Detail: fmt.Sprintf("%d failed runs within %s", c, window)})
			}
		}
	}
	return out
}

// Diff computes newly raised and resolved candidates against open findings.
// Episode identity: an already-open finding with the same rule+subject prefix
// suppresses a duplicate raise (even if the episode suffix differs slightly),
// except flapping/missing where suffix is schedule time — prefix match is used.
func Diff(open map[string]*store.Finding, cands []Candidate) (raised, still []Candidate, resolved []*store.Finding) {
	candKeys := map[string]bool{}
	openByPrefix := map[string]*store.Finding{}
	for _, f := range open {
		openByPrefix[prefix(f.Key)] = f
	}
	for _, c := range cands {
		candKeys[prefix(c.Key)] = true
		if _, exists := openByPrefix[prefix(c.Key)]; exists {
			still = append(still, c)
		} else {
			raised = append(raised, c)
		}
	}
	for _, f := range open {
		if !candKeys[prefix(f.Key)] {
			resolved = append(resolved, f)
		}
	}
	return
}
func prefix(k string) string {
	// key = rule|subject|episode -> prefix rule|subject
	n := 0
	for i, ch := range k {
		if ch == '|' {
			n++
			if n == 2 {
				return k[:i]
			}
		}
	}
	return k
}

// ShouldRemind reports whether an open finding is due for a reminder.
func ShouldRemind(f *store.Finding, cfg *config.Config, now time.Time) bool {
	kind := kindForRule(f.Rule)
	r := cfg.Rule(kind)
	rem := r.Dur("reminder", 24*time.Hour)
	if rem <= 0 {
		return false
	}
	return now.Sub(f.Raised) >= rem
}
func kindForRule(rule string) string {
	switch rule {
	case "run.missing", "run.overdue":
		return "missing_run"
	case "delegation.unreported":
		return "report_missing"
	case "approval.pending_stale":
		return "approval_stale"
	case "actor.silent":
		return "heartbeat_stale"
	case "run.flapping":
		return "repeated_failure"
	}
	return ""
}
