# Tumnus: Rebrand, Embedded Config UI, and Public Release — Design Spec

**Date:** 2026-07-13
**Status:** Awaiting user review
**Builds on:** `docs/2026-07-13-supra-router-plugin-design.md` (the shipped v1 plugin)

## Goal

Turn the private `suprarouter` Bifrost plugin into **Tumnus**, a public open-source
project: an LLM-classified request router that meets every request at the gateway
threshold and guides it to the right model. This spec covers three coupled pieces:

1. Full rebrand (plugin, repo, module path) to Tumnus
2. An embedded configuration UI served by the plugin itself — strictly an add-on,
   **zero modifications to Bifrost core or its UI**
3. Public-release requirements (license, CI, versioning, security defaults,
   documentation, portability contract)

## 1. Rebrand

| Item | From | To |
|---|---|---|
| Plugin name (`GetName`, config.json entry) | `suprarouter` | `tumnus` |
| Log/error prefixes | `suprarouter:` | `tumnus:` |
| Default bypass header | `x-supra-bypass` | `x-tumnus-bypass` |
| UI route | — | `/tumnus` |
| GitHub repo | `SpaceshipCreative/bifrost-suprarouter` | `SpaceshipCreative/bifrost-tumnus` |
| Go module path | `github.com/SpaceshipCreative/bifrost-suprarouter` | `github.com/SpaceshipCreative/bifrost-tumnus` |
| Build artifact | `suprarouter.so` | `tumnus.so` |

Config schema keys are already vendor-neutral (`endpoint`, `router_model`, `roles`,
…) and do not change. `SupraLabs/Supra-Router-51M` remains only the *default*
`router_model` value.

The rename must complete **before** the repo goes public: proxy.golang.org caches
public module paths permanently, so renaming after publication breaks importers.
Before the rename, do a quick search for existing software projects/trademarks
named "Tumnus" (low risk — character names for unrelated software are generally
fine — but cheap insurance against renaming twice).

## 2. Embedded configuration UI

### Serving architecture

The plugin's `HTTPTransportPreHook` (currently a no-op) gains a small router.
It short-circuits exactly two paths and passes everything else through untouched:

- `GET /tumnus` → a single self-contained HTML/JS page embedded via `go:embed`
  (no framework, no build step; plain `fetch` + a form).
- `POST /tumnus/api/classify` → body `{"prompt": "..."}`; runs the real
  `classify` + `chooseRole` against the current atomic state snapshot and returns
  `{domain, complexity, math, code, route, role, target: {provider, model},
  fallbacks, latency_ms}`. Classifier failure → JSON error mirroring the
  fail-open log message. Plugin state nil (mid-reload) → `503` JSON.
- Any other path → `(nil, nil)` pass-through.

The UI is gated by config flag `ui_enabled` (**default `false`** — see Security).
When disabled, the hook is a pure pass-through, identical to today.

### Config persistence — Bifrost stays the source of truth

The page's JS reads current config from Bifrost's existing management API
(`GET /api/plugins/tumnus`, same-origin so the browser's gateway session auth
applies) and saves via `PUT /api/plugins/tumnus`, preserving
`enabled/path/placement/order` from the GET. A save persists in Bifrost's store
and triggers the plugin hot-reload (`Cleanup` → `Init`) path, which is already
race-safe (atomic state snapshot). The plugin adds **no persistence code**.

### The page

One HTML file, two panels:

- **Config form:** the six scalar settings (`endpoint`, `router_model`,
  `timeout_ms`, `max_prompt_chars`, `bypass_header`,
  `frontier_complexity_threshold`) plus four role rows (provider, model,
  optional fallback chain entered as `provider/model` list). Client-side
  validation mirrors `parseConfig` rules. On save: "saved — plugin reloaded".
- **Test console:** textarea → Classify → verdict rendered as labeled chips
  (domain, complexity, code/math flags), the chosen role/target, and the
  fallback chain. This is the threshold-tuning tool; it exercises the live
  classifier without sending real traffic.

### Known risk, resolved first in the plan

Whether `HTTPTransportPreHook` fires for paths Bifrost core doesn't route
(`/tumnus` would otherwise 404) is unverified. Implementation task #1 is a spike
against the transport code; fallback is serving under a path pattern the
transport does expose to plugins. Everything else is prefix-independent.

## 3. Security posture (public defaults)

The transport pre-hook runs before Bifrost core, so the page and classify
endpoint are served **without Bifrost's auth**. For a public project the
defaults must be safe for someone who exposes their gateway:

- `ui_enabled` defaults to **false**. Enabling it is an explicit opt-in.
- Optional `ui_token` config: when set, `POST /tumnus/api/classify` requires
  header `x-tumnus-token: <value>`; the page prompts for it and keeps it in
  memory only. When unset, the endpoint is open (LAN deployments).
- The static page contains no secrets; config values are fetched by the browser
  through the authenticated management API, so an unauthenticated visitor sees
  an empty form.
- README carries a loud "exposure" section covering the above and the general
  advice not to expose gateway ports publicly.

## 4. Public release requirements

### License

Apache-2.0 (matches Bifrost, includes patent grant). `LICENSE` file at repo root.

### Distribution model: build from source

A dynamic Go plugin only loads into a Bifrost binary built with the **exact same**
`bifrost/core` version, Go toolchain, and platform (Linux/macOS only, no
cross-compilation). Therefore prebuilt `.so` releases are not offered. Instead:

- `make build CORE_VERSION=v1.6.4` — pins `bifrost/core`, tidies, builds
  `tumnus.so`. Default `CORE_VERSION` is the newest **tagged** core release
  (replacing the current dev pseudo-version pin).
- README compatibility table: "Tumnus vX ↔ Bifrost core vY–vZ", updated per
  release.
- README documents the constraint prominently, including the Go-version match
  and platform limits.
- Watch item (documented, no action): Bifrost's docs describe webhook-based
  plugins as in development; when they land, they dissolve this constraint and
  Tumnus should grow a webhook adapter.

### CI (GitHub Actions)

On push/PR: `go vet`, `go test -race`, `go build -buildmode=plugin` on
ubuntu-latest and macos-latest. A scheduled weekly job builds against the latest
core release so upstream breakage is caught when Bifrost moves, not when a user
reports it.

### Versioning

Semver git tags from `v0.1.0`; `CHANGELOG.md` maintained per release. Tags are
load-bearing for Go module consumers.

### Documentation rewrite

README rewritten for strangers: what Tumnus is (one paragraph + the lamppost
metaphor, briefly), quickstart with **generic** examples (no internal hostnames
or the author's model lineup), the four-role decision ladder explained as an
opinionated design, the honest classifier claim (see Portability), the security
section, and the build/compatibility section.

### Pre-publication sanitization

Before flipping the repo public: a deliberate scan for secrets, internal
hostnames, and personal details across code, docs, and git history. The
committed v1 spec/plan stay as design history, but internal hostnames and the
author's model lineup in them are scrubbed to generic placeholders.

## 5. Portability: the Tumnus routing contract

The author plans adapters for **LiteLLM, OpenCode, Hermes Agent, and OpenClaw**.
These are different languages/ecosystems (Python, TypeScript, …), so the
portable artifact is not Go code — it is the **contract**, captured in this repo
as `docs/CONTRACT.md`, versioned alongside the plugin:

1. **Classifier I/O:** request = OpenAI-compatible `/v1/completions` call with
   prompt `Task: <text>\nAnalysis: `, `temperature: 0`, `max_tokens: 96`;
   response = pipe-separated fields `Domain | Complexity (1-5) | Math | Code |
   Route (small model/big model) | Justification`, with `Route` required and all
   other fields optional/defaulted.
2. **Decision ladder:** the four-rung first-match table (code+complexity≥T →
   frontier; code → code_cheap; big/math → local_big; else local_small) with the
   derived default fallback chains.
3. **Behavioral guarantees:** fail-open on any classification problem; classify
   the last user message only; bypass mechanism; decision logging fields.

Honesty requirement: v1 supports "any classifier served over an OpenAI-compatible
completions API **that emits this output format**" — the README says exactly
that. Making the prompt template and parser configurable is roadmap, not v1.

Each future adapter is its own repo (`litellm-tumnus`, etc.) implementing
`CONTRACT.md`; this repo remains the Bifrost adapter and the contract's home.

## Non-goals (v1)

- No sidecar/shared routing service for the other platforms — contract doc only.
- No configurable prompt template/parser (roadmap).
- No native Bifrost sidebar entry (requires core UI changes — ruled out).
- No stats dashboard in the UI (Routing Decision Logs already covers it).
- No prebuilt binary releases.

## Testing

- Go tests via the exported hook: `GET /tumnus` returns the embedded HTML;
  classify endpoint happy path / classifier-down / nil-state / `ui_enabled=false`
  / `ui_token` enforcement; non-Tumnus paths pass through byte-identical;
  rename regressions (`GetName() == "tumnus"`, default header).
- Existing 20-test suite continues to pass under `-race` after the rename.
- CI runs the full suite on both platforms.
- Manual: browser once-over of the page against a live gateway (form load, save
  + hot-reload, test console verdicts).
