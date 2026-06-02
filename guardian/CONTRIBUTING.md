# Contributing to Model Conservation Guardian

Thanks for your interest! Here's how to contribute.

## Development

```bash
cd guardian
go build -o guardian .
go test -v -race ./...
go vet ./...
```

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Add tests for new functionality
- Keep the zero-dependency philosophy — stdlib only
- Document exported functions and types

## Pull Requests

1. Fork and create a branch from `guardian`
2. Make your changes with tests
3. Ensure `go vet` and `go test -race` pass
4. Open a PR against the `guardian` branch

## Reporting Issues

- Include Go version, OS, and Ollama version
- Include the command you ran and the output
- Include any fleet JSON or config files (redact sensitive info)

## Architecture

- `main.go` — CLI entry point, command routing
- `ollama_client.go` — Production Ollama API client with pooling and retries
- `ollama_api.go` — Legacy API adapter (kept for backward compatibility)
- `persistence.go` — Fleet state and profile snapshot storage
- `export.go` — JSON, Prometheus, Slack export formats
- `alerting.go` — Rule engine and alert dispatch
- `trends.go` — Historical trend analysis
- `detector.go` — Waste detection engine
- `profiler.go` — Model profiling and metrics computation
- `report.go` — Human-readable report generation
- `budget.go` — Budget validation and fleet data types
- `watch.go` — Continuous monitoring loop

## License

Same as parent Ollama project.
