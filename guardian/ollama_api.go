package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// OllamaAPIModel represents a model from the Ollama /api/ps endpoint.
type OllamaAPIModel struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Size       int64  `json:"size"`
	SizeVRAM   int64  `json:"size_vram"`
	Digest     string `json:"digest"`
	Details    struct {
		ParentModel       string `json:"parent_model"`
		Format            string `json:"format"`
		Family            string `json:"family"`
		Families          string `json:"families"`
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OllamaPSResponse is the response from GET /api/ps.
type OllamaPSResponse struct {
	Models []OllamaAPIModel `json:"models"`
}

// FetchFleetFromAPI queries one or more Ollama instances via GET /api/ps
// and returns a Fleet snapshot. Instances that cannot be reached are logged
// and skipped (the JSON file loader can be used as fallback).
func FetchFleetFromAPI(baseURLs []string) (*Fleet, error) {
	fleet := &Fleet{
		SampledAt: time.Now(),
	}

	client := &http.Client{Timeout: 10 * time.Second}

	for _, baseURL := range baseURLs {
		url := baseURL + "/api/ps"
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("warning: failed to reach Ollama API at %s: %v", url, err)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("warning: failed to read response from %s: %v", url, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			log.Printf("warning: Ollama API at %s returned status %d: %s", url, resp.StatusCode, string(body))
			continue
		}

		var psResp OllamaPSResponse
		if err := json.Unmarshal(body, &psResp); err != nil {
			log.Printf("warning: malformed JSON from %s: %v", url, err)
			continue
		}

		inst := InstanceState{
			Host: baseURL,
		}
		for _, m := range psResp.Models {
			name := m.Name
			if name == "" {
				name = m.Model
			}
			sizeGB := float64(m.SizeVRAM) / (1024 * 1024 * 1024)
			if sizeGB == 0 {
				sizeGB = float64(m.Size) / (1024 * 1024 * 1024)
			}
			inst.Models = append(inst.Models, LoadedModel{
				Name:         name,
				SizeGB:       sizeGB,
				LoadedAt:     time.Now(), // Ollama doesn't expose load time via /api/ps
				LastRequestAt: time.Now(),
			})
			inst.UsedVRAMGB += sizeGB
		}

		fleet.Instances = append(fleet.Instances, inst)
	}

	if len(fleet.Instances) == 0 && len(baseURLs) > 0 {
		return fleet, fmt.Errorf("failed to reach any of %d Ollama instances", len(baseURLs))
	}

	return fleet, nil
}
