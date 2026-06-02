#!/usr/bin/env python3
"""
ollama-intel — CLI for the Ollama Intelligence Monitor.

Usage:
  ollama-intel report [--log PATH] [--output FORMAT] [--out PATH]
  ollama-intel compare MODEL_A MODEL_B [--samples PATH]
  ollama-intel budget [--max-gpu HOURS]
  ollama-intel pareto [--log PATH]
  ollama-intel phases [--log PATH]
  ollama-intel watch [--interval SECONDS]
  ollama-intel version
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

from .monitor import (
    ModelUsageRecord,
    OutputSample,
    Phase,
    parse_ollama_logs,
    read_ollama_api_status,
    detect_redundancy,
    jensen_shannon_divergence,
    transfer_entropy,
    gpu_efficiency_report,
    compute_pareto_frontier,
    compute_budgets,
    detect_phases,
    generate_html_report,
    export_json,
)

logger = logging.getLogger("ollama-intel")

VERSION = "0.1.0"


def _setup_logging(verbose: bool = False) -> None:
    level = logging.DEBUG if verbose else logging.INFO
    logging.basicConfig(
        level=level,
        format="%(message)s",
        stream=sys.stderr,
    )


def _load_samples(sample_dir: str) -> dict[str, list[OutputSample]]:
    """Load output samples from a directory of JSON files."""
    samples: dict[str, list[OutputSample]] = {}
    path = Path(sample_dir)
    if not path.exists():
        logger.warning("Sample directory %s does not exist", sample_dir)
        return samples

    for f in sorted(path.glob("*.json")):
        try:
            data = json.loads(f.read_text())
            if isinstance(data, list):
                for item in data:
                    sample = OutputSample(
                        model_name=item.get("model_name", f.stem),
                        prompt=item.get("prompt", ""),
                        output=item.get("output", ""),
                        timestamp=datetime.fromisoformat(item.get("timestamp", datetime.now(timezone.utc).isoformat())),
                        tokens=item.get("tokens", 0),
                        gpu_seconds=item.get("gpu_seconds", 0.0),
                    )
                    samples.setdefault(sample.model_name, []).append(sample)
            elif isinstance(data, dict):
                sample = OutputSample(
                    model_name=data.get("model_name", f.stem),
                    prompt=data.get("prompt", ""),
                    output=data.get("output", ""),
                    timestamp=datetime.fromisoformat(data.get("timestamp", datetime.now(timezone.utc).isoformat())),
                    tokens=data.get("tokens", 0),
                    gpu_seconds=data.get("gpu_seconds", 0.0),
                )
                samples.setdefault(sample.model_name, []).append(sample)
        except (json.JSONDecodeError, ValueError) as e:
            logger.warning("Failed to load sample %s: %s", f, e)

    return samples


def cmd_report(args: argparse.Namespace) -> int:
    """Generate full intelligence report."""
    log_path = args.log or os.path.expanduser("~/.ollama/logs/server.log")
    verbose = getattr(args, "verbose", False)
    output_format = args.output or "text"

    records = parse_ollama_logs(log_path)
    if not records:
        logger.info("No model usage records found in %s", log_path)
        logger.info("Try: journalctl -u ollama --since '1 hour ago' > ~/ollama.log")
        # Create synthetic records for demo
        records = _demo_records()

    # Load output samples
    samples: dict[str, list[OutputSample]] = {}
    if args.samples:
        samples = _load_samples(args.samples)
    else:
        samples = _demo_samples(records)

    # Detect phases
    phases = detect_phases(records)

    if output_format == "json":
        out_path = args.out or "intel-report.json"
        export_json(records, samples, phases, out_path)
        logger.info("JSON report written to %s", out_path)
        print(out_path)
    elif output_format == "html":
        html = generate_html_report(records, samples, phases)
        out_path = args.out or "intel-report.html"
        with open(out_path, "w") as f:
            f.write(html)
        logger.info("HTML report written to %s", out_path)
        print(out_path)
    else:
        _print_text_report(records, samples, phases)

    return 0


def _print_text_report(records, samples, phases):
    redundancy = detect_redundancy(samples, records)
    allocations, frontier = gpu_efficiency_report(records, samples)
    budgets = compute_budgets(records, redundancy)

    total_gpu_hrs = sum(r.gpu_seconds / 3600.0 for r in records)
    total_tokens = sum(r.tokens_generated for r in records)

    print("=" * 60)
    print("🔬 Ollama Intelligence Monitor Report")
    print("=" * 60)
    print()
    print(f"Models tracked:     {len(records)}")
    print(f"Total GPU hours:    {total_gpu_hrs:.1f}")
    print(f"Tokens generated:   {total_tokens:,}")
    print(f"Workload phases:    {len(phases)}")
    print()

    print("📊 Model Usage & Pareto Efficiency")
    print("-" * 60)
    print(f"{'Model':<30} {'GPU h/day':<12} {'Quality':<10} {'Pareto':<10}")
    print("-" * 60)
    for a in allocations:
        pareto = "✓" if a.pareto_optimal else ""
        print(f"{a.model_name:<30} {a.gpu_hours_per_day:<12.1f} {a.quality_score:<10.3f} {pareto:<10}")

    print()
    print("⚠️  Redundancy Report")
    print("-" * 60)
    if redundancy:
        for r in redundancy:
            print(f"  {r.recommendation}")
    else:
        print("  No redundancy detected.")

    print()
    print("💡 Budget & Conservation")
    print("-" * 60)
    for b in budgets:
        print(f"  {b.model_name}: {b.recommendation}")

    print()
    print("📈 Workload Phases")
    print("-" * 60)
    for p in phases:
        emoji = {"inference": "🚀", "idle": "💤", "mixed": "🔄", "training": "🎓"}.get(p.name, "❓")
        start_s = p.start.strftime("%H:%M") if p.start else "?"
        end_s = p.end.strftime("%H:%M") if p.end else "ongoing"
        print(f"  {emoji} {p.name:<12} {start_s} - {end_s}  (confidence: {p.confidence:.0%})")

    print()


def cmd_compare(args: argparse.Namespace) -> int:
    """Compare two models for redundancy."""
    model_a = args.model_a
    model_b = args.model_b

    samples: dict[str, list[OutputSample]] = {}
    if args.samples:
        samples = _load_samples(args.samples)
    else:
        samples = _demo_samples([])

    a_samples = samples.get(model_a, [])
    b_samples = samples.get(model_b, [])

    if not a_samples or not b_samples:
        print(f"Need output samples for both models. Have: {list(samples.keys())}")
        print("Use --samples to point to a directory of JSON sample files.")
        return 1

    text_a = " ".join(s.output for s in a_samples)
    text_b = " ".join(s.output for s in b_samples)

    jsd = jensen_shannon_divergence(text_a, text_b)
    te_ab = transfer_entropy(a_samples, b_samples)
    te_ba = transfer_entropy(b_samples, a_samples)

    similarity_pct = (1 - jsd) * 100

    print(f"📊 Model Comparison: {model_a} vs {model_b}")
    print(f"   JSD (Jensen-Shannon Divergence):  {jsd:.4f}")
    print(f"   Output similarity:                {similarity_pct:.1f}%")
    print(f"   Transfer Entropy A→B:             {te_ab:.4f}")
    print(f"   Transfer Entropy B→A:             {te_ba:.4f}")

    if similarity_pct >= 95:
        print(f"\n⚠️  WARNING: Models are {similarity_pct:.0f}% similar — highly redundant!")
        if te_ab > te_ba and te_ab > 0.1:
            print(f"   {model_a} appears influenced by {model_b}")
        elif te_ba > te_ab and te_ba > 0.1:
            print(f"   {model_b} appears influenced by {model_a}")
        else:
            print("   Consider keeping only the smaller/faster model.")
    elif similarity_pct >= 80:
        print(f"\n⚡ Note: {similarity_pct:.0f}% similar — some redundancy, may be acceptable.")
    else:
        print(f"\n✅ Models are sufficiently distinct (only {similarity_pct:.0f}% similar).")

    return 0


def cmd_budget(args: argparse.Namespace) -> int:
    """Show GPU hour budget and conservation analysis."""
    records = parse_ollama_logs(args.log or "~/.ollama/logs/server.log")
    if not records:
        records = _demo_records()

    samples = _demo_samples(records)
    redundancy = detect_redundancy(samples, records)
    budgets = compute_budgets(records, redundancy, max_gpu_hours_per_day=args.max_gpu or 24.0)

    print(f"{'Model':<30} {'Budget(h)':<12} {'Current(h)':<12} {'Status':<16} {'Recommendation'}")
    print("-" * 100)
    for b in budgets:
        status = "OVER BUDGET" if b.over_budget else "OK"
        print(f"{b.model_name:<30} {b.daily_budget_hrs:<12.1f} {b.current_daily_hrs:<12.1f} {status:<16} {b.recommendation}")

    print(f"\nTotal GPU hours tracked: {sum(b.current_daily_hrs for b in budgets):.1f}/day")
    return 0


def cmd_pareto(args: argparse.Namespace) -> int:
    """Show Pareto frontier of model efficiency."""
    records = parse_ollama_logs(args.log or "~/.ollama/logs/server.log")
    if not records:
        records = _demo_records()

    samples = _demo_samples(records)
    allocations, frontier = gpu_efficiency_report(records, samples)

    print("📈 GPU Efficiency Analysis")
    print("=" * 80)
    print(f"{'Model':<30} {'GPU h/day':<12} {'Quality':<10} {'Pareto':<10}")
    print("-" * 80)
    for a in allocations:
        pareto = "✓ YES" if a.pareto_optimal else ""
        print(f"{a.model_name:<30} {a.gpu_hours_per_day:<12.1f} {a.quality_score:<10.3f} {pareto:<10}")

    print("\n🏆 Pareto-Optimal Models:")
    for a in frontier:
        print(f"   ✓ {a.model_name} — {a.gpu_hours_per_day:.1f} h/day, quality {a.quality_score:.3f}")

    if frontier:
        print("\n💡 Non-optimal models: consider reducing usage or removing.")
    return 0


def cmd_phases(args: argparse.Namespace) -> int:
    """Detect and display workload phases."""
    records = parse_ollama_logs(args.log or "~/.ollama/logs/server.log")
    if not records:
        records = _demo_records()

    phases = detect_phases(records)
    if not phases:
        print("No workload phases detected.")
        return 0

    print("📈 Workload Phases")
    print("=" * 60)
    print(f"{'Phase':<16} {'Start':<10} {'End':<10} {'Confidence':<12} {'GPU Hours':<10}")
    print("-" * 60)
    for p in phases:
        emoji = {"inference": "🚀", "idle": "💤", "mixed": "🔄", "training": "🎓"}.get(p.name, "❓")
        start_s = p.start.strftime("%H:%M") if p.start else "?"
        end_s = p.end.strftime("%H:%M") if p.end else "ongoing"
        print(f"{emoji} {p.name:<12} {start_s:<10} {end_s:<10} {p.confidence:<12.0%} {p.gpu_hours:<10.1f}")

    return 0


def cmd_watch(args: argparse.Namespace) -> int:
    """Watch live model usage."""
    interval = args.interval or 30
    print(f"👁️  Watching Ollama models (every {interval}s)...")
    print("Press Ctrl+C to stop.\n")

    try:
        while True:
            models = read_ollama_api_status()
            if models:
                print(f"[{datetime.now().strftime('%H:%M:%S')}] Running models:")
                for m in models:
                    print(f"   {m['name']:<30} {m['size']:<10} until {m['until']}")
            else:
                print(f"[{datetime.now().strftime('%H:%M:%S')}] No models currently loaded.")
            print()
            time.sleep(interval)
    except KeyboardInterrupt:
        print("\nStopped.")
    return 0


def cmd_version(args: argparse.Namespace) -> int:
    """Show version."""
    print(f"ollama-intel v{VERSION}")
    return 0


# ---------------------------------------------------------------------------
# Demo / synthetic data for when no real logs exist
# ---------------------------------------------------------------------------

def _demo_records() -> list[ModelUsageRecord]:
    now = datetime.now(timezone.utc)
    demos = [
        ("llama3.1:8b", 1200, 1.5, 0.8),
        ("llama3.1:70b", 350, 6.2, 4.5),
        ("mistral:7b", 980, 0.9, 0.5),
        ("codellama:34b", 210, 3.1, 2.1),
        ("phi3:3.8b", 1500, 0.4, 0.3),
        ("gemma2:9b", 875, 1.1, 0.7),
        ("mixtral:8x7b", 420, 4.8, 3.2),
        ("llama3.1:8b-q4", 1100, 0.8, 0.5),
    ]
    records = []
    for name, tokens, gpu_sec, gpu_hrs in demos:
        gpu_sec_actual = gpu_hrs * 3600
        records.append(ModelUsageRecord(
            model_name=name,
            model_path=name,
            loaded_at=now - timedelta(hours=random_gauss(1, 4)),
            unloaded_at=now - timedelta(hours=random_gauss(0, 1)),
            requests=random_gauss_int(5, 50),
            tokens_generated=tokens,
            gpu_seconds=gpu_sec_actual,
            avg_token_latency_ms=random_gauss(20, 60),
        ))
    return records


def _demo_samples(records: list[ModelUsageRecord]) -> dict[str, list[OutputSample]]:
    """Generate synthetic output samples for demo purposes."""
    model_prompts = [
        ("llama3.1:8b", "Explain quantum computing in simple terms.",
         "Quantum computing uses qubits that exist in superposition, allowing parallel computation on an exponential scale."),
        ("llama3.1:70b", "Explain quantum computing in simple terms.",
         "Quantum computing leverages superposition and entanglement — qubits can be both 0 and 1 simultaneously, "
         "enabling certain calculations to be performed exponentially faster than classical computers."),
        ("mistral:7b", "Write a haiku about AI.",
         "Silicon dreaming\nPatterns form in neural nets\nWisdom without thought"),
        ("codellama:34b", "Write a Python function to sort a list.",
         "def sort_list(items):\n    return sorted(items)"),
        ("phi3:3.8b", "What is the capital of France?",
         "Paris is the capital of France."),
        ("gemma2:9b", "Write a haiku about AI.",
         "Digital mind wakes\nLearning patterns in the dark\nAnswers without knowing"),
        ("llama3.1:8b", "What is the meaning of life?",
         "Life's meaning is subjective — it's what you make of it through connection, growth, and purpose."),
        ("llama3.1:8b-q4", "Explain quantum computing in simple terms.",
         "Quantum computing uses qubits that can be 0 and 1 at the same time, processing many possibilities at once."),
    ]

    known_models = set(r.model_name for r in records)
    samples: dict[str, list[OutputSample]] = {}

    for model, prompt, output in model_prompts:
        if model in known_models or not known_models:
            ts = datetime.now(timezone.utc) - timedelta(hours=random_gauss(0, 2))
            samples.setdefault(model, []).append(OutputSample(
                model_name=model,
                prompt=prompt,
                output=output,
                timestamp=ts,
                tokens=len(output.split()),
                gpu_seconds=0.5,
            ))

    # Also add to models that didn't get a sample specifically
    for rec in records:
        if rec.model_name not in samples:
            ts = datetime.now(timezone.utc) - timedelta(hours=random_gauss(0, 2))
            samples[rec.model_name] = [
                OutputSample(
                    model_name=rec.model_name,
                    prompt="Tell me about yourself.",
                    output=f"I am {rec.model_name}, a language model running on Ollama. I can help with various tasks.",
                    timestamp=ts,
                    tokens=20,
                    gpu_seconds=0.3,
                )
            ]

    return samples


def random_gauss(mu: float = 0, sigma: float = 1) -> float:
    """Gaussian random using Box-Muller."""
    import random
    return random.gauss(mu, sigma)


def random_gauss_int(mu: float, sigma: float) -> int:
    return max(1, int(random_gauss(mu, sigma)))


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="🔬 Ollama Intelligence Monitor — Know your models.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  ollama-intel report                    # Generate text report
  ollama-intel report --output html      # Generate HTML report
  ollama-intel compare llama3.1:8b mistral:7b
  ollama-intel budget --max-gpu 16
  ollama-intel pareto
  ollama-intel phases
  ollama-intel watch --interval 10
        """,
    )
    parser.add_argument("-v", "--verbose", action="store_true", help="Verbose output")

    subparsers = parser.add_subparsers(dest="command", help="Command")

    # report
    p_report = subparsers.add_parser("report", help="Generate intelligence report")
    p_report.add_argument("--log", help="Path to Ollama log file")
    p_report.add_argument("--output", "-o", choices=["text", "json", "html"], default="text", help="Output format")
    p_report.add_argument("--out", help="Output file path")
    p_report.add_argument("--samples", help="Directory of sample outputs (JSON)")

    # compare
    p_compare = subparsers.add_parser("compare", help="Compare two models")
    p_compare.add_argument("model_a", help="First model name")
    p_compare.add_argument("model_b", help="Second model name")
    p_compare.add_argument("--samples", help="Directory of sample outputs (JSON)")

    # budget
    p_budget = subparsers.add_parser("budget", help="Show GPU budget analysis")
    p_budget.add_argument("--log", help="Path to Ollama log file")
    p_budget.add_argument("--max-gpu", type=float, default=24.0, help="Max GPU hours per day")

    # pareto
    p_pareto = subparsers.add_parser("pareto", help="Show Pareto frontier")
    p_pareto.add_argument("--log", help="Path to Ollama log file")

    # phases
    p_phases = subparsers.add_parser("phases", help="Detect workload phases")
    p_phases.add_argument("--log", help="Path to Ollama log file")

    # watch
    p_watch = subparsers.add_parser("watch", help="Watch live model status")
    p_watch.add_argument("--interval", type=int, default=30, help="Poll interval in seconds")

    # version
    subparsers.add_parser("version", help="Show version")

    args = parser.parse_args(argv)
    _setup_logging(args.verbose)

    if not args.command:
        parser.print_help()
        return 1

    commands = {
        "report": cmd_report,
        "compare": cmd_compare,
        "budget": cmd_budget,
        "pareto": cmd_pareto,
        "phases": cmd_phases,
        "watch": cmd_watch,
        "version": cmd_version,
    }

    return commands[args.command](args)


if __name__ == "__main__":
    sys.exit(main())
