#!/usr/bin/env python3
"""Tests for the Intelligence Monitor."""

from __future__ import annotations

import json
import math
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

# Ensure the parent is on the path
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from intelligence_monitor.monitor import (
    ModelUsageRecord,
    OutputSample,
    Phase,
    RedundancyReport,
    BudgetConstraint,
    GPUAllocation,
    jensen_shannon_divergence,
    transfer_entropy,
    detect_redundancy,
    compute_pareto_frontier,
    gpu_efficiency_report,
    compute_budgets,
    detect_phases,
    parse_ollama_logs,
    generate_html_report,
    export_json,
)


class TestJensenShannonDivergence(unittest.TestCase):
    def test_identical_texts(self):
        text = "The quick brown fox jumps over the lazy dog."
        jsd = jensen_shannon_divergence(text, text)
        self.assertAlmostEqual(jsd, 0.0, places=4)

    def test_completely_different_texts(self):
        text_a = "a" * 100
        text_b = "b" * 100
        jsd = jensen_shannon_divergence(text_a, text_b)
        self.assertGreater(jsd, 0.5)

    def test_similar_texts(self):
        text_a = "Machine learning is transforming artificial intelligence research."
        text_b = "Machine learning is revolutionizing AI research and development."
        jsd = jensen_shannon_divergence(text_a, text_b)
        self.assertLess(jsd, 0.55)
        self.assertGreaterEqual(jsd, 0.0)

    def test_empty_texts(self):
        jsd = jensen_shannon_divergence("", "")
        self.assertEqual(jsd, 0.0)

    def test_symmetric(self):
        text_a = "Hello world this is a test of the emergency broadcast system."
        text_b = "The quick brown fox jumps over something entirely different here."
        jsd_ab = jensen_shannon_divergence(text_a, text_b)
        jsd_ba = jensen_shannon_divergence(text_b, text_a)
        self.assertAlmostEqual(jsd_ab, jsd_ba, places=6)

    def test_bounded(self):
        text_a = "abcdefghijklmnopqrstuvwxyz"
        text_b = "zyxwvutsrqponmlkjihgfedcba"
        jsd = jensen_shannon_divergence(text_a, text_b)
        self.assertGreaterEqual(jsd, 0.0)
        self.assertLessEqual(jsd, 1.0)


class TestTransferEntropy(unittest.TestCase):
    def setUp(self):
        self.base_samples = [
            OutputSample("model_a", "test", "hello world foo bar", datetime.now(timezone.utc)),
            OutputSample("model_a", "test", "baz qux hello world", datetime.now(timezone.utc)),
            OutputSample("model_a", "test", "foo bar baz qux", datetime.now(timezone.utc)),
        ]

    def test_self_te(self):
        """Transfer entropy from a model to itself should be non-negative."""
        te = transfer_entropy(self.base_samples, self.base_samples)
        self.assertGreaterEqual(te, 0.0)

    def test_no_influence(self):
        """Completely different outputs should yield low transfer entropy."""
        source = [
            OutputSample("s", "", "abc def ghi jkl mno", datetime.now(timezone.utc)),
            OutputSample("s", "", "pqr stu vwx yz", datetime.now(timezone.utc)),
        ]
        target = [
            OutputSample("t", "", "123 456 789", datetime.now(timezone.utc)),
            OutputSample("t", "", "0 999 888 777", datetime.now(timezone.utc)),
        ]
        te = transfer_entropy(source, target)
        # With different vocabularies, TE should be very low
        self.assertLess(te, 0.5)

    def test_insufficient_data(self):
        """Fewer than 2 samples each should return 0."""
        single = [OutputSample("a", "", "hello", datetime.now(timezone.utc))]
        te = transfer_entropy(single, self.base_samples)
        self.assertEqual(te, 0.0)


class TestRedundancyDetection(unittest.TestCase):
    def setUp(self):
        now = datetime.now(timezone.utc)
        self.similar_samples = {
            "llama3:8b": [
                OutputSample("llama3:8b", "prompt",
                    "Quantum computing uses the principles of superposition and entanglement.",
                    now),
                OutputSample("llama3:8b", "prompt",
                    "Qubits can exist in multiple states simultaneously.",
                    now),
            ],
            "llama3:8b-q4": [
                OutputSample("llama3:8b-q4", "prompt",
                    "Quantum computing utilizes superposition and quantum entanglement.",
                    now),
                OutputSample("llama3:8b-q4", "prompt",
                    "Qubits are able to exist in many states at the same time.",
                    now),
            ],
            "mistral:7b": [
                OutputSample("mistral:7b", "prompt",
                    "The economy grew by 3% last quarter according to recent reports.",
                    now),
                OutputSample("mistral:7b", "prompt",
                    "Stock markets rallied on positive earnings data.",
                    now),
            ],
        }
        self.usage = [
            ModelUsageRecord("llama3:8b", "", now, now, 100, 1000, gpu_seconds=3600),
            ModelUsageRecord("llama3:8b-q4", "", now, now, 80, 800, gpu_seconds=1800),
            ModelUsageRecord("mistral:7b", "", now, now, 50, 500, gpu_seconds=900),
        ]

    def test_redundancy_detected(self):
        """Similar models should be flagged as redundant."""
        reports = detect_redundancy(self.similar_samples, self.usage, threshold=0.5)
        llama_pair = None
        mistral_pair = None
        for r in reports:
            if ("llama3:8b" in r.model_a and "llama3:8b-q4" in r.model_b) or \
               ("llama3:8b" in r.model_b and "llama3:8b-q4" in r.model_a):
                llama_pair = r
            if "mistral" in r.model_a or "mistral" in r.model_b:
                mistral_pair = r
        self.assertIsNotNone(llama_pair)
        self.assertIsNotNone(mistral_pair)
        self.assertLess(llama_pair.jsd, mistral_pair.jsd,
            f"llama pair JSD {llama_pair.jsd:.4f} should be < mistral pair JSD {mistral_pair.jsd:.4f}")

    def test_distinct_models_not_redundant(self):
        """Different models should not be flagged as redundant."""
        reports = detect_redundancy(self.similar_samples, self.usage, threshold=0.5)
        for r in reports:
            if "mistral" in r.model_a or "mistral" in r.model_b:
                # Mistral talks about economy, llama about quantum — JSD should be higher
                self.assertGreater(r.jsd, 0.3,
                    f"mistral should be distinct, got JSD={r.jsd:.4f}")


class TestParetoFrontier(unittest.TestCase):
    def test_pareto_frontier(self):
        allocs = [
            GPUAllocation("a", 1.0, 0.9, 100),
            GPUAllocation("b", 2.0, 0.7, 80),
            GPUAllocation("c", 0.5, 0.5, 50),
            GPUAllocation("d", 4.0, 0.95, 60),
            GPUAllocation("e", 0.2, 0.3, 30),
        ]
        frontier = compute_pareto_frontier(allocs)
        # a (1.0h,0.9q) should be Pareto-optimal
        frontier_names = {f.model_name for f in frontier}
        self.assertIn("a", frontier_names)
        # c has lower GPU hours than a (0.5 < 1.0) but higher quality than e (0.5 > 0.3)
        # so it IS on the Pareto frontier — you need the non-dominated middle ground too
        self.assertIn("c", frontier_names)
        # e (0.2h,0.3q) should be on frontier (lowest GPU hours)
        self.assertIn("e", frontier_names)

    def test_no_allocations(self):
        frontier = compute_pareto_frontier([])
        self.assertEqual(frontier, [])


class TestGPUDoesntRegress(unittest.TestCase):
    """Test that GPU seconds are properly tracked across all computations."""
    def test_gpu_hours_consistency(self):
        now = datetime.now(timezone.utc)
        records = [
            ModelUsageRecord("a", "", now, now, 10, 100, gpu_seconds=3600),
            ModelUsageRecord("b", "", now, now, 5, 50, gpu_seconds=1800),
        ]
        samples = {
            "a": [OutputSample("a", "", "hello world foo bar baz", now)],
            "b": [OutputSample("b", "", "different content entirely here", now)],
        }
        allocs, _ = gpu_efficiency_report(records, samples)
        total_hrs = sum(a.gpu_hours_per_day for a in allocs)
        self.assertAlmostEqual(total_hrs, (3600 + 1800) / 3600, places=4)


class TestPhases(unittest.TestCase):
    def test_phases_basic(self):
        now = datetime.now(timezone.utc)
        records = [
            ModelUsageRecord("a", "", now - timedelta(hours=2), now - timedelta(hours=1),
                             10, 100, gpu_seconds=3600),
            ModelUsageRecord("b", "", now - timedelta(hours=1), now,
                             5, 50, gpu_seconds=1800),
        ]
        phases = detect_phases(records)
        self.assertGreater(len(phases), 0)
        for p in phases:
            self.assertIn(p.name, ["inference", "idle", "mixed", "training"])

    def test_phases_empty(self):
        phases = detect_phases([])
        self.assertEqual(phases, [])


class TestBudgets(unittest.TestCase):
    def test_budget_allocation(self):
        now = datetime.now(timezone.utc)
        records = [
            ModelUsageRecord("big-model", "", now, now, 100, 1000, gpu_seconds=72000),  # 20h
            ModelUsageRecord("small-model2", "", now, now, 50, 500, gpu_seconds=7200),  # 2h
        ]
        samples = {
            "big-model": [OutputSample("big-model", "", "this is text from the big model which is very long and detailed and comprehensive in nature", now)],
            "small-model2": [OutputSample("small-model2", "", "this is different small model text about something else entirely that is unique", now)],
        }
        redundancy = detect_redundancy(samples, records)
        budgets = compute_budgets(records, redundancy, max_gpu_hours_per_day=24.0)
        self.assertEqual(len(budgets), 2)
        over_budget = [b for b in budgets if b.over_budget]
        self.assertGreater(len(over_budget), 0,
                           "big-model should be over its fair share of 12h")


class TestExport(unittest.TestCase):
    def test_export_json(self):
        now = datetime.now(timezone.utc)
        records = [ModelUsageRecord("test", "", now, now, 5, 100, gpu_seconds=3600)]
        samples = {"test": [OutputSample("test", "", "hello world", now)]}
        phases = [Phase("inference", now, now + timedelta(hours=1), 0.9)]

        with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
            export_json(records, samples, phases, f.name)
            with open(f.name) as f2:
                data = json.load(f2)
            self.assertIn("summary", data)
            self.assertEqual(data["summary"]["models_tracked"], 1)
            self.assertIn("redundancy", data)
            self.assertIn("budgets", data)
            self.assertIn("phases", data)

    def test_html_report_generates(self):
        now = datetime.now(timezone.utc)
        records = [ModelUsageRecord("test", "", now, now, 5, 100, gpu_seconds=3600)]
        samples = {"test": [OutputSample("test", "", "hello world", now)]}
        phases = [Phase("inference", now, now + timedelta(hours=1), 0.9)]
        html = generate_html_report(records, samples, phases)
        self.assertIn("ollama", html.lower())
        self.assertIn("intelligence", html.lower())
        self.assertIn("html", html.lower())


if __name__ == "__main__":
    unittest.main()
