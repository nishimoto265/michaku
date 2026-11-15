package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nishimoto265/opsledger/internal/ledger"
)

// State is the in-memory projection derived from events. The SQLite store
// mirrors exactly this state, which makes rebuild consistency testable.
type Delegation struct {
	Key, Subject, Actor, Task string
	Created                   time.Time
	Due                       *time.Time
}
type Approval struct {
	Key, Subject, Actor, What string
	Requested                 time.Time
}
type Finding struct {
	Key, Rule, Subject, Severity, Detail string
	Raised                               time.Time
}
type RunInfo struct {
	Subject string
	Started time.Time
}
type Schedule struct {
	Subject, Expr, Every string
	Grace                time.Duration
	Declared             time.Time
	// Enabled is false only when schedule.declared carried enabled:false;
	// a disabled schedule is excluded from missing_run detection.
	Enabled bool
}
type FinishedRun struct {
	Subject, Status string
	TS              time.Time
}

type State struct {
	// Actors holds the last-seen time of tracked actors only (see
	// TrackedActors). Appearances of untracked actors are not recorded here.
	Actors map[string]time.Time
	// TrackedActors is the set of actors with a heartbeat expectation:
	// those that emitted actor.registered or actor.heartbeat. Only they
	// are subject to heartbeat_stale detection.
	TrackedActors map[string]bool
	ActorKind     map[string]string
	Delegations   map[string]*Delegation // open only, key = corr or event id
	Approvals     map[string]*Approval
	Findings      map[string]*Finding
	OpenRuns      map[string]*RunInfo // key = corr
	LastRun       map[string]time.Time
	LastFinished  map[string]FinishedRun
	Finished      []FinishedRun
	Schedules     map[string]*Schedule
	Events        []ledger.Event
}

func NewState() *State {
	return &State{
		Actors: map[string]time.Time{}, TrackedActors: map[string]bool{}, ActorKind: map[string]string{},
		Delegations: map[string]*Delegation{}, Approvals: map[string]*Approval{},
		Findings: map[string]*Finding{}, OpenRuns: map[string]*RunInfo{},
		LastRun: map[string]time.Time{}, LastFinished: map[string]FinishedRun{},
		Schedules: map[string]*Schedule{},
	}
}

func keyOf(e ledger.Event) string {
	if e.Corr != "" {
		return e.Corr
	}
	return e.ID
}

func (s *State) Apply(e ledger.Event) {
	s.Events = append(s.Events, e)
	// Heartbeat expectation (SPEC §3.5): only actor.registered and
	// actor.heartbeat create a tracked actor. Merely appearing as the
	// actor of any other event (run.*, schedule.declared, approval.*,
	// finding.* written by the checker itself, …) never does.
	if e.Actor != "" {
		if e.Type == "actor.registered" || e.Type == "actor.heartbeat" {
			s.TrackedActors[e.Actor] = true
		}
		if dataActor := e.DataString("actor"); dataActor != "" && e.Type == "actor.heartbeat" {
			s.TrackedActors[dataActor] = true
		}
	}
	// Last-seen is maintained for tracked actors only; any event by a
	// tracked actor (heartbeat, activity.seen, runs, …) refreshes it.
	if e.Actor != "" && s.TrackedActors[e.Actor] {
		s.Actors[e.Actor] = maxTime(s.Actors[e.Actor], e.TS)
	}
	if (e.Type == "actor.heartbeat" || e.Type == "activity.seen") && e.DataString("actor") != "" {
		if subj := e.DataString("actor"); s.TrackedActors[subj] {
			s.Actors[subj] = maxTime(s.Actors[subj], e.TS)
		}
	}
	switch e.Type {
	case "actor.registered":
		if k := e.DataString("kind"); k != "" && e.Actor != "" {
			s.ActorKind[e.Actor] = k
		}
	case "schedule.declared":
		subj := e.Subject
		if subj == "" {
			subj = e.DataString("subject")
		}
		if subj != "" {
			// grace (duration string) takes precedence; grace_sec
			// (numeric seconds) is accepted as an alias when grace
			// is absent or unparseable.
			grace, err := time.ParseDuration(e.DataString("grace"))
			if err != nil {
				if sec, ok := e.DataNumber("grace_sec"); ok {
					grace = time.Duration(sec * float64(time.Second))
				} else {
					grace = 0
				}
			}
			enabled := true
			if v, ok := e.DataBool("enabled"); ok {
				enabled = v
			}
			s.Schedules[subj] = &Schedule{Subject: subj, Expr: e.DataString("expr"), Every: e.DataString("every"), Grace: grace, Declared: e.TS, Enabled: enabled}
		}
	case "run.started":
		s.OpenRuns[keyOf(e)] = &RunInfo{Subject: e.Subject, Started: e.TS}
		if e.Subject != "" {
			s.LastRun[e.Subject] = maxTime(s.LastRun[e.Subject], e.TS)
		}
	case "run.finished":
		delete(s.OpenRuns, keyOf(e))
		fr := FinishedRun{Subject: e.Subject, Status: e.DataString("status"), TS: e.TS}
		if fr.Status == "" {
			fr.Status = "ok"
		}
		s.Finished = append(s.Finished, fr)
		if e.Subject != "" {
			s.LastFinished[e.Subject] = fr
			s.LastRun[e.Subject] = maxTime(s.LastRun[e.Subject], e.TS)
		}
	case "delegation.created":
		d := &Delegation{Key: keyOf(e), Subject: e.Subject, Actor: e.Actor, Task: e.DataString("task"), Created: e.TS}
		if due, ok := e.DataTime("due"); ok {
			d.Due = &due
		}
		s.Delegations[d.Key] = d
	case "delegation.reported", "delegation.cancelled":
		delete(s.Delegations, keyOf(e))
		// also allow matching by subject when corr differs in loose data
		if _, ok := s.Delegations[keyOf(e)]; !ok && e.Corr == "" {
			for k, d := range s.Delegations {
				if d.Subject == e.Subject {
					delete(s.Delegations, k)
					break
				}
			}
		}
	case "approval.requested":
		s.Approvals[keyOf(e)] = &Approval{Key: keyOf(e), Subject: e.Subject, Actor: e.Actor, What: e.DataString("what"), Requested: e.TS}
	case "approval.resolved":
		delete(s.Approvals, keyOf(e))
	case "finding.raised":
		fk := e.DataString("finding_key")
		if fk == "" {
			fk = e.DataString("rule") + "|" + e.Subject
		}
		s.Findings[fk] = &Finding{Key: fk, Rule: e.DataString("rule"), Subject: e.Subject, Severity: e.DataString("severity"), Detail: e.DataString("detail"), Raised: e.TS}
	case "finding.resolved":
		fk := e.DataString("finding_key")
		if fk == "" {
			fk = e.DataString("rule") + "|" + e.Subject
		}
		delete(s.Findings, fk)
	}
}
func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func Project(events []ledger.Event) *State {
	s := NewState()
	for _, e := range events {
		s.Apply(e)
	}
	return s
}

// --- SQLite derived store ---

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	st := &Store{DB: db}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events(id TEXT PRIMARY KEY, ts TEXT, type TEXT, actor TEXT, subject TEXT, corr TEXT, src TEXT, data TEXT);
CREATE TABLE IF NOT EXISTS actors(actor TEXT PRIMARY KEY, last_seen TEXT, kind TEXT);
CREATE TABLE IF NOT EXISTS open_delegations(key TEXT PRIMARY KEY, subject TEXT, actor TEXT, created TEXT, due TEXT, task TEXT);
CREATE TABLE IF NOT EXISTS pending_approvals(key TEXT PRIMARY KEY, subject TEXT, actor TEXT, requested TEXT, what TEXT);
CREATE TABLE IF NOT EXISTS job_schedules(subject TEXT PRIMARY KEY, expr TEXT, every TEXT, grace TEXT, declared TEXT);
CREATE TABLE IF NOT EXISTS open_findings(key TEXT PRIMARY KEY, rule TEXT, subject TEXT, severity TEXT, raised TEXT, detail TEXT);
CREATE TABLE IF NOT EXISTS open_runs(key TEXT PRIMARY KEY, subject TEXT, started TEXT);
CREATE TABLE IF NOT EXISTS job_last_run(subject TEXT PRIMARY KEY, last_run TEXT, last_status TEXT);`)
	return st, err
}
func (s *Store) Close() error { return s.DB.Close() }

// Rebuild wipes derived tables and replays all events.
func (s *Store) Rebuild(events []ledger.Event) (*State, error) {
	for _, t := range []string{"events", "actors", "open_delegations", "pending_approvals", "job_schedules", "open_findings", "open_runs", "job_last_run"} {
		if _, err := s.DB.Exec("DELETE FROM " + t); err != nil {
			return nil, err
		}
	}
	st := Project(events)
	for _, e := range events {
		b, _ := json.Marshal(e.Data)
		_ = b
		raw := string(e.Data)
		if _, err := s.DB.Exec(`INSERT OR REPLACE INTO events(id,ts,type,actor,subject,corr,src,data) VALUES(?,?,?,?,?,?,?,?)`, e.ID, e.TS.Format(time.RFC3339Nano), e.Type, e.Actor, e.Subject, e.Corr, e.Src, raw); err != nil {
			return nil, fmt.Errorf("insert event %s: %w", e.ID, err)
		}
	}
	for a, t := range st.Actors {
		if _, err := s.DB.Exec(`INSERT OR REPLACE INTO actors(actor,last_seen,kind) VALUES(?,?,?)`, a, t.Format(time.RFC3339Nano), st.ActorKind[a]); err != nil {
			return nil, err
		}
	}
	for k, d := range st.Delegations {
		due := ""
		if d.Due != nil {
			due = d.Due.Format(time.RFC3339Nano)
		}
		if _, err := s.DB.Exec(`INSERT OR REPLACE INTO open_delegations(key,subject,actor,created,due,task) VALUES(?,?,?,?,?,?)`, k, d.Subject, d.Actor, d.Created.Format(time.RFC3339Nano), due, d.Task); err != nil {
			return nil, err
		}
	}
	for k, a := range st.Approvals {
		if _, err := s.DB.Exec(`INSERT OR REPLACE INTO pending_approvals(key,subject,actor,requested,what) VALUES(?,?,?,?,?)`, k, a.Subject, a.Actor, a.Requested.Format(time.RFC3339Nano), a.What); err != nil {
			return nil, err
		}
	}
	for subj, sc := range st.Schedules {
		if _, err := s.DB.Exec(`INSERT OR REPLACE INTO job_schedules(subject,expr,every,grace,declared) VALUES(?,?,?,?,?)`, subj, sc.Expr, sc.Every, sc.Grace.String(), sc.Declared.Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
	}
	for k, f := range st.Findings {
		if _, err := s.DB.Exec(`INSERT OR REPLACE INTO open_findings(key,rule,subject,severity,raised,detail) VALUES(?,?,?,?,?,?)`, k, f.Rule, f.Subject, f.Severity, f.Raised.Format(time.RFC3339Nano), f.Detail); err != nil {
			return nil, err
		}
	}
	for k, r := range st.OpenRuns {
		if _, err := s.DB.Exec(`INSERT OR REPLACE INTO open_runs(key,subject,started) VALUES(?,?,?)`, k, r.Subject, r.Started.Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
	}
	for subj, fr := range st.LastFinished {
		if _, err := s.DB.Exec(`INSERT OR REPLACE INTO job_last_run(subject,last_run,last_status) VALUES(?,?,?)`, subj, fr.TS.Format(time.RFC3339Nano), fr.Status); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func (s *Store) Count(table string) (int, error) {
	var n int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
	return n, err
}
