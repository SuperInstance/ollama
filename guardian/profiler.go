package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Profile tracks usage statistics for a model over time.
type Profile struct {
	Model         string
	Host          string
	SizeGB        float64
	ContextLength int

	// Computed metrics
	TokensPerSec      float64
	AvgTokensPerSec   float64
	MemoryPeakGB      float64
	QueueDepth        int
	AvgQueueDepth     float64
	TotalRequests     int
	TotalTokens       int64
	UtilizationPct    float64 // % of time the model was active vs idle
	LoadDuration      time.Duration
	IdleDuration      time.Duration
	Samples           int
}

// ProfileSample is a single measurement point.
type ProfileSample struct {
	Timestamp   time.Time
	TokensPerSec float64
	MemoryGB     float64
	QueueDepth   int
}

// ModelProfiler collects and computes profiles from fleet state.
type ModelProfiler struct {
	samples map[string][]ProfileSample // keyed by "host/model"
	profiles map[string]*Profile
}

// NewModelProfiler creates a new profiler.
func NewModelProfiler() *ModelProfiler {
	return &ModelProfiler{
		samples:  make(map[string][]ProfileSample),
		profiles: make(map[string]*Profile),
	}
}

// AddSample records a measurement for a model.
func (mp *ModelProfiler) AddSample(host, model string, s ProfileSample) {
	key := host + "/" + model
	mp.samples[key] = append(mp.samples[key], s)
}

// ComputeProfiles calculates aggregate profiles from collected samples.
func (mp *ModelProfiler) ComputeProfiles(fleet *Fleet) []Profile {
	// Build profiles from current fleet state
	for _, inst := range fleet.Instances {
		for _, m := range inst.Models {
			key := inst.Host + "/" + m.Name
			p := &Profile{
				Model:         m.Name,
				Host:          inst.Host,
				SizeGB:        m.SizeGB,
				ContextLength: m.ContextLength,
				TotalRequests: m.RequestCount,
				TotalTokens:   m.TokensGenerated,
			}

			// Compute load and idle durations
			if !m.LoadedAt.IsZero() {
				p.LoadDuration = time.Since(m.LoadedAt)
			}
			if !m.LastRequestAt.IsZero() {
				p.IdleDuration = time.Since(m.LastRequestAt)
			}

			// Utilization: fraction of load time that had requests
			if p.LoadDuration > 0 && p.TotalRequests > 0 {
				// Estimate active time from request count and average request duration
				// Assume ~30s per request as baseline
				activeTime := time.Duration(p.TotalRequests) * 30 * time.Second
				if activeTime > p.LoadDuration {
					activeTime = p.LoadDuration
				}
				p.UtilizationPct = float64(activeTime) / float64(p.LoadDuration) * 100
			}

			// Process samples if available
			if samples, ok := mp.samples[key]; ok && len(samples) > 0 {
				p.Samples = len(samples)
				var totalTPS, totalMem float64
				var maxMem float64
				var totalQD int
				for _, s := range samples {
					totalTPS += s.TokensPerSec
					totalMem += s.MemoryGB
					totalQD += s.QueueDepth
					if s.MemoryGB > maxMem {
						maxMem = s.MemoryGB
					}
				}
				n := float64(len(samples))
				p.AvgTokensPerSec = totalTPS / n
				p.MemoryPeakGB = maxMem
				p.AvgQueueDepth = float64(totalQD) / n

				// Latest values
				last := samples[len(samples)-1]
				p.TokensPerSec = last.TokensPerSec
				p.QueueDepth = last.QueueDepth
			}

			mp.profiles[key] = p
		}
	}

	var result []Profile
	for _, p := range mp.profiles {
		result = append(result, *p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Model < result[j].Model
	})
	return result
}

// CollectProfiles is a convenience function that profiles a fleet in one shot.
func CollectProfiles(fleet *Fleet) []Profile {
	profiler := NewModelProfiler()
	return profiler.ComputeProfiles(fleet)
}

// FormatTokensPerSec returns a human-readable throughput string.
func FormatTokensPerSec(tps float64) string {
	if tps == 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.1f tok/s", tps)
}

// FormatDuration returns a human-readable duration.
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}

// ContextEfficiency returns what fraction of the context window is typically used.
func ContextEfficiency(contextLength int, avgTokensPerRequest int64) float64 {
	if contextLength <= 0 {
		return 0
	}
	pct := float64(avgTokensPerRequest) / float64(contextLength) * 100
	return math.Min(pct, 100)
}
