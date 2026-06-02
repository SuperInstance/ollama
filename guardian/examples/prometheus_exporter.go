//go:build ignore

// Example: prometheus_exporter.go
//
// Standalone Prometheus metrics exporter for Ollama fleet monitoring.
// Exposes /metrics endpoint for Prometheus scraping.
//
// Run:
//   go run examples/prometheus_exporter.go -addr :9090 http://localhost:11434
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", ":9090", "HTTP listen address")
	flag.Parse()

	urls := flag.Args()
	if len(urls) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: prometheus_exporter [flags] <ollama_url> [ollama_url2] ...\n")
		os.Exit(1)
	}

	pool := NewClientPool()
	for _, url := range urls {
		pool.Get(url)
	}

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		fleet, err := FetchFleetFromClients(ctx, pool)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		profiles := CollectProfiles(fleet)
		wastes := DetectWaste(profiles)
		exp := BuildExport(fleet, profiles, wastes)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte(ExportPrometheus(exp)))
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	fmt.Printf("🚀 Prometheus exporter listening on %s\n", *addr)
	fmt.Printf("   Metrics: http://%s/metrics\n", *addr)
	fmt.Printf("   Health:  http://%s/health\n", *addr)
	fmt.Printf("   Scraping: %s\n", strings.Join(urls, ", "))

	log.Fatal(http.ListenAndServe(*addr, nil))
}
