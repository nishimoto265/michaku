# Changelog

## v0.1.0 — 2026-11-15

First public release.

### Included

- Append-only JSONL operations ledger with a rebuildable SQLite projection
- 13 event types with delegation / approval / finding lifecycle state
  machines (see SPEC.md)
- Recording from the environment side only: cron wrapper (`run`), ping pair
  (`start` / `finish`), read-only log following (`follow`), operator `emit`
- Five silence-detection rules: `missing_run`, `report_missing`,
  `approval_stale`, `heartbeat_stale`, `repeated_failure`
- Markdown / JSON digest, plus `status`, `rebuild`, `import`
- Notifications: generic webhook, Slack / Discord / ntfy formats, file
  append, and `check`'s exit code
- Single-file TOML configuration

### Known limitations

- Single machine, single operator; no multi-machine sync or server mode
- No custom rule language; the five rules are fixed types with TOML
  parameters
- No OpenTelemetry ingestion or HTTP ping endpoint yet (v0.2 candidates)
- Validated by replaying real operational incidents (all detected, no false
  positives); a live shadow run started 2026-11-15 is still in progress
