package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	fleetDir := flag.String("fleet", ".", "directory containing fleet JSON configs")
	interval := flag.Duration("interval", 5*time.Minute, "polling interval for watch mode")
	reportFile := flag.String("output", "", "write report to file")
	budgetFile := flag.String("budget-file", "", "JSON file with budget definitions")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Model Conservation Guardian — track, budget, and optimize Ollama model usage\n\n")
		fmt.Fprintf(os.Stderr, "3 models loaded, 1 actually used. You're spending 67GB VRAM on ghosts.\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  guardian [flags] <command>\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  report   Generate a conservation report\n")
		fmt.Fprintf(os.Stderr, "  budget   Check budgets against current usage\n")
		fmt.Fprintf(os.Stderr, "  watch    Continuously monitor and alert on waste\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(0)
	}

	switch args[0] {
	case "report":
		runReport(*fleetDir, *reportFile)
	case "budget":
		runBudget(*fleetDir, *budgetFile)
	case "watch":
		runWatch(*fleetDir, *interval)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		flag.Usage()
		os.Exit(1)
	}
}

func runReport(fleetDir, reportFile string) {
	fleet, err := LoadFleet(fleetDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading fleet: %v\n", err)
		os.Exit(1)
	}
	profiles := CollectProfiles(fleet)
	wastes := DetectWaste(profiles)
	r := NewReport(fleet, profiles, wastes)
	rpt := r.Generate()
	if reportFile != "" {
		if err := os.WriteFile(reportFile, []byte(rpt), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Report written to %s\n", reportFile)
	} else {
		fmt.Println(rpt)
	}
}

func runBudget(fleetDir, budgetFile string) {
	if budgetFile == "" {
		fmt.Fprintf(os.Stderr, "Error: -budget-file required\n")
		os.Exit(1)
	}
	fleet, err := LoadFleet(fleetDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading fleet: %v\n", err)
		os.Exit(1)
	}
	budgets, err := LoadBudgets(budgetFile)
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

func runWatch(fleetDir string, interval time.Duration) {
	if err := RunWatch(fleetDir, interval); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
