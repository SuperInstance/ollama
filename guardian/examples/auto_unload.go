//go:build ignore

// Example: auto_unload.go
//
// Periodically check for idle models and unload them to free VRAM.
// Uses the Ollama API to both monitor and unload models.
//
// Run:
//   go run examples/auto_unload.go -interval 10m -idle 1h http://localhost:11434
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	interval := flag.Duration("interval", 10*time.Minute, "check interval")
	idleThreshold := flag.Duration("idle", 1*time.Hour, "idle duration before unloading")
	flag.Parse()

	urls := flag.Args()
	if len(urls) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: auto_unload [flags] <ollama_url> [ollama_url2] ...\n")
		os.Exit(1)
	}

	pool := NewClientPool()
	for _, url := range urls {
		pool.Get(url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("🗑️  Auto-unload watching %d instance(s) every %s (idle threshold: %s)\n",
		len(urls), *interval, *idleThreshold)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nStopped.")
			return
		case <-ticker.C:
			unloadIdle(ctx, pool, *idleThreshold)
		}
	}
}

func unloadIdle(ctx context.Context, pool *ClientPool, threshold time.Duration) {
	for _, client := range pool.All() {
		running, err := client.RunningModels(ctx)
		if err != nil {
			log.Printf("warning: %s: %v", client.baseURL, err)
			continue
		}

		fmt.Printf("\n[%s] Checking %s — %d models loaded\n",
			time.Now().Format("15:04:05"), client.baseURL, len(running))

		for _, rm := range running {
			name := rm.Name
			if name == "" {
				name = rm.Model
			}
			sizeGB := float64(rm.SizeVRAM) / (1024 * 1024 * 1024)
			if sizeGB == 0 {
				sizeGB = float64(rm.Size) / (1024 * 1024 * 1024)
			}

			fmt.Printf("  • %s (%.1fGB)", name, sizeGB)

			// Simple heuristic: unload models larger than 10GB that have no active keep_alive
			if sizeGB > 10 && rm.ExpiresAt == "" {
				fmt.Printf(" → unloading (large model, no keep_alive)")
				if err := client.UnloadModel(ctx, name); err != nil {
					fmt.Printf(" FAILED: %v", err)
				}
			}
			fmt.Println()
		}
	}
}
