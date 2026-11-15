# michaku

An append-only **operations ledger** for people who run personal agents and
cron jobs on their own machines — plus silence detection on top of it:
the report that never arrived, the approval nobody resolved, the job that
quietly stopped running.

> **The observed side does nothing. Everything is placed on the environment side only.**
> The agents and jobs being watched install nothing, run nothing, and are
> modified in no way. There is no SDK to embed and no cooperation to ask for:
> recording happens through a wrapper the *environment* puts around a job,
> log following the *environment* runs against logs that already exist, and
> `emit` commands the *operator* (or operator-side scripts) issue.
> Detection is likewise run by the environment — your own cron/timer runs
> `michaku check`. Findings go to humans, never back to the agent.

## Install in 5 minutes: one binary + one cron line

```sh
# 1. Put the single static binary somewhere on PATH (or `go install` it)
go install github.com/nishimoto265/michaku@latest   # or download a release binary

# 2. Create a config (ledger paths, rules, notifications — all optional defaults)
michaku init

# 3. Wrap your first job — that is the whole integration
#    Before:  20 9 * * *  /home/you/bin/daily.sh
#    After:
20 9 * * *  michaku run --job daily --schedule "20 9 * * *" -- /home/you/bin/daily.sh

# 4. Let the environment check for silence (add one more cron line)
*/15 * * * *  michaku check

# 5. Read the morning digest
michaku digest --since 24h
```

The wrapper records `schedule.declared`, `run.started`, and `run.finished`
(exit code, duration) and **returns the job's exit code unchanged — even if
recording itself fails** (see "Never impedes" below). Prefer not to wrap?
Use the ping pair from inside your existing job script instead:
`michaku start daily` … `michaku finish daily --status ok`
(both are best-effort and never fail your job).

## What it detects (fixed 5 rules, all with grace/threshold)

| rule | catches |
|---|---|
| `missing_run` | a scheduled job that didn't run; a run that started and never finished |
| `report_missing` | a delegation whose report never arrived by its due time (severity rises with age) |
| `approval_stale` | an approval request left unresolved too long |
| `heartbeat_stale` | an actor that went silent |
| `repeated_failure` | a job failing N times within a window |

Findings are written back into the ledger itself (`finding.raised` /
`finding.resolved`), raised once per episode, re-notified only after a
configurable reminder interval. Notifications: generic webhook, Slack /
Discord / ntfy formats, file append, and `check`'s exit code (non-zero on a
new critical finding) so your existing cron mail can carry it.

## Recording paths (all environment/operator-side)

1. **Wrapper / ping pair** — `run`, `start`, `finish` (above).
2. **Log following** — `michaku follow` tails existing logs read-only and
   converts lines to events via a JSON key mapping or a regex with named
   groups. Offsets persist across restarts and rotation; fields can be
   excluded (e.g. secrets) at the mapping. See `examples/follow-presets.toml`.
3. **Operator emit** — `michaku emit <type> …` plus sugar for operator-side
   scripts: `heartbeat`, `delegate`, `report`, `approve-request`,
   `approve-resolve`. This is for the operator's tooling, not code you add
   to an agent.

Plus `import` (validate + load existing JSONL), `status` (open findings,
unreported delegations, pending approvals, silent actors), `rebuild`
(regenerate the SQLite projection from the JSONL source of truth).

## Design principles

- **Observe and report, never act.** No stop/restart/kill/nudge/re-run
  functionality exists in the binary. Findings are addressed to humans
  (digest / notifications) only — the tool is strictly one-way.
- **Role separation: the observed agent does nothing.** Detection runs from
  the environment's cron/timer; recording completes via wrapper, follow,
  and operator emit. No agent cooperation, modification, or SDK is assumed.
- **Never impedes the observed.** Recording failures are isolated: the
  `run` wrapper executes the job regardless and passes its exit code
  through verbatim (proven by tests that run it against an unwritable
  ledger); `start`/`finish` always exit 0; `follow` opens logs read-only
  and never writes, truncates, or renames them.
- **No telemetry.** Nothing is sent anywhere except notifications to
  destinations you configured.
- **Data is not closed.** The source of truth is a plain JSONL file you can
  grep/jq; SQLite is a derived, rebuildable projection.

See [SPEC.md](SPEC.md) for the event envelope, the 13-type vocabulary, and
the delegation / approval / finding lifecycle state machines.

## v0.1 scope and non-goals

In scope: single machine, single operator; emit + wrapper/ping + follow
ingestion; the 5 detection rules; Markdown/JSON digest; webhook/file/exit-code
notifications; TOML config in one file.

Non-goals: LLM tracing or prompt recording, evals, dashboards, any control
action, servers, multi-machine sync, OTel ingestion, HTTP ping endpoints.

## License

MIT — see [LICENSE](LICENSE).
