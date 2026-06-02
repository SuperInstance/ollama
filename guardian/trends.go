package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// TrendAnalysis compares current fleet state to historical data.
type TrendAnalysis struct {
	Current  *Fleet
	History  []*Fleet
	Profiles []Profile
}

// TrendResult describes a detected trend.
type TrendResult struct {
	Model     string
	Host      string
	Metric    string    // "utilization", "vram", "request_count"
	Direction string    // "up", "down", "stable"
	ChangePct float64   // percentage change
	OldValue  float64
	NewValue  float64
	Period    string    // e.g. "7d", "24h"
	Detail    string
}

// NewTrendAnalysis creates a trend analyzer.
func NewTrendAnalysis(current *Fleet, history []*Fleet, profiles []Profile) *TrendAnalysis {
	return &TrendAnalysis{
		Current:  current,
		History:  history,
		Profiles: profiles,
	}
}

// Analyze compares current state to history and returns trends.
func (ta *TrendAnalysis) Analyze() []TrendResult {
	if len(ta.History) < 2 {
		return nil
	}

	var results []TrendResult

	// Find the oldest snapshot in history
	oldest := ta.History[0]
	for _, h := range ta.History {
		if h.SampledAt.Before(oldest.SampledAt) {
			oldest = h
		}
	}

	period := FormatDuration(time.Since(oldest.SampledAt))

	// Build utilization map from current profiles
	utilMap := make(map[string]float64) // "host/model" -> utilization
	reqMap := make(map[string]int)      // "host/model" -> request count
	for _, p := range ta.Profiles {
		key := p.Host + "/" + p.Model
		utilMap[key] = p.UtilizationPct
		reqMap[key] = p.TotalRequests
	}

	// Build old utilization from oldest fleet
	oldUtilMap := make(map[string]float64)
	oldVRAMMap := make(map[string]float64)
	for _, inst := range oldest.Instances {
		for _, m := range inst.Models {
			key := inst.Host + "/" + m.Name
			oldVRAMMap[key] = m.SizeGB
			// Estimate old utilization from request count ratio
			oldUtilMap[key] = float64(m.RequestCount) * 0.5 // rough estimate
		}
	}

	// Current VRAM map
	curVRAMMap := make(map[string]float64)
	for _, inst := range ta.Current.Instances {
		for _, m := range inst.Models {
			key := inst.Host + "/" + m.Name
			curVRAMMap[key] = m.SizeGB
		}
	}

	// Compute utilization trends
	for key, curUtil := range utilMap {
		oldUtil, existed := oldUtilMap[key]
		if !existed {
			continue
		}
		change := curUtil - oldUtil
		changePct := float64(0)
		if oldUtil > 0 {
			changePct = change / oldUtil * 100
		}

		direction := "stable"
		if changePct > 10 {
			direction = "up"
		} else if changePct < -10 {
			direction = "down"
		}

		if direction == "stable" {
			continue
		}

		parts := strings.SplitN(key, "/", 2)
		host, model := parts[0], parts[1]
		results = append(results, TrendResult{
			Model:     model,
			Host:      host,
			Metric:    "utilization",
			Direction: direction,
			ChangePct: changePct,
			OldValue:  oldUtil,
			NewValue:  curUtil,
			Period:    period,
			Detail:    fmt.Sprintf("%s on %s utilization %s %.1f%% → %.1f%% over %s", model, host, direction, oldUtil, curUtil, period),
		})
	}

	// Compute VRAM trends
	for key, curVRAM := range curVRAMMap {
		oldVRAM, existed := oldVRAMMap[key]
		if !existed || oldVRAM == 0 {
			continue
		}
		changePct := (curVRAM - oldVRAM) / oldVRAM * 100
		if changePct < -5 || changePct > 5 {
			parts := strings.SplitN(key, "/", 2)
			host, model := parts[0], parts[1]
			direction := "up"
			if changePct < 0 {
				direction = "down"
			}
			results = append(results, TrendResult{
				Model:     model,
				Host:      host,
				Metric:    "vram",
				Direction: direction,
				ChangePct: changePct,
				OldValue:  oldVRAM,
				NewValue:  curVRAM,
				Period:    period,
				Detail:    fmt.Sprintf("%s on %s VRAM %s %.1fGB → %.1fGB over %s", model, host, direction, oldVRAM, curVRAM, period),
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		ai, aj := results[i], results[j]
		if ai.Model != aj.Model {
			return ai.Model < aj.Model
		}
		return ai.Metric < aj.Metric
	})

	return results
}

// FormatTrends produces a human-readable trend report.
func FormatTrends(trends []TrendResult) string {
	if len(trends) == 0 {
		return "No significant trends detected in the analysis period."
	}

	var b strings.Builder
	b.WriteString("── Trend Analysis ──────────────────────────────────────\n")
	for _, t := range trends {
		icon := "📈"
		if t.Direction == "down" {
			icon = "📉"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", icon, t.Detail))
	}
	return b.String()
}
