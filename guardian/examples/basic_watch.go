//go:build ignore

// Example: basic_watch.go
//
// Simple fleet monitoring — connect to one or more Ollama instances,
// print a conservation report, and exit.
//
// Run:
//   go run examples/basic_watch.go http://localhost:11434
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: basic_watch <ollama_url> [ollama_url2] ...\n")
		fmt.Fprintf(os.Stderr, "Example: basic_watch http://localhost:11434\n")
		os.Exit(1)
	}

	pool := NewClientPool()
	for _, url := range os.Args[1:] {
		pool.Get(url)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fleet, err := FetchFleetFromClients(ctx, pool)
	if err != nil {
		log.Fatalf("Failed to fetch fleet: %v", err)
	}

	profiles := CollectProfiles(fleet)
	wastes := DetectWaste(profiles)

	r := NewReport(fleet, profiles, wastes)
	fmt.Println(r.Generate())

	if len(wastes) > 0 {
		fmt.Printf("\n⚠️  Found %d issues — consider running auto_unload.go\n", len(wastes))
	}
}
