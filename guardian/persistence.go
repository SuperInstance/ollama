package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Persistence handles saving and loading fleet state and profiling history.
type Persistence struct {
	mu       sync.RWMutex
	baseDir  string
	stateDir string
	histDir  string
}

// NewPersistence creates a persistence manager rooted at baseDir.
// It creates the necessary subdirectories.
func NewPersistence(baseDir string) (*Persistence, error) {
	stateDir := filepath.Join(baseDir, "state")
	histDir := filepath.Join(baseDir, "history")
	for _, dir := range []string{stateDir, histDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return &Persistence{
		baseDir:  baseDir,
		stateDir: stateDir,
		histDir:  histDir,
	}, nil
}

// SaveFleetSnapshot persists the current fleet state with a timestamped filename.
func (p *Persistence) SaveFleetSnapshot(fleet *Fleet) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	ts := fleet.SampledAt.Format("2006-01-02_150405")
	path := filepath.Join(p.stateDir, fmt.Sprintf("fleet_%s.json", ts))
	data, err := json.MarshalIndent(fleet, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal fleet: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadLatestFleet loads the most recent fleet snapshot.
func (p *Persistence) LoadLatestFleet() (*Fleet, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	entries, err := os.ReadDir(p.stateDir)
	if err != nil {
		return nil, fmt.Errorf("read state dir: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no fleet snapshots found")
	}

	// Entries are sorted by name which contains timestamp; last is latest
	latest := entries[len(entries)-1]
	path := filepath.Join(p.stateDir, latest.Name())
	return loadFleetFromFile(path)
}

// LoadFleetHistory loads all fleet snapshots within the given duration from now.
func (p *Persistence) LoadFleetHistory(since time.Duration) ([]*Fleet, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cutoff := time.Now().Add(-since)
	entries, err := os.ReadDir(p.stateDir)
	if err != nil {
		return nil, fmt.Errorf("read state dir: %w", err)
	}

	var fleets []*Fleet
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(p.stateDir, e.Name())
		f, err := loadFleetFromFile(path)
		if err != nil {
			continue
		}
		fleets = append(fleets, f)
	}
	return fleets, nil
}

// SaveProfileSnapshot saves profiling data with a timestamp.
func (p *Persistence) SaveProfileSnapshot(profiles []Profile) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	ts := time.Now().Format("2006-01-02_150405")
	path := filepath.Join(p.histDir, fmt.Sprintf("profiles_%s.json", ts))
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profiles: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadProfileHistory loads profile snapshots within the given duration.
func (p *Persistence) LoadProfileHistory(since time.Duration) ([][]Profile, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cutoff := time.Now().Add(-since)
	entries, err := os.ReadDir(p.histDir)
	if err != nil {
		return nil, fmt.Errorf("read history dir: %w", err)
	}

	var result [][]Profile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(p.histDir, e.Name())
		var profiles []Profile
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &profiles); err != nil {
			continue
		}
		result = append(result, profiles)
	}
	return result, nil
}

// CleanupOldSnapshots removes snapshots older than maxAge.
func (p *Persistence) CleanupOldSnapshots(maxAge time.Duration) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	removed := 0

	for _, dir := range []string{p.stateDir, p.histDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(dir, e.Name()))
				removed++
			}
		}
	}
	return removed, nil
}

func loadFleetFromFile(path string) (*Fleet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f Fleet
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &f, nil
}
