# Model Conservation Guardian

> 3 models loaded, 1 actually used. You're spending 67GB VRAM on ghosts.

**v0.2.0** — Production-grade Ollama fleet monitor with waste detection, alerting, trend analysis, and auto-unload.

## What It Does

Guardian watches your Ollama fleet and tells you when VRAM is being wasted on idle models, oversized contexts, or duplicate variants across instances. It can auto-unload idle models, export metrics to Prometheus, and alert via Slack webhooks.

## Quick Start

```bash
# Build
cd guardian && go build -o guardian .

# Report on local fleet
./guardian -fleet ./fleet_data report

# Report via live Ollama API
./guardian -api http://localhost:11434 report

# Continuous watch with alerting
./guardian -api http://localhost:11434 -alerts alerts.json watch

# Export JSON for dashboards
./guardian -api http://localhost:11434 -export json -export-file fleet.json report

# Prometheus metrics endpoint
./guardian -api http://localhost:11434 -prometheus-addr :9090 watch

# Auto-unload idle models
./guardian -api http://localhost:11434 -auto-unload auto-unload
```

## Commands

| Command | Description |
|---------|-------------|
| `report` | Generate a conservation report |
| `budget` | Check model usage against budget limits |
| `watch` | Continuously monitor and alert on waste |
| `export` | Export fleet state (json/prometheus/slack) |
| `trends` | Analyze utilization trends from saved history |
| `auto-unload` | Automatically unload idle models |

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-fleet` | `.` | Directory with fleet JSON configs |
| `-api` | | Comma-separated Ollama API base URLs |
| `-interval` | `5m` | Watch/auto-unload polling interval |
| `-output` | | Write report to file |
| `-budget-file` | | JSON file with budget definitions |
| `-export` | | Export format: `json`, `prometheus`, `slack` |
| `-export-file` | | Write export to file |
| `-alerts` | | JSON file with alert rules |
| `-data-dir` | | Persistence directory for snapshots |
| `-prometheus-addr` | | Start Prometheus HTTP server on address |
| `-auto-unload` | `false` | Enable auto-unload of idle models |
| `-auto-unload-idle` | `1h` | Idle threshold for auto-unload |
| `-version` | | Print version |

## API Reference

### OllamaClient

Production HTTP client with connection pooling, retries, and timeout management.

```go
client := NewOllamaClient("http://localhost:11434")
client.SetTimeout(15 * time.Second)
client.SetRetries(3, 500*time.Millisecond)

// List all available models
models, err := client.ListModels(ctx)

// Get running models with VRAM usage
running, err := client.RunningModels(ctx)

// Unload a specific model
err := client.UnloadModel(ctx, "llama3:70b")
```

### ClientPool

Thread-safe pool for managing multiple instances.

```go
pool := NewClientPool()
pool.Get("http://gpu1:11434")
pool.Get("http://gpu2:11434")

fleet, err := FetchFleetFromClients(ctx, pool)
```

### Persistence

Save and load fleet snapshots and profiling history.

```go
p, _ := NewPersistence("/var/lib/guardian")
p.SaveFleetSnapshot(fleet)
p.SaveProfileSnapshot(profiles)

history, _ := p.LoadFleetHistory(7 * 24 * time.Hour)
p.CleanupOldSnapshots(30 * 24 * time.Hour)
```

### Alerting

Configurable alert rules with multiple dispatch channels.

```json
{
  "rules": [
    {
      "id": "idle_1hr",
      "type": "idle_model",
      "duration": "1h",
      "message": "Model idle for over 1 hour"
    },
    {
      "id": "vram_90",
      "type": "vram_threshold",
      "threshold": 90,
      "message": "VRAM usage above 90%"
    }
  ],
  "channels": ["stdout", "file:/var/log/guardian/alerts.log", "webhook:https://hooks.slack.com/..."]
}
```

### Export Formats

```bash
# JSON output
./guardian -api http://localhost:11434 -export json report

# Prometheus metrics
./guardian -api http://localhost:11434 -export prometheus report

# Slack webhook payload
./guardian -api http://localhost:11434 -export slack report
```

### Trend Analysis

```bash
# Requires persistence data (-data-dir)
./guardian -data-dir /var/lib/guardian -api http://localhost:11434 trends
```

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌───────────────┐
│  Ollama API  │────▶│  OllamaClient │────▶│  ClientPool   │
│  /api/tags   │     │  /api/ps      │     │  (per-host)   │
│  /api/ps     │     │  /api/unload  │     │               │
└─────────────┘     └──────────────┘     └───────┬───────┘
                                                  │
                    ┌─────────────────────────────┤
                    │                             │
              ┌─────▼─────┐              ┌────────▼───────┐
              │  Fleet     │              │  Persistence   │
              │  Snapshot  │              │  (JSON files)  │
              └─────┬──────┘              └────────┬───────┘
                    │                              │
              ┌─────▼──────┐              ┌────────▼───────┐
              │  Profiler   │              │  Trend         │
              │             │              │  Analysis      │
              └─────┬──────┘              └────────────────┘
                    │
         ┌──────────┼──────────┐
         │          │          │
    ┌────▼───┐ ┌───▼────┐ ┌──▼──────┐
    │ Report  │ │ Detector│ │ Budget  │
    └────┬───┘ └───┬────┘ └──┬──────┘
         │         │         │
         └─────────┼─────────┘
                   │
            ┌──────▼──────┐
            │   Export     │
            │ JSON/Prom/   │
            │ Slack        │
            └──────┬──────┘
                   │
            ┌──────▼──────┐
            │  Alerting    │
            │  Rules/Chans │
            └─────────────┘
```

## Examples

See `examples/` for standalone programs:

- **basic_watch.go** — Simple fleet monitoring
- **auto_unload.go** — Auto-cleanup idle models
- **prometheus_exporter.go** — Standalone Prometheus metrics endpoint

## License

Same as parent Ollama project.
