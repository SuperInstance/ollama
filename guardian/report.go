package main

import (
	"fmt"
	"strings"
	"time"
)

// Report generates conservation reports from fleet data.
type Report struct {
	Fleet    *Fleet
	Profiles []Profile
	Wastes   []Waste
}

// NewReport creates a report generator.
func NewReport(fleet *Fleet, profiles []Profile, wastes []Waste) *Report {
	return &Report{Fleet: fleet, Profiles: profiles, Wastes: wastes}
}

// Generate produces a full conservation report.
func (r *Report) Generate() string {
	var b strings.Builder

	b.WriteString("╔══════════════════════════════════════════════════════════╗\n")
	b.WriteString("║          MODEL CONSERVATION GUARDIAN REPORT             ║\n")
	b.WriteString("╚══════════════════════════════════════════════════════════╝\n")
	b.WriteString(fmt.Sprintf("  Generated: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("  Fleet: %d instances, %d models loaded\n\n",
		len(r.Fleet.Instances), r.countModels()))

	// Fleet overview
	r.writeFleetOverview(&b)

	// Model details
	r.writeModelDetails(&b)

	// Waste detection
	r.writeWasteSection(&b)

	// Recommendations
	r.writeRecommendations(&b)

	// Summary
	r.writeSummary(&b)

	return b.String()
}

func (r *Report) countModels() int {
	count := 0
	for _, inst := range r.Fleet.Instances {
		count += len(inst.Models)
	}
	return count
}

func (r *Report) totalVRAM() (used, total float64) {
	for _, inst := range r.Fleet.Instances {
		total += inst.TotalVRAMGB
		used += inst.UsedVRAMGB
	}
	return
}

func (r *Report) writeFleetOverview(b *strings.Builder) {
	used, total := r.totalVRAM()
	pct := float64(0)
	if total > 0 {
		pct = used / total * 100
	}

	b.WriteString("── Fleet Overview ──────────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("  VRAM: %.1fGB / %.1fGB used (%.0f%%)\n", used, total, pct))

	activeModels := 0
	ghostModels := 0
	for _, p := range r.Profiles {
		if p.UtilizationPct > 5 {
			activeModels++
		} else {
			ghostModels++
		}
	}
	b.WriteString(fmt.Sprintf("  Models: %d active, %d ghosts (loaded but barely used)\n\n", activeModels, ghostModels))
}

func (r *Report) writeModelDetails(b *strings.Builder) {
	b.WriteString("── Model Details ───────────────────────────────────────\n")
	if len(r.Profiles) == 0 {
		b.WriteString("  No models loaded.\n\n")
		return
	}

	for _, p := range r.Profiles {
		icon := "✅"
		if p.UtilizationPct < 5 {
			icon = "👻"
		} else if p.UtilizationPct < 20 {
			icon = "⚠️"
		}
		b.WriteString(fmt.Sprintf("  %s %s (%s)\n", icon, p.Model, p.Host))
		b.WriteString(fmt.Sprintf("     VRAM: %.1fGB | Context: %d | Utilization: %.1f%%\n",
			p.SizeGB, p.ContextLength, p.UtilizationPct))
		b.WriteString(fmt.Sprintf("     Throughput: %s | Queue: %d | Requests: %d\n\n",
			FormatTokensPerSec(p.AvgTokensPerSec), p.QueueDepth, p.TotalRequests))
	}
}

func (r *Report) writeWasteSection(b *strings.Builder) {
	b.WriteString("── Waste Detection ─────────────────────────────────────\n")
	if len(r.Wastes) == 0 {
		b.WriteString("  ✅ No waste detected. Fleet is efficient.\n\n")
		return
	}

	totalSavings := float64(0)
	highCount := 0
	for _, w := range r.Wastes {
		severity := "🔵"
		switch w.Severity {
		case "high":
			severity = "🔴"
			highCount++
		case "medium":
			severity = "🟡"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", severity, w.Detail))
		totalSavings += w.SavingsGB
	}
	b.WriteString(fmt.Sprintf("\n  Potential savings: %.1fGB VRAM (%d high-priority)\n\n", totalSavings, highCount))
}

func (r *Report) writeRecommendations(b *strings.Builder) {
	b.WriteString("── Recommendations ─────────────────────────────────────\n")

	if len(r.Wastes) == 0 {
		b.WriteString("  No changes needed.\n\n")
		return
	}

	recs := 0
	for _, w := range r.Wastes {
		switch w.Type {
		case WasteIdleModel:
			b.WriteString(fmt.Sprintf("  → Unload %s on %s when idle >30min (save %.0fGB)\n",
				w.Model, w.Host, w.SavingsGB))
			recs++
		case WasteOversizedContext:
			b.WriteString(fmt.Sprintf("  → Reduce %s context window on %s (save ~%.1fGB)\n",
				w.Model, w.Host, w.SavingsGB))
			recs++
		case WasteDuplicateVariant:
			b.WriteString(fmt.Sprintf("  → Consolidate %s variants — keep the busiest, unload the rest\n",
				w.Model))
			recs++
		}
	}
	if recs == 0 {
		b.WriteString("  No actionable recommendations.\n")
	}
	b.WriteString("\n")
}

func (r *Report) writeSummary(b *strings.Builder) {
	b.WriteString("── Summary ─────────────────────────────────────────────\n")

	used, total := r.totalVRAM()
	wastedGB, highCount := SummarizeWaste(r.Wastes)

	b.WriteString(fmt.Sprintf("  %d models loaded, ", r.countModels()))
	activeCount := 0
	for _, p := range r.Profiles {
		if p.UtilizationPct > 5 {
			activeCount++
		}
	}
	b.WriteString(fmt.Sprintf("%d actually used.\n", activeCount))

	if wastedGB > 0 {
		b.WriteString(fmt.Sprintf("  You're spending %.0fGB VRAM on ghosts.\n", wastedGB))
	} else {
		b.WriteString("  Fleet is operating efficiently.\n")
	}

	if highCount > 0 {
		b.WriteString(fmt.Sprintf("  🚨 %d high-priority issues need attention.\n", highCount))
	}

	b.WriteString(fmt.Sprintf("  VRAM efficiency: %.0f%% (%.1fGB of %.1fGB)\n",
		used/total*100, used, total))
	b.WriteString("\n")
}
