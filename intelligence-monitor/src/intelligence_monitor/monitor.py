"""
intelligence-monitor — Model Intelligence & Efficiency Tracker for Ollama

Analytics:
  - Jensen-Shannon Divergence: detect redundant model outputs (95%+ similarity → flag)
  - Transfer Entropy: detect if one model's outputs influence another's (copying vs original)
  - GPU Efficiency: correlate model uniqueness (JSD) with GPU-hours → Pareto frontier
  - RedundancyReport: actionable conservation recommendations
  - Phase detection: workload phases (training vs inference vs idle)
"""

from __future__ import annotations

import json
import logging
import math
import os
import re
import sqlite3
import subprocess
import time
from collections import defaultdict, Counter
from dataclasses import dataclass, field, asdict
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Dict, List, Optional, Tuple

logger = logging.getLogger("intelmon")

# ---------------------------------------------------------------------------
# Data structures
# ---------------------------------------------------------------------------

@dataclass
class ModelUsageRecord:
    """A single model-load event from Ollama's scheduler."""
    model_name: str
    model_path: str
    loaded_at: datetime
    unloaded_at: Optional[datetime] = None
    requests: int = 0
    tokens_generated: int = 0
    tokens_input: int = 0
    gpu_seconds: float = 0.0  # estimated GPU-seconds consumed
    avg_token_latency_ms: float = 0.0


@dataclass
class OutputSample:
    """A sampled generation output for similarity analysis."""
    model_name: str
    prompt: str
    output: str
    timestamp: datetime
    tokens: int = 0
    gpu_seconds: float = 0.0


@dataclass
class RedundancyReport:
    """Actionable report about model redundancy."""
    model_a: str
    model_b: str
    jsd: float  # Jensen-Shannon Divergence (0 = identical, 1 = maximally different)
    transfer_entropy_ab: float  # influence from A → B
    transfer_entropy_ba: float  # influence from B → A
    estimated_a_gpu_hrs_day: float
    estimated_b_gpu_hrs_day: float
    wasted_gpu_hrs_day: float
    recommendation: str = ""


@dataclass
class Phase:
    """Detected workload phase."""
    name: str  # "training", "inference", "idle", "mixed"
    start: datetime
    end: Optional[datetime]
    confidence: float
    gpu_hours: float = 0.0
    model_usage: Dict[str, float] = field(default_factory=dict)


# ---------------------------------------------------------------------------
# Ollama log / state parser
# ---------------------------------------------------------------------------

OLLAMA_LOG_PATTERNS = {
    "load_model": re.compile(
        r'loaded (\S+).*model=(\S+)'
    ),
    "unload_model": re.compile(
        r'unload.*model=(\S+)'
    ),
    "generate_tokens": re.compile(
        r'generated (\d+) tokens.*model=(\S+)'
    ),
    "gpu_memory": re.compile(
        r'VRAM used: (\d+\.?\d*) (MiB|GiB)'
    ),
}


def _parse_size(s: str) -> int:
    s = s.strip().lower()
    if s.endswith("gib"):
        return int(float(s.replace("gib", "").strip()) * 1024)
    if s.endswith("mib"):
        return int(float(s.replace("mib", "").strip()))
    if s.endswith("gb"):
        return int(float(s.replace("gb", "").strip()) * 1024)
    if s.endswith("mb"):
        return int(float(s.replace("mb", "").strip()))
    return int(s) if s.isdigit() else 0


def parse_ollama_logs(log_path: str = "/var/log/ollama.log", since: Optional[datetime] = None) -> List[ModelUsageRecord]:
    """Parse Ollama server logs for model load/unload/token events."""
    records: Dict[str, ModelUsageRecord] = {}
    log_path = os.path.expanduser(log_path)

    if not os.path.exists(log_path):
        # Try journalctl if we're on a systemd system
        try:
            journal = subprocess.run(
                ["journalctl", "-u", "ollama", "--since", "1 hour ago", "--no-pager", "-o", "cat"],
                capture_output=True, text=True, timeout=30
            )
            lines = journal.stdout.splitlines()
        except (subprocess.SubprocessError, FileNotFoundError):
            logger.warning("No ollama logs found at %s or via journalctl", log_path)
            return []
    else:
        with open(log_path) as f:
            lines = f.readlines()

    current_model = None
    current_record = None

    for line in lines:
        line = line.strip()
        if not line:
            continue

        # Try to extract timestamp from line
        ts = _extract_timestamp(line)
        if since and ts and ts < since:
            continue

        # Model loaded
        m = re.search(r'listening on.*model=(\S+)', line)
        if not m:
            m = re.search(r'loaded.*model=(\S+)', line)
        if m:
            model_name = m.group(1)
            if model_name not in records:
                records[model_name] = ModelUsageRecord(
                    model_name=model_name,
                    model_path=model_name,
                    loaded_at=ts or datetime.now(timezone.utc),
                )
            current_model = model_name
            current_record = records[current_model]
            continue

        # Tokens generated
        m = re.search(r'generated (\d+) tokens', line)
        if m and current_record:
            current_record.tokens_generated += int(m.group(1))
            continue

        # GPU usage estimate from latency
        m = re.search(r'(\d+\.?\d*) (ms/token|tok/s)', line)
        if m and current_record:
            val = float(m.group(1))
            if 'tok/s' in m.group(2) and val > 0:
                current_record.avg_token_latency_ms = 1000.0 / val
            elif 'ms/token' in m.group(2):
                # Weighted average
                prev = current_record.avg_token_latency_ms
                cnt = current_record.tokens_generated
                current_record.avg_token_latency_ms = (
                    (prev * (cnt - 1) + val) / cnt if cnt > 0 else val
                )
            continue

        # Request count
        m = re.search(r'\[(\d+)\] request', line)
        if m and current_record:
            current_record.requests += 1
            continue

    return list(records.values())


def _extract_timestamp(line: str) -> Optional[datetime]:
    """Try to extract ISO-8601 timestamp from a log line."""
    # Common formats: 2024-01-15T10:30:00, time="2024-01-15T10:30:00Z"
    m = re.search(r'time="?(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)"?', line)
    if m:
        try:
            return datetime.fromisoformat(m.group(1))
        except ValueError:
            pass
    m = re.search(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})', line)
    if m:
        try:
            return datetime.fromisoformat(m.group(1))
        except ValueError:
            pass
    return None


def read_ollama_api_status() -> List[Dict]:
    """Get running models via Ollama API."""
    try:
        result = subprocess.run(
            ["ollama", "ps"], capture_output=True, text=True, timeout=10
        )
        lines = result.stdout.strip().splitlines()
        models = []
        for line in lines[1:]:  # skip header
            parts = line.split()
            if parts:
                models.append({
                    "name": parts[0],
                    "size": parts[1] if len(parts) > 1 else "?",
                    "until": " ".join(parts[2:]) if len(parts) > 2 else "?",
                })
        return models
    except (subprocess.SubprocessError, FileNotFoundError):
        return []


# ---------------------------------------------------------------------------
# Linguistic / output analysis
# ---------------------------------------------------------------------------

def _tokenize(text: str) -> Counter:
    """Simple whitespace + punctuation tokenizer for comparison."""
    tokens = re.findall(r'\b\w+\b', text.lower())
    return Counter(tokens)


def _kl_divergence(p: Counter, q: Counter, vocab: set) -> float:
    """Kullback-Leibler divergence D(P||Q) with Laplace smoothing."""
    epsilon = 1e-10
    total_p = sum(p.values()) + epsilon
    total_q = sum(q.values()) + epsilon

    kl = 0.0
    for w in vocab:
        pp = (p.get(w, 0) + epsilon) / total_p
        qq = (q.get(w, 0) + epsilon) / total_q
        if pp > 0:
            kl += pp * math.log(pp / qq)
    return kl


def jensen_shannon_divergence(text_a: str, text_b: str) -> float:
    """
    Compute Jensen-Shannon divergence between two texts.
    0.0 = identical, 1.0 = maximally different.
    JSD is symmetric and bounded [0, 1].
    """
    tokens_a = _tokenize(text_a)
    tokens_b = _tokenize(text_b)
    vocab = set(tokens_a.keys()) | set(tokens_b.keys())

    if not vocab:
        return 0.0

    # Compute midpoint distribution M = 0.5 * (P + Q)
    total_a = sum(tokens_a.values()) + 1e-10
    total_b = sum(tokens_b.values()) + 1e-10

    # D(P||M)
    kl_pm = 0.0
    kl_qm = 0.0
    for w in vocab:
        pp = tokens_a.get(w, 0) / total_a
        qq = tokens_b.get(w, 0) / total_b
        mm = 0.5 * (pp + qq)
        if pp > 0 and mm > 0:
            kl_pm += pp * math.log(pp / mm)
        if qq > 0 and mm > 0:
            kl_qm += qq * math.log(qq / mm)

    jsd = 0.5 * (kl_pm + kl_qm)

    # Normalize to [0, 1] (maximum JSD is log(2) for base-e)
    return min(jsd / math.log(2), 1.0)


def transfer_entropy(
    source_samples: List[OutputSample],
    target_samples: List[OutputSample],
    delay: int = 1
) -> float:
    """
    Compute transfer entropy from source → target.
    Measures reduction in uncertainty about target's next token given
    source's current token. High value = strong causal influence.

    Simplified: uses output text token distributions at the
    character/word level with a discrete delay.
    """
    if len(source_samples) < 2 or len(target_samples) < 2:
        return 0.0

    # Build token sequences
    source_tokens = []
    for s in source_samples:
        source_tokens.extend(re.findall(r'\b\w+\b', s.output.lower()))
    target_tokens = []
    for t in target_samples:
        target_tokens.extend(re.findall(r'\b\w+\b', t.output.lower()))

    if len(source_tokens) <= delay or len(target_tokens) <= delay:
        return 0.0

    # Align sequences to same length
    n = min(len(source_tokens), len(target_tokens)) - delay
    if n < 2:
        return 0.0

    source_tokens = source_tokens[:n + delay]
    target_tokens = target_tokens[:n + delay]

    # H(T_t | T_{t-1}) — uncertainty in target given its own past
    # Joint P(T_t, T_{t-1})
    joint_counts_tt = Counter()
    for t in range(delay, n + delay):
        joint_counts_tt[(target_tokens[t], target_tokens[t - delay])] += 1

    # H(T_t | T_{t-1}, S_{t-delay}) — uncertainty given source too
    joint_counts_tst = Counter()
    for t in range(delay, n + delay):
        joint_counts_tst[(target_tokens[t], target_tokens[t - delay], source_tokens[t - delay])] += 1

    total_tt = sum(joint_counts_tt.values()) + 1e-10
    total_tst = sum(joint_counts_tst.values()) + 1e-10

    # Compute conditional entropy terms
    # H(Y|X) = -sum P(x,y) log P(y|x)
    ht_given_past = 0.0
    for (yt, yt_1), cnt in joint_counts_tt.items():
        p_yt_yt1 = cnt / total_tt
        # P(yt | yt_1)
        marginal = sum(
            c for (y2, y2_1), c in joint_counts_tt.items()
            if y2_1 == yt_1
        ) + 1e-10
        p_cond = cnt / marginal
        ht_given_past -= p_yt_yt1 * math.log2(p_cond)

    ht_given_past_and_source = 0.0
    for (yt, yt_1, st), cnt in joint_counts_tst.items():
        p_yt_yt1_st = cnt / total_tst
        marginal = sum(
            c for (y2, y2_1, s2), c in joint_counts_tst.items()
            if y2_1 == yt_1 and s2 == st
        ) + 1e-10
        p_cond = cnt / marginal
        ht_given_past_and_source -= p_yt_yt1_st * math.log2(p_cond)

    te = ht_given_past - ht_given_past_and_source
    return max(te, 0.0)


# ---------------------------------------------------------------------------
# GPU Efficiency & Pareto frontier
# ---------------------------------------------------------------------------

@dataclass
class GPUAllocation:
    model_name: str
    gpu_hours_per_day: float
    quality_score: float  # 1.0 - avg(JSD with all other models); high = unique
    tokens_per_gpu_hour: float = 0.0
    pareto_optimal: bool = False


def compute_pareto_frontier(allocations: List[GPUAllocation]) -> List[GPUAllocation]:
    """
    Find Pareto-optimal models: maximize quality_score while minimizing gpu_hours.
    Returns the frontier (non-dominated set).
    """
    sorted_alloc = sorted(allocations, key=lambda a: a.gpu_hours_per_day)
    frontier = []
    max_quality = -1.0
    for a in sorted_alloc:
        if a.quality_score > max_quality:
            max_quality = a.quality_score
            a.pareto_optimal = True
            frontier.append(a)
    return frontier


def gpu_efficiency_report(
    usage_records: List[ModelUsageRecord],
    samples: Dict[str, List[OutputSample]],
    gpu_power_watts: float = 350.0  # default for RTX 4090
) -> Tuple[List[GPUAllocation], List[GPUAllocation]]:
    """
    Correlate model quality (JSD uniqueness) with GPU-hours spent.
    Returns (allocations, pareto_frontier).
    """
    model_names = list(set(r.model_name for r in usage_records))
    allocations: Dict[str, GPUAllocation] = {}

    for rec in usage_records:
        if rec.model_name not in allocations:
            gpu_hours = rec.gpu_seconds / 3600.0
            tph = (rec.tokens_generated / gpu_hours) if gpu_hours > 0 else 0
            allocations[rec.model_name] = GPUAllocation(
                model_name=rec.model_name,
                gpu_hours_per_day=gpu_hours,
                quality_score=0.5,  # will be refined
                tokens_per_gpu_hour=tph,
            )
        else:
            allocations[rec.model_name].gpu_hours_per_day += rec.gpu_seconds / 3600.0
            allocations[rec.model_name].tokens_per_gpu_hour = (
                allocations[rec.model_name].tokens_per_gpu_hour +
                (rec.tokens_generated / (rec.gpu_seconds / 3600.0))
            ) / 2

    # Compute quality from JSD uniqueness
    if len(model_names) > 1 and samples:
        jsd_matrix = _compute_jsd_matrix(samples, model_names)
        for name in model_names:
            if name in jsd_matrix:
                avg_jsd = sum(jsd_matrix[name].values()) / max(len(jsd_matrix[name]), 1)
                allocations[name].quality_score = min(avg_jsd, 1.0)

    alloc_list = list(allocations.values())
    frontier = compute_pareto_frontier(alloc_list)
    return alloc_list, frontier


def _compute_jsd_matrix(
    samples: Dict[str, List[OutputSample]],
    model_names: List[str]
) -> Dict[str, Dict[str, float]]:
    """Compute pairwise JSD between all model sample sets."""
    matrix: Dict[str, Dict[str, float]] = defaultdict(dict)
    for i, a in enumerate(model_names):
        for b in model_names[i + 1:]:
            text_a = " ".join(s.output for s in samples.get(a, []))
            text_b = " ".join(s.output for s in samples.get(b, []))
            jsd = jensen_shannon_divergence(text_a, text_b)
            matrix[a][b] = jsd
            matrix[b][a] = jsd
    return dict(matrix)


# ---------------------------------------------------------------------------
# Redundancy detection
# ---------------------------------------------------------------------------

def detect_redundancy(
    samples: Dict[str, List[OutputSample]],
    usage_records: List[ModelUsageRecord],
    threshold: float = 0.05   # JSD ≤ 0.05 → "95% similar"
) -> List[RedundancyReport]:
    """
    Detect pairwise model redundancy using JSD + Transfer Entropy.
    Returns actionable RedundancyReport for each pair exceeding threshold.
    """
    reports: List[RedundancyReport] = []
    model_names = list(samples.keys())
    if len(model_names) < 2:
        return reports

    # GPU hours per day per model
    gpu_hrs: Dict[str, float] = defaultdict(float)
    for rec in usage_records:
        gpu_hrs[rec.model_name] += rec.gpu_seconds / 3600.0

    for i, a in enumerate(model_names):
        for b in model_names[i + 1:]:
            text_a = " ".join(s.output for s in samples.get(a, []))
            text_b = " ".join(s.output for s in samples.get(b, []))
            jsd = jensen_shannon_divergence(text_a, text_b)

            te_ab = transfer_entropy(samples.get(a, []), samples.get(b, []))
            te_ba = transfer_entropy(samples.get(b, []), samples.get(a, []))

            wasted = (gpu_hrs.get(a, 0) + gpu_hrs.get(b, 0)) * 0.5

            rec = f"Model {a} and {b} are {((1 - jsd) * 100):.0f}% similar. "
            if jsd <= threshold:
                rec += f"You're wasting approximately {wasted:.1f} GPU hours/day. "
                if te_ab > te_ba and te_ab > 0.1:
                    rec += f"{a}'s outputs appear to be influenced by {b} (TE={te_ab:.3f}). Consider keeping {b} only."
                elif te_ba > te_ab and te_ba > 0.1:
                    rec += f"{b}'s outputs appear to be influenced by {a} (TE={te_ba:.3f}). Consider keeping {a} only."
                else:
                    rec += "Models produce nearly identical outputs. Consider keeping the smaller/faster one."
            else:
                rec += "Models produce sufficiently distinct outputs. No action needed."

            reports.append(RedundancyReport(
                model_a=a,
                model_b=b,
                jsd=jsd,
                transfer_entropy_ab=te_ab,
                transfer_entropy_ba=te_ba,
                estimated_a_gpu_hrs_day=gpu_hrs.get(a, 0),
                estimated_b_gpu_hrs_day=gpu_hrs.get(b, 0),
                wasted_gpu_hrs_day=wasted if jsd <= threshold else 0,
                recommendation=rec,
            ))

    return sorted(reports, key=lambda r: r.wasted_gpu_hrs_day, reverse=True)


# ---------------------------------------------------------------------------
# Phase detection
# ---------------------------------------------------------------------------

def detect_phases(
    usage_records: List[ModelUsageRecord],
    interval_minutes: int = 30
) -> List[Phase]:
    """
    Detect workload phases from model usage over time.

    Phases:
    - "inference": sustained model loading/generation
    - "idle": no model activity
    - "mixed": multiple models loading simultaneously
    """
    if not usage_records:
        return []

    # Build activity timeline
    timeline: Dict[str, int] = defaultdict(int)  # model -> active intervals
    all_times: List[datetime] = []
    for rec in usage_records:
        t = rec.loaded_at
        if rec.unloaded_at:
            # Crude: round to nearest interval
            slot = t.replace(minute=(t.minute // (interval_minutes // 2)) * (interval_minutes // 2), second=0, microsecond=0)
            all_times.append(slot)

    if not all_times:
        return []

    start = min(all_times)
    end = max(all_times)
    phases: List[Phase] = []

    slot_duration = timedelta(minutes=interval_minutes)
    current_slot = start
    while current_slot < end:
        slot_end = current_slot + slot_duration

        # Count models active in this slot
        active_models = set()
        for rec in usage_records:
            if rec.loaded_at <= slot_end and (rec.unloaded_at is None or rec.unloaded_at >= current_slot):
                active_models.add(rec.model_name)

        n_active = len(active_models)
        if n_active == 0:
            phase_name = "idle"
            confidence = 1.0
        elif n_active == 1:
            phase_name = "inference"
            confidence = 0.9
        else:
            phase_name = "mixed"
            confidence = min(0.5 + 0.1 * n_active, 1.0)

        # Merge with last phase if same
        if phases and phases[-1].name == phase_name:
            phases[-1].end = slot_end
            phases[-1].confidence = (phases[-1].confidence + confidence) / 2
        else:
            phases.append(Phase(
                name=phase_name,
                start=current_slot,
                end=slot_end,
                confidence=confidence,
            ))

        current_slot = slot_end

    # Calculate GPU hours per phase
    total_gpu_seconds = sum(r.gpu_seconds for r in usage_records)
    total_seconds = (end - start).total_seconds() if end > start else 1
    for phase in phases:
        phase_seconds = (phase.end - phase.start).total_seconds() if phase.end else 0
        phase.gpu_hours = total_gpu_seconds * (phase_seconds / total_seconds)

    return phases


# ---------------------------------------------------------------------------
# Budget conservation
# ---------------------------------------------------------------------------

@dataclass
class BudgetConstraint:
    """GPU hour budget per model with phase awareness."""
    model_name: str
    daily_budget_hrs: float
    current_daily_hrs: float
    phase: str
    redundant_with: Optional[str] = None
    over_budget: bool = False
    recommendation: str = ""


def compute_budgets(
    usage_records: List[ModelUsageRecord],
    redundancy: List[RedundancyReport],
    max_gpu_hours_per_day: float = 24.0,
    min_useful_threshold: float = 0.05,  # JSD below this = redundant
) -> List[BudgetConstraint]:
    """
    Compute GPU-hour budgets per model with conservation recommendations.
    """
    gpu_hrs: Dict[str, float] = defaultdict(float)
    for rec in usage_records:
        gpu_hrs[rec.model_name] += rec.gpu_seconds / 3600.0

    redundant_pairs: Dict[str, str] = {}
    for r in redundancy:
        if r.jsd <= min_useful_threshold:
            redundant_pairs[r.model_a] = r.model_b
            redundant_pairs[r.model_b] = r.model_a

    constraints = []
    for model_name, hrs in gpu_hrs.items():
        # Proportional budget: share the GPU pie
        n_models = len(gpu_hrs)
        fair_share = max_gpu_hours_per_day / max(n_models, 1)
        allocated_budget = min(hrs, fair_share)

        if hrs > fair_share:
            if model_name in redundant_pairs:
                rec = f"Over budget ({hrs:.1f}h > {fair_share:.1f}h fair share) AND redundant with {redundant_pairs[model_name]}. Eliminate to save."
            else:
                rec = f"Over budget ({hrs:.1f}h > {fair_share:.1f}h fair share). Consider reducing usage."
        else:
            if model_name in redundant_pairs:
                rec = f"Under budget but redundant with {redundant_pairs[model_name]}. Consider removing."
            else:
                rec = f"Within budget. Unique model — keep it."

        constraints.append(BudgetConstraint(
            model_name=model_name,
            daily_budget_hrs=allocated_budget,
            current_daily_hrs=hrs,
            phase="inference",
            redundant_with=redundant_pairs.get(model_name),
            over_budget=hrs > fair_share,
            recommendation=rec,
        ))

    return sorted(constraints, key=lambda c: c.current_daily_hrs, reverse=True)


# ---------------------------------------------------------------------------
# HTML report generation
# ---------------------------------------------------------------------------

def generate_html_report(
    usage_records: List[ModelUsageRecord],
    samples: Dict[str, List[OutputSample]],
    phases: List[Phase],
    show_all: bool = True
) -> str:
    """Generate an HTML report summarizing intelligence monitor findings."""
    jsd_matrix: Dict[str, Dict[str, float]] = {}
    model_names = list(samples.keys())
    if len(model_names) > 1:
        jsd_matrix = _compute_jsd_matrix(samples, model_names)

    redundancy = detect_redundancy(samples, usage_records)
    allocations, frontier = gpu_efficiency_report(usage_records, samples)
    budgets = compute_budgets(usage_records, redundancy)

    total_gpu_hrs = sum(r.gpu_seconds / 3600.0 for r in usage_records)
    total_tokens = sum(r.tokens_generated for r in usage_records)

    html = f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Ollama Intelligence Monitor Report</title>
<style>
body {{ font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 960px; margin: 0 auto; padding: 20px; background: #0d1117; color: #c9d1d9; }}
h1, h2, h3 {{ color: #58a6ff; }}
.metric {{ display: inline-block; background: #161b22; padding: 12px 24px; margin: 8px; border-radius: 8px; border: 1px solid #30363d; }}
.metric .value {{ font-size: 24px; font-weight: bold; color: #3fb950; }}
.metric .label {{ font-size: 12px; color: #8b949e; text-transform: uppercase; }}
table {{ width: 100%; border-collapse: collapse; margin: 16px 0; }}
th, td {{ padding: 8px 12px; text-align: left; border-bottom: 1px solid #30363d; }}
th {{ background: #161b22; color: #58a6ff; }}
tr:hover {{ background: #1c2128; }}
.warning {{ color: #d29922; }}
.danger {{ color: #f85149; }}
.success {{ color: #3fb950; }}
.pareto {{ background: #0d2818; }}
.frontier-badge {{ display: inline-block; padding: 2px 8px; border-radius: 12px; background: #238636; color: #fff; font-size: 11px; }}
</style>
</head>
<body>
<h1>🔬 Ollama Intelligence Monitor</h1>
<p>Report generated at {datetime.now(timezone.utc).strftime('%Y-%m-%d %H:%M:%S UTC')}</p>

<h2>📊 Overview</h2>
<div class="metric"><div class="value">{len(usage_records)}</div><div class="label">Models Tracked</div></div>
<div class="metric"><div class="value">{total_gpu_hrs:.1f}</div><div class="label">GPU Hours (Total)</div></div>
<div class="metric"><div class="value">{total_tokens:,}</div><div class="label">Tokens Generated</div></div>
<div class="metric"><div class="value">{len(phases)}</div><div class="label">Workload Phases</div></div>

<h2>🧠 Model Usage</h2>
<table>
<tr><th>Model</th><th>GPU Hours</th><th>Tokens</th><th>Efficiency</th><th>Pareto</th></tr>
"""
    for alloc in allocations:
        pareto_tag = '<span class="frontier-badge">Pareto-optimal</span>' if alloc.pareto_optimal else ''
        html += f"<tr><td>{alloc.model_name}</td><td>{alloc.gpu_hours_per_day:.1f}</td><td>{alloc.tokens_per_gpu_hour:.0f}/h</td><td>{alloc.quality_score:.3f}</td><td>{pareto_tag}</td></tr>"

    html += """</table>

<h2>⚠️ Redundancy Report</h2>
<table>
<tr><th>Model A</th><th>Model B</th><th>Similarity</th><th>Transfer Entropy</th><th>Wasted GPU h/day</th><th>Recommendation</th></tr>
"""
    for r in redundancy:
        similarity_pct = (1 - r.jsd) * 100
        css = 'danger' if similarity_pct >= 90 else ('warning' if similarity_pct >= 75 else '')
        te_str = f"A→B: {r.transfer_entropy_ab:.3f} / B→A: {r.transfer_entropy_ba:.3f}"
        html += f'<tr class="{css}"><td>{r.model_a}</td><td>{r.model_b}</td><td>{similarity_pct:.0f}%</td><td>{te_str}</td><td>{r.wasted_gpu_hrs_day:.1f}</td><td>{r.recommendation}</td></tr>'

    html += """</table>

<h2>💡 Budget &amp; Conservation</h2>
<table>
<tr><th>Model</th><th>Budget (h/day)</th><th>Current (h/day)</th><th>Status</th><th>Recommendation</th></tr>
"""
    for b in budgets:
        status = '<span class="success">Under budget</span>' if not b.over_budget else '<span class="danger">Over budget</span>'
        html += f"<tr><td>{b.model_name}</td><td>{b.daily_budget_hrs:.1f}</td><td>{b.current_daily_hrs:.1f}</td><td>{status}</td><td>{b.recommendation}</td></tr>"

    html += """</table>

<h2>📈 Workload Phases</h2>
<table>
<tr><th>Phase</th><th>Start</th><th>End</th><th>Confidence</th><th>GPU Hours</th></tr>
"""
    for p in phases:
        emoji = {"inference": "🚀", "idle": "💤", "mixed": "🔄", "training": "🎓"}.get(p.name, "❓")
        start_str = p.start.strftime('%H:%M') if p.start else '?'
        end_str = p.end.strftime('%H:%M') if p.end else 'ongoing'
        html += f"<tr><td>{emoji} {p.name}</td><td>{start_str}</td><td>{end_str}</td><td>{p.confidence:.0%}</td><td>{p.gpu_hours:.1f}</td></tr>"

    html += """
</table>
<hr>
<p><em>SuperInstance Intelligence Monitor — Know your models.</em></p>
</body>
</html>
"""
    return html


# ---------------------------------------------------------------------------
# JSON export
# ---------------------------------------------------------------------------

def export_json(
    usage_records: List[ModelUsageRecord],
    samples: Dict[str, List[OutputSample]],
    phases: List[Phase],
    output_path: str
) -> None:
    """Export full analysis as JSON."""
    redundancy = detect_redundancy(samples, usage_records)
    allocations, frontier = gpu_efficiency_report(usage_records, samples)
    budgets = compute_budgets(usage_records, redundancy)

    report = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "summary": {
            "models_tracked": len(usage_records),
            "total_gpu_hours": sum(r.gpu_seconds / 3600.0 for r in usage_records),
            "total_tokens_generated": sum(r.tokens_generated for r in usage_records),
            "phases_detected": len(phases),
        },
        "usage_records": [asdict(r) for r in usage_records],
        "redundancy": [asdict(r) for r in redundancy],
        "gpu_allocations": [asdict(a) for a in allocations],
        "pareto_frontier": [asdict(a) for a in frontier],
        "budgets": [asdict(b) for b in budgets],
        "phases": [
            {
                "name": p.name,
                "start": p.start.isoformat() if p.start else None,
                "end": p.end.isoformat() if p.end else None,
                "confidence": p.confidence,
                "gpu_hours": p.gpu_hours,
                "model_usage": p.model_usage,
            }
            for p in phases
        ],
    }

    with open(output_path, "w") as f:
        json.dump(report, f, indent=2, default=str)

    logger.info("Report exported to %s", output_path)
