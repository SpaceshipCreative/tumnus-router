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
