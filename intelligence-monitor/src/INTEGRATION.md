# Integration Guide: Intelligence Monitor + Ollama

## Overview

The Intelligence Monitor is a Python-based sidecar that reads Ollama's logs and API output to benchmark model redundancy and GPU efficiency. It does not modify any Ollama source files — all analysis is based on log parsing, output sampling, and basic information theory.

## Architecture

```
┌──────────────────────┐          ┌──────────────────────────────┐
│     Ollama Server     │──────    │  Intelligence Monitor CLI    │
│  (Go, model loading,  │  logs   │  (ollama-intel, Python)     │
│   scheduler, logs)    │──────→  │                              │
└──────────────────────┘  API     │  parse_ollama_logs()         │
        │                        │  detect_redundancy()          │
        ▼                        │  jensen_shannon_divergence()  │
┌──────────────────────┐         │  transfer_entropy()           │
│  ~/.ollama/logs/      │         │  compute_pareto_frontier()    │
│  journalctl -u ollama │         │  detect_phases()              │
│  ollama ps             │         │  generate_html_report()      │
└──────────────────────┘         └──────────────────────────────┘
```

### Data flow

```
Ollama logs
    │
    ▼
parse_ollama_logs()  ──→  ModelUsageRecord[]
    │
    ├── detect_phases()         → Phase[]
    ├── compute_budgets()       → BudgetConstraint[]
    ├── gpu_efficiency_report() → GPUAllocation[] (+ Pareto frontier)
    │
User samples (JSON directory)
    │
    ▼
_load_samples()  ──→  OutputSample[]
    │
    └── detect_redundancy()     → RedundancyReport[]
         ├── jensen_shannon_divergence()  — output similarity
         └── transfer_entropy()           — causal influence
```

## Installation

### From source

```bash
cd intelligence-monitor
pip install -e .
```

This installs the `ollama-intel` CLI command. Test it:

```bash
ollama-intel version
# → ollama-intel v0.1.0
```

### Dependencies

- Python ≥ 3.10
- Standard library only (no external packages): `json`, `math`, `re`, `subprocess`, `collections`, `dataclasses`, `pathlib`, `logging`

## Data sources (and how to get them)

### 1. Ollama server logs

The monitor parses two log sources in order of preference:

**journald (Linux with systemd):**
```bash
journalctl -u ollama --since "1 day ago" --no-pager > /tmp/ollama.log
ollama-intel report --log /tmp/ollama.log
```

**Log file:**
```bash
# Default location
~/.ollama/logs/server.log

# Or any path
ollama-intel report --log /var/log/ollama.log
```

The parser looks for:
- Model load events: `loaded ... model=<name>`
- Token generation: `generated N tokens`
- Latency: `N ms/token` or `N tok/s`
- Request count: `[N] request`

If no logs are found, the monitor generates synthetic records so you can explore the tool without a running Ollama instance.

### 2. Output samples for JSD analysis

To compare model outputs, save each model's responses as JSON files in a directory:

```bash
# Create a samples directory
mkdir -p ./samples

# Collect outputs from the same prompt for comparison fairness
ollama run llama3.1:8b "Explain quantum computing in simple terms" > ./samples/llama3.1.json
ollama run mistral:7b "Explain quantum computing in simple terms" > ./samples/mistral.json
ollama run qwen2:7b "Give three key benefits of functional programming" > ./samples/qwen2.json
```

Each file should contain either a single JSON object or an array of objects:

```json
{
  "model_name": "llama3.1:8b",
  "prompt": "Explain quantum computing in simple terms",
  "output": "Quantum computing uses qubits in superposition, allowing parallel computation...",
  "tokens": 47,
  "gpu_seconds": 0.5
}
```

For best results, collect **3–5 responses per model** from the same set of prompts. This gives the transfer entropy computation enough signal.

```bash
ollama-intel report --samples ./samples/
```

### 3. Live API status

While Ollama is running:

```bash
ollama-intel watch --interval 10
# → [14:32:05] Running models:
#      llama3.1:8b      4.2 GiB    until 4 minutes
```

This polls `ollama ps` every N seconds and shows currently loaded models.

## Detailed walkthrough

### Full report with HTML output

```bash
ollama-intel report \
  --log /tmp/ollama-24h.log \
  --samples ./samples/ \
  --output html \
  --out report.html
# Open report.html in your browser
```

The HTML report includes:
- A summary bar (models tracked, total GPU hours, tokens generated)
- Model usage table with quality scores and Pareto badges
- Redundancy report colored by similarity severity
- Budget and conservation recommendations
- Workload phase timeline

### Redundancy detection in detail

The core analysis uses two complementary metrics:

**Jensen-Shannon Divergence** (JSD):
- Range: 0.0 (identical) to 1.0 (maximally different)
- JSD ≤ 0.05 → models are **95%+ similar**
- Symmetric: `JSD(A, B) == JSD(B, A)`
- Implementation: Laplace-smoothed unigram token distributions normalized by `log(2)`

**Transfer Entropy** (TE):
- Measures causal influence from model A's outputs → model B's outputs
- High TE (≥ 0.1) → model B's output distribution is partially *caused* by model A
- Asymmetric: `TE(A→B) != TE(B→A)` — direction matters
- Implementation: conditional entropy reduction with delay=1 using word-level tokens

**When to trust the numbers:**

| JSD | TE high one-way | TE low both ways | Meaning |
|-----|-----------------|------------------|---------|
| ≤ 0.05 | Yes | — | Redundant + one model is derivative. Drop the influenced one. |
| ≤ 0.05 | — | Yes | Redundant but no clear direction. Keep the smaller/faster one. |
| 0.05–0.20 | — | — | Some similarity but likely different strengths (code vs creative, etc.) |
| ≥ 0.20 | — | — | Distinct models. Keep both. |

### Pareto frontier logic

The Pareto frontier identifies models that maximize *output quality* (uniqueness score: 1 − avg JSD with peers) while minimizing *GPU hours*. A model is Pareto-optimal if no other model has both lower GPU usage and higher quality.

```
                 Quality (uniqueness)
                 ↑
     1.0 ─       ●
                 │
     0.8 ─    ───●── frontier
                 │  ●
     0.5 ─       │     ●
                 │        ●
     0.2 ─       │           ●
                 │
                 └────────────────────────→ GPU hours/day
                     0    2    4    6
```

Models below/right of the frontier are dominated — you can find a model that costs less GPU and gives the same or better quality.

## Reading the output

### Text report

```
📊 Model Usage & Pareto Efficiency
------------------------------------------------------------
Model                          GPU h/day    Quality    Pareto
------------------------------------------------------------
codellama:34b                  0.5          0.723
llama3.1:8b                    1.2          0.612      ✓
mistral:7b                     0.8          0.598      ✓
phi3:3.8b                      0.3          0.210
```

`✓` in the Pareto column means this model is on the Pareto frontier — it's the best tradeoff of uniqueness vs GPU cost at that point.

```
⚠️  Redundancy Report
------------------------------------------------------------
  Model llama3.1:8b and mistral:7b are 96% similar.
  You're spending 4 GPU hours/day on what amounts to a clone.
  Transfer entropy: llama3.1:8b's outputs appear to be
  influencing mistral:7b (TE=0.284). Consider keeping
  llama3.1:8b only.
```

### JSON report

```bash
ollama-intel report --output json --out report.json
cat report.json | jq '.redundancy[] | select(.wasted_gpu_hrs_day > 1.0)'
```

```json
{
  "model_a": "llama3.1:8b",
  "model_b": "mistral:7b",
  "jsd": 0.0412,
  "transfer_entropy_ab": 0.284,
  "transfer_entropy_ba": 0.021,
  "wasted_gpu_hrs_day": 3.4,
  "recommendation": "Models produce nearly identical outputs..."
}
```

## Hooking into scheduling decisions

The monitor is designed as an offline audit tool, but you can feed its output into Ollama's scheduler:

**Option 1: Log-based streaming**

Ollama's scheduler (server/sched.go) logs model load/unload events. Pipe those directly:

```bash
journalctl -u ollama -f | ollama-intel report --log /dev/stdin
```

**Option 2: Pre-processing log files for automation**

```python
from intelligence_monitor.monitor import parse_ollama_logs, detect_redundancy

records = parse_ollama_logs("/tmp/ollama.log")
samples = _load_samples("./samples/")
reports = detect_redundancy(samples, records)

for r in reports:
    if r.wasted_gpu_hrs_day > 2.0:
        print(f"ACTION: Unload {r.model_a} or {r.model_b}")
```

## Extending the monitor

The `monitor.py` module is designed for extension:

- **Add a new metric:** Write a function that takes `List[OutputSample]` and returns a float, then call it from `detect_redundancy()`.
- **Add a new data source:** Implement a new parser function (like `parse_ollama_logs()`) that produces `ModelUsageRecord[]`.
- **Export to a new format:** Write a function that takes `(records, samples, phases)` and writes to your target.

All dataclasses are importable and JSON-serializable via `asdict()`.

## Testing

```bash
cd intelligence-monitor/src
python3 tests.py
# → .....................
# → Ran 19 tests in 0.003s
# → OK
```
