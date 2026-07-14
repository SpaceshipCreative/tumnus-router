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

func TestSaveUsesFreshPluginMetadata(t *testing.T) {
	gets := 0
	var lastPut map[string]any
	bf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			order := 0
			if gets > 1 {
				order = 7 // another admin changed it between load and save
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "tumnus", "enabled": true, "order": order, "config": validConfig(),
			})
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&lastPut)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "ok"})
		}
	}))
	defer bf.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	s := &server{target: &BifrostTarget{BaseURL: bf.URL, PluginName: "tumnus", Client: client}, client: client}
	ts := httptest.NewServer(newMux(s))
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/api/config") // first GET
	resp.Body.Close()
	buf, _ := json.Marshal(validConfig())
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", strings.NewReader(string(buf)))
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != 200 {
		t.Fatalf("PUT status %d", resp2.StatusCode)
	}
	if lastPut["order"].(float64) != 7 {
		t.Fatalf("save used stale metadata: %+v", lastPut)
	}
}

func TestListenAllowed(t *testing.T) {
	cases := []struct {
		listen, token string
		ok            bool
	}{
		{"127.0.0.1:9090", "", true},
		{"localhost:9090", "", true},
		{"[::1]:9090", "", true},
		{"0.0.0.0:9090", "", false},
		{"192.168.1.10:9090", "", false},
		{"0.0.0.0:9090", "sekrit", true},
	}
	for _, tc := range cases {
		err := listenAllowed(tc.listen, tc.token)
		if (err == nil) != tc.ok {
			t.Errorf("listenAllowed(%q,%q) = %v, want ok=%v", tc.listen, tc.token, err, tc.ok)
		}
	}
}

// TestConfigMinimalSaves guards the README's minimal config (endpoint+roles,
// defaults omitted) — exactly what the UI sends when optional fields are blank.
func TestConfigMinimalSaves(t *testing.T) {
	ts, lastPut := newTestServer(t, "")
	buf, _ := json.Marshal(validConfig())
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", strings.NewReader(string(buf)))
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("minimal config PUT status %d", resp.StatusCode)
	}
	if (*lastPut)["config"] == nil {
		t.Fatal("no PUT recorded")
	}
}

func TestLoopbackHostHeaderEnforced(t *testing.T) {
	ts, _ := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/config", nil)
	req.Host = "evil.example.com" // DNS-rebound page's Host survives to the server
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebound Host: want 403, got %d", resp.StatusCode)
	}
	resp2, _ := http.Get(ts.URL + "/api/config")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("loopback Host: want 200, got %d", resp2.StatusCode)
	}
}
