package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// AlertRule defines a condition that triggers an alert.
type AlertRule struct {
	ID      string `json:"id"`
	Type    string `json:"type"`    // "idle_model", "vram_threshold", "model_count", "custom"
	Model   string `json:"model"`   // optional model filter (glob)
	Host    string `json:"host"`    // optional host filter
	Threshold float64 `json:"threshold"` // varies by type
	Duration  string  `json:"duration"`  // e.g. "1h", "30m"
	Message   string  `json:"message"`

	duration time.Duration // parsed
}

// Alert represents a fired alert.
type Alert struct {
	Rule      AlertRule `json:"rule"`
	FiredAt   time.Time `json:"fired_at"`
	Detail    string    `json:"detail"`
	Severity  string    `json:"severity"`
}

// AlertConfig holds all alert configuration.
type AlertConfig struct {
	Rules   []AlertRule `json:"rules"`
	Channels []string   `json:"channels"` // "stdout", "file:/path", "webhook:URL"
}

// AlertManager evaluates rules and dispatches alerts.
type AlertManager struct {
	mu      sync.Mutex
	config  AlertConfig
	history []Alert
}

// NewAlertManager creates an alert manager from config.
func NewAlertManager(cfg AlertConfig) (*AlertManager, error) {
	// Parse durations
	for i := range cfg.Rules {
		if cfg.Rules[i].Duration != "" {
			d, err := time.ParseDuration(cfg.Rules[i].Duration)
			if err != nil {
				return nil, fmt.Errorf("parse duration for rule %q: %w", cfg.Rules[i].ID, err)
			}
			cfg.Rules[i].duration = d
		}
	}
	return &AlertManager{config: cfg}, nil
}

// LoadAlertConfig reads alert configuration from a JSON file.
func LoadAlertConfig(path string) (*AlertManager, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read alert config: %w", err)
	}
	var cfg AlertConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse alert config: %w", err)
	}
	return NewAlertManager(cfg)
}

// Evaluate checks all rules against the current fleet state and profiles.
func (am *AlertManager) Evaluate(fleet *Fleet, profiles []Profile, wastes []Waste) []Alert {
	am.mu.Lock()
	defer am.mu.Unlock()

	var alerts []Alert

	for _, rule := range am.config.Rules {
		switch rule.Type {
		case "idle_model":
			alerts = append(alerts, am.checkIdleModel(rule, profiles)...)
		case "vram_threshold":
			alerts = append(alerts, am.checkVRAMThreshold(rule, fleet)...)
		case "model_count":
			alerts = append(alerts, am.checkModelCount(rule, fleet)...)
		}
	}

	// Also generate alerts from high-severity waste detections
	for _, w := range wastes {
		if w.Severity == "high" {
			alerts = append(alerts, Alert{
				Rule:     AlertRule{ID: "waste_detection", Type: "waste"},
				FiredAt:  time.Now(),
				Detail:   w.String(),
				Severity: "high",
			})
		}
	}

	// Dispatch to channels
	for _, a := range alerts {
		am.dispatch(a)
	}

	am.history = append(am.history, alerts...)
	return alerts
}

func (am *AlertManager) checkIdleModel(rule AlertRule, profiles []Profile) []Alert {
	var alerts []Alert
	for _, p := range profiles {
		if rule.Model != "" && !matchGlob(rule.Model, p.Model) {
			continue
		}
		if rule.Host != "" && rule.Host != p.Host {
			continue
		}
		threshold := rule.duration
		if threshold == 0 {
			threshold = 1 * time.Hour
		}
		if p.IdleDuration > threshold && p.UtilizationPct < 10 {
			alerts = append(alerts, Alert{
				Rule:     rule,
				FiredAt:  time.Now(),
				Detail:   fmt.Sprintf("%s on %s idle for %s (%.1f%% utilization)", p.Model, p.Host, FormatDuration(p.IdleDuration), p.UtilizationPct),
				Severity: "warning",
			})
		}
	}
	return alerts
}

func (am *AlertManager) checkVRAMThreshold(rule AlertRule, fleet *Fleet) []Alert {
	var alerts []Alert
	for _, inst := range fleet.Instances {
		if rule.Host != "" && rule.Host != inst.Host {
			continue
		}
		pct := float64(0)
		if inst.TotalVRAMGB > 0 {
			pct = inst.UsedVRAMGB / inst.TotalVRAMGB * 100
		}
		if pct > rule.Threshold {
			alerts = append(alerts, Alert{
				Rule:     rule,
				FiredAt:  time.Now(),
				Detail:   fmt.Sprintf("%s: VRAM at %.1f%% (%.1fGB / %.1fGB)", inst.Host, pct, inst.UsedVRAMGB, inst.TotalVRAMGB),
				Severity: "critical",
			})
		}
	}
	return alerts
}

func (am *AlertManager) checkModelCount(rule AlertRule, fleet *Fleet) []Alert {
	total := 0
	for _, inst := range fleet.Instances {
		total += len(inst.Models)
	}
	if rule.Threshold > 0 && float64(total) > rule.Threshold {
		return []Alert{{
			Rule:     rule,
			FiredAt:  time.Now(),
			Detail:   fmt.Sprintf("%d models loaded across fleet (threshold: %.0f)", total, rule.Threshold),
			Severity: "warning",
		}}
	}
	return nil
}

func (am *AlertManager) dispatch(a Alert) {
	msg := fmt.Sprintf("[%s] %s: %s", a.Severity, a.Rule.ID, a.Detail)
	for _, ch := range am.config.Channels {
		switch {
		case ch == "stdout":
			log.Printf("ALERT: %s", msg)
		case strings.HasPrefix(ch, "file:"):
			path := strings.TrimPrefix(ch, "file:")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				log.Printf("alert: failed to open file %s: %v", path, err)
				continue
			}
			fmt.Fprintf(f, "[%s] %s\n", a.FiredAt.Format(time.RFC3339), msg)
			f.Close()
		case strings.HasPrefix(ch, "webhook:"):
			url := strings.TrimPrefix(ch, "webhook:")
			go am.sendWebhook(url, a, msg)
		}
	}
}

func (am *AlertManager) sendWebhook(url string, a Alert, msg string) {
	payload := map[string]string{
		"text":    msg,
		"severity": a.Severity,
		"rule":    a.Rule.ID,
		"time":    a.FiredAt.Format(time.RFC3339),
	}
	data, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(data)))
	if err != nil {
		log.Printf("webhook alert failed: %v", err)
		return
	}
	resp.Body.Close()
}

// History returns all past alerts.
func (am *AlertManager) History() []Alert {
	am.mu.Lock()
	defer am.mu.Unlock()
	return append([]Alert{}, am.history...)
}

// matchGlob does simple glob matching (* only).
func matchGlob(pattern, s string) bool {
	if pattern == "*" || pattern == "" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	parts := strings.SplitN(pattern, "*", 2)
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	if len(parts) == 1 {
		return s == parts[0]
	}
	if parts[1] == "" {
		return true
	}
	return strings.HasSuffix(s, parts[1])
}
