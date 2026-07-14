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
