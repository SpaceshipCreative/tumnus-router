package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeBifrost serves GET/PUT /api/plugins/tumnus and records the last PUT body.
func fakeBifrost(t *testing.T, config map[string]any, classifierURL string) (*httptest.Server, *map[string]any) {
	t.Helper()
	config["endpoint"] = classifierURL
	plugin := map[string]any{
		"name": "tumnus", "enabled": true, "path": "/x/tumnus.so",
		"placement": "pre_builtin", "order": 0, "config": config,
	}
	var lastPut map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/plugins/tumnus") {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(plugin)
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&lastPut)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "ok"})
		}
	}))
	return srv, &lastPut
}

func fakeClassifier(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"text":"Domain: chat | Complexity: 1 | Math: False | Code: False | Route: small model"}]}`))
	}))
}

func validConfig() map[string]any {
	return map[string]any{
		"endpoint": "http://placeholder/v1/completions",
		"roles": map[string]any{
			"local_small": map[string]any{"provider": "p1", "model": "m1"},
			"local_big":   map[string]any{"provider": "p1", "model": "m2"},
			"code_cheap":  map[string]any{"provider": "p2", "model": "m3"},
			"frontier":    map[string]any{"provider": "p3", "model": "m4"},
		},
	}
}

func newTestServer(t *testing.T, token string) (*httptest.Server, *map[string]any) {
	t.Helper()
	cls := fakeClassifier(t)
	t.Cleanup(cls.Close)
	bf, lastPut := fakeBifrost(t, validConfig(), cls.URL+"/v1/completions")
	t.Cleanup(bf.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	s := &server{
		target: &BifrostTarget{BaseURL: bf.URL, PluginName: "tumnus", Client: client},
		token:  token, client: client,
	}
	ts := httptest.NewServer(newMux(s))
	t.Cleanup(ts.Close)
	return ts, lastPut
}

func TestUIServed(t *testing.T) {
	ts, _ := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET /: %v %v", resp.StatusCode, err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: %s", ct)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	ts, lastPut := newTestServer(t, "")
	resp, _ := http.Get(ts.URL + "/api/config")
	var cfg map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&cfg)
	if cfg["roles"] == nil {
		t.Fatal("GET /api/config missing roles")
	}
	cfg["timeout_ms"] = 300
	buf, _ := json.Marshal(cfg)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", strings.NewReader(string(buf)))
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != 200 {
		t.Fatalf("PUT status %d", resp2.StatusCode)
	}
	put := *lastPut
	if put["placement"] != "pre_builtin" || put["enabled"] != true {
		t.Fatalf("PUT lost plugin fields: %+v", put)
	}
	if put["config"].(map[string]any)["timeout_ms"].(float64) != 300 {
		t.Fatalf("PUT config not updated: %+v", put["config"])
	}
}

func TestConfigValidationRejected(t *testing.T) {
	ts, _ := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", strings.NewReader(`{"endpoint":""}`))
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", resp.StatusCode)
	}
}

func TestClassifyEndpoint(t *testing.T) {
	ts, _ := newTestServer(t, "")
	resp, _ := http.Post(ts.URL+"/api/classify", "application/json", strings.NewReader(`{"prompt":"hi"}`))
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["role"] != "local_small" || out["target"].(map[string]any)["model"] != "m1" {
		t.Fatalf("bad verdict: %+v", out)
	}
}

func TestTokenEnforced(t *testing.T) {
	ts, _ := newTestServer(t, "sekrit")
	resp, _ := http.Get(ts.URL + "/api/config")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/config", nil)
	req.Header.Set("x-tumnus-token", "sekrit")
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("with token want 200, got %d", resp2.StatusCode)
	}
}
