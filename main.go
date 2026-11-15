package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nishimoto265/opsledger/internal/config"
	"github.com/nishimoto265/opsledger/internal/detect"
	"github.com/nishimoto265/opsledger/internal/digest"
	"github.com/nishimoto265/opsledger/internal/followx"
	"github.com/nishimoto265/opsledger/internal/ledger"
	"github.com/nishimoto265/opsledger/internal/notify"
	"github.com/nishimoto265/opsledger/internal/store"
)

func main() { os.Exit(runMain(os.Args[1:])) }

func runMain(args []string) int {
	cfgPath := "opsledger.toml"
	// extract --config / -config anywhere before subcommand
	var rest []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--config" || args[i] == "-config") && i+1 < len(args) {
			cfgPath = args[i+1]
			i++
		} else if strings.HasPrefix(args[i], "--config=") {
			cfgPath = strings.TrimPrefix(args[i], "--config=")
		} else {
			rest = append(rest, args[i])
		}
	}
	args = rest
	if len(args) == 0 {
		usage()
		return 2
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 2
	}
	cmd, cargs := args[0], args[1:]
	switch cmd {
	case "init":
		return cmdInit(cargs)
	case "emit":
		return cmdEmit(cfg, cargs)
	case "heartbeat":
		return cmdHeartbeat(cfg, cargs)
	case "delegate":
		return cmdDelegate(cfg, cargs)
	case "report":
		return cmdReport(cfg, cargs)
	case "approve-request":
		return cmdApproveRequest(cfg, cargs)
	case "approve-resolve":
		return cmdApproveResolve(cfg, cargs)
	case "run":
		return cmdRun(cfg, cargs)
	case "start":
		return cmdStartFinish(cfg, cargs, true)
	case "finish":
		return cmdStartFinish(cfg, cargs, false)
	case "follow":
		return cmdFollow(cfg, cargs)
	case "import":
		return cmdImport(cfg, cargs)
	case "check":
		return cmdCheck(cfg, cargs)
	case "digest":
		return cmdDigest(cfg, cargs)
	case "status":
		return cmdStatus(cfg, cargs)
	case "rebuild":
		return cmdRebuild(cfg, cargs)
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `opsledger — append-only operations ledger (observe and report, never act)
Usage: opsledger [--config opsledger.toml] <command> [flags]
Commands: init, emit, heartbeat, delegate, report, approve-request, approve-resolve,
          run, start, finish, follow, import, check, digest, status, rebuild`)
}

func warnf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

func mustAppend(cfg *config.Config, e ledger.Event) int {
	if err := ledger.Append(cfg.LedgerPath, e); err != nil {
		warnf("opsledger: failed to record: %v", err)
		return 1
	}
	fmt.Println(e.ID)
	return 0
}

func parseDue(s string) string {
	if s == "" {
		return ""
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d).Format(time.RFC3339)
	}
	return s // assume RFC3339
}

func projectState(cfg *config.Config) (*store.State, []ledger.Event, error) {
	events, err := ledger.ReadAll(cfg.LedgerPath)
	if err != nil {
		return nil, nil, err
	}
	if cfg.DBPath != "" {
		if st, err := store.Open(cfg.DBPath); err == nil {
			_, _ = st.Rebuild(events)
			st.Close()
		}
	}
	return store.Project(events), events, nil
}

// --- init ---
func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	path := fs.String("path", "opsledger.toml", "config path")
	force := fs.Bool("force", false, "overwrite")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := os.Stat(*path); err == nil && !*force {
		warnf("%s already exists (use --force)", *path)
		return 1
	}
	if err := os.WriteFile(*path, []byte(config.Template), 0o644); err != nil {
		warnf("%v", err)
		return 1
	}
	fmt.Println("wrote", *path)
	return 0
}

// --- emit & sugar ---
func cmdEmit(cfg *config.Config, args []string) int {
	if len(args) < 1 {
		warnf("usage: emit <type> [--actor A] [--subject S] [--corr C] [--data JSON]")
		return 2
	}
	typ := args[0]
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	actor := fs.String("actor", "", "")
	subject := fs.String("subject", "", "")
	corr := fs.String("corr", "", "")
	src := fs.String("src", "cli", "")
	dataStr := fs.String("data", "{}", "")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	var data any
	if err := json.Unmarshal([]byte(*dataStr), &data); err != nil {
		warnf("invalid --data JSON: %v", err)
		return 2
	}
	return mustAppend(cfg, ledger.NewEvent(typ, *actor, *subject, *corr, *src, data))
}

func cmdHeartbeat(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("heartbeat", flag.ContinueOnError)
	actor := fs.String("actor", "", "actor (required)")
	note := fs.String("note", "", "")
	if err := fs.Parse(args); err != nil || *actor == "" {
		warnf("usage: heartbeat --actor A [--note N]")
		return 2
	}
	data := map[string]any{}
	if *note != "" {
		data["note"] = *note
	}
	return mustAppend(cfg, ledger.NewEvent("actor.heartbeat", *actor, *actor, "", "cli", data))
}

func cmdDelegate(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("delegate", flag.ContinueOnError)
	from := fs.String("from", "", "delegating actor")
	to := fs.String("to", "", "delegatee actor")
	subject := fs.String("subject", "", "subject")
	task := fs.String("task", "", "task summary")
	due := fs.String("due", "", "RFC3339 or duration e.g. 2h")
	corr := fs.String("corr", "", "correlation id (auto if empty)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c := *corr
	if c == "" {
		c = "delegation:" + ledger.NewID()
	}
	data := map[string]any{"task": *task, "to": *to}
	if *due != "" {
		data["due"] = parseDue(*due)
	}
	e := ledger.NewEvent("delegation.created", *from, *subject, c, "cli", data)
	if err := ledger.Append(cfg.LedgerPath, e); err != nil {
		warnf("failed to record: %v", err)
		return 1
	}
	fmt.Printf("%s %s\n", e.ID, c)
	return 0
}

func cmdReport(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	actor := fs.String("actor", "", "")
	subject := fs.String("subject", "", "")
	corr := fs.String("corr", "", "delegation corr (required)")
	outcome := fs.String("outcome", "completed", "completed|failed|partial")
	ref := fs.String("ref", "", "")
	if err := fs.Parse(args); err != nil || *corr == "" {
		warnf("usage: report --corr C --subject S [--outcome completed] [--ref R]")
		return 2
	}
	return mustAppend(cfg, ledger.NewEvent("delegation.reported", *actor, *subject, *corr, "cli", map[string]any{"outcome": *outcome, "ref": *ref}))
}

func cmdApproveRequest(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("approve-request", flag.ContinueOnError)
	actor := fs.String("actor", "", "")
	subject := fs.String("subject", "", "")
	what := fs.String("what", "", "")
	corr := fs.String("corr", "", "")
	expires := fs.String("expires", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c := *corr
	if c == "" {
		c = "approval:" + ledger.NewID()
	}
	data := map[string]any{"what": *what, "requested_by": *actor}
	if *expires != "" {
		data["expires"] = parseDue(*expires)
	}
	e := ledger.NewEvent("approval.requested", *actor, *subject, c, "cli", data)
	if err := ledger.Append(cfg.LedgerPath, e); err != nil {
		warnf("failed: %v", err)
		return 1
	}
	fmt.Printf("%s %s\n", e.ID, c)
	return 0
}

func cmdApproveResolve(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("approve-resolve", flag.ContinueOnError)
	by := fs.String("by", "", "")
	subject := fs.String("subject", "", "")
	corr := fs.String("corr", "", "approval corr (required)")
	decision := fs.String("decision", "approved", "approved|rejected")
	if err := fs.Parse(args); err != nil || *corr == "" {
		warnf("usage: approve-resolve --corr C --decision approved|rejected")
		return 2
	}
	return mustAppend(cfg, ledger.NewEvent("approval.resolved", *by, *subject, *corr, "cli", map[string]any{"decision": *decision, "by": *by}))
}

// --- run wrapper: FAILURE ISOLATION is the core guarantee here. ---
// Recording failures are warnings only; the wrapped command always runs and
// its exit code is returned verbatim.
func cmdRun(cfg *config.Config, args []string) int {
	// split at --
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		warnf("usage: run --job NAME [--schedule EXPR] [--every DUR] -- command...")
		return 2
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	job := fs.String("job", "", "job name (required)")
	schedule := fs.String("schedule", "", "cron expr")
	every := fs.String("every", "", "interval e.g. 1h")
	grace := fs.String("grace", "", "grace e.g. 5m")
	if err := fs.Parse(args[:sep]); err != nil || *job == "" {
		warnf("usage: run --job NAME [--schedule EXPR] [--every DUR] -- command...")
		return 2
	}
	childArgs := args[sep+1:]
	if len(childArgs) == 0 {
		warnf("run: no command after --")
		return 2
	}
	subject := *job
	if !strings.HasPrefix(subject, "job:") {
		subject = "job:" + subject
	}
	corr := "run:" + ledger.NewID()
	warn := func(s string) { fmt.Fprintln(os.Stderr, s) }

	// schedule.declared (best-effort, only when new/changed)
	if *schedule != "" || *every != "" {
		need := true
		if events, err := ledger.ReadAll(cfg.LedgerPath); err == nil {
			st := store.Project(events)
			if sc, ok := st.Schedules[subject]; ok && sc.Expr == *schedule && sc.Every == *every {
				need = false
			}
		}
		if need {
			ledger.AppendBestEffort(cfg.LedgerPath, ledger.NewEvent("schedule.declared", "opsledger:run", subject, corr, "wrap", map[string]any{"expr": *schedule, "every": *every, "grace": *grace}), warn)
		}
	}
	ledger.AppendBestEffort(cfg.LedgerPath, ledger.NewEvent("run.started", "opsledger:run", subject, corr, "wrap", map[string]any{"job": subject}), warn)

	start := time.Now()
	cmd := exec.Command(childArgs[0], childArgs[1:]...)
	cmd.Stdin = os.Stdin
	var tailBuf *tailBuffer
	if cfg.CaptureOutputTail {
		tailBuf = &tailBuffer{max: cfg.OutputTailLines}
		mw := io.MultiWriter(os.Stdout, tailBuf)
		cmd.Stdout = mw
		cmd.Stderr = io.MultiWriter(os.Stderr, tailBuf)
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	runErr := cmd.Run()
	exitCode := 0
	status := "ok"
	if runErr != nil {
		status = "failed"
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			// command could not start: this is the wrapped job's failure, not recording's
			warnf("run: failed to start command: %v", runErr)
			exitCode = 127
		}
	}
	data := map[string]any{"status": status, "exit_code": exitCode, "duration_ms": time.Since(start).Milliseconds(), "job": subject}
	if status == "failed" && tailBuf != nil {
		data["output_tail"] = tailBuf.String()
	}
	ledger.AppendBestEffort(cfg.LedgerPath, ledger.NewEvent("run.finished", "opsledger:run", subject, corr, "wrap", data), warn)
	// TRANSPARENCY: return child's code regardless of any recording failure above.
	return exitCode
}

type tailBuffer struct {
	max   int
	lines []string
	buf   bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf.Write(p)
	s := t.buf.String()
	parts := strings.Split(s, "\n")
	if len(parts) > 1 {
		t.lines = append(t.lines, parts[:len(parts)-1]...)
		if len(t.lines) > t.max*4 {
			t.lines = t.lines[len(t.lines)-t.max*2:]
		}
		t.buf.Reset()
		t.buf.WriteString(parts[len(parts)-1])
	}
	return len(p), nil
}
func (t *tailBuffer) String() string {
	lines := t.lines
	if len(lines) > t.max {
		lines = lines[len(lines)-t.max:]
	}
	return strings.Join(lines, "\n")
}

// start/finish ping pair: best-effort, ALWAYS exit 0 so a recording problem
// can never fail the caller's job.
func cmdStartFinish(cfg *config.Config, args []string, isStart bool) int {
	name := "finish"
	if isStart {
		name = "start"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	status := fs.String("status", "ok", "ok|failed")
	exitCodeF := fs.Int("exit-code", 0, "")
	corrF := fs.String("corr", "", "")
	rest := args
	// allow flags before/after positional job name: parse positional first if not flag
	var job string
	var flagArgs []string
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") && job == "" {
			job = a
		} else {
			flagArgs = append(flagArgs, a)
		}
	}
	_ = fs.Parse(flagArgs)
	if job == "" {
		warnf("usage: %s <job> [--corr C]", name)
		return 0 // best-effort: never break caller even on misuse? still warn
	}
	subject := job
	if !strings.HasPrefix(subject, "job:") {
		subject = "job:" + subject
	}
	corr := *corrF
	if corr == "" {
		corr = "run:" + subject
	}
	warn := func(s string) { fmt.Fprintln(os.Stderr, s) }
	if isStart {
		ledger.AppendBestEffort(cfg.LedgerPath, ledger.NewEvent("run.started", "opsledger:ping", subject, corr, "ping", map[string]any{"job": subject}), warn)
	} else {
		st := *status
		ledger.AppendBestEffort(cfg.LedgerPath, ledger.NewEvent("run.finished", "opsledger:ping", subject, corr, "ping", map[string]any{"status": st, "exit_code": *exitCodeF, "job": subject}), warn)
	}
	return 0
}

// --- follow (read-only) ---
func cmdFollow(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("follow", flag.ContinueOnError)
	once := fs.Bool("once", false, "process new lines once and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(cfg.Follow) == 0 {
		warnf("follow: no [[follow]] targets in config")
		return 1
	}
	appendFn := func(e ledger.Event) error { return ledger.Append(cfg.LedgerPath, e) }
	if *once {
		total := 0
		for _, t := range cfg.Follow {
			n, err := followx.ProcessNew(t, appendFn)
			if err != nil {
				warnf("follow %s: %v", t.Name, err)
				continue
			}
			total += n
		}
		fmt.Printf("followed: %d events\n", total)
		return 0
	}
	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; close(done) }()
	fmt.Fprintln(os.Stderr, "following (read-only); Ctrl-C to stop")
	return boolToInt(followx.Follow(cfg.Follow, appendFn, done, time.Second) != nil)
}
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- import ---
func cmdImport(cfg *config.Config, args []string) int {
	if len(args) < 1 {
		warnf("usage: import <file.jsonl>")
		return 2
	}
	existing, _ := ledger.ReadAll(cfg.LedgerPath)
	seen := map[string]bool{}
	for _, e := range existing {
		seen[e.ID] = true
	}
	f, err := os.Open(args[0])
	if err != nil {
		warnf("%v", err)
		return 1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	imported, skipped := 0, 0
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		e, err := ledger.ValidateLine(s)
		if err != nil {
			warnf("import: line %d invalid, skipped: %v", line, err)
			skipped++
			continue
		}
		if seen[e.ID] {
			skipped++
			continue
		}
		if err := ledger.Append(cfg.LedgerPath, e); err != nil {
			warnf("import: line %d append failed: %v", line, err)
			skipped++
			continue
		}
		seen[e.ID] = true
		imported++
	}
	fmt.Printf("imported=%d skipped=%d\n", imported, skipped)
	return 0
}

// --- check ---
func cmdCheck(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	nowStr := fs.String("now", "", "evaluate at RFC3339 time (testing)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	now := time.Now()
	if *nowStr != "" {
		if t, err := time.Parse(time.RFC3339, *nowStr); err == nil {
			now = t
		}
	}
	st, _, err := projectState(cfg)
	if err != nil {
		warnf("check: %v", err)
		return 1
	}
	cands := detect.Evaluate(st, cfg, now)
	raised, _, resolved := detect.Diff(st.Findings, cands)
	hasCritical := false
	warn := func(s string) { fmt.Fprintln(os.Stderr, s) }
	_ = warn
	for _, c := range raised {
		if c.Severity == "critical" {
			hasCritical = true
		}
		e := ledger.NewEvent("finding.raised", "opsledger:check", c.Subject, "", "check", map[string]any{"rule": c.Rule, "finding_key": c.Key, "severity": c.Severity, "detail": c.Detail})
		e.TS = now
		if err := ledger.Append(cfg.LedgerPath, e); err != nil {
			warnf("check: failed to write finding: %v", err)
			continue
		}
		fmt.Printf("RAISED [%s] %s %s — %s\n", c.Severity, c.Rule, c.Subject, c.Detail)
		notify.SendAll(cfg.Notify, notify.Message{Title: "opsledger finding raised: " + c.Rule, Body: c.Detail + " (" + c.Subject + ")", Kind: "raised"})
	}
	for _, f := range resolved {
		e := ledger.NewEvent("finding.resolved", "opsledger:check", f.Subject, "", "check", map[string]any{"rule": f.Rule, "finding_key": f.Key, "detail": "resolved"})
		e.TS = now
		if err := ledger.Append(cfg.LedgerPath, e); err != nil {
			warnf("check: failed to write resolved: %v", err)
			continue
		}
		fmt.Printf("RESOLVED %s %s\n", f.Rule, f.Subject)
		notify.SendAll(cfg.Notify, notify.Message{Title: "opsledger finding resolved: " + f.Rule, Body: f.Subject, Kind: "resolved"})
	}
	// reminders for still-open findings (no new ledger event, notification only)
	for _, f := range st.Findings {
		if detect.ShouldRemind(f, cfg, now) {
			// only remind if it was not just raised in this run
			justRaised := false
			for _, c := range raised {
				if strings.HasPrefix(c.Key, f.Rule+"|") {
					justRaised = true
				}
			}
			if !justRaised {
				notify.SendAll(cfg.Notify, notify.Message{Title: "opsledger reminder: " + f.Rule, Body: f.Detail + " (" + f.Subject + ")", Kind: "reminder"})
			}
		}
	}
	if len(raised) == 0 && len(resolved) == 0 {
		fmt.Println("check: no new findings")
	}
	if hasCritical {
		return 1
	}
	return 0
}

// --- digest ---
func cmdDigest(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("digest", flag.ContinueOnError)
	since := fs.String("since", "24h", "duration")
	format := fs.String("format", "markdown", "markdown|json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, _, err := projectState(cfg)
	if err != nil {
		warnf("%v", err)
		return 1
	}
	d, _ := time.ParseDuration(*since)
	now := time.Now()
	rep := digest.Build(st, now.Add(-d), now)
	if *format == "json" {
		b, _ := rep.JSON()
		fmt.Println(string(b))
	} else {
		fmt.Print(rep.Markdown())
	}
	return 0
}

// --- status ---
func cmdStatus(cfg *config.Config, args []string) int {
	st, _, err := projectState(cfg)
	if err != nil {
		warnf("%v", err)
		return 1
	}
	fmt.Printf("Open findings: %d\n", len(st.Findings))
	for _, f := range st.Findings {
		fmt.Printf("  [%s] %s %s since %s — %s\n", f.Severity, f.Rule, f.Subject, f.Raised.Format(time.RFC3339), f.Detail)
	}
	fmt.Printf("Unreported delegations: %d\n", len(st.Delegations))
	for _, d := range st.Delegations {
		due := ""
		if d.Due != nil {
			due = " due " + d.Due.Format(time.RFC3339)
		}
		fmt.Printf("  %s (%s)%s — %s\n", d.Subject, d.Key, due, d.Task)
	}
	fmt.Printf("Pending approvals: %d\n", len(st.Approvals))
	for _, a := range st.Approvals {
		fmt.Printf("  %s (%s) since %s — %s\n", a.Subject, a.Key, a.Requested.Format(time.RFC3339), a.What)
	}
	fmt.Printf("Open runs: %d\n", len(st.OpenRuns))
	for k, r := range st.OpenRuns {
		fmt.Printf("  %s (%s) since %s\n", r.Subject, k, r.Started.Format(time.RFC3339))
	}
	fmt.Printf("Actors: %d\n", len(st.Actors))
	for a, t := range st.Actors {
		fmt.Printf("  %s last seen %s\n", a, t.Format(time.RFC3339))
	}
	return 0
}

// --- rebuild ---
func cmdRebuild(cfg *config.Config, args []string) int {
	events, err := ledger.ReadAll(cfg.LedgerPath)
	if err != nil {
		warnf("%v", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		warnf("%v", err)
		return 1
	}
	defer st.Close()
	proj, err := st.Rebuild(events)
	if err != nil {
		warnf("%v", err)
		return 1
	}
	fmt.Printf("rebuilt: events=%d actors=%d delegations=%d approvals=%d findings=%d schedules=%d\n", len(events), len(proj.Actors), len(proj.Delegations), len(proj.Approvals), len(proj.Findings), len(proj.Schedules))
	return 0
}
