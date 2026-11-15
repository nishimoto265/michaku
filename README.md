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
# 1. Install the single static binary (checksum-verified, into ~/.local/bin)
curl -fsSL https://raw.githubusercontent.com/nishimoto265/michaku/main/install.sh | sh
#    …or download a release archive from GitHub Releases, or:
#    go install github.com/nishimoto265/michaku@latest

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

## What the digest looks like

Generated from the synthetic ledger in [`examples/`](examples/) — a job that
ran fine yesterday and not today, a delegation whose report never came, an
agent that went quiet:

```markdown
# michaku digest

## Summary

- Events in ledger: 7 types tracked
- Open findings: 3
- Completed items in period: 1

## Completed

- job:daily-report run.finished status=ok at 2026-11-14T09:21:00+09:00

## Stalled / Abnormal

- [warn] actor.silent on agent:example-agent — last seen 2026-11-14T10:00:00+09:00 (threshold 1h0m0s)
- [critical] delegation.unreported on task:write-summary — due 2026-11-14T12:00:00+09:00 unreported (task: write the daily summary)
- [warn] run.missing on job:daily-report — expected run at 2026-11-14T09:20:00Z, none recorded (grace 10m0s)

## Pending Approvals

- (none)

## Unreported Delegations & Silent Actors

- task:write-summary created 2026-11-14T09:22:00+09:00, due 2026-11-14T12:00:00+09:00 — write the daily summary
- agent:example-agent last seen 2026-11-14T10:00:00+09:00 (29h34m0s ago)
```

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

Status: michaku is in daily use by its author. Before this release it was
validated by replaying real operational incidents (all detected, no false
positives); a live shadow run against a real multi-project agent operation
(started 2026-11-15) is still in progress and may drive v0.1.x fixes.

In scope: single machine, single operator; emit + wrapper/ping + follow
ingestion; the 5 detection rules; Markdown/JSON digest; webhook/file/exit-code
notifications; TOML config in one file.

Non-goals: LLM tracing or prompt recording, evals, dashboards, any control
action, servers, multi-machine sync, OTel ingestion, HTTP ping endpoints.

## License

MIT — see [LICENSE](LICENSE).
