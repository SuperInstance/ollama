package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// OllamaClient is a production HTTP client for the Ollama API with connection
// pooling, retries, and timeout management.
type OllamaClient struct {
	baseURL    string
	httpClient *http.Client
	maxRetries int
	retryWait  time.Duration
}

// ClientPool manages a set of OllamaClients, one per instance.
type ClientPool struct {
	mu      sync.RWMutex
	clients map[string]*OllamaClient
}

// NewClientPool creates an empty client pool.
func NewClientPool() *ClientPool {
	return &ClientPool{clients: make(map[string]*OllamaClient)}
}

// Get returns a client for the given base URL, creating one if needed.
func (p *ClientPool) Get(baseURL string) *OllamaClient {
	p.mu.RLock()
	c, ok := p.clients[baseURL]
	p.mu.RUnlock()
	if ok {
		return c
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// double-check
	if c, ok = p.clients[baseURL]; ok {
		return c
	}
	c = NewOllamaClient(baseURL)
	p.clients[baseURL] = c
	return c
}

// All returns all registered clients.
func (p *ClientPool) All() []*OllamaClient {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*OllamaClient, 0, len(p.clients))
	for _, c := range p.clients {
		out = append(out, c)
	}
	return out
}

// NewOllamaClient creates a production-grade client with connection pooling.
func NewOllamaClient(baseURL string) *OllamaClient {
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	}
	return &OllamaClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		maxRetries: 3,
		retryWait:  500 * time.Millisecond,
	}
}

// SetTimeout configures the HTTP client timeout.
func (c *OllamaClient) SetTimeout(d time.Duration) {
	c.httpClient.Timeout = d
}

// SetRetries configures retry count and backoff.
func (c *OllamaClient) SetRetries(maxRetries int, wait time.Duration) {
	c.maxRetries = maxRetries
	c.retryWait = wait
}

// doWithRetry executes an HTTP request with exponential backoff retries.
func (c *OllamaClient) doWithRetry(ctx context.Context, method, path string, body interface{}) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(c.retryWait * time.Duration(attempt)):
			}
			log.Printf("retry %d/%d for %s %s", attempt, c.maxRetries, method, path)
		}

		var reader io.Reader
		if reqBody != nil {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
		if err != nil {
			return nil, 0, fmt.Errorf("create request: %w", err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read response: %w", err)
			continue
		}
		return respBody, resp.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("after %d retries: %w", c.maxRetries, lastErr)
}

// --- GET /api/tags ---

// ModelTag represents a model from GET /api/tags.
type ModelTag struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	ModifiedAt string `json:"modified_at"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
	Details    struct {
		ParentModel       string `json:"parent_model"`
		Format            string `json:"format"`
		Family            string `json:"family"`
		Families          string `json:"families"`
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
}

// TagsResponse is the response from GET /api/tags.
type TagsResponse struct {
	Models []ModelTag `json:"models"`
}

// ListModels returns all available models via GET /api/tags.
func (c *OllamaClient) ListModels(ctx context.Context) ([]ModelTag, error) {
	body, status, err := c.doWithRetry(ctx, "GET", "/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list models: status %d: %s", status, string(body))
	}
	var resp TagsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal tags response: %w", err)
	}
	return resp.Models, nil
}

// --- GET /api/ps ---

// RunningModel represents a currently loaded model from GET /api/ps.
type RunningModel struct {
	Name     string `json:"name"`
	Model    string `json:"model"`
	Size     int64  `json:"size"`
	SizeVRAM int64  `json:"size_vram"`
	Digest   string `json:"digest"`
	Details  struct {
		ParentModel       string `json:"parent_model"`
		Format            string `json:"format"`
		Family            string `json:"family"`
		Families          string `json:"families"`
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
	ExpiresAt string `json:"expires_at"`
}

// PSResponse is the response from GET /api/ps.
type PSResponse struct {
	Models []RunningModel `json:"models"`
}

// RunningModels returns currently loaded models with VRAM usage via GET /api/ps.
func (c *OllamaClient) RunningModels(ctx context.Context) ([]RunningModel, error) {
	body, status, err := c.doWithRetry(ctx, "GET", "/api/ps", nil)
	if err != nil {
		return nil, fmt.Errorf("running models: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("running models: status %d: %s", status, string(body))
	}
	var resp PSResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal ps response: %w", err)
	}
	return resp.Models, nil
}

// --- POST /api/unload ---

// UnloadModel unloads a model via POST /api/unload (available in newer Ollama versions).
func (c *OllamaClient) UnloadModel(ctx context.Context, modelName string) error {
	payload := map[string]string{"model": modelName}
	body, status, err := c.doWithRetry(ctx, "POST", "/api/unload", payload)
	if err != nil {
		return fmt.Errorf("unload %s: %w", modelName, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("unload %s: status %d: %s", modelName, status, string(body))
	}
	return nil
}

// FetchFleetFromClients queries multiple Ollama instances using a client pool
// and returns a Fleet snapshot with running models and total VRAM info.
func FetchFleetFromClients(ctx context.Context, pool *ClientPool) (*Fleet, error) {
	fleet := &Fleet{SampledAt: time.Now()}
	clients := pool.All()
	if len(clients) == 0 {
		return fleet, fmt.Errorf("no clients configured")
	}

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	for _, cl := range clients {
		wg.Add(1)
		go func(client *OllamaClient) {
			defer wg.Done()
			running, err := client.RunningModels(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", client.baseURL, err))
				return
			}
			inst := InstanceState{Host: client.baseURL}
			for _, rm := range running {
				name := rm.Name
				if name == "" {
					name = rm.Model
				}
				sizeGB := float64(rm.SizeVRAM) / (1024 * 1024 * 1024)
				if sizeGB == 0 {
					sizeGB = float64(rm.Size) / (1024 * 1024 * 1024)
				}
				inst.Models = append(inst.Models, LoadedModel{
					Name:       name,
					SizeGB:     sizeGB,
					LoadedAt:   time.Now(),
					LastRequestAt: time.Now(),
				})
				inst.UsedVRAMGB += sizeGB
			}
			fleet.Instances = append(fleet.Instances, inst)
		}(cl)
	}
	wg.Wait()

	if len(fleet.Instances) == 0 && len(errs) > 0 {
		return fleet, fmt.Errorf("all %d instances failed: %v", len(errs), errs[0])
	}
	for _, e := range errs {
		log.Printf("warning: %v", e)
	}
	return fleet, nil
}
