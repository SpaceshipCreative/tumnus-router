// Package router implements Tumnus's shared routing logic: it classifies chat
// requests with a router model (default SupraLabs/Supra-Router-51M) and maps
// the verdict to a role-assigned target model.
package router

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
	RoleLocalSmall = "local_small"
	RoleLocalBig   = "local_big"
	RoleCodeCheap  = "code_cheap"
	RoleFrontier   = "frontier"
)

var AllRoles = []string{RoleLocalSmall, RoleLocalBig, RoleCodeCheap, RoleFrontier}

// Instance is one of several deployments serving a role's model.
type Instance struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// RoleTarget is the provider/model a routing role resolves to.
type RoleTarget struct {
	Provider  string             `json:"provider"`
	Model     string             `json:"model"`
	Fallbacks []schemas.Fallback `json:"fallbacks,omitempty"` // optional; derived from ladder when empty
	Instances []Instance         `json:"instances,omitempty"` // optional peer deployments; selected by conversation affinity
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
	TrajectoryWindow            int                   `json:"trajectory_window"`   // 0 disables; 3-32 tracks per-conversation complexity trend
	SessionHeader               string                `json:"session_header"`      // client-supplied conversation id header
	SessionTTLSec               int                   `json:"session_ttl_seconds"` // idle conversations are forgotten after this
}

// ParseConfig converts the raw config (map[string]any from config.json) into a
// validated Config with defaults applied. Fails fast so bad config surfaces at
// plugin load, never at request time.
func ParseConfig(raw any) (*Config, error) {
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("tumnus: cannot marshal config: %w", err)
	}
	cfg := &Config{
		RouterModel:                 "SupraLabs/Supra-Router-51M",
		TimeoutMs:                   150,
		MaxPromptChars:              4000,
		BypassHeader:                "x-tumnus-bypass",
		FrontierComplexityThreshold: 5,
		SessionHeader:               "x-tumnus-session",
		SessionTTLSec:               3600,
	}
	if err := json.Unmarshal(buf, cfg); err != nil {
		return nil, fmt.Errorf("tumnus: invalid config: %w", err)
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("tumnus: endpoint is required")
	}
	if cfg.FrontierComplexityThreshold < 1 || cfg.FrontierComplexityThreshold > 5 {
		return nil, fmt.Errorf("tumnus: frontier_complexity_threshold must be 1-5, got %d", cfg.FrontierComplexityThreshold)
	}
	if cfg.TimeoutMs <= 0 {
		return nil, fmt.Errorf("tumnus: timeout_ms must be positive, got %d", cfg.TimeoutMs)
	}
	if cfg.MaxPromptChars <= 0 {
		return nil, fmt.Errorf("tumnus: max_prompt_chars must be positive, got %d", cfg.MaxPromptChars)
	}
	if cfg.TrajectoryWindow != 0 && (cfg.TrajectoryWindow < 3 || cfg.TrajectoryWindow > 32) {
		return nil, fmt.Errorf("tumnus: trajectory_window must be 0 (disabled) or 3-32, got %d", cfg.TrajectoryWindow)
	}
	if cfg.SessionTTLSec <= 0 {
		return nil, fmt.Errorf("tumnus: session_ttl_seconds must be positive, got %d", cfg.SessionTTLSec)
	}
	for _, role := range AllRoles {
		target, ok := cfg.Roles[role]
		if !ok || target.Provider == "" || target.Model == "" {
			return nil, fmt.Errorf("tumnus: roles.%s with provider and model is required", role)
		}
		for i, fb := range target.Fallbacks {
			if fb.Provider == "" || fb.Model == "" {
				return nil, fmt.Errorf("tumnus: roles.%s.fallbacks[%d] requires provider and model", role, i)
			}
		}
		for i, inst := range target.Instances {
			if inst.Provider == "" || inst.Model == "" {
				return nil, fmt.Errorf("tumnus: roles.%s.instances[%d] requires provider and model", role, i)
			}
		}
	}
	return cfg, nil
}

// Analysis is Supra-Router-51M's structured verdict for one prompt.
type Analysis struct {
	Domain     string
	Complexity int
	Math       bool
	Code       bool
	Route      string // "small model" or "big model"
}

// ParseAnalysis parses the model's pipe-separated output, e.g.:
//
//	Domain: poetry | Complexity: 1 | Math: False | Code: False | Route: small model | Justification: ...
//
// Route is required; other fields default to zero values when absent.
func ParseAnalysis(s string) (*Analysis, error) {
	a := &Analysis{}
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
			// otherwise-valid verdict. Unparseable or outside the contract's
			// 1-5 range -> stays 0 (lowest tier).
			if n, err := strconv.Atoi(value); err == nil && n >= 1 && n <= 5 {
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
	// The contract enumerates exactly two Route values; anything else makes
	// the whole verdict malformed and the caller must fail open (a request
	// must never be rerouted on output we don't understand).
	if a.Route != "small model" && a.Route != "big model" {
		// Bound the excerpts: this error is logged per request, and the raw
		// model output can be up to the 1 MiB body cap.
		return nil, fmt.Errorf("tumnus: missing or unknown Route %q in analysis %q",
			TruncatePrompt(a.Route, 40), TruncatePrompt(s, 200))
	}
	return a, nil
}

// ChooseRole maps a Supra Analysis to a routing role. The ladder is fixed by
// design (spec: "the ladder is code, not config"); first match wins.
func ChooseRole(a *Analysis, frontierThreshold int) string {
	switch {
	case a.Code && a.Complexity >= frontierThreshold:
		return RoleFrontier
	case a.Code:
		return RoleCodeCheap
	case a.Route == "big model" || a.Math:
		return RoleLocalBig
	default:
		return RoleLocalSmall
	}
}

// DefaultFallbacks derives a role's fallback chain from the other roles'
// assignments, per the spec's ladder table.
func DefaultFallbacks(role string, roles map[string]RoleTarget) []schemas.Fallback {
	fb := func(names ...string) []schemas.Fallback {
		out := make([]schemas.Fallback, 0, len(names))
		for _, n := range names {
			t := roles[n]
			out = append(out, schemas.Fallback{Provider: schemas.ModelProvider(t.Provider), Model: t.Model})
		}
		return out
	}
	switch role {
	case RoleFrontier:
		return fb(RoleCodeCheap)
	case RoleCodeCheap:
		return fb(RoleFrontier)
	case RoleLocalBig:
		return fb(RoleCodeCheap, RoleFrontier)
	default: // RoleLocalSmall
		return fb(RoleLocalBig)
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

// Classify sends the prompt to the Supra vLLM endpoint using the model card's
// template ("Task: <prompt>\nAnalysis: ", greedy decoding) and parses the verdict.
func Classify(ctx context.Context, client *http.Client, cfg *Config, prompt string) (*Analysis, error) {
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
		return nil, fmt.Errorf("tumnus: classifier returned status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var cr completionResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("tumnus: bad classifier response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("tumnus: classifier returned no choices")
	}
	return ParseAnalysis(cr.Choices[0].Text)
}

// ExtractLastUserText returns the text of the most recent user message,
// joining text blocks with newlines and ignoring non-text blocks, truncated
// to maxChars. Returns "" when there is nothing to Classify.
func ExtractLastUserText(msgs []schemas.ChatMessage, maxChars int) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != schemas.ChatMessageRoleUser || msgs[i].Content == nil {
			continue
		}
		return TruncatePrompt(userText(msgs[i]), maxChars)
	}
	return ""
}

// ExtractFirstUserText returns the text of the earliest non-empty user
// message, same joining/truncation rules as ExtractLastUserText. It is the
// conversation's stable identity: every later turn of the same conversation
// still starts with the same first message.
func ExtractFirstUserText(msgs []schemas.ChatMessage, maxChars int) string {
	for i := range msgs {
		if text := userText(msgs[i]); text != "" {
			return TruncatePrompt(text, maxChars)
		}
	}
	return ""
}

// userText joins a user message's text (string content or text blocks) with
// newlines, trimmed. "" for non-user messages or messages without text.
func userText(msg schemas.ChatMessage) string {
	if msg.Role != schemas.ChatMessageRoleUser || msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil {
		return strings.TrimSpace(*msg.Content.ContentStr)
	}
	var parts []string
	for _, block := range msg.Content.ContentBlocks {
		if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
			parts = append(parts, *block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// TruncatePrompt returns s truncated to at most maxChars runes, never
// splitting a UTF-8 sequence (the contract counts chars, not bytes).
func TruncatePrompt(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == maxChars {
			return s[:i]
		}
		n++
	}
	return s
}
