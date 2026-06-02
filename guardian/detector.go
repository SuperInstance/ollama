package main

import (
	"fmt"
	"strings"
	"time"
)

// WasteType categorizes different kinds of resource waste.
type WasteType int

const (
	WasteIdleModel     WasteType = iota // Model loaded but unused
	WasteOversizedContext               // Context window much larger than needed
	WasteDuplicateVariant               // Multiple variants of same model loaded
	WasteExpiredCache                   // Model loaded beyond useful lifetime
)

// Waste represents a detected inefficiency.
type Waste struct {
	Type      WasteType
	Model     string
	Host      string
	Detail    string
	VRAMGB    float64 // VRAM that could be freed
	SavingsGB float64 // Net VRAM savings if fixed
	Severity  string  // "low", "medium", "high"
}

// String returns a human-readable description of the waste.
func (w Waste) String() string {
	return fmt.Sprintf("[%s] %s on %s: %s (recover %.1fGB VRAM)",
		w.Severity, w.Model, w.Host, w.Detail, w.SavingsGB)
}

// IdleThreshold is how long a model can be idle before it's flagged.
const IdleThreshold = 30 * time.Minute

// ContextOversizeRatio flags when context is more than this ratio of actual usage.
const ContextOversizeRatio = 4.0

// DetectWaste scans profiles for resource waste.
func DetectWaste(profiles []Profile) []Waste {
	var wastes []Waste

	modelInstances := make(map[string][]Profile) // group by model name
	for _, p := range profiles {
		modelInstances[p.Model] = append(modelInstances[p.Model], p)
	}

	for _, p := range profiles {
		// Check 1: Idle model
		if p.IdleDuration > IdleThreshold && p.UtilizationPct < 10 {
			severity := "medium"
			savings := p.SizeGB
			if p.UtilizationPct < 2 {
				severity = "high"
			}
			wastes = append(wastes, Waste{
				Type:      WasteIdleModel,
				Model:     p.Model,
				Host:      p.Host,
				Detail:    fmt.Sprintf("loaded %s but used %.1f%% of the time. Switch to on-demand: saves %.0fGB VRAM.", FormatDuration(p.LoadDuration), p.UtilizationPct, savings),
				VRAMGB:    p.SizeGB,
				SavingsGB: savings,
				Severity:  severity,
			})
		}

		// Check 2: Oversized context window
		if p.ContextLength > 0 && p.TotalTokens > 0 && p.TotalRequests > 0 {
			avgTokensPerReq := p.TotalTokens / int64(p.TotalRequests)
			if float64(p.ContextLength) > float64(avgTokensPerReq)*ContextOversizeRatio && avgTokensPerReq > 0 {
				// Estimate wasted context VRAM (rough: 1GB per 8K context for a 7B model)
				wastedRatio := 1.0 - float64(avgTokensPerReq)/float64(p.ContextLength)
				wastedGB := p.SizeGB * wastedRatio * 0.3 // ~30% of model VRAM is context
				if wastedGB > 1.0 {
					wastes = append(wastes, Waste{
						Type:      WasteOversizedContext,
						Model:     p.Model,
						Host:      p.Host,
						Detail:    fmt.Sprintf("context window %d but avg usage ~%d tokens (%.0f%% wasted). Reduce context to save ~%.1fGB.", p.ContextLength, avgTokensPerReq, wastedRatio*100, wastedGB),
						VRAMGB:    p.SizeGB,
						SavingsGB: wastedGB,
						Severity:  "low",
					})
				}
			}
		}
	}

	// Check 3: Duplicate model variants across instances
	// Regroup all profiles by their base model (stripping quantization)
	baseGroups := make(map[string][]Profile)
	for _, p := range profiles {
		base := stripQuant(p.Model)
		baseGroups[base] = append(baseGroups[base], p)
	}
	for base, instances := range baseGroups {
		if len(instances) <= 1 {
			continue
		}
		duplicates := instances
		if len(duplicates) > 1 {
			var totalGB float64
			var hosts []string
			for _, d := range duplicates {
				totalGB += d.SizeGB
				hosts = append(hosts, d.Host)
			}
			// Keep the most-used one, flag the rest
			wastes = append(wastes, Waste{
				Type:      WasteDuplicateVariant,
				Model:     base,
				Host:      strings.Join(hosts, ", "),
				Detail:    fmt.Sprintf("%d variants of %s loaded across fleet (%.1fGB total). Consolidate to most-used variant.", len(duplicates), base, totalGB),
				VRAMGB:    totalGB,
				SavingsGB: totalGB / float64(len(duplicates)) * float64(len(duplicates)-1), // save all but one
				Severity:  "medium",
			})
		}
	}

	return wastes
}

// stripQuant removes quantization suffixes to find the base model name.
// e.g., "llama3:70b-q4_0" → "llama3:70b"
func stripQuant(model string) string {
	parts := strings.SplitN(model, "-", 2)
	if len(parts) > 1 && (strings.Contains(parts[1], "q") || strings.Contains(parts[1], "fp")) {
		return parts[0]
	}
	return model
}

// SummarizeWaste returns aggregate waste statistics.
func SummarizeWaste(wastes []Waste) (totalWastedGB float64, highCount int) {
	for _, w := range wastes {
		totalWastedGB += w.SavingsGB
		if w.Severity == "high" {
			highCount++
		}
	}
	return
}
