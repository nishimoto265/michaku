# michaku SPEC v0.1

This document is the normative specification of the michaku event model.
The ledger is the spec's asset: tools may be replaced, the vocabulary and
lifecycles below are meant to be adopted as-is.

## 1. Envelope

The source of truth is an append-only JSONL file: one event per line.

```json
{"v":1,"id":"<ULID>","ts":"2026-11-15T09:20:03+09:00","type":"run.finished",
 "actor":"agent:poikatsu-daily","subject":"job:poikatsu-daily-routine",
 "corr":"run:<ULID>","src":"wrap","data":{...}}
```

| field | meaning |
|---|---|
| `v` | Schema version. Changes are additive-only; breaking changes bump `v`. |
| `id` | ULID, unique per event. |
| `ts` | Occurrence time, RFC 3339 with timezone. Detection always applies a grace period, because ingestion time may lag occurrence time. |
| `type` | One of the 13 event types in §2. |
| `actor` | Who/what the event is about or from (e.g. `agent:...`). |
| `subject` | The thing acted on (e.g. `job:...`). |
| `corr` | Correlation id linking a lifecycle (a run, a delegation, an approval). Lifecycle peers share one `corr`. |
| `src` | Ingestion path: `wrap`, `ping`, `follow:<name>`, `cli`, `check`, `import`. |
| `data` | Type-specific payload (§2). |

Appends are atomic: an exclusive `flock` plus a single-line write.
The SQLite database is a *derived* projection only; `michaku rebuild`
regenerates it from the JSONL at any time. If they disagree, the JSONL wins.

## 2. Event vocabulary (13 types)

| type | meaning | main `data` fields |
|---|---|---|
| `actor.registered` | An agent/process registers | `kind` (`personal-agent`/`cron-job`/`subagent`/…), `host`, `runtime` |
| `actor.heartbeat` | Heartbeat: alive and active | `note` (optional) |
| `activity.seen` | Generic "activity observed" from a follow adapter | `source`, `ref` |
| `schedule.declared` | Expected schedule declared | `expr` (cron, or one-shot `runonce@<RFC3339>`; a timestamp without an offset is read in the local timezone) or `every` (interval), `grace` (duration string) or `grace_sec` (numeric seconds, alias used only when `grace` is absent), `enabled` (optional; `false` excludes the schedule from detection) |
| `run.started` | A run started | `job` |
| `run.finished` | A run finished | `status` (`ok`/`failed`), `exit_code`, `duration_ms`, `output_tail` (optional), `partial` (optional; `true` = completed but incomplete — the digest marks it “partial: completed but incomplete”), `sites_incomplete` (optional list of unfinished items, e.g. sites; generic alias `incomplete`), `note` (optional) |
| `delegation.created` | A delegation occurred | `to`, `task`, `due` (report deadline) |
| `delegation.reported` | A report/handover arrived | `outcome` (`completed`/`failed`/`partial`), `ref` |
| `delegation.cancelled` | A delegation was cancelled/expired | `reason` |
| `approval.requested` | An approval was requested | `what`, `requested_by`, `expires` (optional) |
| `approval.resolved` | An approval was resolved | `decision` (`approved`/`rejected`), `by` |
| `finding.raised` | Detection fired (written back by the checker) | `rule`, `finding_key`, `severity`, `detail` |
| `finding.resolved` | Detection cleared (written back by the checker) | `rule`, `finding_key` |

## 3. Lifecycle state machines

### 3.1 Run
```
run.started ──► run.finished          (closed)
run.started ──► (no finished past max_duration) ──► finding: run.overdue
schedule.declared + no run past expected time + grace ──► finding: run.missing
schedule.declared with expr runonce@<time> is a one-shot schedule: it is
judged once, at that time + grace; a run at or after that time resolves it.
schedule.declared with data enabled=false is not a detection target at all.
```

### 3.2 Delegation
```
delegation.created ──► delegation.reported    (closed)
                   └─► delegation.cancelled   (closed)
                   └─► due passes unreported ──► finding: delegation.unreported
                        severity: warn at due (+grace), critical at due + critical_after
```

### 3.3 Approval
```
approval.requested ──► approval.resolved      (closed)
                   └─► unresolved past threshold ──► finding: approval.pending_stale
```

### 3.4 Finding (episode)
```
(condition appears) ──► finding.raised        (exactly once per episode)
(condition persists) ──► no duplicate raise; notification re-sent only after `reminder`
(condition clears)   ──► finding.resolved
```
Episode identity is `rule + subject + episode reference` (the `finding_key`).
An already-open finding with the same rule+subject suppresses duplicates.

### 3.5 Actor
Only actors with a heartbeat expectation are tracked. An actor becomes
tracked by emitting `actor.registered` or `actor.heartbeat`; nothing else
creates an expectation.
```
actor.registered / actor.heartbeat ──► actor becomes tracked, last-seen updated
(tracked actor) any later event, incl. activity.seen ──► last-seen updated
(untracked actor) appearing as actor of any other event ──► not tracked,
     no last-seen recorded, never a heartbeat_stale target
tracked actor's last-seen older than threshold ──► finding: actor.silent
```
In particular, the checker's own `finding.*` events (actor
`michaku:check`) and actors that only declare schedules, execute runs,
or request approvals are untracked and must not raise `actor.silent`.

## 4. Detection rules (fixed 5 types)

| rule type | condition | finding |
|---|---|---|
| `missing_run` | schedule (cron/`every`/`runonce@<time>`) + grace passed with no run; or a started run exceeds `max_duration`. Schedules declared with `enabled: false` are excluded | `run.missing` / `run.overdue` |
| `report_missing` | `delegation.created` passes `due` with no report; severity rises by age band | `delegation.unreported` |
| `approval_stale` | `approval.requested` unresolved past `threshold` | `approval.pending_stale` |
| `heartbeat_stale` | tracked actor's (registered or heartbeat-emitting, §3.5) last heartbeat/`activity.seen` older than `threshold` | `actor.silent` |
| `repeated_failure` | ≥ N `run.finished` with `status=failed` for one subject within `window` | `run.flapping` |

Every rule requires grace/threshold parameters — no rule fires on raw
timestamps without an allowance for clock skew and ingestion delay.

## 5. Design principles

1. **Observe and report, never act.** The binary contains no function to
   stop, restart, kill, nudge, or re-run anything it observes. Its only
   outward actions are notifications to destinations the operator configured
   (webhook POST / file append) and its own exit code.
2. **Role separation — the observed agent does nothing.**
   - Detection (`check`) is executed by the *environment*: the operator's
     cron / systemd timer runs it. An observed agent never runs anything
     to monitor itself.
   - Recording completes through exactly three paths, all on the
     environment/operator side:
     1. **Wrapper** — the environment wraps jobs (`michaku run -- …`);
     2. **Follow** — the environment reads logs the observed systems
        already produce (`michaku follow`);
     3. **Operator emit** — the operator or operator-side scripts issue
        `emit` and its sugar commands. Emit is *not* something embedded
        into the observed agent.
   - No cooperation, modification, or SDK integration of the observed
     agent is ever assumed or required.
3. **Never impede the observed (non-interference).**
   - *One-way only:* the tool receives records; it never sends instructions,
     interrupts, or response requests to an agent. Findings are addressed
     to humans (digest / notifications) only.
   - *Recording-failure isolation (most important):* if appending to the
     ledger fails, the observed process is unaffected. In particular the
     `run` wrapper always executes the wrapped command and returns its
     exit code verbatim — a recording failure never turns a job into a
     failure and never masks a real failure. `start`/`finish` are
     best-effort and exit 0 even when recording fails.
   - *Follow is read-only:* target logs are opened with `O_RDONLY` only.
     Follow never writes to, truncates, or renames them; its offset
     bookmark lives in a separate file.
   - *No control functions:* there are no stop/restart/kill commands.
     The sole exception in spirit is `run`, which executes the operator's
     own command as a child — exactly the command the operator already
     put in their crontab (the runitor position). Operators who do not
     want even that use the `start`/`finish` ping pair instead.
4. **No telemetry.** The tool sends nothing anywhere except notifications
   to destinations the operator explicitly configured.
5. **Data is not closed.** The JSONL source of truth is grep/jq-readable,
   digest is available as JSON, and notifications are plain webhooks, so
   leaving or combining michaku with other tools stays cheap.

## 6. Non-goals (v0.1)

LLM-call tracing, prompt/response recording, evals/scoring, web dashboards,
any control action, multi-machine sync, servers required to run, OTel span
ingestion, HTTP ping endpoints, and custom span schemas.
