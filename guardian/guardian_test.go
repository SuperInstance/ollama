package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// helper to create a temp fleet directory with instance data
func writeTestFleet(t *testing.T, instances []InstanceState) string {
	t.Helper()
	dir := t.TempDir()
	for i, inst := range instances {
		data, err := json.Marshal(inst)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("inst%d.json", i)), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func makeLoadedModel(name string, sizeGB float64, idleMinutes int, reqCount int, tokensToday int64) LoadedModel {
	now := time.Now()
	return LoadedModel{
		Name:            name,
		SizeGB:          sizeGB,
		ContextLength:   4096,
		LoadedAt:        now.Add(-6 * time.Hour),
		LastRequestAt:   now.Add(-time.Duration(idleMinutes) * time.Minute),
		RequestCount:    reqCount,
		TokensGenerated: int64(reqCount) * 500,
		TokensToday:     tokensToday,
		ConcurrentReqs:  0,
	}
}

func TestLoadFleet(t *testing.T) {
	instances := []InstanceState{
		{
			Host: "gpu01",
			Models: []LoadedModel{
				makeLoadedModel("llama3:70b", 40.0, 120, 3, 15000),
			},
			TotalVRAMGB: 80.0,
			UsedVRAMGB:  40.0,
		},
	}
	dir := writeTestFleet(t, instances)

	fleet, err := LoadFleet(dir)
	if err != nil {
		t.Fatalf("LoadFleet: %v", err)
	}
	if len(fleet.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(fleet.Instances))
	}
	if fleet.Instances[0].Host != "gpu01" {
		t.Errorf("expected host gpu01, got %s", fleet.Instances[0].Host)
	}
	if len(fleet.Instances[0].Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(fleet.Instances[0].Models))
	}
}

func TestLoadFleetEmptyDir(t *testing.T) {
	fleet, err := LoadFleet(t.TempDir())
	if err != nil {
		t.Fatalf("LoadFleet on empty dir: %v", err)
	}
	if len(fleet.Instances) != 0 {
		t.Errorf("expected 0 instances, got %d", len(fleet.Instances))
	}
}

// Bug fix #1: LoadFleet logs warnings for malformed JSON instead of silent continue
func TestLoadFleetMalformedJSONLogsWarning(t *testing.T) {
	dir := t.TempDir()
	// Write a valid instance
	validInst := InstanceState{Host: "gpu01", Models: []LoadedModel{{Name: "test"}}}
	validData, _ := json.Marshal(validInst)
	os.WriteFile(filepath.Join(dir, "valid.json"), validData, 0644)

	// Write a malformed JSON file
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid json!!!}"), 0644)

	// Write an unreadable file (well, we can't easily make unreadable in tempdir as root,
	// but malformed JSON test is the key one)
	os.WriteFile(filepath.Join(dir, "also-bad.json"), []byte("not json at all"), 0644)

	fleet, err := LoadFleet(dir)
	if err != nil {
		t.Fatalf("LoadFleet: %v", err)
	}
	// Should still load the valid one
	if len(fleet.Instances) != 1 {
		t.Errorf("expected 1 valid instance, got %d", len(fleet.Instances))
	}
	if fleet.Instances[0].Host != "gpu01" {
		t.Errorf("expected gpu01, got %s", fleet.Instances[0].Host)
	}
}

func TestCheckBudgets(t *testing.T) {
	fleet := &Fleet{
		Instances: []InstanceState{
			{
				Host: "gpu01",
				Models: []LoadedModel{
					{Name: "llama3:70b", SizeGB: 47.0, ConcurrentReqs: 2, TokensToday: 500_000},
					{Name: "mistral:7b", SizeGB: 4.0, ConcurrentReqs: 0, TokensToday: 100},
				},
			},
		},
	}

	budgets := &BudgetSet{
		GlobalMaxVRAMGB: 50.0,
		Models: []ModelBudget{
			{Model: "llama3:70b", MaxVRAMGB: 40.0, MaxConcurrentReqs: 1, MaxTokensPerDay: 100_000},
		},
	}

	violations := CheckBudgets(fleet, budgets)
	if len(violations) == 0 {
		t.Fatal("expected budget violations, got none")
	}

	foundVRAM, foundConcurrent, foundTokens, foundGlobal := false, false, false, false
	for _, v := range violations {
		if contains(v, "exceeds budget of 40") {
			foundVRAM = true
		}
		if contains(v, "concurrent requests exceeds") {
			foundConcurrent = true
		}
		if contains(v, "tokens today exceeds") {
			foundTokens = true
		}
		if contains(v, "Global") {
			foundGlobal = true
		}
	}
	if !foundVRAM {
		t.Error("expected VRAM budget violation")
	}
	if !foundConcurrent {
		t.Error("expected concurrent request violation")
	}
	if !foundTokens {
		t.Error("expected tokens/day violation")
	}
	if !foundGlobal {
		t.Error("expected global VRAM violation")
	}
}

func TestCheckBudgetsAllowedInstances(t *testing.T) {
	fleet := &Fleet{
		Instances: []InstanceState{
			{Host: "gpu01", Models: []LoadedModel{
				{Name: "secret-model", SizeGB: 10},
			}},
		},
	}

	budgets := &BudgetSet{
		Models: []ModelBudget{
			{Model: "secret-model", AllowedInstances: []string{"gpu99"}},
		},
	}

	violations := CheckBudgets(fleet, budgets)
	if len(violations) == 0 {
		t.Fatal("expected allowed-instance violation")
	}
}

func TestCheckBudgetsAllClear(t *testing.T) {
	fleet := &Fleet{
		Instances: []InstanceState{
			{Host: "gpu01", Models: []LoadedModel{
				{Name: "small:1b", SizeGB: 1.0, ConcurrentReqs: 1, TokensToday: 100},
			}},
		},
	}

	budgets := &BudgetSet{
		GlobalMaxVRAMGB: 100.0,
		Default:         ModelBudget{MaxVRAMGB: 10, MaxConcurrentReqs: 10, MaxTokensPerDay: 1_000_000},
	}

	violations := CheckBudgets(fleet, budgets)
	if len(violations) != 0 {
		t.Errorf("expected no violations, got %d: %v", len(violations), violations)
	}
}

func TestDetectWasteIdleModel(t *testing.T) {
	profiles := []Profile{
		{
			Model:          "llama3:70b",
			Host:           "gpu01",
			SizeGB:         47.0,
			UtilizationPct: 1.5,
			LoadDuration:   24 * time.Hour,
			IdleDuration:   2 * time.Hour,
		},
	}

	wastes := DetectWaste(profiles)
	if len(wastes) == 0 {
		t.Fatal("expected waste detection")
	}
	found := false
	for _, w := range wastes {
		if w.Type == WasteIdleModel {
			found = true
			if w.SavingsGB != 47.0 {
				t.Errorf("expected 47GB savings, got %.1f", w.SavingsGB)
			}
			if w.Severity != "high" {
				t.Errorf("expected high severity for <2%% utilization, got %s", w.Severity)
			}
		}
	}
	if !found {
		t.Error("expected WasteIdleModel")
	}
}

func TestDetectWasteOversizedContext(t *testing.T) {
	profiles := []Profile{
		{
			Model:          "codellama:34b",
			Host:           "gpu01",
			SizeGB:         20.0,
			ContextLength:  32768,
			TotalTokens:    10000,
			TotalRequests:  100,
			UtilizationPct: 50,
			LoadDuration:   1 * time.Hour,
		},
	}

	wastes := DetectWaste(profiles)
	found := false
	for _, w := range wastes {
		if w.Type == WasteOversizedContext {
			found = true
		}
	}
	if !found {
		t.Error("expected WasteOversizedContext detection")
	}
}

func TestDetectWasteDuplicateVariants(t *testing.T) {
	profiles := []Profile{
		{Model: "llama3:70b-q4", Host: "gpu01", SizeGB: 40.0, UtilizationPct: 50},
		{Model: "llama3:70b-q5", Host: "gpu02", SizeGB: 47.0, UtilizationPct: 10},
	}

	wastes := DetectWaste(profiles)
	found := false
	for _, w := range wastes {
		if w.Type == WasteDuplicateVariant {
			found = true
		}
	}
	if !found {
		t.Error("expected WasteDuplicateVariant detection")
	}
}

// Bug fix #5: Duplicate detection should NOT flag when all variants are active
func TestDetectWasteDuplicateVariantsAllActive(t *testing.T) {
	profiles := []Profile{
		{Model: "llama3:70b-q4", Host: "gpu01", SizeGB: 40.0, UtilizationPct: 80, IdleDuration: 1 * time.Minute},
		{Model: "llama3:70b-q5", Host: "gpu02", SizeGB: 47.0, UtilizationPct: 90, IdleDuration: 1 * time.Minute},
	}

	wastes := DetectWaste(profiles)
	for _, w := range wastes {
		if w.Type == WasteDuplicateVariant {
			t.Error("should not flag duplicate variants when all are actively used")
		}
	}
}

// Bug fix #5: Duplicate detection should flag when at least one is idle
func TestDetectWasteDuplicateVariantsOneIdle(t *testing.T) {
	profiles := []Profile{
		{Model: "llama3:70b-q4", Host: "gpu01", SizeGB: 40.0, UtilizationPct: 80, IdleDuration: 1 * time.Minute},
		{Model: "llama3:70b-q5", Host: "gpu02", SizeGB: 47.0, UtilizationPct: 5, IdleDuration: 2 * time.Hour},
	}

	wastes := DetectWaste(profiles)
	found := false
	for _, w := range wastes {
		if w.Type == WasteDuplicateVariant {
			found = true
		}
	}
	if !found {
		t.Error("expected WasteDuplicateVariant when one variant is idle/low-util")
	}
}

func TestDetectNoWaste(t *testing.T) {
	profiles := []Profile{
		{
			Model:          "mistral:7b",
			Host:           "gpu01",
			SizeGB:         4.0,
			UtilizationPct: 80,
			LoadDuration:   1 * time.Hour,
			IdleDuration:   1 * time.Minute,
		},
	}
	wastes := DetectWaste(profiles)
	if len(wastes) != 0 {
		t.Errorf("expected no waste, got %d", len(wastes))
	}
}

// Bug fix #2: Configurable IdleThreshold via WasteDetector
func TestWasteDetectorConfigurableIdleThreshold(t *testing.T) {
	cfg := WasteDetectorConfig{
		IdleThreshold:        5 * time.Minute,
		ContextOversizeRatio: 4.0,
	}
	detector := NewWasteDetector(cfg)

	profiles := []Profile{
		{
			Model:          "llama3:70b", Host: "gpu01", SizeGB: 47.0,
			UtilizationPct: 5, LoadDuration: 1 * time.Hour, IdleDuration: 10 * time.Minute,
		},
	}

	wastes := detector.Detect(profiles)
	found := false
	for _, w := range wastes {
		if w.Type == WasteIdleModel {
			found = true
		}
	}
	if !found {
		t.Error("expected idle detection with 5min threshold for 10min idle model")
	}

	// Now with default 30min threshold — should NOT flag 10min idle
	defaultDetector := NewWasteDetector(DefaultWasteDetectorConfig())
	wastes2 := defaultDetector.Detect(profiles)
	for _, w := range wastes2 {
		if w.Type == WasteIdleModel {
			t.Error("should not flag 10min idle with 30min default threshold")
		}
	}
}

// Bug fix #3: Configurable ContextOversizeRatio
func TestWasteDetectorConfigurableContextRatio(t *testing.T) {
	cfg := WasteDetectorConfig{
		IdleThreshold:        30 * time.Minute,
		ContextOversizeRatio: 2.0, // tighter ratio
	}
	detector := NewWasteDetector(cfg)

	profiles := []Profile{
		{
			Model: "codellama:34b", Host: "gpu01", SizeGB: 20.0,
			ContextLength: 32768, TotalTokens: 50000, TotalRequests: 100,
			UtilizationPct: 50, LoadDuration: 1 * time.Hour, IdleDuration: 1 * time.Minute,
		},
	}
	// avg 500 tokens/req, context 32768, ratio = 32768/500 = 65.5x — way over 2.0 threshold
	wastes := detector.Detect(profiles)
	found := false
	for _, w := range wastes {
		if w.Type == WasteOversizedContext {
			found = true
		}
	}
	if !found {
		t.Error("expected context waste with tighter ratio")
	}
}

// Bug fix #4: Utilization is marked as estimated
func TestProfilerUtilizationMarkedAsEstimated(t *testing.T) {
	now := time.Now()
	fleet := &Fleet{
		Instances: []InstanceState{
			{
				Host: "gpu01",
				Models: []LoadedModel{
					{
						Name: "llama3:70b", SizeGB: 40.0,
						LoadedAt:      now.Add(-2 * time.Hour),
						LastRequestAt: now.Add(-5 * time.Minute),
						RequestCount:  10,
					},
				},
			},
		},
	}
	profiles := CollectProfiles(fleet)
	if len(profiles) == 0 {
		t.Fatal("expected profiles")
	}
	if !profiles[0].UtilizationIsEst {
		t.Error("expected UtilizationIsEst to be true when using estimated request duration")
	}
	if profiles[0].UtilizationPct <= 0 {
		t.Error("expected non-zero utilization")
	}
}

// Bug fix #4: Configurable estimated request duration
func TestProfilerConfigurableRequestDuration(t *testing.T) {
	now := time.Now()
	fleet := &Fleet{
		Instances: []InstanceState{
			{
				Host: "gpu01",
				Models: []LoadedModel{
					{
						Name: "llama3:70b", SizeGB: 40.0,
						LoadedAt:      now.Add(-1 * time.Hour),
						LastRequestAt: now,
						RequestCount:  30,
					},
				},
			},
		},
	}

	// With 60s estimate: 30 reqs * 60s = 1800s = 30min active out of 60min = 50%
	cfg60s := ModelProfilerConfig{EstimatedRequestDuration: 60 * time.Second}
	profiles60 := CollectProfilesWithConfig(fleet, cfg60s)

	// With 30s estimate: 30 reqs * 30s = 900s = 15min active out of 60min = 25%
	cfg30s := ModelProfilerConfig{EstimatedRequestDuration: 30 * time.Second}
	profiles30 := CollectProfilesWithConfig(fleet, cfg30s)

	if len(profiles60) == 0 || len(profiles30) == 0 {
		t.Fatal("expected profiles")
	}

	if profiles60[0].UtilizationPct <= profiles30[0].UtilizationPct {
		t.Errorf("expected higher utilization with longer estimate: 60s=%.1f%% 30s=%.1f%%",
			profiles60[0].UtilizationPct, profiles30[0].UtilizationPct)
	}
}

// Bug fix #6: RunWatch respects context cancellation
func TestRunWatchGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	// Write empty fleet so checkAndAlert succeeds
	os.WriteFile(filepath.Join(dir, "empty.json"), []byte(`{"host":"test"}`), 0644)

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	var watchErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		watchErr = RunWatch(ctx, dir, 100*time.Millisecond)
	}()

	// Let it tick once
	time.Sleep(250 * time.Millisecond)
	cancel()
	wg.Wait()

	if watchErr != nil {
		t.Errorf("RunWatch returned error: %v", watchErr)
	}
}

// Bug fix #7: FetchFleetFromAPI works with mock Ollama server
func TestFetchFleetFromAPI(t *testing.T) {
	psResponse := OllamaPSResponse{
		Models: []OllamaAPIModel{
			{
				Name:     "llama3:70b",
				SizeVRAM: 40 * 1024 * 1024 * 1024, // 40GB
			},
			{
				Name: "mistral:7b",
				Size: 4 * 1024 * 1024 * 1024, // 4GB (only Size, not SizeVRAM)
			},
		},
	}
	respBody, _ := json.Marshal(psResponse)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("expected /api/ps, got %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBody)
	}))
	defer server.Close()

	fleet, err := FetchFleetFromAPI([]string{server.URL})
	if err != nil {
		t.Fatalf("FetchFleetFromAPI: %v", err)
	}
	if len(fleet.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(fleet.Instances))
	}
	inst := fleet.Instances[0]
	if len(inst.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(inst.Models))
	}
	if inst.Models[0].Name != "llama3:70b" {
		t.Errorf("expected llama3:70b, got %s", inst.Models[0].Name)
	}
	if inst.Models[0].SizeGB < 39.9 || inst.Models[0].SizeGB > 40.1 {
		t.Errorf("expected ~40GB, got %.1f", inst.Models[0].SizeGB)
	}
	if inst.Models[1].SizeGB < 3.9 || inst.Models[1].SizeGB > 4.1 {
		t.Errorf("expected ~4GB, got %.1f", inst.Models[1].SizeGB)
	}
}

// Bug fix #7: FetchFleetFromAPI handles unreachable instances gracefully
func TestFetchFleetFromAPIUnreachable(t *testing.T) {
	_, err := FetchFleetFromAPI([]string{"http://127.0.0.1:1"})
	if err == nil {
		t.Error("expected error when all instances unreachable")
	}
}

// Bug fix #7: FetchFleetFromAPI skips bad instances but returns good ones
func TestFetchFleetFromAPIMixed(t *testing.T) {
	psResponse := OllamaPSResponse{
		Models: []OllamaAPIModel{{Name: "test-model"}},
	}
	respBody, _ := json.Marshal(psResponse)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(respBody)
	}))
	defer server.Close()

	fleet, err := FetchFleetFromAPI([]string{"http://127.0.0.1:1", server.URL})
	if err != nil {
		t.Fatalf("expected success with partial reachability: %v", err)
	}
	if len(fleet.Instances) != 1 {
		t.Errorf("expected 1 instance (from good server), got %d", len(fleet.Instances))
	}
}

func TestReportGeneration(t *testing.T) {
	fleet := &Fleet{
		Instances: []InstanceState{
			{
				Host:        "gpu01",
				TotalVRAMGB: 80.0,
				UsedVRAMGB:  51.0,
				Models: []LoadedModel{
					makeLoadedModel("llama3:70b", 47.0, 120, 3, 15000),
					makeLoadedModel("mistral:7b", 4.0, 1, 500, 200000),
				},
			},
		},
	}

	profiles := CollectProfiles(fleet)
	wastes := DetectWaste(profiles)
	report := NewReport(fleet, profiles, wastes)
	output := report.Generate()

	if len(output) == 0 {
		t.Fatal("expected non-empty report")
	}
	if !contains(output, "CONSERVATION") {
		t.Error("report missing CONSERVATION header")
	}
	if !contains(output, "Fleet Overview") {
		t.Error("report missing Fleet Overview")
	}
	if !contains(output, "Model Details") {
		t.Error("report missing Model Details")
	}
	if !contains(output, "Waste Detection") {
		t.Error("report missing Waste Detection")
	}
	if !contains(output, "Recommendations") {
		t.Error("report missing Recommendations")
	}
}

func TestReportNoWaste(t *testing.T) {
	fleet := &Fleet{
		Instances: []InstanceState{
			{Host: "gpu01", TotalVRAMGB: 24.0, UsedVRAMGB: 4.0, Models: []LoadedModel{
				{Name: "mistral:7b", SizeGB: 4.0},
			}},
		},
	}
	profiles := CollectProfiles(fleet)
	r := NewReport(fleet, profiles, nil)
	output := r.Generate()
	if !contains(output, "No waste detected") {
		t.Error("expected 'No waste detected' in clean report")
	}
}

// Bug fix #4: Report marks utilization as estimated
func TestReportShowsEstimatedLabel(t *testing.T) {
	now := time.Now()
	fleet := &Fleet{
		Instances: []InstanceState{
			{
				Host: "gpu01", TotalVRAMGB: 80.0, UsedVRAMGB: 40.0,
				Models: []LoadedModel{
					{
						Name: "llama3:70b", SizeGB: 40.0,
						LoadedAt: now.Add(-1 * time.Hour), LastRequestAt: now, RequestCount: 10,
					},
				},
			},
		},
	}
	profiles := CollectProfiles(fleet)
	r := NewReport(fleet, profiles, nil)
	output := r.Generate()
	if !contains(output, "estimated") {
		t.Error("report should show '(estimated)' label for utilization")
	}
}

func TestSummarizeWaste(t *testing.T) {
	wastes := []Waste{
		{SavingsGB: 47.0, Severity: "high"},
		{SavingsGB: 10.0, Severity: "medium"},
		{SavingsGB: 3.0, Severity: "low"},
	}
	total, high := SummarizeWaste(wastes)
	if total != 60.0 {
		t.Errorf("expected 60GB total, got %.1f", total)
	}
	if high != 1 {
		t.Errorf("expected 1 high, got %d", high)
	}
}

func TestStripQuant(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"llama3:70b-q4_0", "llama3:70b"},
		{"llama3:70b-q5_K_M", "llama3:70b"},
		{"llama3:70b-fp16", "llama3:70b"},
		{"mistral:7b", "mistral:7b"},
		{"codellama", "codellama"},
	}
	for _, tt := range tests {
		got := stripQuant(tt.input)
		if got != tt.expected {
			t.Errorf("stripQuant(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestFormatTokensPerSec(t *testing.T) {
	if FormatTokensPerSec(0) != "N/A" {
		t.Error("expected N/A for zero")
	}
	if FormatTokensPerSec(42.5) != "42.5 tok/s" {
		t.Errorf("unexpected format: %s", FormatTokensPerSec(42.5))
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m"},
		{2 * time.Hour, "2.0h"},
		{48 * time.Hour, "2.0d"},
	}
	for _, tt := range tests {
		got := FormatDuration(tt.d)
		if got != tt.expected {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.expected)
		}
	}
}

func TestContextEfficiency(t *testing.T) {
	eff := ContextEfficiency(4096, 1024)
	if eff < 24.0 || eff > 26.0 {
		t.Errorf("expected ~25%% efficiency, got %.1f%%", eff)
	}
}

func TestLoadBudgets(t *testing.T) {
	dir := t.TempDir()
	budgetData := `{
		"global_max_vram_gb": 200,
		"default": {"model": "default", "max_vram_gb": 10, "max_concurrent_requests": 5, "max_tokens_per_day": 1000000},
		"models": [
			{"model": "llama3:70b", "max_vram_gb": 50, "max_concurrent_requests": 2, "max_tokens_per_day": 500000}
		]
	}`
	path := filepath.Join(dir, "budgets.json")
	if err := os.WriteFile(path, []byte(budgetData), 0644); err != nil {
		t.Fatal(err)
	}

	bs, err := LoadBudgets(path)
	if err != nil {
		t.Fatalf("LoadBudgets: %v", err)
	}
	if bs.GlobalMaxVRAMGB != 200 {
		t.Errorf("expected global 200, got %.0f", bs.GlobalMaxVRAMGB)
	}
	if len(bs.Models) != 1 {
		t.Errorf("expected 1 model budget, got %d", len(bs.Models))
	}
	if bs.Models[0].MaxConcurrentReqs != 2 {
		t.Errorf("expected 2 concurrent, got %d", bs.Models[0].MaxConcurrentReqs)
	}
}

func TestLoadBudgetsBadFile(t *testing.T) {
	_, err := LoadBudgets("/nonexistent/path.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
