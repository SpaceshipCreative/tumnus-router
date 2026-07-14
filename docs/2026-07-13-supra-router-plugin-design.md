> **Historical document** (superseded 2026-07-13): kept for design context. Naming, test counts, and route semantics may be outdated — docs/CONTRACT.md and the README are canonical.


# Supra Router Plugin — Design Spec

**Date:** 2026-07-13
**Status:** Approved for planning
**Base branch:** `dev`

## Goal

Offload the majority of LLM traffic to self-hosted models while preserving frontier
capability for tasks that need it — with **zero manual route configuration**. A small
router LLM ([SupraLabs/Supra-Router-51M](https://huggingface.co/SupraLabs/Supra-Router-51M))
classifies every chat request and the plugin routes it directly. The user configures
*which model plays which role*, not routes.

## Non-goals

- No CEL routing rules are required or created. (Because the plugin runs `pre_builtin`,
  any rules the operator adds later run *after* it and can override its decision —
  that is the manual escape hatch, and it comes free from Bifrost's plugin sequencing.)
- No replacement of Bifrost's built-in keyword Complexity Router; it stays untouched
  and unused by this design.
- Only chat completion requests are routed. Embeddings, images, audio, etc. pass through.

## Architecture

A native Go plugin, `plugins/suprarouter/`, implementing `schemas.LLMPlugin`
(pattern-matched on `plugins/mocker/`). Loaded with `placement: pre_builtin, order: 0`
so its routing decision lands before the governance plugin and the
model-catalog-resolver (see `docs/plugins/sequencing.mdx`).

All logic lives in `PreRequestHook` (`core/schemas/plugin.go:297`) — the canonical
routing phase; mutations to provider/model/fallbacks commit once per request and
propagate to every fallback attempt.

### Data flow

1. **Scope guard** — act only when `RequestType` is chat and the request has a
   text-bearing last user message. Skip when the bypass header (`x-supra-bypass: true`)
   is present. Everything skipped passes through untouched.
2. **Extract** the last user message's text (concatenate text blocks, ignore
   image/file blocks), truncated to `max_prompt_chars` (default 4000).
3. **Classify** — POST to the Supra vLLM endpoint (`/v1/completions`) with prompt
   `Task: <text>\nAnalysis: `, `temperature: 0`, `max_tokens: 96`, within
   `timeout_ms` (default 150). Plain HTTP client — the call does **not** re-enter
   Bifrost, so there is no recursion risk.
4. **Parse** the pipe-separated output into
   `{Domain string, Complexity int, Math bool, Code bool, Route string}`.
5. **Route** via the built-in decision ladder (below): set provider/model with
   `req.SetProvider` / `req.SetModel`, and set the fallback chain.
6. **Log the decision** using the native routing-observability convention:
   `ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, ...)` with domain,
   complexity, flags, chosen role/target, and classification latency, plus
   `schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, ...)`
   so decisions appear in the Routing Decision Logs UI.

### Decision ladder (baked into the plugin)

Four **roles**, evaluated top-down, first match wins:

| # | Condition (from Supra output) | Role | Default fallbacks |
|---|---|---|---|
| 1 | `Code` and `Complexity >= frontier_complexity_threshold` (default 5) | `frontier` | `code_cheap` |
| 2 | `Code` | `code_cheap` | `frontier` |
| 3 | `Route == "big model"` or `Math` | `local_big` | `code_cheap`, `frontier` |
| 4 | otherwise (`Route == "small model"`) | `local_small` | `local_big` |

The ladder is code, not config — that is the point of the plugin. Only the
role→model assignments are configurable.

### Initial role assignments (operator's deployment)

| Role | Provider | Model |
|---|---|---|
| `local_small` | local vLLM (custom provider) | `<your-fast-local-model>` |
| `local_big` | local vLLM (custom provider) | `<your-big-local-model>` |
| `code_cheap` | Ollama Cloud | `<your-cheap-code-api>` |
| `frontier` | Anthropic | `<your-frontier-model>` |

All four targets must already exist as providers in Bifrost's provider config;
the plugin only points at them.

## Plugin config

```json
{
  "endpoint": "http://classifier-host:8000/v1/completions",
  "router_model": "SupraLabs/Supra-Router-51M",
  "timeout_ms": 150,
  "max_prompt_chars": 4000,
  "bypass_header": "x-supra-bypass",
  "frontier_complexity_threshold": 5,
  "roles": {
    "local_small": { "provider": "vllm-local", "model": "qwen3.6-35b-a3b" },
    "local_big":   { "provider": "vllm-local", "model": "deepseek-v4-flash" },
    "code_cheap":  { "provider": "ollama-cloud", "model": "glm-5.2" },
    "frontier":    { "provider": "anthropic", "model": "claude-fable-5" }
  }
}
```

Each role optionally accepts a `"fallbacks": [{provider, model}, ...]` array;
when omitted, the defaults from the decision ladder table are derived from the
other roles' assignments.

`Init` validates: endpoint non-empty, all four roles present, threshold in 1–5.
Invalid config fails plugin load (fail-fast at startup, never at request time).

## Error handling — fail open, always

Any classification problem → the request proceeds **exactly as the client sent it**,
with a warning in the routing-engine log and an internal fail-open counter:

- Supra endpoint unreachable / timeout
- Non-200 response or unparseable analysis string
- Empty/no-text user message

The plugin can never block or fail a request; worst case the gateway behaves as if
the plugin were not installed. Target-model failures at request time are handled by
Bifrost's native fallback machinery via the chains the plugin sets.

## Streaming and multi-turn

- Streaming needs nothing special: `PreRequestHook` runs before the provider call
  for streaming and non-streaming alike.
- Multi-turn: classify the **last user message only**. Conversation-history blending
  is deliberately out of scope until misrouting on continuations ("do it", "continue")
  is observed in practice.

## Deployment

- **Supra serving:** one more model on the existing vLLM fleet
  (`vllm serve SupraLabs/Supra-Router-51M`), OpenAI-compatible completions API.
  51M params — fits in leftover VRAM anywhere.
- **Plugin build:** standalone Go module compiled as a dynamic plugin
  (`go build -buildmode=plugin`), loaded via the `plugins` array in `config.json`
  with `placement: pre_builtin, order: 0`. Constraints per
  `docs/plugins/writing-go-plugin.mdx`: Go version must match Bifrost's (1.26.1),
  Linux/macOS only, built on the target platform, dynamic Bifrost binary required.

## Testing

Unit tests in the plugin package (pattern: `plugins/mocker/plugin_test.go`):

- **Parser:** well-formed analysis string, truncated output, garbage, missing fields.
- **Decision ladder:** each rung, threshold boundary (complexity 4 vs 5),
  derived vs explicit fallbacks.
- **PreRequestHook** against an `httptest` fake Supra server: happy path routes and
  sets fallbacks; timeout → fail-open (request unchanged); bypass header honored;
  non-chat request untouched; empty user message untouched.
- **Config validation:** missing role, bad threshold, empty endpoint each fail `Init`.

## Open items for the implementation plan

- Confirm the custom-provider names for the two local vLLM boxes and Ollama Cloud
  as configured in the operator's Bifrost instance (config values, not design).
