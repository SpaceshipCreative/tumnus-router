# Tumnus Routing Contract

Contract version 1. Adapters (Bifrost, LiteLLM, OpenCode, Hermes Agent,
OpenClaw) implement this document; breaking changes bump the version.

## 1. Classifier I/O

**Request.** OpenAI-compatible `POST /v1/completions` with:

```json
{
  "model": "<router_model>",
  "prompt": "Task: <text>\nAnalysis: ",
  "temperature": 0,
  "max_tokens": 96
}
```

**Response.** `choices[0].text` is a pipe-separated verdict:

```
Domain: ... | Complexity: 1-5 | Math: True/False | Code: True/False | Route: small model/big model | Justification: ...
```

`Route` is required and must be exactly `small model` or `big model`; a
missing or unrecognized `Route` makes the **whole verdict malformed** —
adapters must fail open, never route on it. All other fields are optional
with zero-value defaults (`Domain` empty, `Complexity` 0, `Math`/`Code`
false). Malformed optional fields are ignored — an unparseable `Complexity`
never discards an otherwise-valid verdict.

## 2. Decision ladder

First match wins; the ladder is code, not config:

| Rung | Condition | Role | Default fallbacks |
|------|-----------|------|-------------------|
| 1 | `Code` && `Complexity` >= threshold (default 5) | `frontier` | `code_cheap` |
| 2 | `Code` | `code_cheap` | `frontier` |
| 3 | `Route` contains "big" or `Math` | `local_big` | `code_cheap`, `frontier` |
| 4 | otherwise | `local_small` | `local_big` |

Default fallbacks are derived from the other roles' provider/model
assignments; an explicit per-role `fallbacks` array overrides the derivation.

## 3. Behavioral guarantees

- **Fail open** on any classification problem — timeout, bad status, malformed
  verdict, missing config. Never block or reroute a request that could not be
  classified.
- Classify the **last user message only**, text blocks joined with newlines,
  truncated to `max_prompt_chars`.
- The **bypass header** (default `x-tumnus-bypass: true`) skips routing for
  that request.
- Every routing decision is **logged** with domain, complexity, code/math
  flags, chosen role, target provider/model, and classification latency.
