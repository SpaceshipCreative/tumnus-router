package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	routerpkg "github.com/SpaceshipCreative/tumnus/router"
	"github.com/maximhq/bifrost/core/schemas"
)

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

func fakeSupra(t *testing.T, analysisText string, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"text":"` + analysisText + `"}]}`))
	}))
}

func strPtr(s string) *string { return &s }

func userMsg(content schemas.ChatMessageContent) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &content}
}

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
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-tumnus-bypass": "true"})
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

// TestConcurrentInitCleanupHook is the -race regression test for the
// snapshot-based plugin state: hooks racing Init/Cleanup must neither race
// nor panic, and must fail open when the plugin is torn down mid-flight.
func TestConcurrentInitCleanupHook(t *testing.T) {
	srv := fakeSupra(t, "Domain: x | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	defer srv.Close()
	raw := validRawConfig()
	raw["endpoint"] = srv.URL

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = Init(raw)
			_ = Cleanup()
		}
	}()
	for i := 0; i < 200; i++ {
		req := chatRequest("hello")
		if err := PreRequestHook(newCtx(), req); err != nil {
			t.Errorf("hook returned error under churn: %v", err)
		}
	}
	<-done
	_ = Cleanup()
}

func TestPreRequestHookInstanceAffinity(t *testing.T) {
	srv := fakeSupra(t, "Domain: chat | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	t.Cleanup(srv.Close)
	raw := validRawConfig()
	raw["endpoint"] = srv.URL
	raw["roles"].(map[string]any)["local_small"] = map[string]any{
		"provider": "vllm-blackwell", "model": "qwen3.6-35b-a3b",
		"instances": []map[string]any{{"provider": "vllm-sparks", "model": "qwen3.6-35b-a3b"}},
	}
	if err := Init(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Cleanup() })

	// Same conversation twice -> same instance both times, and the picked
	// instance is one of the declared pair. Spread across conversations is
	// covered by the router-level TestPickInstance.
	req1 := chatRequest("hello there")
	req2 := chatRequest("hello there")
	if err := PreRequestHook(newCtx(), req1); err != nil {
		t.Fatal(err)
	}
	if err := PreRequestHook(newCtx(), req2); err != nil {
		t.Fatal(err)
	}
	if req1.ChatRequest.Provider != req2.ChatRequest.Provider {
		t.Fatalf("same conversation split across instances: %s vs %s", req1.ChatRequest.Provider, req2.ChatRequest.Provider)
	}
	got := string(req1.ChatRequest.Provider)
	if got != "vllm-blackwell" && got != "vllm-sparks" {
		t.Fatalf("picked unknown instance %q", got)
	}
}

func TestPreRequestHookTrajectoryEscalates(t *testing.T) {
	// Three turns, complexity 2 -> 3 -> 4, all Code. Threshold 5 means the
	// final turn would normally stay on code_cheap; the upward trend lowers
	// the effective threshold to 4 and routes it to frontier.
	turns := []string{
		"Domain: software | Complexity: 2 | Math: False | Code: True | Route: small model",
		"Domain: software | Complexity: 3 | Math: False | Code: True | Route: big model",
		"Domain: software | Complexity: 4 | Math: False | Code: True | Route: big model",
	}
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"text":"` + turns[i] + `"}]}`))
		i++
	}))
	t.Cleanup(srv.Close)
	raw := validRawConfig()
	raw["endpoint"] = srv.URL
	raw["trajectory_window"] = 5
	if err := Init(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Cleanup() })

	ctx := newCtx()
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-tumnus-session": "conv-1"})
	for turn := 0; turn < 3; turn++ {
		req := chatRequest("write the next part of the scraper")
		if err := PreRequestHook(ctx, req); err != nil {
			t.Fatal(err)
		}
		if turn < 2 && string(req.ChatRequest.Provider) == "anthropic" {
			t.Fatalf("turn %d escalated too early", turn)
		}
		if turn == 2 && string(req.ChatRequest.Provider) != "anthropic" {
			t.Fatalf("turn 2 should escalate to frontier, got %s/%s", req.ChatRequest.Provider, req.ChatRequest.Model)
		}
	}
}

func TestPreRequestHookTrajectoryDisabledByDefault(t *testing.T) {
	setupPlugin(t, "Domain: software | Complexity: 4 | Math: False | Code: True | Route: big model", 0)
	ctx := newCtx()
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-tumnus-session": "conv-2"})
	for turn := 0; turn < 4; turn++ {
		req := chatRequest("more code")
		if err := PreRequestHook(ctx, req); err != nil {
			t.Fatal(err)
		}
		if string(req.ChatRequest.Provider) != "ollama-cloud" {
			t.Fatalf("trajectory off: turn %d must stay on code_cheap, got %s", turn, req.ChatRequest.Provider)
		}
	}
}

func cascadeConfig() map[string]any {
	raw := validRawConfig()
	raw["cascade_enabled"] = true
	return raw
}

func emptyChatResponse() *schemas.BifrostResponse {
	empty := ""
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{ContentStr: &empty},
				},
			},
		}},
	}}
}

func goodChatResponse() *schemas.BifrostResponse {
	text := "a useful answer"
	resp := emptyChatResponse()
	resp.ChatResponse.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr = &text
	return resp
}

// routeAndRespond runs the pre-hook (arming the cascade on ctx) then feeds
// the given response to the post-hook, returning the post-hook's error.
func routeAndRespond(t *testing.T, ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) *schemas.BifrostError {
	t.Helper()
	req := chatRequest("hello")
	if err := PreRequestHook(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(req.ChatRequest.Fallbacks) == 0 {
		t.Fatal("test premise broken: routed request must have fallbacks")
	}
	_, bifrostErr, err := PostLLMHook(ctx, resp, nil)
	if err != nil {
		t.Fatal(err)
	}
	return bifrostErr
}

func TestCascadeEscalatesEmptyResponse(t *testing.T) {
	srv := fakeSupra(t, "Domain: chat | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	t.Cleanup(srv.Close)
	raw := cascadeConfig()
	raw["endpoint"] = srv.URL
	if err := Init(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Cleanup() })

	ctx := newCtx()
	if bifrostErr := routeAndRespond(t, ctx, emptyChatResponse()); bifrostErr == nil {
		t.Fatal("empty response must escalate (return BifrostError)")
	} else if bifrostErr.AllowFallbacks != nil && !*bifrostErr.AllowFallbacks {
		t.Fatal("escalation error must allow fallbacks")
	}
	// The fallback attempt's response passes through even if still empty:
	// one escalation per request.
	if _, bifrostErr, _ := PostLLMHook(ctx, emptyChatResponse(), nil); bifrostErr != nil {
		t.Fatal("second post-hook on same request must pass through (escalation cap)")
	}
}

func TestCascadeAcceptsGoodResponse(t *testing.T) {
	srv := fakeSupra(t, "Domain: chat | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	t.Cleanup(srv.Close)
	raw := cascadeConfig()
	raw["endpoint"] = srv.URL
	if err := Init(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Cleanup() })

	if bifrostErr := routeAndRespond(t, newCtx(), goodChatResponse()); bifrostErr != nil {
		t.Fatalf("good response must pass through, got %+v", bifrostErr)
	}
}

func TestCascadeIgnoresUnroutedAndDisabled(t *testing.T) {
	// Disabled (default config): post-hook must never touch anything.
	setupPlugin(t, "Domain: chat | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	if _, bifrostErr, _ := PostLLMHook(newCtx(), emptyChatResponse(), nil); bifrostErr != nil {
		t.Fatal("cascade disabled: must pass through")
	}
	_ = Cleanup()

	// Enabled but request never went through the pre-hook (e.g. bypassed):
	// no armed flag on ctx, must pass through.
	srv := fakeSupra(t, "Domain: chat | Complexity: 1 | Math: False | Code: False | Route: small model", 0)
	defer srv.Close()
	raw := cascadeConfig()
	raw["endpoint"] = srv.URL
	if err := Init(raw); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = Cleanup() }()
	if _, bifrostErr, _ := PostLLMHook(newCtx(), emptyChatResponse(), nil); bifrostErr != nil {
		t.Fatal("unrouted request: must pass through")
	}
}

func TestRebrand(t *testing.T) {
	if GetName() != "tumnus" {
		t.Fatalf("GetName() = %q, want tumnus", GetName())
	}
	cfg, err := routerpkg.ParseConfig(validRawConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BypassHeader != "x-tumnus-bypass" {
		t.Fatalf("default bypass header = %q", cfg.BypassHeader)
	}
}
