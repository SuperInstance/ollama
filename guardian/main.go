package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	flagFleetDir       string
	flagInterval       time.Duration
	flagOutput         string
	flagBudgetFile     string
	flagAPIHosts       string
	flagExportFmt      string
	flagExportFile     string
	flagAlertFile      string
	flagDataDir        string
	flagPromAddr       string
	flagAutoUnload     bool
	flagAutoUnloadIdle time.Duration
	flagVersion        bool
)

func init() {
	flag.StringVar(&flagFleetDir, "fleet", ".", "directory containing fleet JSON configs")
	flag.DurationVar(&flagInterval, "interval", 5*time.Minute, "polling interval for watch mode")
	flag.StringVar(&flagOutput, "output", "", "write report to file")
	flag.StringVar(&flagBudgetFile, "budget-file", "", "JSON file with budget definitions")
	flag.StringVar(&flagAPIHosts, "api", "", "comma-separated Ollama API base URLs")
	flag.StringVar(&flagExportFmt, "export", "", "export format: json, prometheus, slack")
	flag.StringVar(&flagExportFile, "export-file", "", "write export to file")
	flag.StringVar(&flagAlertFile, "alerts", "", "JSON file with alert rules")
	flag.StringVar(&flagDataDir, "data-dir", "", "persistence directory for state and history")
	flag.StringVar(&flagPromAddr, "prometheus-addr", "", "start Prometheus metrics HTTP server on address")
	flag.BoolVar(&flagAutoUnload, "auto-unload", false, "enable auto-unload of idle models (requires -api)")
	flag.DurationVar(&flagAutoUnloadIdle, "auto-unload-idle", 1*time.Hour, "idle threshold for auto-unload")
	flag.BoolVar(&flagVersion, "version", false, "print version and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Model Conservation Guardian v0.2.0 — track, budget, and optimize Ollama model usage\n\n")
		fmt.Fprintf(os.Stderr, "3 models loaded, 1 actually used. You're spending 67GB VRAM on ghosts.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  guardian [flags] <command>\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  report       Generate a conservation report\n")
		fmt.Fprintf(os.Stderr, "  budget       Check budgets against current usage\n")
		fmt.Fprintf(os.Stderr, "  watch        Continuously monitor and alert on waste\n")
		fmt.Fprintf(os.Stderr, "  export       Export fleet state in various formats\n")
		fmt.Fprintf(os.Stderr, "  trends       Analyze utilization trends over time\n")
		fmt.Fprintf(os.Stderr, "  auto-unload  Continuously unload idle models\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()

	if flagVersion {
		fmt.Println("guardian v0.2.0")
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(0)
	}

	switch args[0] {
	case "report":
		cmdReport()
	case "budget":
		cmdBudget()
	case "watch":
		cmdWatch()
	case "export":
		cmdExport()
	case "trends":
		cmdTrends()
	case "auto-unload":
		cmdAutoUnload()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		flag.Usage()
		os.Exit(1)
	}
}

func cmdReport() {
	fleet := mustLoadFleet()
	profiles := CollectProfiles(fleet)
	wastes := DetectWaste(profiles)
	r := NewReport(fleet, profiles, wastes)
	rpt := r.Generate()

	if flagExportFmt != "" {
		doExport(fleet, profiles, wastes)
	}

	if flagOutput != "" {
		writeFile(flagOutput, rpt)
		fmt.Printf("Report written to %s\n", flagOutput)
	} else if flagExportFmt == "" {
		fmt.Println(rpt)
	}

	maybePersist(fleet, profiles)
}

func cmdBudget() {
	if flagBudgetFile == "" {
		fmt.Fprintf(os.Stderr, "Error: -budget-file required\n")
		os.Exit(1)
	}
	fleet := mustLoadFleet()
	budgets, err := LoadBudgets(flagBudgetFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading budgets: %v\n", err)
		os.Exit(1)
	}
	violations := CheckBudgets(fleet, budgets)
	if len(violations) == 0 {
		fmt.Println("✅ All models within budget.")
		return
	}
	for _, v := range violations {
		fmt.Printf("🚫 %s\n", v)
	}
	os.Exit(1)
}

func cmdWatch() {
	// Start Prometheus endpoint if configured
	if flagPromAddr != "" {
		go startPromEndpoint()
	}

	// Load alert manager
	var alertMgr *AlertManager
	if flagAlertFile != "" {
		var err error
		alertMgr, err = LoadAlertConfig(flagAlertFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading alerts: %v\n", err)
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("👁️  Guardian watching fleet (interval: %s)\n", flagInterval)
	fmt.Println("Press Ctrl+C to stop.")

	// Initial check
	watchCycle(alertMgr)

	ticker := time.NewTicker(flagInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n🛑 Guardian shutting down gracefully.")
			return
		case <-ticker.C:
			watchCycle(alertMgr)
		}
	}
}

func cmdExport() {
	fleet := mustLoadFleet()
	profiles := CollectProfiles(fleet)
	wastes := DetectWaste(profiles)
	doExport(fleet, profiles, wastes)
}

func cmdTrends() {
	if flagDataDir == "" {
		fmt.Fprintf(os.Stderr, "Error: -data-dir required for trend analysis\n")
		os.Exit(1)
	}

	p, err := NewPersistence(flagDataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	history, err := p.LoadFleetHistory(7 * 24 * time.Hour)
	if err != nil || len(history) < 2 {
		fmt.Fprintf(os.Stderr, "Not enough historical data for trend analysis (need ≥2 snapshots)\n")
		os.Exit(1)
	}

	current := mustLoadFleet()
	profiles := CollectProfiles(current)
	ta := NewTrendAnalysis(current, history, profiles)
	trends := ta.Analyze()
	fmt.Println(FormatTrends(trends))
}

func cmdAutoUnload() {
	if flagAPIHosts == "" {
		fmt.Fprintf(os.Stderr, "Error: -api required for auto-unload\n")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool := buildClientPool()
	fmt.Printf("🗑️  Auto-unload watching (idle threshold: %s, interval: %s)\n", flagAutoUnloadIdle, flagInterval)
	fmt.Println("Press Ctrl+C to stop.")

	ticker := time.NewTicker(flagInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n🛑 Auto-unload shutting down.")
			return
		case <-ticker.C:
			autoUnloadCycle(ctx, pool)
		}
	}
}

// --- Helpers ---

func mustLoadFleet() *Fleet {
	if flagAPIHosts != "" {
		pool := buildClientPool()
		fleet, err := FetchFleetFromClients(context.Background(), pool)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching fleet from API: %v\n", err)
			os.Exit(1)
		}
		return fleet
	}
	fleet, err := LoadFleet(flagFleetDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading fleet: %v\n", err)
		os.Exit(1)
	}
	return fleet
}

func buildClientPool() *ClientPool {
	pool := NewClientPool()
	for _, h := range strings.Split(flagAPIHosts, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			pool.Get(h)
		}
	}
	return pool
}

func doExport(fleet *Fleet, profiles []Profile, wastes []Waste) {
	exp := BuildExport(fleet, profiles, wastes)
	switch ExportFormat(flagExportFmt) {
	case FormatJSON:
		if err := WriteJSON(exp, flagExportFile); err != nil {
			fmt.Fprintf(os.Stderr, "Error exporting JSON: %v\n", err)
			os.Exit(1)
		}
	case FormatPrometheus:
		out := RenderPrometheus(exp)
		if flagExportFile == "" {
			fmt.Println(out)
		} else {
			writeFile(flagExportFile, out)
		}
	case FormatSlack:
		msg := RenderSlack(exp)
		if msg == "" {
			fmt.Println("No issues to alert on.")
			return
		}
		if flagExportFile == "" {
			fmt.Println(msg)
		} else {
			writeFile(flagExportFile, msg)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown export format: %s (use: json, prometheus, slack)\n", flagExportFmt)
		os.Exit(1)
	}
}

func maybePersist(fleet *Fleet, profiles []Profile) {
	if flagDataDir == "" {
		return
	}
	p, err := NewPersistence(flagDataDir)
	if err != nil {
		log.Printf("warning: persistence init failed: %v", err)
		return
	}
	if err := p.SaveFleetSnapshot(fleet); err != nil {
		log.Printf("warning: failed to save fleet snapshot: %v", err)
	}
	if err := p.SaveProfileSnapshot(profiles); err != nil {
		log.Printf("warning: failed to save profile snapshot: %v", err)
	}
}

func watchCycle(alertMgr *AlertManager) {
	fleet := mustLoadFleet()
	profiles := CollectProfiles(fleet)
	wastes := DetectWaste(profiles)

	if len(wastes) == 0 {
		fmt.Printf("[%s] ✅ All clear — %d models, no waste detected\n",
			time.Now().Format("15:04:05"), len(profiles))
	} else {
		fmt.Printf("[%s] 🚨 %d waste issues detected:\n",
			time.Now().Format("15:04:05"), len(wastes))
		for _, w := range wastes {
			fmt.Printf("  %s\n", w.String())
		}
	}

	if alertMgr != nil {
		alerts := alertMgr.Evaluate(fleet, profiles, wastes)
		if len(alerts) > 0 {
			fmt.Printf("[%s] 📢 %d alerts fired\n", time.Now().Format("15:04:05"), len(alerts))
		}
	}

	maybePersist(fleet, profiles)
}

func startPromEndpoint() {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fleet := mustLoadFleet()
		profiles := CollectProfiles(fleet)
		wastes := DetectWaste(profiles)
		exp := BuildExport(fleet, profiles, wastes)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte(RenderPrometheus(exp)))
	})
	log.Printf("Prometheus metrics endpoint on %s/metrics", flagPromAddr)
	log.Fatal(http.ListenAndServe(flagPromAddr, nil))
}

func autoUnloadCycle(ctx context.Context, pool *ClientPool) {
	for _, client := range pool.All() {
		running, err := client.RunningModels(ctx)
		if err != nil {
			log.Printf("warning: %s: %v", client.baseURL, err)
			continue
		}
		for _, rm := range running {
			name := rm.Name
			if name == "" {
				name = rm.Model
			}
			sizeGB := float64(rm.SizeVRAM) / (1024 * 1024 * 1024)
			if sizeGB == 0 {
				sizeGB = float64(rm.Size) / (1024 * 1024 * 1024)
			}
			log.Printf("auto-unload: unloading %s (%.1fGB) from %s", name, sizeGB, client.baseURL)
			if err := client.UnloadModel(ctx, name); err != nil {
				log.Printf("auto-unload: failed to unload %s: %v", name, err)
			}
		}
	}
}

func writeFile(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", path, err)
		os.Exit(1)
	}
}
