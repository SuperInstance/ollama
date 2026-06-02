# Model Conservation Guardian

> 3 models loaded, 1 actually used. You're spending 67GB VRAM on ghosts.

## What It Does

Guardian watches your Ollama fleet and tells you what's wasting resources — models loaded 24/7 that nobody uses, oversized context windows eating VRAM, duplicate quantized variants scattered across instances.

It doesn't guess. It measures.

## Quick Start

```bash
# Generate a conservation report
go run . report --fleet ./fleet-data/

# Check budgets
go run . budget --fleet ./fleet-data/ --budget-file budgets.json

# Continuous monitoring
go run . watch --fleet ./fleet-data/ --interval 5m
```

## Fleet Data Format

Drop JSON files (one per instance) in a directory:

```json
{
  "host": "gpu01",
  "total_vram_gb": 80,
  "used_vram_gb": 67,
  "models": [
    {
      "name": "llama3:70b",
      "size_gb": 47,
      "context_length": 32768,
      "loaded_at": "2024-01-15T08:00:00Z",
      "last_request_at": "2024-01-15T14:30:00Z",
      "request_count": 3,
      "tokens_generated": 1500,
      "tokens_today": 15000,
      "concurrent_requests": 0
    }
  ]
}
```

## Budget Definitions

```json
{
  "global_max_vram_gb": 200,
  "default": {
    "model": "default",
    "max_vram_gb": 10,
    "max_concurrent_requests": 5,
    "max_tokens_per_day": 1000000
  },
  "models": [
    {
      "model": "llama3:70b",
      "max_vram_gb": 50,
      "max_concurrent_requests": 2,
      "max_tokens_per_day": 500000,
      "allowed_instances": ["gpu01"]
    }
  ]
}
```

## What Gets Detected

### Idle Models
Models loaded for hours but barely used. The biggest waste.

```
Model llama3:70b loaded 24/7 but used 2.1% of the time.
Switch to on-demand: saves 47GB VRAM.
```

### Oversized Context Windows
32K context when you're averaging 500 tokens per conversation. That's ~95% of your context memory wasted.

### Duplicate Variants
Three different quantizations of the same model loaded across your fleet. Pick one.

## Report Output

```
╔══════════════════════════════════════════════════════════╗
║          MODEL CONSERVATION GUARDIAN REPORT             ║
╚══════════════════════════════════════════════════════════╝
  Generated: 2024-01-15 14:00:00
  Fleet: 2 instances, 3 models loaded

── Fleet Overview ──────────────────────────────────────
  VRAM: 67.0GB / 160.0GB used (42%)
  Models: 1 active, 2 ghosts (loaded but barely used)

── Waste Detection ─────────────────────────────────────
  🔴 llama3:70b on gpu01: loaded 6.0h but used 1.5% of the time.
     Switch to on-demand: saves 47GB VRAM. (recover 47.0GB)
  🟡 codellama:34b on gpu02: context 32768 but avg ~100 tokens (99% wasted)

── Recommendations ─────────────────────────────────────
  → Unload llama3:70b on gpu01 when idle >30min (save 47GB)
  → Reduce codellama:34b context window on gpu02 (save ~6.0GB)

── Summary ─────────────────────────────────────────────
  3 models loaded, 1 actually used.
  You're spending 53GB VRAM on ghosts.
```

## Architecture

| File | Purpose |
|------|---------|
| `main.go` | CLI interface (report, budget, watch commands) |
| `budget.go` | Budget definitions, fleet state, budget enforcement |
| `profiler.go` | Usage profiling: throughput, memory, queue depth |
| `detector.go` | Waste detection: idle models, oversized contexts, duplicates |
| `report.go` | Conservation report generation |
| `watch.go` | Continuous monitoring loop |

## Why

VRAM is expensive. GPUs sitting at 80% utilization with models nobody's talking to is money on fire. Guardian makes waste visible so you can do something about it.
