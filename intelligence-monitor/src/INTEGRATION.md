# Integration Guide: Intelligence Monitor + Ollama

## Overview

The Intelligence Monitor is a Python-based analytics tool that sits **alongside** Ollama. It does not modify any Ollama source files — it reads Ollama's logs and API to build its intelligence layer.

## How It Works

```
┌──────────────────────┐     ┌──────────────────────────────┐
│     Ollama Server     │────→│  Intelligence Monitor CLI    │
│  (Go, model loading,  │     │  (ollama-intel, Python)     │
│   scheduler, logs)    │     │                              │
└──────────────────────┘     │  • Parses server logs         │
        │                    │  • Queries ollama ps          │
        ▼                    │  • JSD analysis               │
┌──────────────────────┐     │  • Transfer entropy           │
│  ~/.ollama/logs/      │     │  • Pareto efficiency          │
│  journalctl -u ollama │     │  • Phase detection            │
└──────────────────────┘     │  • Budget conservation         │
                             │  • HTML/JSON reports           │
                             └──────────────────────────────┘
```

## What Gets Instrumented

The Intelligence Monitor uses **three data sources**:

### 1. Ollama Server Logs
- **Source:** `journalctl -u ollama` or `~/.ollama/logs/server.log`
- **Signal extracted:** Model loaded, model unloaded, tokens generated, GPU memory usage, latency
- **Ollama's scheduler** (server/sched.go) logs model load/unload events — the monitor parses these.

### 2. Ollama API `ollama ps`
- **Source:** `ollama ps` command
- **Signal extracted:** Currently loaded models, memory size, time until unload

### 3. Output Samples (user-provided)
- **Source:** JSON files in a samples directory
- **Signal extracted:** Model outputs for comparison (JSD, transfer entropy)

## Installation

```bash
# Option A: Local pip install
cd intelligence-monitor
pip install -e .

# Option B: Just run from source
./cli.py report
```

## Usage with Real Ollama

```bash
# 1. Ensure Ollama logs are available
journalctl -u ollama --since "today" > /tmp/ollama.log

# 2. Run a report
ollama-intel report --log /tmp/ollama.log

# 3. Collect output samples for JSD analysis
ollama run llama3.1:8b "Explain quantum computing" > sample_llama.json
ollama run mistral:7b "Explain quantum computing" > sample_mistral.json

# 4. Compare models
ollama-intel compare llama3.1:8b mistral:7b --samples ./samples/
```

## Data Flow

```
Ollama Logs
    │
    ▼
parse_ollama_logs()
    │
    ├──→ ModelUsageRecord[]  ──→ detect_phases()         → Phase[]
    │                           → compute_budgets()       → BudgetConstraint[]
    │                           → gpu_efficiency_report() → GPUAllocation[]
    │
User Samples (JSON)
    │
    ▼
_load_samples()
    │
    ├──→ OutputSample[] ──→ detect_redundancy() → RedundancyReport[]
                             (JSD + Transfer Entropy)
```

## Conservation Model

The monitor implements a **GPU-hour budget** system:

```
Fair Share = Max GPU Hours / N_models
If Current > Fair Share → "Over budget. Cut usage or eliminate redundant model."
If Current ≤ Fair Share → "Within budget. Keep if unique."
```

Redundant models (JSD ≤ 0.05, i.e., 95%+ similarity) are prime candidates for removal.

## Integrating with Ollama's Scheduler

The Intelligence Monitor can optionally integrate with Ollama's scheduler (server/sched.go) by:

1. **Log streaming** — Subscribe to scheduler events (model loaded/unloaded) via the log channel
2. **API extension** — Add a `/api/intel` endpoint for real-time queries (future work)
3. **Webhook** — Push redundancy reports as structured data

## Requirements

- Python 3.10+
- `ollama` CLI (for `ollama ps`)
- Log access (journald or log file)

## Architecture Notes

- **Zero-copy:** The monitor never modifies Ollama's Go codebase directly
- **Loose coupling:** All communication is via log files and CLI commands
- **Extensible:** Add new analytics by subclassing or adding new functions to monitor.py

## Future Integrations

- Prometheus metrics export
- Grafana dashboard for real-time model intelligence
- Slack/email alerts for detected redundancy
- Automatic model recommendations via Ollama's existing `/api/pull` flow
