# Tumnus: Rebrand, Embedded Config UI, and Public Release — Design Spec

**Date:** 2026-07-13
**Status:** Awaiting user review
**Builds on:** `docs/2026-07-13-supra-router-plugin-design.md` (the shipped v1 plugin)

## Goal

Turn the private `suprarouter` Bifrost plugin into **Tumnus**, a public open-source
project: an LLM-classified request router that meets every request at the gateway
threshold and guides it to the right model. This spec covers three coupled pieces:

1. Full rebrand (plugin, repo, module path) to Tumnus
2. A standalone dashboard (`tumnus-dashboard`) for configuring the router —
   strictly an add-on, **zero modifications to Bifrost core or its UI** — that
   runs on the same machine as the services it manages
3. Public-release requirements (license, CI, versioning, security defaults,
   documentation, portability contract)

## 1. Rebrand

| Item | From | To |
|---|---|---|
| Plugin name (`GetName`, config.json entry) | `suprarouter` | `tumnus` |
| Log/error prefixes | `suprarouter:` | `tumnus:` |
| Default bypass header | `x-supra-bypass` | `x-tumnus-bypass` |
| Dashboard binary | — | `tumnus-dashboard` (default `127.0.0.1:9090`) |
| GitHub repo | `SpaceshipCreative/bifrost-tumnus` | `SpaceshipCreative/bifrost-tumnus` |
| Go module path | `github.com/SpaceshipCreative/bifrost-tumnus` | `github.com/SpaceshipCreative/bifrost-tumnus` |
| Build artifact | `suprarouter.so` | `tumnus.so` |

Config schema keys are already vendor-neutral (`endpoint`, `router_model`, `roles`,
…) and do not change. `SupraLabs/Supra-Router-51M` remains only the *default*
`router_model` value.

The rename must complete **before** the repo goes public: proxy.golang.org caches
public module paths permanently, so renaming after publication breaks importers.
Before the rename, do a quick search for existing software projects/trademarks
named "Tumnus" (low risk — character names for unrelated software are generally
fine — but cheap insurance against renaming twice).

## 2. Standalone dashboard

**Architecture decision record:** the original design served the UI from the
plugin's `HTTPTransportPreHook`. Verification against the transport source
killed that: `TransportInterceptorMiddleware` wraps inference routes only
(`transports/bifrost-http/server/server.go:1945`), so plugin hooks never fire
for a browser's `GET /tumnus`. The replacement — and the better fit for the
project's multi-service future — is a standalone dashboard process that runs on
the same machine as the services it manages.

### Module layout (one Go module, three parts)

- `router/` — shared package holding all routing logic, extracted from today's
  root-package `router.go`: `Config`, `ParseConfig`, `ParseAnalysis`,
  `ChooseRole`, `DefaultFallbacks`, `Classify`, `ExtractLastUserText` (names
  exported on extraction).
- root `package main` — the Bifrost `.so` plugin, now thin glue importing
  `router/`. Its HTTP transport hooks stay pure no-ops.
- `cmd/dashboard/` — standalone binary `tumnus-dashboard`, embedding the UI
  page via `go:embed`.

### Dashboard endpoints

- `GET /` → single self-contained HTML/JS page (no framework, no build step).
- `POST /api/classify` → body `{"prompt": "..."}`; calls the shared
  `router.Classify` + `router.ChooseRole` directly against the currently-loaded
  config and returns `{domain, complexity, math, code, route, role,
  target: {provider, model}, fallbacks, latency_ms}`. Classifier failure →
  JSON error. No plugin involvement.
- `GET /api/config` / `PUT /api/config` → server-side proxy to Bifrost's
  management API (`GET/PUT /api/plugins/tumnus`), preserving
  `enabled/path/placement/order`. A save persists in Bifrost's store and
  triggers the plugin hot-reload (`Cleanup` → `Init`), which is already
  race-safe. The browser never talks to Bifrost directly — no CORS, and the
  gateway credential lives server-side.

### Dashboard configuration (flags/env)

- `--listen` (default `127.0.0.1:9090`) — localhost-only by default.
- `--bifrost-url` (e.g. `http://localhost:8080`) and `--bifrost-token`
  (or `TUMNUS_BIFROST_TOKEN`) for the management API.
- `--token` (optional): when set, all dashboard endpoints require
  `x-tumnus-token: <value>`.

### The page

One HTML file, two panels:

- **Config form:** the six scalar settings (`endpoint`, `router_model`,
  `timeout_ms`, `max_prompt_chars`, `bypass_header`,
  `frontier_complexity_threshold`) plus four role rows (provider, model,
  optional fallback chain entered as `provider/model` list). Client-side
  validation mirrors `ParseConfig` rules. On save: "saved — plugin reloaded".
- **Test console:** textarea → Classify → verdict rendered as labeled chips
  (domain, complexity, code/math flags), the chosen role/target, and the
  fallback chain. This is the threshold-tuning tool; it exercises the live
  classifier without sending real traffic.

### Deploy targets (multi-service future, YAGNI-scoped)

The dashboard's config write path is one small interface —
`type DeployTarget interface { Load(ctx) (map[string]any, error);
Save(ctx, map[string]any) error }` — with exactly **one** v1 implementation:
Bifrost via its management API. Future adapters for LiteLLM, OpenCode, Hermes
Agent, and OpenClaw (config-file writers or their own APIs) slot in behind the
same interface, each arriving with its own spec. Designed-for, not built.

## 3. Security posture (public defaults)

- The dashboard binds `127.0.0.1` by default; exposing it on a LAN/interface is
  an explicit `--listen` choice.
- Optional `--token` gates every dashboard endpoint via `x-tumnus-token`.
- The Bifrost credential is held by the dashboard process, never sent to the
  browser; the page contains no secrets.
- The plugin itself serves nothing — its HTTP attack surface is zero.
- README carries a loud "exposure" section: don't bind the dashboard publicly,
  use `--token` beyond localhost, and general advice against exposing gateway
  ports.

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

- `router/` package: the existing unit suite moves with the extraction and
  keeps passing unchanged in behavior.
- Dashboard (`httptest` against its handler): `GET /` returns the embedded
  HTML; classify endpoint happy path / classifier-down; config proxy round-trip
  against a fake Bifrost management API (preserves `enabled/path/placement/
  order`); `--token` enforcement (401 without header when set).
- Plugin: rename regressions (`GetName() == "tumnus"`, default bypass header
  `x-tumnus-bypass`); transport hooks remain no-ops.
- Existing 20-test suite continues to pass under `-race` after the rename.
- CI runs the full suite on both platforms.
- Manual: browser once-over of the page against a live gateway (form load, save
  + hot-reload, test console verdicts).
