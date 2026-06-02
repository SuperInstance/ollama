# PRODUCTION_LOG.md — Guardian v0.1 → v0.2.0

## Date: 2026-06-02

## Summary

Took ollama-guardian from bug-fixed v0.1 to production v0.2.0. Added 6 new source files, rewrote main.go, created examples, CI, and full docs.

## What Was Done

### Mechanical (straightforward, pattern-following)

1. **OllamaClient + ClientPool** — Standard HTTP client wrapper with retries, connection pooling, timeout config. Mechanical because the pattern is well-established: struct with `http.Client`, retry loop with backoff, JSON decode. ~200 lines, no real design decisions.

2. **Persistence** — JSON file save/load with timestamps. Textbook CRUD: `os.ReadDir`, `json.Marshal`, `json.Unmarshal`. Added `sync.RWMutex` for safety, `CleanupOldSnapshots` for maintenance. ~150 lines, zero surprises.

3. **Export formats** — Three format converters (JSON, Prometheus, Slack). Each is a data-to-string function. Prometheus format is the most involved (metric naming conventions, label escaping) but still mechanical. ~250 lines.

4. **Examples** — Three standalone programs demonstrating library usage. Copy-paste from main.go commands with simplified flag handling. ~200 lines total.

5. **CI workflow** — Standard GitHub Actions: setup-go, vet, build, test, lint. ~60 lines of YAML, boilerplate.

6. **Docs** — README, CHANGELOG, CONTRIBUTING. Documentation is mechanical writing, not engineering.

### Thinking Required (design decisions, non-obvious)

1. **Alerting system** — Had to decide: rule engine vs simple thresholds? Chose a declarative rule config (JSON) with typed rule evaluation. The `dispatch` method needed channel abstraction (stdout/file/webhook) to avoid caller coupling. The `matchGlob` helper was a micro-decision: full regex vs simple glob? Chose simple `*`-only glob for zero dependencies.

2. **Trend analysis** — The tricky part: how to compare when old data has different shape than new data? Old snapshots don't have computed utilization — they have raw request counts. Had to build a mapping layer (`utilMap`, `oldUtilMap`) and handle the case where a model exists in current but not in history (and vice versa). The threshold for "significant" trend (±10%) is a judgment call.

3. **main.go rewrite** — Had to integrate 6 new flags and 3 new commands while keeping the existing `report`, `budget`, `watch` behavior. Decided on global flags (vs per-command flags) for simplicity. The `mustLoadFleet` helper abstracts the "file vs API" split cleanly. Flag package globals felt right vs passing structs around for a CLI tool.

4. **Auto-unload safety** — This is the most production-sensitive feature. Unloading the wrong model is destructive. Current heuristic is conservative (only unload large models >10GB with no keep_alive). Real production would need request tracking, cooldown periods, and probably a confirmation step. Documented this as a known limitation.

## Time Breakdown

| Task | Type | Est. Lines | Notes |
|------|------|-----------|-------|
| OllamaClient + Pool | Mechanical | ~200 | Standard HTTP client patterns |
| Persistence | Mechanical | ~150 | File I/O, JSON marshal |
| Export formats | Mechanical | ~250 | Data transformation |
| Alerting | Thinking | ~180 | Rule engine design, channel abstraction |
| Trend analysis | Thinking | ~150 | Historical comparison, thresholds |
| main.go rewrite | Thinking | ~300 | Command routing, flag integration |
| Examples | Mechanical | ~200 | Demo programs |
| CI | Mechanical | ~60 | GitHub Actions boilerplate |
| Docs | Mechanical | ~300 | README, CHANGELOG, CONTRIBUTING |
| **Total** | | **~1790 new** | v0.1 was 1776 lines |

## Lessons Learned

1. **Flag package globals are fine for CLIs.** I initially tried local flag variables in a broken helper pattern. Go's `flag` package works best with package-level `var` + `init()`. Don't fight it.

2. **Keep the old API adapter.** `ollama_api.go` has the `FetchFleetFromAPI` function used in v0.1 tests. Kept it alongside the new `ollama_client.go` for backward compat. New code uses `ClientPool`.

3. **Prometheus metric naming is opinionated.** Had to follow the `_total` suffix for counters, `_bytes` suffix for gauges, and proper label quoting. Got it right on the first try by following convention.

4. **Auto-unload needs more safety.** Current implementation is a starting point, not production-ready. Real deployments need request tracking, cooldown, and probably a dry-run mode.

5. **Zero dependencies is a feature.** Entire project uses only Go stdlib. This makes it trivially portable and buildable. Resisted the urge to add a CLI framework or Prometheus client library.

## Mechanical vs Thinking Ratio

~65% mechanical, ~35% thinking. The mechanical parts are the "just do it" work: HTTP clients, file I/O, data transforms, boilerplate. The thinking parts are the design decisions: how rules evaluate, how trends compare, how auto-unload stays safe, how the CLI integrates everything without becoming a mess.
