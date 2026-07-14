package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func validRawConfig() map[string]any {
	return map[string]any{
		"endpoint": "http://spark-1:8000/v1/completions",
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
	for _, s := range []string{"", "hello world"} {
		if _, err := parseAnalysis(s); err == nil {
			t.Errorf("%q: expected error, got nil", s)
		}
	}
}

func TestParseAnalysisMalformedComplexityIsOptional(t *testing.T) {
	a, err := parseAnalysis("Domain: x | Complexity: banana | Route: small model")
	if err != nil {
		t.Fatalf("malformed optional field should not fail the verdict: %v", err)
	}
	if a.Complexity != 0 || a.Route != "small model" {
		t.Fatalf("bad parse: %+v", a)
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

func TestParseAnalysisMissingRoute(t *testing.T) {
	if _, err := parseAnalysis("Domain: x | Complexity: 2 | Math: False | Code: False"); err == nil {
		t.Error("expected error for missing Route field")
	}
}

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
			{Type: schemas.ChatContentBlockTypeImage},
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
