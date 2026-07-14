package router

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
	cfg, err := ParseConfig(validRawConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TimeoutMs != 150 || cfg.MaxPromptChars != 4000 ||
		cfg.BypassHeader != "x-tumnus-bypass" || cfg.FrontierComplexityThreshold != 5 ||
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
		if _, err := ParseConfig(raw); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestParseAnalysis(t *testing.T) {
	out := "Domain: software engineering | Complexity: 4 | Math: False | Code: True | Route: big model | Justification: multi-step code task"
	a, err := ParseAnalysis(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Domain != "software engineering" || a.Complexity != 4 || a.Math || !a.Code || a.Route != "big model" {
		t.Fatalf("bad parse: %+v", a)
	}
}

func TestParseAnalysisGarbage(t *testing.T) {
	for _, s := range []string{"", "hello world"} {
		if _, err := ParseAnalysis(s); err == nil {
			t.Errorf("%q: expected error, got nil", s)
		}
	}
}

func TestParseAnalysisMalformedComplexityIsOptional(t *testing.T) {
	a, err := ParseAnalysis("Domain: x | Complexity: banana | Route: small model")
	if err != nil {
		t.Fatalf("malformed optional field should not fail the verdict: %v", err)
	}
	if a.Complexity != 0 || a.Route != "small model" {
		t.Fatalf("bad parse: %+v", a)
	}
}

func TestParseAnalysisMissingRoute(t *testing.T) {
	if _, err := ParseAnalysis("Domain: x | Complexity: 2 | Math: False | Code: False"); err == nil {
		t.Error("expected error for missing Route field")
	}
}

func TestChooseRole(t *testing.T) {
	cases := []struct {
		name string
		a    Analysis
		want string
	}{
		{"frontier code", Analysis{Code: true, Complexity: 5, Route: "big model"}, RoleFrontier},
		{"code below threshold", Analysis{Code: true, Complexity: 4, Route: "big model"}, RoleCodeCheap},
		{"big route", Analysis{Route: "big model", Complexity: 3}, RoleLocalBig},
		{"math forces big", Analysis{Math: true, Route: "small model", Complexity: 2}, RoleLocalBig},
		{"small route", Analysis{Route: "small model", Complexity: 1}, RoleLocalSmall},
	}
	for _, tc := range cases {
		if got := ChooseRole(&tc.a, 5); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestDefaultFallbacks(t *testing.T) {
	cfg, err := ParseConfig(validRawConfig())
	if err != nil {
		t.Fatal(err)
	}
	got := DefaultFallbacks(RoleLocalBig, cfg.Roles)
	if len(got) != 2 || got[0].Model != "glm-5.2" || got[1].Model != "claude-fable-5" {
		t.Fatalf("local_big fallbacks wrong: %+v", got)
	}
	if got := DefaultFallbacks(RoleLocalSmall, cfg.Roles); len(got) != 1 || got[0].Model != "deepseek-v4-flash" {
		t.Fatalf("local_small fallbacks wrong: %+v", got)
	}
	if got := DefaultFallbacks(RoleFrontier, cfg.Roles); len(got) != 1 || got[0].Model != "glm-5.2" {
		t.Fatalf("frontier fallbacks wrong: %+v", got)
	}
	if got := DefaultFallbacks(RoleCodeCheap, cfg.Roles); len(got) != 1 || got[0].Model != "claude-fable-5" {
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
	cfg, _ := ParseConfig(validRawConfig())
	cfg.Endpoint = srv.URL
	a, err := Classify(context.Background(), srv.Client(), cfg, "prove Fermat's little theorem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !a.Math || a.Complexity != 5 {
		t.Fatalf("bad Analysis: %+v", a)
	}
}

func TestClassifyTimeout(t *testing.T) {
	srv := fakeSupra(t, "Domain: x | Route: small model", 300*time.Millisecond)
	defer srv.Close()
	cfg, _ := ParseConfig(validRawConfig())
	cfg.Endpoint = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Classify(ctx, srv.Client(), cfg, "hi"); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestClassifyBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg, _ := ParseConfig(validRawConfig())
	cfg.Endpoint = srv.URL
	if _, err := Classify(context.Background(), srv.Client(), cfg, "hi"); err == nil {
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
	if got := ExtractLastUserText(msgs, 4000); got != "second question" {
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
	if got := ExtractLastUserText(msgs, 4000); got != "part one\npart two" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractLastUserTextTruncationAndEmpty(t *testing.T) {
	long := userMsg(schemas.ChatMessageContent{ContentStr: strPtr("abcdefghij")})
	if got := ExtractLastUserText([]schemas.ChatMessage{long}, 4); got != "abcd" {
		t.Fatalf("truncation: got %q", got)
	}
	if got := ExtractLastUserText(nil, 4000); got != "" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestParseConfigNonPositiveLimits(t *testing.T) {
	for name, mutate := range map[string]func(m map[string]any){
		"zero timeout":          func(m map[string]any) { m["timeout_ms"] = 0 },
		"negative timeout":      func(m map[string]any) { m["timeout_ms"] = -5 },
		"zero prompt chars":     func(m map[string]any) { m["max_prompt_chars"] = 0 },
		"negative prompt chars": func(m map[string]any) { m["max_prompt_chars"] = -1 },
	} {
		raw := validRawConfig()
		mutate(raw)
		if _, err := ParseConfig(raw); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestTruncatePromptRuneAware(t *testing.T) {
	if got := TruncatePrompt("héllo wörld", 2); got != "hé" {
		t.Fatalf("got %q", got)
	}
	if got := TruncatePrompt("héllo", 99); got != "héllo" {
		t.Fatalf("no-op: got %q", got)
	}
	if got := TruncatePrompt("héllo", 0); got != "" {
		t.Fatalf("zero: got %q", got)
	}
	if got := ExtractLastUserText([]schemas.ChatMessage{userMsg(schemas.ChatMessageContent{ContentStr: strPtr("ééééé")})}, 3); got != "ééé" {
		t.Fatalf("extract: got %q", got)
	}
}

func TestParseAnalysisUnknownRouteFailsOpen(t *testing.T) {
	for _, s := range []string{
		"Domain: x | Route: banana",
		"Domain: x | Route: not big",
		"Domain: x | Complexity: 3",
	} {
		if _, err := ParseAnalysis(s); err == nil {
			t.Errorf("%q: expected error (must fail open on unknown Route), got nil", s)
		}
	}
}
