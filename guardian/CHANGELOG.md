# Changelog

All notable changes to the Model Conservation Guardian.

## [0.2.0] - 2026-06-02

### Added
- **Real Ollama API adapter** (`ollama_client.go`): Connection pooling, timeout config, exponential backoff retries
  - `GET /api/tags` — list all available models
  - `GET /api/ps` — running models with VRAM usage
  - `POST /api/unload` — unload models (flag-gated)
  - `ClientPool` for concurrent multi-instance queries
- **Persistence** (`persistence.go`): Save/load fleet state and profiling history to timestamped JSON files
  - Snapshot retention and cleanup
  - Historical profile tracking for trend analysis
- **Export formats** (`export.go`):
  - JSON output for dashboards
  - Prometheus metrics endpoint (`/metrics`)
  - Slack webhook alert payloads
- **Alerting** (`alerting.go`): Configurable alert rules with multiple dispatch channels
  - Rule types: `idle_model`, `vram_threshold`, `model_count`
  - Channels: `stdout`, `file:<path>`, `webhook:<url>`
  - Alert history tracking
- **Trend analysis** (`trends.go`): Compare current fleet state to saved history
  - Utilization trends (up/down/stable)
  - VRAM usage trends
  - Human-readable trend reports
- **New commands**: `export`, `trends`, `auto-unload`
- **New flags**: `-api`, `-export`, `-export-file`, `-alerts`, `-data-dir`, `-prometheus-addr`, `-auto-unload`, `-auto-unload-idle`
- **Integration examples** (`examples/`):
  - `basic_watch.go` — Simple fleet monitoring
  - `auto_unload.go` — Auto-cleanup idle models
  - `prometheus_exporter.go` — Standalone Prometheus endpoint
- **CI**: GitHub Actions with `go vet`, `go test -race`, and golangci-lint
- **Docs**: Full README with API reference and architecture diagram

### Changed
- `main.go` rewritten with proper flag parsing, all new commands integrated
- Watch mode now supports live API polling, alerting, and persistence in a single cycle

## [0.1.0] - 2026-05-30

### Added
- Initial release: fleet loading, profiling, waste detection, budgets, reports, watch mode
- Waste types: idle models, oversized contexts, duplicate variants
- Budget checking against configurable limits
- Continuous monitoring with graceful shutdown
