# Supra Router Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Bifrost dynamic Go plugin that classifies every chat request with SupraLabs/Supra-Router-51M (served on vLLM) and routes it directly to one of four role-assigned models — no manual routes.

**Architecture:** `package main` dynamic plugin (`.so`) at `plugins/suprarouter/`, modeled on `examples/plugins/hello-world/`. All logic in `PreRequestHook` (routing phase, `pre_builtin` placement): extract last user message → POST to Supra vLLM `/v1/completions` → parse pipe-separated analysis → decision ladder picks a role → mutate `req.ChatRequest.Provider/Model/Fallbacks`. Fail-open on any classification problem.

**Tech Stack:** Go 1.26.1 (must match Bifrost), stdlib only (`net/http`, `encoding/json`), `github.com/maximhq/bifrost/core/schemas`, `httptest` for tests.

**Spec:** `docs/superpowers/specs/2026-07-13-supra-router-plugin-design.md`

**Verified API facts (don't re-derive):**
- `PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error` — mutations to `req.ChatRequest.Provider/Model/Fallbacks` commit for all attempts; errors are non-blocking (`core/schemas/plugin.go:283-297`).
- `BifrostChatRequest{Provider schemas.ModelProvider, Model string, Input []ChatMessage, Fallbacks []Fallback}` (`core/schemas/chatcompletions.go:14`).
- `Fallback{Provider ModelProvider, Model string}` (`core/schemas/bifrost.go:473`).
- Request types: `schemas.ChatCompletionRequest` / `schemas.ChatCompletionStreamRequest` (`core/schemas/bifrost.go:133-134`).
- Headers: `ctx.Value(schemas.BifrostContextKeyRequestHeaders)` → `map[string]string`, keys lowercased (`core/schemas/bifrost.go:324`).
- Observability: `ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, level, msg)` (`core/schemas/context.go:575`, `bifrost.go:392`). Do **NOT** write `BifrostContextKeyRoutingEnginesUsed` — it's marked "set by bifrost - DO NOT SET THIS MANUALLY" (`bifrost.go:311`).
- Messages: `ChatMessage{Role, Content *ChatMessageContent}`, `ChatMessageContent{ContentStr *string, ContentBlocks []ChatContentBlock}`, text blocks have `Type == schemas.ChatContentBlockTypeText` and `Text *string`; user role is `schemas.ChatMessageRoleUser` (`chatcompletions.go:994-1137`).
- Test context: `schemas.NewBifrostContext(context.Background(), time.Time{})`; set headers with `ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{...})`.
- Dynamic plugin shape: `package main` with exported `Init/GetName/HTTPTransportPreHook/HTTPTransportPostHook/HTTPTransportStreamChunkHook/PreRequestHook/PreLLMHook/PostLLMHook/Cleanup` (see `examples/plugins/hello-world/main.go`).

**File structure:**
- `plugins/suprarouter/go.mod` — standalone module
- `plugins/suprarouter/router.go` — config, analysis parser, decision ladder, Supra HTTP client, text extraction (pure logic, all unit-testable)
- `plugins/suprarouter/main.go` — exported plugin functions, PreRequestHook wiring
- `plugins/suprarouter/router_test.go`, `plugins/suprarouter/main_test.go` — tests
- `plugins/suprarouter/Makefile` — `.so` build

---

### Task 1: Scaffold module + config parsing/validation

**Files:**
- Create: `plugins/suprarouter/go.mod`
- Create: `plugins/suprarouter/router.go`
- Test: `plugins/suprarouter/router_test.go`

- [ ] **Step 1: Create the module**

```bash
mkdir -p ~/Projects/bifrost/plugins/suprarouter
cd ~/Projects/bifrost/plugins/suprarouter
go mod init github.com/maximhq/bifrost/plugins/suprarouter
go mod edit -go=1.26.1
go get github.com/maximhq/bifrost/core@latest
```

Check `plugins/mocker/go.mod` for a `replace` directive pattern; if it uses `replace github.com/maximhq/bifrost/core => ../../core`, add the same:

```bash
go mod edit -replace github.com/maximhq/bifrost/core=../../core && go mod tidy
```

- [ ] **Step 2: Write failing config tests**

Create `plugins/suprarouter/router_test.go`:

```go
package main

import "testing"

func validRawConfig() map[string]any {
	return map[string]any{
		"endpoint": "http://classifier-host:8000/v1/completions",
		"roles": map[string]any{
			"local_small": map[string]any{"provider": "vllm-local", "model": "qwen3.6-35b-a3b"},
			"local_big":   map[string]any{"provider": "vllm-local", "model": "deepseek-v4-flash"},
			"code_cheap":  map[string]any{"provider": "ollama-cloud", "model": "glm-5.2"},
			"frontier":    map[string]any{"provider": "anthropic", "model": "claude-fable-5"},
		},
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(validRawConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TimeoutMs != 150 || cfg.MaxPromptChars != 4000 ||
		cfg.BypassHeader != "x-supra-bypass" || cfg.FrontierComplexityThreshold != 5 ||
		cfg.RouterModel != "SupraLabs/Supra-Router-51M" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestParseConfigInvalid(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"missing endpoint": func(m map[string]any) { delete(m, "endpoint") },
		"missing role":     func(m map[string]any) { delete(m["roles"].(map[string]any), "frontier") },
		"bad threshold":    func(m map[string]any) { m["frontier_complexity_threshold"] = 9 },
	}
	for name, mutate := range cases {
		raw := validRawConfig()
		mutate(raw)
		if _, err := parseConfig(raw); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd ~/Projects/bifrost/plugins/suprarouter && go test ./...`
Expected: FAIL — `undefined: parseConfig`

- [ ] **Step 4: Implement config in `router.go`**

```go
// Package main implements the Supra Router Bifrost plugin: it classifies chat
// requests with SupraLabs/Supra-Router-51M and routes them to role-assigned models.
package main

import (
	"encoding/json"
	"fmt"

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
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add plugins/suprarouter
git commit -m "feat(suprarouter): scaffold plugin module with config parsing"
```

---

### Task 2: Analysis parser

**Files:**
- Modify: `plugins/suprarouter/router.go` (append)
- Test: `plugins/suprarouter/router_test.go` (append)

- [ ] **Step 1: Write failing parser tests** (append to `router_test.go`)

```go
func TestParseAnalysis(t *testing.T) {
	out := "Domain: software engineering | Complexity: 4 | Math: False | Code: True | Route: big model | Justification: multi-step code task"
	a, err := parseAnalysis(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Domain != "software engineering" || a.Complexity != 4 || a.Math || !a.Code || a.Route != "big model" {
		t.Fatalf("bad parse: %+v", a)
	}
}

func TestParseAnalysisGarbage(t *testing.T) {
	for _, s := range []string{"", "hello world", "Domain: x | Complexity: banana | Route: small model"} {
		if _, err := parseAnalysis(s); err == nil {
			t.Errorf("%q: expected error, got nil", s)
		}
	}
}

func TestParseAnalysisMissingRoute(t *testing.T) {
	if _, err := parseAnalysis("Domain: x | Complexity: 2 | Math: False | Code: False"); err == nil {
		t.Error("expected error for missing Route field")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `undefined: parseAnalysis`

- [ ] **Step 3: Implement parser** (append to `router.go`; add `"strconv"` and `"strings"` to imports)

```go
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
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("suprarouter: bad complexity %q", value)
			}
			a.Complexity = n
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugins/suprarouter
git commit -m "feat(suprarouter): parse Supra analysis output"
```

---

### Task 3: Decision ladder + derived fallbacks

**Files:**
- Modify: `plugins/suprarouter/router.go` (append)
- Test: `plugins/suprarouter/router_test.go` (append)

- [ ] **Step 1: Write failing ladder tests** (append to `router_test.go`)

```go
func TestChooseRole(t *testing.T) {
	cases := []struct {
		name string
		a    analysis
		want string
	}{
		{"frontier code", analysis{Code: true, Complexity: 5, Route: "big model"}, roleFrontier},
		{"code below threshold", analysis{Code: true, Complexity: 4, Route: "big model"}, roleCodeCheap},
		{"big route", analysis{Route: "big model", Complexity: 3}, roleLocalBig},
		{"math forces big", analysis{Math: true, Route: "small model", Complexity: 2}, roleLocalBig},
		{"small route", analysis{Route: "small model", Complexity: 1}, roleLocalSmall},
	}
	for _, tc := range cases {
		if got := chooseRole(&tc.a, 5); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestDefaultFallbacks(t *testing.T) {
	cfg, err := parseConfig(validRawConfig())
	if err != nil {
		t.Fatal(err)
	}
	got := defaultFallbacks(roleLocalBig, cfg.Roles)
	if len(got) != 2 || got[0].Model != "glm-5.2" || got[1].Model != "claude-fable-5" {
		t.Fatalf("local_big fallbacks wrong: %+v", got)
	}
	if got := defaultFallbacks(roleLocalSmall, cfg.Roles); len(got) != 1 || got[0].Model != "deepseek-v4-flash" {
		t.Fatalf("local_small fallbacks wrong: %+v", got)
	}
	if got := defaultFallbacks(roleFrontier, cfg.Roles); len(got) != 1 || got[0].Model != "glm-5.2" {
		t.Fatalf("frontier fallbacks wrong: %+v", got)
	}
	if got := defaultFallbacks(roleCodeCheap, cfg.Roles); len(got) != 1 || got[0].Model != "claude-fable-5" {
		t.Fatalf("code_cheap fallbacks wrong: %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `undefined: chooseRole`

- [ ] **Step 3: Implement ladder** (append to `router.go`)

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugins/suprarouter
git commit -m "feat(suprarouter): decision ladder and derived fallbacks"
```

---

### Task 4: Supra HTTP client

**Files:**
- Modify: `plugins/suprarouter/router.go` (append)
- Test: `plugins/suprarouter/router_test.go` (append)

- [ ] **Step 1: Write failing client tests** (append to `router_test.go`; add imports `"context"`, `"net/http"`, `"net/http/httptest"`, `"time"`)

```go
func fakeSupra(t *testing.T, analysisText string, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"text":"` + analysisText + `"}]}`))
	}))
}

func TestClassify(t *testing.T) {
	srv := fakeSupra(t, "Domain: math | Complexity: 5 | Math: True | Code: False | Route: big model", 0)
	defer srv.Close()
	cfg, _ := parseConfig(validRawConfig())
	cfg.Endpoint = srv.URL
	a, err := classify(context.Background(), srv.Client(), cfg, "prove Fermat's little theorem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !a.Math || a.Complexity != 5 {
		t.Fatalf("bad analysis: %+v", a)
	}
}

func TestClassifyTimeout(t *testing.T) {
	srv := fakeSupra(t, "Domain: x | Route: small model", 300*time.Millisecond)
	defer srv.Close()
	cfg, _ := parseConfig(validRawConfig())
	cfg.Endpoint = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := classify(ctx, srv.Client(), cfg, "hi"); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestClassifyBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg, _ := parseConfig(validRawConfig())
	cfg.Endpoint = srv.URL
	if _, err := classify(context.Background(), srv.Client(), cfg, "hi"); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `undefined: classify`

- [ ] **Step 3: Implement client** (append to `router.go`; add imports `"bytes"`, `"context"`, `"io"`, `"net/http"`)

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugins/suprarouter
git commit -m "feat(suprarouter): Supra vLLM classification client"
```

---

### Task 5: Last-user-message text extraction

**Files:**
- Modify: `plugins/suprarouter/router.go` (append)
- Test: `plugins/suprarouter/router_test.go` (append)

- [ ] **Step 1: Write failing extraction tests** (append to `router_test.go`; add import `"github.com/maximhq/bifrost/core/schemas"`)

```go
func strPtr(s string) *string { return &s }

func userMsg(content schemas.ChatMessageContent) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &content}
}

func TestExtractLastUserText(t *testing.T) {
	msgs := []schemas.ChatMessage{
		userMsg(schemas.ChatMessageContent{ContentStr: strPtr("first question")}),
		{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: strPtr("answer")}},
		userMsg(schemas.ChatMessageContent{ContentStr: strPtr("second question")}),
	}
	if got := extractLastUserText(msgs, 4000); got != "second question" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractLastUserTextBlocks(t *testing.T) {
	msgs := []schemas.ChatMessage{
		userMsg(schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: strPtr("part one")},
			{Type: schemas.ChatContentBlockTypeImageURL},
			{Type: schemas.ChatContentBlockTypeText, Text: strPtr("part two")},
		}}),
	}
	if got := extractLastUserText(msgs, 4000); got != "part one\npart two" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractLastUserTextTruncationAndEmpty(t *testing.T) {
	long := userMsg(schemas.ChatMessageContent{ContentStr: strPtr("abcdefghij")})
	if got := extractLastUserText([]schemas.ChatMessage{long}, 4); got != "abcd" {
		t.Fatalf("truncation: got %q", got)
	}
	if got := extractLastUserText(nil, 4000); got != "" {
		t.Fatalf("empty: got %q", got)
	}
}
```

Note: if `schemas.ChatContentBlockTypeImageURL` or `schemas.ChatMessageRoleAssistant` doesn't compile, check the exact constant names near `chatcompletions.go:994-1119` (`ChatContentBlockTypeText` and `ChatMessageRoleUser` are confirmed) and substitute.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `undefined: extractLastUserText`

- [ ] **Step 3: Implement extraction** (append to `router.go`)

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugins/suprarouter
git commit -m "feat(suprarouter): extract last user message text"
```

---

### Task 6: Plugin exports + PreRequestHook wiring

**Files:**
- Create: `plugins/suprarouter/main.go`
- Test: `plugins/suprarouter/main_test.go`

- [ ] **Step 1: Write failing hook tests**

Create `plugins/suprarouter/main_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func chatRequest(text string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.ModelProvider("openai"),
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{userMsg(schemas.ChatMessageContent{ContentStr: strPtr(text)})},
		},
	}
}

func setupPlugin(t *testing.T, analysisText string, delay time.Duration) {
	t.Helper()
	srv := fakeSupra(t, analysisText, delay)
	t.Cleanup(srv.Close)
	raw := validRawConfig()
	raw["endpoint"] = srv.URL
	if err := Init(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Cleanup() })
}

func newCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func TestPreRequestHookRoutesCode(t *testing.T) {
	setupPlugin(t, "Domain: software | Complexity: 3 | Math: False | Code: True | Route: big model", 0)
	req := chatRequest("write a binary search in Go")
	if err := PreRequestHook(newCtx(), req); err != nil {
		t.Fatal(err)
	}
	if string(req.ChatRequest.Provider) != "ollama-cloud" || req.ChatRequest.Model != "glm-5.2" {
		t.Fatalf("wrong target: %s/%s", req.ChatRequest.Provider, req.ChatRequest.Model)
	}
	if len(req.ChatRequest.Fallbacks) != 1 || req.ChatRequest.Fallbacks[0].Model != "claude-fable-5" {
		t.Fatalf("wrong fallbacks: %+v", req.ChatRequest.Fallbacks)
	}
}

func TestPreRequestHookFailOpenOnTimeout(t *testing.T) {
	setupPlugin(t, "Domain: x | Route: small model", 2*time.Second)
	req := chatRequest("hello")
	if err := PreRequestHook(newCtx(), req); err != nil {
		t.Fatal(err)
	}
	if string(req.ChatRequest.Provider) != "openai" || req.ChatRequest.Model != "gpt-4o" {
		t.Fatalf("request mutated on classifier timeout: %s/%s", req.ChatRequest.Provider, req.ChatRequest.Model)
	}
}

func TestPreRequestHookBypassHeader(t *testing.T) {
	setupPlugin(t, "Domain: x | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	ctx := newCtx()
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-supra-bypass": "true"})
	req := chatRequest("hello")
	if err := PreRequestHook(ctx, req); err != nil {
		t.Fatal(err)
	}
	if req.ChatRequest.Model != "gpt-4o" {
		t.Fatalf("bypassed request was mutated: %s", req.ChatRequest.Model)
	}
}

func TestPreRequestHookSkipsNonChat(t *testing.T) {
	setupPlugin(t, "Domain: x | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	req := &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest}
	if err := PreRequestHook(newCtx(), req); err != nil {
		t.Fatal(err)
	}
}

func TestPreRequestHookRoutesSmall(t *testing.T) {
	setupPlugin(t, "Domain: chitchat | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	req := chatRequest("hey, how's it going?")
	if err := PreRequestHook(newCtx(), req); err != nil {
		t.Fatal(err)
	}
	if string(req.ChatRequest.Provider) != "vllm-local" || req.ChatRequest.Model != "qwen3.6-35b-a3b" {
		t.Fatalf("wrong target: %s/%s", req.ChatRequest.Provider, req.ChatRequest.Model)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `undefined: Init`, `undefined: PreRequestHook`, `undefined: Cleanup`

- [ ] **Step 3: Implement `main.go`**

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Plugin-level state, set by Init. Dynamic Bifrost plugins are exported
// package-level functions (see examples/plugins/hello-world), so state is
// package-level too. Init is called once at load; hooks only read.
var (
	cfg        *Config
	httpClient *http.Client
)

// Init parses and validates the plugin config. Invalid config fails the load.
func Init(rawConfig any) error {
	parsed, err := parseConfig(rawConfig)
	if err != nil {
		return err
	}
	cfg = parsed
	httpClient = &http.Client{Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond}
	return nil
}

// GetName returns the plugin's system identifier.
func GetName() string {
	return "suprarouter"
}

// PreRequestHook classifies the chat request with Supra-Router-51M and routes
// it to the role-assigned model. Every failure path leaves the request
// untouched (fail-open): worst case the gateway behaves as if this plugin
// were not installed.
func PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if cfg == nil || req == nil || req.ChatRequest == nil {
		return nil
	}
	if req.RequestType != schemas.ChatCompletionRequest && req.RequestType != schemas.ChatCompletionStreamRequest {
		return nil
	}
	if headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string); headers[cfg.BypassHeader] == "true" {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, "suprarouter: bypass header set, skipping")
		return nil
	}
	text := extractLastUserText(req.ChatRequest.Input, cfg.MaxPromptChars)
	if text == "" {
		return nil
	}

	cctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutMs)*time.Millisecond)
	defer cancel()
	start := time.Now()
	a, err := classify(cctx, httpClient, cfg, text)
	if err != nil {
		// Fail open: never block or reroute a request we could not classify.
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelWarn,
			fmt.Sprintf("suprarouter: classification failed, passing through unchanged: %v", err))
		return nil
	}

	role := chooseRole(a, cfg.FrontierComplexityThreshold)
	target := cfg.Roles[role]
	req.ChatRequest.Provider = schemas.ModelProvider(target.Provider)
	req.ChatRequest.Model = target.Model
	if len(target.Fallbacks) > 0 {
		req.ChatRequest.Fallbacks = target.Fallbacks
	} else {
		req.ChatRequest.Fallbacks = defaultFallbacks(role, cfg.Roles)
	}

	ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo,
		fmt.Sprintf("suprarouter: domain=%q complexity=%d code=%t math=%t route=%q -> role=%s target=%s/%s (classified in %s)",
			a.Domain, a.Complexity, a.Code, a.Math, a.Route, role, target.Provider, target.Model, time.Since(start).Round(time.Millisecond)))
	return nil
}

// The hooks below are required exports for dynamic plugins; this plugin does
// not use them.

func HTTPTransportPreHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

func HTTPTransportPostHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, _ *schemas.HTTPResponse) error {
	return nil
}

func HTTPTransportStreamChunkHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

func PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

func PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// Cleanup releases plugin state at shutdown (and between tests).
func Cleanup() error {
	cfg = nil
	httpClient = nil
	return nil
}
```

- [ ] **Step 4: Run all tests**

Run: `go test ./... && go vet ./...`
Expected: PASS, no vet findings

- [ ] **Step 5: Commit**

```bash
git add plugins/suprarouter
git commit -m "feat(suprarouter): PreRequestHook routing with fail-open classification"
```

---

### Task 7: Build artifact + operator documentation

**Files:**
- Create: `plugins/suprarouter/Makefile`
- Create: `plugins/suprarouter/README.md`
- Create: `plugins/suprarouter/.gitignore`

- [ ] **Step 1: Create `Makefile`** (pattern: `examples/plugins/hello-world/Makefile` — check it first and mirror its targets; minimal version below)

```makefile
.PHONY: build test clean

build:
	go build -buildmode=plugin -o suprarouter.so .

test:
	go test ./...

clean:
	rm -f suprarouter.so
```

- [ ] **Step 2: Verify the plugin builds as a shared object**

Run: `cd ~/Projects/bifrost/plugins/suprarouter && make build && ls -la suprarouter.so`
Expected: `suprarouter.so` exists. (Requires Go 1.26.1 on macOS/Linux; no cross-compilation.)

- [ ] **Step 3: Write `README.md`**

```markdown
# suprarouter

Routes every chat request through [SupraLabs/Supra-Router-51M](https://huggingface.co/SupraLabs/Supra-Router-51M):
the 51M-param router LLM classifies the prompt (domain, complexity 1-5, code/math
flags, small/big verdict) and the plugin picks one of four role-assigned models.
No routing rules to maintain — configure which model plays which role and done.

Design spec: `docs/superpowers/specs/2026-07-13-supra-router-plugin-design.md`.

## Serve the router model

```bash
vllm serve SupraLabs/Supra-Router-51M --port 8000
```

## Decision ladder (fixed)

1. code && complexity >= threshold (default 5) -> `frontier` (fallback: `code_cheap`)
2. code -> `code_cheap` (fallback: `frontier`)
3. big-model verdict or math -> `local_big` (fallbacks: `code_cheap`, `frontier`)
4. otherwise -> `local_small` (fallback: `local_big`)

## Register in Bifrost `config.json`

Requires a dynamically-built Bifrost binary (see docs/plugins/building-dynamic-binary.mdx).
`pre_builtin` placement makes the routing decision land before governance —
any CEL routing rule you add later still overrides it (manual escape hatch).

```json
{
  "plugins": [
    {
      "name": "suprarouter",
      "enabled": true,
      "path": "/path/to/suprarouter.so",
      "placement": "pre_builtin",
      "order": 0,
      "config": {
        "endpoint": "http://classifier-host:8000/v1/completions",
        "roles": {
          "local_small": { "provider": "vllm-local", "model": "qwen3.6-35b-a3b" },
          "local_big":   { "provider": "vllm-local", "model": "deepseek-v4-flash" },
          "code_cheap":  { "provider": "ollama-cloud", "model": "glm-5.2" },
          "frontier":    { "provider": "anthropic", "model": "claude-fable-5" }
        }
      }
    }
  ]
}
```

Optional config keys (defaults): `router_model` (`SupraLabs/Supra-Router-51M`),
`timeout_ms` (150), `max_prompt_chars` (4000), `bypass_header` (`x-supra-bypass`),
`frontier_complexity_threshold` (5), per-role `fallbacks` array (derived from
the ladder when omitted).

All four role providers must already exist in Bifrost's provider config.
Send header `x-supra-bypass: true` to skip routing for one request.
Any classification failure fails open: the request proceeds unchanged.
Decisions appear in the Routing Decision Logs (`routing-rule` engine).
```

- [ ] **Step 4: Add `.gitignore` for the build artifact**

Create `plugins/suprarouter/.gitignore`:

```
suprarouter.so
```

- [ ] **Step 5: Final full check and commit**

Run: `go test ./... && go vet ./... && make build`
Expected: all PASS, `.so` builds.

```bash
git add plugins/suprarouter
git commit -m "feat(suprarouter): build tooling and operator README"
```

---

## Manual verification (post-implementation, operator)

Not automatable from this repo — requires the live fleet:

1. `vllm serve SupraLabs/Supra-Router-51M` and confirm `curl <endpoint> -d '{"model":"SupraLabs/Supra-Router-51M","prompt":"Task: hi\nAnalysis: ","max_tokens":96,"temperature":0}'` returns a pipe-separated analysis.
2. Build a dynamic Bifrost binary, register the plugin, send: a greeting (expect Qwen), a coding task (expect GLM), a hard architecture task phrased as code (expect Fable), and verify targets in the Routing Decision Logs UI.
3. Stop the Supra vLLM server and confirm requests pass through on their original model (fail-open).
