package main

import (
	"fmt"
	"time"
)

// RunWatch continuously monitors for waste and alerts.
func RunWatch(fleetDir string, interval time.Duration) error {
	fmt.Printf("👁️  Guardian watching fleet at %s (interval: %s)\n", fleetDir, interval)
	fmt.Println("Press Ctrl+C to stop.")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial check
	if err := checkAndAlert(fleetDir); err != nil {
		fmt.Printf("Error: %v\n", err)
	}

	for range ticker.C {
		if err := checkAndAlert(fleetDir); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}

	return nil
}

func checkAndAlert(fleetDir string) error {
	fleet, err := LoadFleet(fleetDir)
	if err != nil {
		return err
	}
	profiles := CollectProfiles(fleet)
	wastes := DetectWaste(profiles)

	if len(wastes) == 0 {
		fmt.Printf("[%s] ✅ All clear — %d models, no waste detected\n",
			time.Now().Format("15:04:05"), len(profiles))
		return nil
	}

	fmt.Printf("[%s] 🚨 %d waste issues detected:\n",
		time.Now().Format("15:04:05"), len(wastes))
	for _, w := range wastes {
		fmt.Printf("  %s\n", w.String())
	}
	return nil
}
