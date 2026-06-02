package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ExportFormat represents supported output formats.
type ExportFormat string

const (
	FormatJSON       ExportFormat = "json"
	FormatPrometheus ExportFormat = "prometheus"
	FormatSlack      ExportFormat = "slack"
)

// FleetExport contains exportable fleet data.
type FleetExport struct {
	Timestamp  time.Time     `json:"timestamp"`
	Instances  []InstanceExport `json:"instances"`
	Summary    ExportSummary `json:"summary"`
	Wastes     []WasteExport `json:"wastes,omitempty"`
}

// InstanceExport is the export view of a single instance.
type InstanceExport struct {
	Host        string         `json:"host"`
	Models      []ModelExport  `json:"models"`
	TotalVRAMGB float64        `json:"total_vram_gb"`
	UsedVRAMGB  float64        `json:"used_vram_gb"`
	UtilPct     float64        `json:"utilization_pct"`
}

// ModelExport is the export view of a loaded model.
type ModelExport struct {
	Name          string  `json:"name"`
	SizeGB        float64 `json:"size_gb"`
	UtilizationPct float64 `json:"utilization_pct"`
	TotalRequests int     `json:"total_requests"`
	TotalTokens   int64   `json:"total_tokens"`
	AvgTokPerSec  float64 `json:"avg_tokens_per_sec"`
	Status        string  `json:"status"` // "active", "idle", "ghost"
}

// ExportSummary holds aggregate stats.
type ExportSummary struct {
	TotalInstances int     `json:"total_instances"`
	TotalModels    int     `json:"total_models"`
	ActiveModels   int     `json:"active_models"`
	GhostModels    int     `json:"ghost_models"`
	TotalVRAMGB    float64 `json:"total_vram_gb"`
	UsedVRAMGB     float64 `json:"used_vram_gb"`
	WastedVRAMGB   float64 `json:"wasted_vram_gb"`
}

// WasteExport is the export view of a waste detection.
type WasteExport struct {
	Type      string  `json:"type"`
	Model     string  `json:"model"`
	Host      string  `json:"host"`
	Detail    string  `json:"detail"`
	SavingsGB float64 `json:"savings_gb"`
	Severity  string  `json:"severity"`
}

// BuildExport creates an exportable snapshot from fleet data.
func BuildExport(fleet *Fleet, profiles []Profile, wastes []Waste) *FleetExport {
	exp := &FleetExport{
		Timestamp: time.Now(),
		Summary: ExportSummary{
			TotalInstances: len(fleet.Instances),
		},
	}

	var wastedGB float64
	for _, w := range wastes {
		wastedGB += w.SavingsGB
		exp.Wastes = append(exp.Wastes, WasteExport{
			Type:      wasteTypeName(w.Type),
			Model:     w.Model,
			Host:      w.Host,
			Detail:    w.Detail,
			SavingsGB: w.SavingsGB,
			Severity:  w.Severity,
		})
	}
	exp.Summary.WastedVRAMGB = wastedGB

	// Build profile lookup
	profileMap := make(map[string]*Profile)
	for i := range profiles {
		key := profiles[i].Host + "/" + profiles[i].Model
		profileMap[key] = &profiles[i]
	}

	for _, inst := range fleet.Instances {
		ie := InstanceExport{
			Host:        inst.Host,
			TotalVRAMGB: inst.TotalVRAMGB,
			UsedVRAMGB:  inst.UsedVRAMGB,
		}
		if inst.TotalVRAMGB > 0 {
			ie.UtilPct = inst.UsedVRAMGB / inst.TotalVRAMGB * 100
		}

		for _, m := range inst.Models {
			me := ModelExport{
				Name:   m.Name,
				SizeGB: m.SizeGB,
			}
			key := inst.Host + "/" + m.Name
			if p, ok := profileMap[key]; ok {
				me.UtilizationPct = p.UtilizationPct
				me.TotalRequests = p.TotalRequests
				me.TotalTokens = p.TotalTokens
				me.AvgTokPerSec = p.AvgTokensPerSec
			}

			if me.UtilizationPct > 5 {
				me.Status = "active"
				exp.Summary.ActiveModels++
			} else if me.UtilizationPct > 0 {
				me.Status = "idle"
				exp.Summary.ActiveModels++
			} else {
				me.Status = "ghost"
				exp.Summary.GhostModels++
			}

			ie.Models = append(ie.Models, me)
			exp.Summary.TotalModels++
			exp.Summary.TotalVRAMGB += inst.TotalVRAMGB
			exp.Summary.UsedVRAMGB += inst.UsedVRAMGB
		}
		exp.Instances = append(exp.Instances, ie)
	}

	return exp
}

// WriteJSON writes the export as JSON to a file or stdout.
func WriteJSON(exp *FleetExport, path string) error {
	data, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	if path == "" || path == "-" {
		fmt.Println(string(data))
		return nil
	}
	return os.WriteFile(path, data, 0644)
}

// RenderPrometheus writes Prometheus-format metrics.
func RenderPrometheus(exp *FleetExport) string {
	var b strings.Builder

	b.WriteString("# HELP guardian_instances_total Total number of Ollama instances\n")
	b.WriteString("# TYPE guardian_instances_total gauge\n")
	fmt.Fprintf(&b, "guardian_instances_total %d\n", exp.Summary.TotalInstances)

	b.WriteString("\n# HELP guardian_models_total Total loaded models\n")
	b.WriteString("# TYPE guardian_models_total gauge\n")
	fmt.Fprintf(&b, "guardian_models_total %d\n", exp.Summary.TotalModels)

	b.WriteString("\n# HELP guardian_models_active Active models (utilization > 5%)\n")
	b.WriteString("# TYPE guardian_models_active gauge\n")
	fmt.Fprintf(&b, "guardian_models_active %d\n", exp.Summary.ActiveModels)

	b.WriteString("\n# HELP guardian_models_ghost Ghost models (utilization < 5%)\n")
	b.WriteString("# TYPE guardian_models_ghost gauge\n")
	fmt.Fprintf(&b, "guardian_models_ghost %d\n", exp.Summary.GhostModels)

	b.WriteString("\n# HELP guardian_vram_total_bytes Total VRAM across fleet\n")
	b.WriteString("# TYPE guardian_vram_total_bytes gauge\n")
	fmt.Fprintf(&b, "guardian_vram_total_bytes %.0f\n", exp.Summary.TotalVRAMGB*1024*1024*1024)

	b.WriteString("\n# HELP guardian_vram_used_bytes Used VRAM across fleet\n")
	b.WriteString("# TYPE guardian_vram_used_bytes gauge\n")
	fmt.Fprintf(&b, "guardian_vram_used_bytes %.0f\n", exp.Summary.UsedVRAMGB*1024*1024*1024)

	b.WriteString("\n# HELP guardian_vram_wasted_bytes Wasted VRAM from detected inefficiencies\n")
	b.WriteString("# TYPE guardian_vram_wasted_bytes gauge\n")
	fmt.Fprintf(&b, "guardian_vram_wasted_bytes %.0f\n", exp.Summary.WastedVRAMGB*1024*1024*1024)

	for _, inst := range exp.Instances {
		safeHost := promSafe(inst.Host)
		fmt.Fprintf(&b, "\n# HELP guardian_instance_vram_used_bytes VRAM used by instance\n")
		fmt.Fprintf(&b, "# TYPE guardian_instance_vram_used_bytes gauge\n")
		fmt.Fprintf(&b, "guardian_instance_vram_used_bytes{host=%q} %.0f\n", safeHost, inst.UsedVRAMGB*1024*1024*1024)

		for _, m := range inst.Models {
			fmt.Fprintf(&b, "\n# HELP guardian_model_vram_bytes VRAM used by model\n")
			fmt.Fprintf(&b, "# TYPE guardian_model_vram_bytes gauge\n")
			fmt.Fprintf(&b, "guardian_model_vram_bytes{host=%q,model=%q,status=%q} %.0f\n",
				safeHost, promSafe(m.Name), m.Status, m.SizeGB*1024*1024*1024)

			fmt.Fprintf(&b, "guardian_model_utilization_pct{host=%q,model=%q} %.1f\n",
				safeHost, promSafe(m.Name), m.UtilizationPct)
		}
	}

	return b.String()
}

// RenderSlack formats a Slack-compatible webhook payload.
func RenderSlack(exp *FleetExport) string {
	if len(exp.Wastes) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🚨 *Guardian Alert* — %d issues detected\n", len(exp.Wastes))
	fmt.Fprintf(&b, "Fleet: %d instances, %d models (%d ghosts)\n",
		exp.Summary.TotalInstances, exp.Summary.TotalModels, exp.Summary.GhostModels)
	fmt.Fprintf(&b, "Potential savings: %.1fGB VRAM\n\n", exp.Summary.WastedVRAMGB)

	for _, w := range exp.Wastes {
		icon := "🟡"
		if w.Severity == "high" {
			icon = "🔴"
		}
		fmt.Fprintf(&b, "%s *%s* on %s — %s (%.1fGB)\n", icon, w.Model, w.Host, w.Severity, w.SavingsGB)
	}

	return b.String()
}

func wasteTypeName(t WasteType) string {
	switch t {
	case WasteIdleModel:
		return "idle_model"
	case WasteOversizedContext:
		return "oversized_context"
	case WasteDuplicateVariant:
		return "duplicate_variant"
	case WasteExpiredCache:
		return "expired_cache"
	default:
		return "unknown"
	}
}

func promSafe(s string) string {
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
