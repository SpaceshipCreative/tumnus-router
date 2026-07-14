// Package main implements the Supra Router Bifrost plugin: it classifies chat
// requests with SupraLabs/Supra-Router-51M and routes them to role-assigned models.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	roleLocalSmall = "local_small"
	roleLocalBig   = "local_big"
	roleCodeCheap  = "code_cheap"
	roleFrontier   = "frontier"
)

var allRoles = []string{roleLocalSmall, roleLocalBig, roleCodeCheap, roleFrontier}

// RoleTarget is the provider/model a routing role resolves to.
type RoleTarget struct {
	Provider  string             `json:"provider"`
	Model     string             `json:"model"`
	Fallbacks []schemas.Fallback `json:"fallbacks,omitempty"` // optional; derived from ladder when empty
}

// Config is the plugin configuration (config.json "config" block).
type Config struct {
	Endpoint                    string                `json:"endpoint"`
	RouterModel                 string                `json:"router_model"`
	TimeoutMs                   int                   `json:"timeout_ms"`
	MaxPromptChars              int                   `json:"max_prompt_chars"`
	BypassHeader                string                `json:"bypass_header"`
	FrontierComplexityThreshold int                   `json:"frontier_complexity_threshold"`
	Roles                       map[string]RoleTarget `json:"roles"`
}

// parseConfig converts the raw config (map[string]any from config.json) into a
// validated Config with defaults applied. Fails fast so bad config surfaces at
// plugin load, never at request time.
func parseConfig(raw any) (*Config, error) {
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("suprarouter: cannot marshal config: %w", err)
	}
	cfg := &Config{
		RouterModel:                 "SupraLabs/Supra-Router-51M",
		TimeoutMs:                   150,
		MaxPromptChars:              4000,
		BypassHeader:                "x-supra-bypass",
		FrontierComplexityThreshold: 5,
	}
	if err := json.Unmarshal(buf, cfg); err != nil {
		return nil, fmt.Errorf("suprarouter: invalid config: %w", err)
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("suprarouter: endpoint is required")
	}
	if cfg.FrontierComplexityThreshold < 1 || cfg.FrontierComplexityThreshold > 5 {
		return nil, fmt.Errorf("suprarouter: frontier_complexity_threshold must be 1-5, got %d", cfg.FrontierComplexityThreshold)
	}
	for _, role := range allRoles {
		target, ok := cfg.Roles[role]
		if !ok || target.Provider == "" || target.Model == "" {
			return nil, fmt.Errorf("suprarouter: roles.%s with provider and model is required", role)
		}
	}
	return cfg, nil
}

// analysis is Supra-Router-51M's structured verdict for one prompt.
type analysis struct {
	Domain     string
	Complexity int
	Math       bool
	Code       bool
	Route      string // "small model" or "big model"
}

// parseAnalysis parses the model's pipe-separated output, e.g.:
//
//	Domain: poetry | Complexity: 1 | Math: False | Code: False | Route: small model | Justification: ...
//
// Route is required; other fields default to zero values when absent.
func parseAnalysis(s string) (*analysis, error) {
	a := &analysis{}
	for _, part := range strings.Split(s, "|") {
		key, value, found := strings.Cut(part, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "domain":
			a.Domain = value
		case "complexity":
			// Optional field: model-output drift here shouldn't discard an
			// otherwise-valid verdict. Unparseable -> stays 0 (lowest tier).
			if n, err := strconv.Atoi(value); err == nil {
				a.Complexity = n
			}
		case "math":
			a.Math = strings.EqualFold(value, "true")
		case "code":
			a.Code = strings.EqualFold(value, "true")
		case "route":
			a.Route = strings.ToLower(value)
		}
	}
	if a.Route == "" {
		return nil, fmt.Errorf("suprarouter: no Route field in analysis %q", s)
	}
	return a, nil
}

// chooseRole maps a Supra analysis to a routing role. The ladder is fixed by
// design (spec: "the ladder is code, not config"); first match wins.
func chooseRole(a *analysis, frontierThreshold int) string {
	switch {
	case a.Code && a.Complexity >= frontierThreshold:
		return roleFrontier
	case a.Code:
		return roleCodeCheap
	case strings.Contains(a.Route, "big") || a.Math:
		return roleLocalBig
	default:
		return roleLocalSmall
	}
}

// defaultFallbacks derives a role's fallback chain from the other roles'
// assignments, per the spec's ladder table.
func defaultFallbacks(role string, roles map[string]RoleTarget) []schemas.Fallback {
	fb := func(names ...string) []schemas.Fallback {
		out := make([]schemas.Fallback, 0, len(names))
		for _, n := range names {
			t := roles[n]
			out = append(out, schemas.Fallback{Provider: schemas.ModelProvider(t.Provider), Model: t.Model})
		}
		return out
	}
	switch role {
	case roleFrontier:
		return fb(roleCodeCheap)
	case roleCodeCheap:
		return fb(roleFrontier)
	case roleLocalBig:
		return fb(roleCodeCheap, roleFrontier)
	default: // roleLocalSmall
		return fb(roleLocalBig)
	}
}

// completionRequest / completionResponse are the minimal OpenAI-compatible
// /v1/completions shapes vLLM serves.
type completionRequest struct {
	Model       string  `json:"model"`
	Prompt      string  `json:"prompt"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
}

type completionResponse struct {
	Choices []struct {
		Text string `json:"text"`
	} `json:"choices"`
}

// classify sends the prompt to the Supra vLLM endpoint using the model card's
// template ("Task: <prompt>\nAnalysis: ", greedy decoding) and parses the verdict.
func classify(ctx context.Context, client *http.Client, cfg *Config, prompt string) (*analysis, error) {
	body, err := json.Marshal(completionRequest{
		Model:       cfg.RouterModel,
		Prompt:      "Task: " + prompt + "\nAnalysis: ",
		MaxTokens:   96,
		Temperature: 0,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Drain (bounded) so the connection returns to the idle pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("suprarouter: classifier returned status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var cr completionResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("suprarouter: bad classifier response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("suprarouter: classifier returned no choices")
	}
	return parseAnalysis(cr.Choices[0].Text)
}

// extractLastUserText returns the text of the most recent user message,
// joining text blocks with newlines and ignoring non-text blocks, truncated
// to maxChars. Returns "" when there is nothing to classify.
func extractLastUserText(msgs []schemas.ChatMessage, maxChars int) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != schemas.ChatMessageRoleUser || msg.Content == nil {
			continue
		}
		var text string
		if msg.Content.ContentStr != nil {
			text = *msg.Content.ContentStr
		} else {
			var parts []string
			for _, block := range msg.Content.ContentBlocks {
				if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
					parts = append(parts, *block.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return ""
		}
		if len(text) > maxChars {
			// ponytail: byte truncation may split a UTF-8 rune; the classifier
			// tolerates a mangled trailing char. Switch to rune-aware slicing if it matters.
			text = text[:maxChars]
		}
		return text
	}
	return ""
}
