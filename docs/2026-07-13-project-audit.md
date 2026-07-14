# Tumnus Project Audit

**Date:** 2026-07-13  
**Readiness:** 66/100 — risky for exposed dashboard use

The routing plugin is compact, race-tested, and consistent with the current
routing contract. The dashboard has several trust-boundary and concurrency
problems that should be fixed before it is exposed beyond localhost.

## High-priority findings

### 1. Stored XSS in the dashboard

**Evidence:** `cmd/dashboard/ui.html:55-63`, `cmd/dashboard/ui.html:88-93`

Configuration and classifier response values are interpolated into
`innerHTML`. A malicious provider, model, fallback, domain, or route value can
execute JavaScript in the operator's browser and read the dashboard token from
`sessionStorage`.

**Recommendation:** Construct elements with `createElement` and assign untrusted
values through `textContent` or input `.value`. Add a restrictive Content
Security Policy.

### 2. Dashboard configuration saves are stale and racy

**Evidence:** `cmd/dashboard/target.go:69-90`

`BifrostTarget.lastPlugin` is shared between concurrent handlers without
synchronization. It also caches plugin metadata indefinitely. A save can race
with another load or restore stale `enabled`, `path`, `placement`, or `order`
values changed by another administrator.

**Recommendation:** Make `Save` load the latest plugin immediately before its
PUT and remove `lastPlugin`. If caching remains necessary, protect it with a
mutex and add optimistic concurrency where Bifrost supports it.

### 3. Invalid configuration can panic request handling

**Evidence:** `router/router.go:49-76`, `router/router.go:243-246`

`ParseConfig` does not reject non-positive `max_prompt_chars`. A negative value
reaches `text[:maxChars]`, causing a slice-bounds panic instead of the promised
fail-open behavior. Non-positive `timeout_ms` is also accepted and causes every
classification to fail immediately.

**Recommendation:** Require `max_prompt_chars > 0` and `timeout_ms > 0` during
configuration parsing. Add boundary tests.

## Medium-priority findings

### 4. Classification ignores request cancellation

**Evidence:** `main.go:72-75`

The classifier timeout is derived from `context.Background()` instead of the
`BifrostContext`. Classification can therefore continue after a client
disconnect or upstream deadline, consuming latency and classifier capacity.

**Recommendation:** Derive the timeout from `ctx`, which implements
`context.Context`.

### 5. Dashboard classification ignores the configured prompt limit

**Evidence:** `cmd/dashboard/main.go:82-100`

The plugin truncates prompts to `max_prompt_chars`, but `/api/classify` accepts
nearly 1 MiB and sends it directly to the classifier. The test console therefore
does not accurately reproduce production behavior and can unnecessarily load
the classifier.

**Recommendation:** Apply the same rune-aware prompt limit before calling
`Classify`.

### 6. Authentication is optional when changing the listen address

**Evidence:** `cmd/dashboard/main.go:27-34`, `cmd/dashboard/main.go:128-142`

The loopback default is safe for the documented single-operator model, but the
program permits a non-loopback bind without `--token`. That exposes live router
configuration and classifier access without authentication.

**Recommendation:** Refuse non-loopback listen addresses unless a token is
configured. Add HTTP read-header, read, write, and idle timeouts.

### 7. Configured classifier endpoints are an SSRF capability

**Evidence:** `router/router.go:64-65`, `router/router.go:189-194`

An authenticated dashboard operator can configure any URL and make the
dashboard or plugin POST to it. This is expected administrative power in a
trusted localhost deployment, but becomes an SSRF risk if dashboard access is
shared or compromised.

**Recommendation:** Document this trust boundary. For shared deployments,
restrict schemes and hosts or require an endpoint allowlist.

### 8. UTF-8 prompt truncation is byte-based

**Evidence:** `router/router.go:243-246`

The contract says `max_prompt_chars`, but truncation counts bytes and can split
a multibyte rune, producing invalid UTF-8.

**Recommendation:** Truncate by rune count and add a non-ASCII boundary test.

### 9. Dashboard lacks resource-abuse controls

**Evidence:** `cmd/dashboard/main.go:77-117`, `cmd/dashboard/main.go:135-142`

Each classify request performs a Bifrost configuration load and classifier
request. There is no rate limit or in-flight concurrency bound, and the
dashboard's fixed ten-second client timeout does not reflect configured
`timeout_ms`.

**Recommendation:** Add a small classify semaphore or rate limit and use the
configured classifier timeout.

## Lower-priority improvements

- Avoid returning raw Bifrost and classifier errors to dashboard clients;
  internal URLs and response bodies may be disclosed.
- Add `X-Content-Type-Options`, frame protection, and a Content Security Policy.
- Normalize or validate classifier `Route` values instead of relying on a broad
  `"big"` substring match.
- Validate classifier complexity as 1–5.
- Add `/healthz` and graceful shutdown to the dashboard.
- Add Dependabot, `govulncheck`, `go mod verify`, and a `SECURITY.md`.
- Add benchmarks or qualify README's approximately 100 ms latency claim as
  hardware-dependent.
- Mark historical implementation plans as superseded. Several retain unchecked
  tasks, old Supra branding, old test counts, and route semantics that differ
  from `docs/CONTRACT.md`.

## Test gaps

- Hook-level frontier routing and streaming requests
- Malformed classifier verdict fail-open behavior
- Request cancellation propagation
- Classifier request body contract
- Non-positive timeout and prompt-limit validation
- UTF-8 truncation
- Concurrent dashboard load/save behavior
- Stale plugin metadata during save
- Dashboard classifier-down and Bifrost-error responses
- Authentication coverage for `/`, `/api/config`, and `/api/classify`
- XSS regression coverage

## Confirmed strengths

- Atomic plugin-state snapshots safely handle concurrent `Init` and `Cleanup`.
- Classification failures fail open without mutating the original request.
- The decision ladder and derived fallbacks match `docs/CONTRACT.md`.
- Successful routing logs omit the raw user prompt.
- Classifier response bodies are bounded, and non-200 bodies are drained for
  connection reuse.
- CI runs vet, race-enabled tests, and Linux/macOS builds.

## Verification performed

- `go test -race ./...`: 26 tests passed across three packages
- `go vet ./...`: passed
- `go mod verify`: all modules verified
- Plugin and dashboard release builds: passed
- Statement coverage: 77.6%
- Four most recent GitHub Actions runs: passed
- Credential-pattern scan: no credential-like values found

No external vulnerability scanner was installed or run. Runtime testing against
a live Bifrost gateway and classifier was outside the available evidence.

## Recommended sequence

1. Fix dashboard XSS and add regression tests.
2. Remove or synchronize the `lastPlugin` cache and reload before every save.
3. Validate numeric limits and inherit request cancellation.
4. Require authentication for non-loopback binds and add server timeouts.
5. Add supply-chain checks, missing contract tests, benchmarks, and canonical
   documentation markers.
