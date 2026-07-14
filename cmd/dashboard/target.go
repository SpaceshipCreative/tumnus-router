package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// DeployTarget loads and saves a router config living inside some managed
// service. v1 has exactly one implementation (Bifrost); future adapters
// (LiteLLM, OpenCode, Hermes Agent, OpenClaw) implement the same interface.
type DeployTarget interface {
	Load(ctx context.Context) (map[string]any, error)
	Save(ctx context.Context, config map[string]any) error
}

// BifrostTarget manages the tumnus plugin entry via Bifrost's management API.
// It preserves every PluginResponse field it read on Load (enabled, path,
// placement, order, ...) and swaps only "config" on Save.
type BifrostTarget struct {
	BaseURL    string // e.g. http://localhost:8080
	Token      string // optional; sent as Authorization: Bearer
	PluginName string // "tumnus"
	Client     *http.Client
	lastPlugin map[string]any // full PluginResponse from the last Load
}

func (b *BifrostTarget) do(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := b.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("bifrost API %s %s: status %d: %s", method, path, resp.StatusCode, raw)
	}
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("bifrost API %s %s: bad JSON: %w", method, path, err)
		}
	}
	return out, nil
}

func (b *BifrostTarget) Load(ctx context.Context) (map[string]any, error) {
	plugin, err := b.do(ctx, http.MethodGet, "/api/plugins/"+b.PluginName, nil)
	if err != nil {
		return nil, err
	}
	b.lastPlugin = plugin
	cfg, _ := plugin["config"].(map[string]any)
	return cfg, nil
}

func (b *BifrostTarget) Save(ctx context.Context, config map[string]any) error {
	if b.lastPlugin == nil {
		if _, err := b.Load(ctx); err != nil {
			return err
		}
	}
	body := map[string]any{}
	for k, v := range b.lastPlugin {
		body[k] = v
	}
	body["config"] = config
	_, err := b.do(ctx, http.MethodPut, "/api/plugins/"+b.PluginName, body)
	return err
}
