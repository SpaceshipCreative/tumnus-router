package main

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	routerpkg "github.com/SpaceshipCreative/tumnus/router"
	"github.com/maximhq/bifrost/core/schemas"
)

// pluginState bundles everything Init produces so hooks can snapshot it in a
// single atomic load. Bifrost may call Cleanup/Init (unload/reload) while
// requests are in flight; hooks must never observe a half-torn-down state.
type pluginState struct {
	cfg      *routerpkg.Config
	client   *http.Client
	sessions *routerpkg.Sessions // nil when trajectory_window is 0
}

var state atomic.Pointer[pluginState]

// cascadeArmedKey marks a request Tumnus routed, non-streaming, with
// fallbacks available — the only requests the cascade may escalate.
// escalatedKey marks that the cascade already fired once for this request;
// the fallback attempt's post-hook must pass its response through unjudged.
type cascadeArmedKey struct{}
type escalatedKey struct{}

// Init parses and validates the plugin config. Invalid config fails the load.
func Init(rawConfig any) error {
	parsed, err := routerpkg.ParseConfig(rawConfig)
	if err != nil {
		return err
	}
	// One outbound call per chat request to a single classifier host: raise
	// the idle pool above DefaultTransport's 2 conns/host so bursts reuse
	// connections instead of paying TCP setup inside the timeout budget.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 32
	var sessions *routerpkg.Sessions
	if parsed.TrajectoryWindow >= 3 {
		sessions = routerpkg.NewSessions(parsed.TrajectoryWindow, time.Duration(parsed.SessionTTLSec)*time.Second)
	}
	old := state.Swap(&pluginState{
		cfg: parsed,
		client: &http.Client{
			Timeout:   time.Duration(parsed.TimeoutMs) * time.Millisecond,
			Transport: transport,
		},
		sessions: sessions,
	})
	if old != nil {
		// Reloads would otherwise strand the replaced transport's idle pool.
		// Safe for in-flight hooks on the old snapshot: only idle conns close.
		old.client.CloseIdleConnections()
	}
	return nil
}

// GetName returns the plugin's system identifier.
func GetName() string {
	return "tumnus"
}

// PreRequestHook classifies the chat request with Supra-Router-51M and routes
// it to the role-assigned model. Every failure path leaves the request
// untouched (fail-open): worst case the gateway behaves as if this plugin
// were not installed.
func PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	s := state.Load() // snapshot once; only s.* is used below
	if s == nil || req == nil || req.ChatRequest == nil {
		return nil
	}
	cfg := s.cfg
	if req.RequestType != schemas.ChatCompletionRequest && req.RequestType != schemas.ChatCompletionStreamRequest {
		return nil
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	if headers[cfg.BypassHeader] == "true" {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, "tumnus: bypass header set, skipping")
		return nil
	}
	text := routerpkg.ExtractLastUserText(req.ChatRequest.Input, cfg.MaxPromptChars)
	if text == "" {
		return nil
	}

	// Derive from the request context so a client disconnect or upstream
	// deadline cancels the classifier call instead of letting it run on.
	cctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMs)*time.Millisecond)
	defer cancel()
	start := time.Now()
	a, err := routerpkg.Classify(cctx, s.client, cfg, text)
	if err != nil {
		// Fail open: never block or reroute a request we could not classify.
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelWarn,
			fmt.Sprintf("tumnus: classification failed, passing through unchanged: %v", err))
		return nil
	}

	threshold := cfg.FrontierComplexityThreshold
	// Complexity 0 means the classifier omitted the field; never feed it to
	// the trend or a later real score would fake an escalation.
	if s.sessions != nil && a.Complexity >= 1 {
		if key := routerpkg.SessionKey(headers, cfg.SessionHeader, req.ChatRequest.Input); key != "" {
			if s.sessions.Observe(key, a.Complexity) && threshold > 1 {
				threshold-- // escalating conversation: reach frontier one rung earlier
			}
		}
	}
	role := routerpkg.ChooseRole(a, threshold)
	target := cfg.Roles[role]
	// Affinity key = the conversation's first user message: stable across
	// turns, so one conversation always hits the same instance (warm KV cache).
	affinityKey := routerpkg.ExtractFirstUserText(req.ChatRequest.Input, 200)
	provider, model := routerpkg.PickInstance(target, affinityKey)
	req.ChatRequest.Provider = schemas.ModelProvider(provider)
	req.ChatRequest.Model = model
	if len(target.Fallbacks) > 0 {
		req.ChatRequest.Fallbacks = target.Fallbacks
	} else {
		req.ChatRequest.Fallbacks = routerpkg.DefaultFallbacks(role, cfg.Roles)
	}

	// Arm the cascade only for non-streaming requests: streamed chunks are
	// already on the wire before a post-hoc gate could judge them.
	if cfg.CascadeEnabled && req.RequestType == schemas.ChatCompletionRequest && len(req.ChatRequest.Fallbacks) > 0 {
		ctx.SetValue(cascadeArmedKey{}, true)
	}

	ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo,
		fmt.Sprintf("tumnus: domain=%q complexity=%d code=%t math=%t route=%q threshold=%d -> role=%s target=%s/%s (classified in %s)",
			a.Domain, a.Complexity, a.Code, a.Math, a.Route, threshold, role, provider, model, time.Since(start).Round(time.Millisecond)))
	return nil
}

// The hooks below (except PostLLMHook) are required exports for dynamic
// plugins; this plugin does not use them.

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

// PostLLMHook gates routed, non-streaming responses. A response that is
// unusable on its face is converted into a BifrostError with fallbacks
// allowed (nil AllowFallbacks defaults to true), so Bifrost core retries the
// fallback chain the pre-hook set. Everything else passes through untouched.
func PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	s := state.Load()
	if s == nil || !s.cfg.CascadeEnabled || ctx == nil || bifrostErr != nil || resp == nil || resp.ChatResponse == nil {
		return resp, bifrostErr, nil
	}
	if armed, _ := ctx.Value(cascadeArmedKey{}).(bool); !armed {
		return resp, bifrostErr, nil
	}
	// One escalation per request: fallback-attempt responses pass through
	// even if they would fail the gate again — re-failing after the last
	// rung would surface an error where the client could have had a response.
	if esc, _ := ctx.Value(escalatedKey{}).(bool); esc {
		return resp, bifrostErr, nil
	}
	if routerpkg.AcceptResponse(resp.ChatResponse, s.cfg.CascadeMinChars) {
		return resp, bifrostErr, nil
	}
	ctx.SetValue(escalatedKey{}, true)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelWarn,
		"tumnus: response failed cascade quality gate, escalating to fallback chain")
	return nil, &schemas.BifrostError{
		Error: &schemas.ErrorField{Message: "tumnus: low-quality response, escalating to fallback"},
	}, nil
}

// Cleanup releases plugin state at shutdown (and between tests). In-flight
// hooks that already snapshotted the state finish safely on the old pointer.
func Cleanup() error {
	if old := state.Swap(nil); old != nil {
		old.client.CloseIdleConnections()
	}
	return nil
}
