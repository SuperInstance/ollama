package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ModelBudget defines resource limits for a single model.
type ModelBudget struct {
	Model              string  `json:"model"`
	MaxVRAMGB          float64 `json:"max_vram_gb"`
	MaxConcurrentReqs  int     `json:"max_concurrent_requests"`
	MaxTokensPerDay    int64   `json:"max_tokens_per_day"`
	AllowedInstances   []string `json:"allowed_instances,omitempty"` // restrict to specific hosts
}

// BudgetSet is a collection of model budgets.
type BudgetSet struct {
	GlobalMaxVRAMGB float64       `json:"global_max_vram_gb"`
	Default         ModelBudget   `json:"default"`
	Models          []ModelBudget `json:"models"`
}

// InstanceState represents a single Ollama instance's current state.
type InstanceState struct {
	Host        string        `json:"host"`
	Models      []LoadedModel `json:"models"`
	TotalVRAMGB float64       `json:"total_vram_gb"`
	UsedVRAMGB  float64       `json:"used_vram_gb"`
}

// LoadedModel is a model currently loaded on an instance.
type LoadedModel struct {
	Name            string    `json:"name"`
	SizeGB          float64   `json:"size_gb"`
	ContextLength   int       `json:"context_length"`
	LoadedAt        time.Time `json:"loaded_at"`
	LastRequestAt   time.Time `json:"last_request_at"`
	RequestCount    int       `json:"request_count"`
	TokensGenerated int64     `json:"tokens_generated"`
	TokensToday     int64     `json:"tokens_today"`
	ConcurrentReqs  int       `json:"concurrent_requests"`
}

// Fleet is a snapshot of all instances.
type Fleet struct {
	Instances []InstanceState `json:"instances"`
	SampledAt time.Time       `json:"sampled_at"`
}

// LoadFleet reads fleet state from a directory of JSON files.
func LoadFleet(dir string) (*Fleet, error) {
	fleet := &Fleet{SampledAt: time.Now()}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Return empty fleet if dir doesn't exist
		return fleet, nil
	}
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) < 5 || e.Name()[len(e.Name())-5:] != ".json" {
			continue
		}
		path := dir + "/" + e.Name()
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var inst InstanceState
		if err := json.Unmarshal(data, &inst); err != nil {
			continue
		}
		fleet.Instances = append(fleet.Instances, inst)
	}
	return fleet, nil
}

// LoadBudgets reads budget definitions from a JSON file.
func LoadBudgets(path string) (*BudgetSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading budgets: %w", err)
	}
	var bs BudgetSet
	if err := json.Unmarshal(data, &bs); err != nil {
		return nil, fmt.Errorf("parsing budgets: %w", err)
	}
	return &bs, nil
}

// CheckBudgets validates current fleet state against budgets.
func CheckBudgets(fleet *Fleet, budgets *BudgetSet) []string {
	var violations []string
	modelBudgetMap := make(map[string]ModelBudget)
	for _, b := range budgets.Models {
		modelBudgetMap[b.Model] = b
	}

	var totalVRAM float64
	for _, inst := range fleet.Instances {
		for _, m := range inst.Models {
			totalVRAM += m.SizeGB

			b, ok := modelBudgetMap[m.Name]
			if !ok {
				b = budgets.Default
			}

			if b.MaxVRAMGB > 0 && m.SizeGB > b.MaxVRAMGB {
				violations = append(violations, fmt.Sprintf(
					"%s on %s: %.1fGB VRAM exceeds budget of %.1fGB",
					m.Name, inst.Host, m.SizeGB, b.MaxVRAMGB,
				))
			}
			if b.MaxConcurrentReqs > 0 && m.ConcurrentReqs > b.MaxConcurrentReqs {
				violations = append(violations, fmt.Sprintf(
					"%s on %s: %d concurrent requests exceeds budget of %d",
					m.Name, inst.Host, m.ConcurrentReqs, b.MaxConcurrentReqs,
				))
			}
			if b.MaxTokensPerDay > 0 && m.TokensToday > b.MaxTokensPerDay {
				violations = append(violations, fmt.Sprintf(
					"%s on %s: %d tokens today exceeds daily budget of %d",
					m.Name, inst.Host, m.TokensToday, b.MaxTokensPerDay,
				))
			}

			// Instance allowlist check
			if len(b.AllowedInstances) > 0 {
				allowed := false
				for _, a := range b.AllowedInstances {
					if a == inst.Host {
						allowed = true
						break
					}
				}
				if !allowed {
					violations = append(violations, fmt.Sprintf(
						"%s: not allowed on %s (allowed: %v)",
						m.Name, inst.Host, b.AllowedInstances,
					))
				}
			}
		}
	}

	if budgets.GlobalMaxVRAMGB > 0 && totalVRAM > budgets.GlobalMaxVRAMGB {
		violations = append(violations, fmt.Sprintf(
			"Global: %.1fGB total VRAM exceeds fleet budget of %.1fGB",
			totalVRAM, budgets.GlobalMaxVRAMGB,
		))
	}

	return violations
}
