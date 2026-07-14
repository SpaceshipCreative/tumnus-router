// Command dashboard is the Tumnus control panel: a standalone server that
// edits the router config of a managed service (v1: Bifrost) and offers a
// live classification test console.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	// Inline script/style are the page; connect-src 'self' blocks exfiltration
	// fetches if markup injection ever slips past the client-side escaping.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; form-action 'self'")
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
	// Same limit the plugin applies, so the console reproduces production.
	a, err := routerpkg.Classify(r.Context(), s.client, cfg, routerpkg.TruncatePrompt(in.Prompt, cfg.MaxPromptChars))
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

// listenAllowed refuses a non-loopback bind unless a dashboard token is set:
// the dashboard exposes live router config and classifier access.
func listenAllowed(listen, token string) error {
	if token != "" {
		return nil
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("bad --listen address %q: %w", listen, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to listen on non-loopback %q without --token", listen)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:9090", "listen address")
	bifrostURL := flag.String("bifrost-url", "http://localhost:8080", "Bifrost gateway base URL")
	bifrostToken := flag.String("bifrost-token", os.Getenv("TUMNUS_BIFROST_TOKEN"), "Bifrost management API token")
	token := flag.String("token", "", "require x-tumnus-token on all endpoints")
	flag.Parse()

	if err := listenAllowed(*listen, *token); err != nil {
		log.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	s := &server{
		target: &BifrostTarget{BaseURL: *bifrostURL, Token: *bifrostToken, PluginName: "tumnus", Client: client},
		token:  *token,
		client: client,
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           newMux(s),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("tumnus-dashboard listening on http://%s (bifrost: %s)", *listen, *bifrostURL)
	log.Fatal(srv.ListenAndServe())
}
