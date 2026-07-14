# Tumnus Rebrand, Standalone Dashboard, and Public Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebrand the plugin to Tumnus (repo, module, names), add a standalone `tumnus-dashboard` binary (config UI + test console), and make the repo public-ready (license, CI, versioning, docs, contract).

**Architecture:** One Go module, three parts: `router/` (shared routing logic, extracted from root), root `package main` (thin Bifrost `.so` plugin), `cmd/dashboard/` (standalone server embedding a single HTML page; proxies Bifrost's management API server-side). Spec: `docs/2026-07-13-tumnus-ui-public-release-design.md`.

**Tech Stack:** Go 1.26.x stdlib only; `go:embed`; GitHub Actions; Apache-2.0.

**Verified facts (don't re-derive):**
- Bifrost management API: `GET /api/plugins/{name}` returns a flat `PluginResponse` JSON (fields include `name`, `enabled`, `path`, `placement`, `order`, `config`); `PUT /api/plugins/{name}` updates. Source: `bifrost/transports/bifrost-http/handlers/plugins.go:96-102,119`. Verify exact `PluginResponse` field names at that line before writing the proxy structs; adjust JSON tags if they differ.
- Plugin transport hooks wrap inference routes only (`server.go:1945`) — the plugin serves NO HTTP. Dashboard is standalone.
- Repo currently: `github.com/SpaceshipCreative/bifrost-suprarouter`, private, core pinned to dev pseudo-version `v1.6.4-0.20260714002449-d48dea621f55`, 21 tests passing under `-race`.
- Go binary: `/opt/homebrew/bin/go` (add to PATH in every shell).

**Working directory for all tasks:** `/Users/sjordan/Projects/bifrost-suprarouter` (will be renamed in Task 2 — `~/Projects/bifrost-tumnus` afterward).

---

### Task 1: Rebrand strings

**Files:**
- Modify: `main.go`, `router.go`, `main_test.go`, `router_test.go`, `Makefile`, `.gitignore`, `README.md`

- [ ] **Step 1: Update names**

```bash
cd /Users/sjordan/Projects/bifrost-suprarouter
export PATH=/opt/homebrew/bin:$PATH
sed -i '' 's/suprarouter:/tumnus:/g; s/x-supra-bypass/x-tumnus-bypass/g' main.go router.go main_test.go router_test.go
sed -i '' 's/return "suprarouter"/return "tumnus"/' main.go
sed -i '' 's/suprarouter\.so/tumnus.so/g' Makefile .gitignore
```

- [ ] **Step 2: Add rename regression test** (append to `main_test.go`)

```go
func TestRebrand(t *testing.T) {
	if GetName() != "tumnus" {
		t.Fatalf("GetName() = %q, want tumnus", GetName())
	}
	cfg, err := parseConfig(validRawConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BypassHeader != "x-tumnus-bypass" {
		t.Fatalf("default bypass header = %q", cfg.BypassHeader)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test -race ./...`
Expected: 22 tests PASS (existing suite + TestRebrand; the bypass-header default assertion in `TestParseConfigDefaults` was updated by the sed).

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "refactor: rebrand suprarouter -> tumnus (name, header, prefixes, artifact)"
```

---

### Task 2: Repo + module rename

- [ ] **Step 1: Name-collision check**

Run: `gh search repos tumnus --limit 5` and `curl -s "https://pkg.go.dev/search?q=tumnus" -o /dev/null -w "%{http_code}"`.
Expected: no active Go router project named Tumnus. If a direct collision exists, STOP and report to the user.

- [ ] **Step 2: Rename repo and module**

```bash
gh repo rename bifrost-tumnus --repo SpaceshipCreative/bifrost-suprarouter --yes
cd /Users/sjordan/Projects && mv bifrost-suprarouter bifrost-tumnus && cd bifrost-tumnus
git remote set-url origin https://github.com/SpaceshipCreative/bifrost-tumnus.git
sed -i '' 's|SpaceshipCreative/bifrost-suprarouter|SpaceshipCreative/bifrost-tumnus|g' go.mod
grep -rl "bifrost-suprarouter" --include="*.go" --include="*.md" . | xargs sed -i '' 's/bifrost-suprarouter/bifrost-tumnus/g'
```

- [ ] **Step 3: Verify and commit**

Run: `go test -race ./... && git add -A && git commit -m "refactor: rename module to github.com/SpaceshipCreative/bifrost-tumnus" && git push`
Expected: tests pass, push succeeds to the renamed repo (GitHub redirects the old URL).

---

### Task 3: Extract `router/` package

**Files:**
- Create: `router/router.go` (moved from root `router.go`)
- Create: `router/router_test.go` (moved from root `router_test.go`, minus plugin-hook helpers)
- Modify: `main.go`, `main_test.go`

- [ ] **Step 1: Move and export**

```bash
mkdir router && git mv router.go router/router.go && git mv router_test.go router/router_test.go
sed -i '' 's/^package main/package router/' router/router.go router/router_test.go
# Export the API (order matters: longest names first to avoid partial hits)
sed -i '' \
 -e 's/parseConfig/ParseConfig/g' -e 's/parseAnalysis/ParseAnalysis/g' \
 -e 's/chooseRole/ChooseRole/g' -e 's/defaultFallbacks/DefaultFallbacks/g' \
 -e 's/\bclassify\b/Classify/g' -e 's/extractLastUserText/ExtractLastUserText/g' \
 -e 's/\banalysis\b/Analysis/g' \
 -e 's/roleLocalSmall/RoleLocalSmall/g' -e 's/roleLocalBig/RoleLocalBig/g' \
 -e 's/roleCodeCheap/RoleCodeCheap/g' -e 's/roleFrontier/RoleFrontier/g' \
 -e 's/allRoles/AllRoles/g' \
 router/router.go router/router_test.go main.go main_test.go
```

Then hand-fix: `router/router_test.go` keeps `validRawConfig`, `fakeSupra`, `strPtr`, `userMsg` (used cross-file before; `main_test.go` needs its own copies — copy those four helpers into `main_test.go` verbatim). Struct field names inside `Analysis` (`Domain`, `Complexity`, …) are already exported. In `main.go` add the import and qualify all uses:

```go
import routerpkg "github.com/SpaceshipCreative/bifrost-tumnus/router"
```

with `routerpkg.ParseConfig`, `routerpkg.Classify`, `routerpkg.ChooseRole`, `routerpkg.DefaultFallbacks`, `routerpkg.ExtractLastUserText`, `*routerpkg.Config` in `pluginState`, `routerpkg.RoleTarget` etc. The doc comment `// Package router implements ...` replaces the old package comment.

- [ ] **Step 2: Build until clean**

Run: `go build ./... && go vet ./...` — fix any missed qualifier mechanically (the compiler lists them).

- [ ] **Step 3: Full suite**

Run: `go test -race ./...`
Expected: both packages pass; total test count unchanged (22).

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "refactor: extract shared routing logic into router/ package"
```

---

### Task 4: Pin core to a tagged release; parameterize the Makefile

**Files:**
- Modify: `Makefile`, `go.mod`

- [ ] **Step 1: Find the newest tagged core release and try it**

```bash
gh api repos/maximhq/bifrost/tags --jq '.[].name' | grep '^core/v' | head -3   # tags are like core/v1.6.3
go get github.com/maximhq/bifrost/core@<newest tag version> && go mod tidy && go test -race ./...
```

If tests fail because a tagged release lacks `PreRequestHook`/`AppendRoutingEngineLog` (dev-only APIs), revert with `git checkout go.mod go.sum`, keep the dev pseudo-version, and record in README's compatibility table that Tumnus currently requires a Bifrost build from `dev` ≥ commit `d48dea621f55`. Either outcome is acceptable; document which one happened.

- [ ] **Step 2: Parameterize Makefile**

```makefile
CORE_VERSION ?=

.PHONY: build test clean pin

pin:
ifneq ($(CORE_VERSION),)
	go get github.com/maximhq/bifrost/core@$(CORE_VERSION) && go mod tidy
endif

build: pin
	go build -buildmode=plugin -o tumnus.so .
	go build -o tumnus-dashboard ./cmd/dashboard

test:
	go test -race ./...

clean:
	rm -f tumnus.so tumnus-dashboard
```

(Add `tumnus-dashboard` to `.gitignore`.)

- [ ] **Step 3: Verify and commit**

Run: `go build -buildmode=plugin -o tumnus.so .` (the full `make build` includes the dashboard and is verified at the end of Task 5).

```bash
git add -A && git commit -m "build: parameterize core version pin (make build CORE_VERSION=...)"
```

---

### Task 5: Dashboard backend

**Files:**
- Create: `cmd/dashboard/main.go`
- Create: `cmd/dashboard/target.go`
- Create: `cmd/dashboard/ui.html` (placeholder one-liner `<html>tumnus</html>` until Task 6)
- Test: `cmd/dashboard/main_test.go`

- [ ] **Step 1: Write `cmd/dashboard/target.go`**

```go
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
```

- [ ] **Step 2: Write `cmd/dashboard/main.go`**

```go
// Command dashboard is the Tumnus control panel: a standalone server that
// edits the router config of a managed service (v1: Bifrost) and offers a
// live classification test console.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	routerpkg "github.com/SpaceshipCreative/bifrost-tumnus/router"
)

//go:embed ui.html
var uiHTML []byte

type server struct {
	target DeployTarget
	token  string
	client *http.Client
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("x-tumnus-token") != s.token {
			http.Error(w, `{"error":"missing or bad x-tumnus-token"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(uiHTML)
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.target.Load(r.Context())
		if err != nil {
			jsonOut(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		jsonOut(w, http.StatusOK, cfg)
	case http.MethodPut:
		var cfg map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if _, err := routerpkg.ParseConfig(cfg); err != nil { // validate before deploying
			jsonOut(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		if err := s.target.Save(r.Context(), cfg); err != nil {
			jsonOut(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		jsonOut(w, http.StatusOK, map[string]string{"status": "saved"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *server) handleClassify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || in.Prompt == "" {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"prompt\": \"...\"}"})
		return
	}
	raw, err := s.target.Load(r.Context())
	if err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "load config: " + err.Error()})
		return
	}
	cfg, err := routerpkg.ParseConfig(raw)
	if err != nil {
		jsonOut(w, http.StatusUnprocessableEntity, map[string]string{"error": "deployed config invalid: " + err.Error()})
		return
	}
	start := time.Now()
	a, err := routerpkg.Classify(r.Context(), s.client, cfg, in.Prompt)
	if err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "classifier: " + err.Error()})
		return
	}
	role := routerpkg.ChooseRole(a, cfg.FrontierComplexityThreshold)
	target := cfg.Roles[role]
	fallbacks := target.Fallbacks
	if len(fallbacks) == 0 {
		fallbacks = routerpkg.DefaultFallbacks(role, cfg.Roles)
	}
	jsonOut(w, http.StatusOK, map[string]any{
		"domain": a.Domain, "complexity": a.Complexity, "math": a.Math,
		"code": a.Code, "route": a.Route, "role": role,
		"target":     map[string]string{"provider": target.Provider, "model": target.Model},
		"fallbacks":  fallbacks,
		"latency_ms": time.Since(start).Milliseconds(),
	})
}

func newMux(s *server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.auth(s.handleUI))
	mux.HandleFunc("/api/config", s.auth(s.handleConfig))
	mux.HandleFunc("/api/classify", s.auth(s.handleClassify))
	return mux
}

func main() {
	listen := flag.String("listen", "127.0.0.1:9090", "listen address")
	bifrostURL := flag.String("bifrost-url", "http://localhost:8080", "Bifrost gateway base URL")
	bifrostToken := flag.String("bifrost-token", os.Getenv("TUMNUS_BIFROST_TOKEN"), "Bifrost management API token")
	token := flag.String("token", "", "require x-tumnus-token on all endpoints")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second}
	s := &server{
		target: &BifrostTarget{BaseURL: *bifrostURL, Token: *bifrostToken, PluginName: "tumnus", Client: client},
		token:  *token,
		client: client,
	}
	log.Printf("tumnus-dashboard listening on http://%s (bifrost: %s)", *listen, *bifrostURL)
	log.Fatal(http.ListenAndServe(*listen, newMux(s)))
}
```

- [ ] **Step 3: Write failing tests** (`cmd/dashboard/main_test.go`)

```go
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
```

- [ ] **Step 4: Run tests (fail) → implement → pass**

Run: `go test -race ./cmd/dashboard/` — first run fails on missing files; after writing all three files: PASS (5 tests). Then `go test -race ./...` — everything passes. `make build` now builds both artifacts.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: standalone tumnus-dashboard (config proxy + classify console backend)"
```

---

### Task 6: The UI page

**Files:**
- Modify: `cmd/dashboard/ui.html` (replace placeholder)

- [ ] **Step 1: Write `cmd/dashboard/ui.html`**

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Tumnus</title>
<style>
 :root{color-scheme:dark light}
 body{font:14px/1.5 system-ui;max-width:760px;margin:2rem auto;padding:0 1rem}
 h1{font-size:1.3rem} h2{font-size:1rem;margin-top:2rem}
 fieldset{border:1px solid #8884;border-radius:8px;margin:.8rem 0;padding:.8rem}
 label{display:block;margin:.4rem 0 .1rem;font-weight:600}
 input,textarea{width:100%;box-sizing:border-box;padding:.4rem;border:1px solid #8886;border-radius:6px;background:transparent;color:inherit}
 .row{display:grid;grid-template-columns:1fr 1fr 1.4fr;gap:.5rem}
 button{margin-top:.8rem;padding:.5rem 1.2rem;border-radius:6px;border:1px solid #8886;cursor:pointer}
 .chips span{display:inline-block;border:1px solid #8886;border-radius:999px;padding:.1rem .6rem;margin:.15rem}
 #status,#verdict{margin-top:.6rem;white-space:pre-wrap}
 .err{color:#e66}
</style>
</head>
<body>
<h1>Tumnus <small>— router control panel</small></h1>

<h2>Configuration</h2>
<form id="cfg">
 <fieldset>
  <label>Classifier endpoint <input name="endpoint" required></label>
  <label>Router model <input name="router_model"></label>
  <div class="row">
   <label>Timeout (ms) <input name="timeout_ms" type="number" min="1"></label>
   <label>Max prompt chars <input name="max_prompt_chars" type="number" min="1"></label>
   <label>Frontier threshold (1-5) <input name="frontier_complexity_threshold" type="number" min="1" max="5"></label>
  </div>
  <label>Bypass header <input name="bypass_header"></label>
 </fieldset>
 <fieldset id="roles"><legend>Roles</legend></fieldset>
 <button>Save</button><div id="status"></div>
</form>

<h2>Test console</h2>
<textarea id="prompt" rows="3" placeholder="Paste a prompt to see where it routes"></textarea>
<button id="run">Classify</button>
<div id="verdict"></div>

<script>
const ROLES=["local_small","local_big","code_cheap","frontier"];
const $=q=>document.querySelector(q);
const hdrs=()=>{const t=sessionStorage.tumnusToken;return t?{"x-tumnus-token":t}:{ }};
async function api(path,opts={}){
 const r=await fetch(path,{...opts,headers:{"Content-Type":"application/json",...hdrs(),...(opts.headers||{})}});
 if(r.status===401){sessionStorage.tumnusToken=prompt("Dashboard token:")||"";return api(path,opts);}
 const j=await r.json().catch(()=>({}));
 if(!r.ok)throw new Error(j.error||r.status);
 return j;
}
function roleRows(cfg){
 $("#roles").innerHTML='<legend>Roles</legend>'+ROLES.map(r=>{
  const t=(cfg.roles||{})[r]||{};
  const fb=(t.fallbacks||[]).map(f=>`${f.provider}/${f.model}`).join(", ");
  return `<div class="row" data-role="${r}">
   <label>${r} provider <input data-k="provider" value="${t.provider||""}" required></label>
   <label>model <input data-k="model" value="${t.model||""}" required></label>
   <label>fallbacks (provider/model, …) <input data-k="fb" value="${fb}"></label></div>`;
 }).join("");
}
function readForm(){
 const f=new FormData($("#cfg")),cfg={
  endpoint:f.get("endpoint"),router_model:f.get("router_model"),
  bypass_header:f.get("bypass_header"),
  timeout_ms:+f.get("timeout_ms"),max_prompt_chars:+f.get("max_prompt_chars"),
  frontier_complexity_threshold:+f.get("frontier_complexity_threshold"),roles:{}};
 for(const div of document.querySelectorAll("#roles [data-role]")){
  const g=k=>div.querySelector(`[data-k="${k}"]`).value.trim();
  const fb=g("fb")?g("fb").split(",").map(s=>{const[p,...m]=s.trim().split("/");return{provider:p,model:m.join("/")}}):[];
  cfg.roles[div.dataset.role]={provider:g("provider"),model:g("model"),...(fb.length?{fallbacks:fb}:{})};
 }
 return cfg;
}
async function load(){
 try{const cfg=await api("/api/config");
  for(const k of["endpoint","router_model","bypass_header","timeout_ms","max_prompt_chars","frontier_complexity_threshold"])
   if(cfg[k]!=null)$(`[name="${k}"]`).value=cfg[k];
  roleRows(cfg);
 }catch(e){$("#status").textContent="load failed: "+e.message;$("#status").className="err";roleRows({});}
}
$("#cfg").onsubmit=async e=>{e.preventDefault();const s=$("#status");s.className="";
 try{await api("/api/config",{method:"PUT",body:JSON.stringify(readForm())});s.textContent="saved — plugin reloaded";}
 catch(err){s.textContent="save failed: "+err.message;s.className="err";}};
$("#run").onclick=async()=>{const v=$("#verdict");v.className="";
 try{const r=await api("/api/classify",{method:"POST",body:JSON.stringify({prompt:$("#prompt").value})});
  v.innerHTML=`<div class="chips"><span>domain: ${r.domain||"—"}</span><span>complexity: ${r.complexity}</span>
   <span>code: ${r.code}</span><span>math: ${r.math}</span><span>route: ${r.route}</span></div>
   → <b>${r.role}</b>: ${r.target.provider}/${r.target.model}
   (fallbacks: ${(r.fallbacks||[]).map(f=>f.provider+"/"+f.model).join(" → ")||"none"}) — ${r.latency_ms} ms`;
 }catch(err){v.textContent="classify failed: "+err.message;v.className="err";}};
load();
</script>
</body>
</html>
```

- [ ] **Step 2: Verify** — `go test -race ./...` still passes; then a manual once-over: `go run ./cmd/dashboard --bifrost-url http://localhost:8080` and open `http://127.0.0.1:9090` (works fully only against a live gateway; without one the form shows "load failed" and empty role rows — expected).

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "feat: dashboard UI page (config form + test console)"
```

---

### Task 7: License, changelog

- [ ] **Step 1:**

```bash
curl -s https://www.apache.org/licenses/LICENSE-2.0.txt -o LICENSE
printf '# Changelog\n\n## v0.1.0\n\n- Initial public release: Bifrost routing plugin (Supra-Router-51M classification,\n  four-role decision ladder, fail-open) and standalone tumnus-dashboard.\n' > CHANGELOG.md
git add LICENSE CHANGELOG.md && git commit -m "chore: add Apache-2.0 license and changelog"
```

---

### Task 8: CI

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Write the workflow**

```yaml
name: ci
on:
  push: { branches: [main] }
  pull_request:
  schedule:
    - cron: "0 6 * * 1"   # weekly canary vs latest core
jobs:
  test:
    strategy:
      matrix: { os: [ubuntu-latest, macos-latest] }
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - name: canary bump (scheduled only)
        if: github.event_name == 'schedule'
        run: go get github.com/maximhq/bifrost/core@latest && go mod tidy
      - run: go vet ./...
      - run: go test -race ./...
      - run: go build -buildmode=plugin -o tumnus.so .
      - run: go build -o tumnus-dashboard ./cmd/dashboard
```

- [ ] **Step 2: Commit and verify** — `git add .github && git commit -m "ci: vet, race tests, plugin+dashboard builds; weekly core canary" && git push`, then `gh run watch` until green on both OSes.

---

### Task 9: README rewrite, CONTRACT.md, sanitization

**Files:**
- Modify: `README.md` (full rewrite)
- Create: `docs/CONTRACT.md`
- Modify: `docs/2026-07-13-supra-router-plugin-design.md`, `docs/2026-07-13-supra-router-plugin.md` (scrub)

- [ ] **Step 1: Rewrite `README.md`** — sections, in order, each 1-3 short paragraphs with the code blocks shown: **What is Tumnus** (LLM-classified request router for Bifrost: a small router model reads each chat request and routes it to the right model — named for the faun who meets travellers at the lamppost and guides them where they need to go); **How it routes** (the four-role ladder table from the current README, kept verbatim); **Classifier support** (exactly: "any classifier served over an OpenAI-compatible completions API that emits the Tumnus output format — see docs/CONTRACT.md. Default: SupraLabs/Supra-Router-51M."); **Quickstart** (generic example: `vllm serve SupraLabs/Supra-Router-51M --port 8000`, then `make build CORE_VERSION=<your bifrost core version>`, then the config.json plugin block using generic providers `openai`/`anthropic`/`my-vllm` — no personal hostnames); **Compatibility** (table: Tumnus v0.1.0 ↔ core version chosen in Task 4; the exact-version/.so constraints paragraph from the current README, plus Go-version match, Linux/macOS only); **Dashboard** (`go build -o tumnus-dashboard ./cmd/dashboard && ./tumnus-dashboard --bifrost-url http://localhost:8080 --bifrost-token $TOKEN`, flags table); **Security** (dashboard binds 127.0.0.1 by default; use `--token` beyond localhost; never expose the dashboard or gateway publicly; bypass header note); **Roadmap** (configurable prompt template/parser; adapters for LiteLLM, OpenCode, Hermes Agent, OpenClaw per CONTRACT.md; webhook-plugin adapter when Bifrost ships webhook plugins).

- [ ] **Step 2: Write `docs/CONTRACT.md`** — the portable routing contract, exactly three sections: **1. Classifier I/O** (request: OpenAI-compatible `POST /v1/completions`, prompt `Task: <text>\nAnalysis: `, `temperature: 0`, `max_tokens: 96`; response: `choices[0].text` pipe-separated `Domain: ... | Complexity: 1-5 | Math: True/False | Code: True/False | Route: small model/big model | Justification: ...`; `Route` required, all else optional with zero-value defaults, malformed optional fields ignored); **2. Decision ladder** (the four-rung first-match table with default-fallback derivation, copied from the ladder in `router/router.go` and the README); **3. Behavioral guarantees** (fail-open on any classification problem — never block or reroute unclassified requests; classify last user message only, truncated to `max_prompt_chars`; bypass header skips routing; decisions logged with domain/complexity/flags/role/target/latency). Header note: "Contract version 1. Adapters (Bifrost, LiteLLM, OpenCode, Hermes Agent, OpenClaw) implement this document; breaking changes bump the version."

- [ ] **Step 3: Sanitize** — in both older docs: `sed -i '' 's/spark-1/classifier-host/g' docs/2026-07-13-*.md`; replace the "Initial role assignments (operator's deployment)" table's model column values with `<your-fast-local-model>`, `<your-big-local-model>`, `<your-cheap-code-api>`, `<your-frontier-model>`; then scan: `git grep -iE "spark-|sjordan|spaceshipcreative\.com|api[_-]?key|token" -- ':!docs/CONTRACT.md'` and clear every hit that is personal or secret (module-path hits for the SpaceshipCreative org are fine; `--token`/`x-tumnus-token` flag hits are fine). Also scan history: `git log -p | grep -icE "apikey|secret|bearer [a-z0-9]{20}"` — expect 0.

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "docs: public README, portable routing contract, sanitization pass" && git push
```

---

### Task 10: Tag and publish (user confirmation gate)

- [ ] **Step 1: Tag**

```bash
git tag v0.1.0 && git push origin v0.1.0
```

- [ ] **Step 2: STOP — confirm with the user before flipping visibility.** Making the repo public is irreversible in effect (module proxy caching). On explicit confirmation:

```bash
gh repo edit SpaceshipCreative/bifrost-tumnus --visibility public --accept-visibility-change-consequences
```

- [ ] **Step 3: Verify public module fetch**

```bash
cd $(mktemp -d) && GOPROXY=proxy.golang.org go mod download github.com/SpaceshipCreative/bifrost-tumnus@v0.1.0 && echo OK
```

Expected: `OK` (may take a few minutes for proxy indexing; retry once).

---

## Plan self-review notes

- Spec coverage: rebrand (T1-2), module layout + dashboard + endpoints + flags + page (T3, T5, T6), deploy-target interface (T5), security defaults (T5 `--listen 127.0.0.1`, `--token`; README T9), license (T7), build-from-source + CORE_VERSION + compatibility table (T4, T9), CI + weekly canary (T8), semver + changelog (T7, T10), README rewrite + CONTRACT.md + sanitization (T9), name check + rename-before-public ordering (T2 before T10), publish gate (T10).
- The dashboard classify endpoint uses `s.client` (10 s timeout) rather than the plugin's per-request budget — intentional; the console is interactive, not on the request path.
- `GET /{$}` mux pattern requires Go ≥1.22 — satisfied (1.26).
