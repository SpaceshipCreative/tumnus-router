# Tumnus Project Re-audit

**Date:** 2026-07-13  
**Revision:** `8e620a2` (`v0.1.0`)  
**Readiness:** 68/100 — risky while main-branch CI is failing

The latest hardening commit fixed the most serious findings from
`docs/2026-07-13-project-audit.md`: stored XSS, stale shared dashboard state,
invalid numeric-limit panics, ignored request cancellation, byte-based prompt
truncation, unauthenticated non-loopback binds, and missing HTTP server
timeouts.

The project is substantially safer, but it should not cut another release until
the pinned Go toolchain vulnerability is fixed and CI is green. The dashboard
also has a configuration round-trip bug, and malformed classifier output can
violate the documented fail-open guarantee.

## Release blocker

### 1. The pinned Go version has a reachable TLS vulnerability

**Evidence:** `go.mod:3`, `.github/workflows/ci.yml:14-21`, GitHub Actions run
`29304366119`

The module declares Go 1.26.4. `govulncheck` fails on main with
`GO-2026-5856` / `CVE-2026-42505`, an Encrypted Client Hello privacy leak in
`crypto/tls`. It reports reachable call paths through the dashboard and router
HTTP clients. The vulnerability is fixed in Go 1.26.5.

The local checks passed because the local toolchain is already Go 1.26.5, while
CI uses the version declared in `go.mod`.

**Recommendation:** update the module to Go 1.26.5, rerun the full CI matrix,
and require a green main branch before another release.

## High-priority findings

### 2. The documented minimal configuration cannot be saved through the UI

**Evidence:** `README.md:63-83`, `cmd/dashboard/ui.html:70-78`,
`router/router.go:54-75`

The README's valid minimal configuration omits optional values such as
`timeout_ms`, `max_prompt_chars`, and `frontier_complexity_threshold`.
`ParseConfig` correctly supplies defaults in memory, but `GET /api/config`
returns the unnormalized source map.

The dashboard leaves omitted fields blank. `readForm` converts those blanks to
numeric zero values and sends them back. `ParseConfig` then rejects the save
because the numeric limits must be positive. A user can load the documented
configuration but cannot save it without manually filling every optional
field.

**Recommendation:** return a normalized configuration with applied defaults
from `GET /api/config`, or apply the same defaults in the form before
serialization. Add a regression test that models the browser's actual
load-read-save sequence.

### 3. Malformed classifier routes can reroute instead of failing open

**Evidence:** `router/router.go:99-142`, `docs/CONTRACT.md:19-28`,
`docs/CONTRACT.md:44-48`

`ParseAnalysis` requires `Route` to be non-empty but does not require the
contract values `small model` or `big model`. Any other value reaches
`ChooseRole` and normally selects `local_small`. For example, `Route: banana`
is treated as a successful classification and mutates the request.

That conflicts with the contract's guarantee that malformed verdicts pass
through unchanged. The broad `strings.Contains(a.Route, "big")` check can also
treat unintended text such as `not big` as a big-model verdict.

**Recommendation:** accept only the contract's enumerated route values.
Unknown routes should make classification fail open. Keep the documented
zero-value behavior for malformed optional complexity unless the contract is
deliberately revised.

### 4. Loopback-only access does not fully address browser DNS rebinding

**Evidence:** `cmd/dashboard/main.go:29-36`, `cmd/dashboard/main.go:138-155`,
`SECURITY.md:15-21`

The dashboard now correctly refuses a non-loopback bind without `--token`.
However, its default loopback mode has no authentication and does not validate
the `Host` or `Origin` header. In browser environments where private-network
protections do not block the request, DNS rebinding can make a hostile website
same-origin with a service on `127.0.0.1`.

This is a conditional browser attack rather than direct network exposure, but
the dashboard has operator-level configuration and classifier access.

**Recommendation:** validate the expected host/origin or require a token for
all browser access. For non-loopback deployments, document a TLS-terminating
reverse proxy because `x-tumnus-token` is otherwise sent over plain HTTP.

## Performance and reliability opportunities

### 5. Hot reloads leave cloned HTTP transports behind

**Evidence:** `main.go:25-42`, `main.go:124-128`

Every `Init` clones `http.DefaultTransport`. `Cleanup` drops the plugin state
without closing that transport's idle connections. Dashboard saves trigger
plugin reloads, so repeated configuration changes can leave idle connections
and transport resources alive until their own timeouts expire.

**Recommendation:** call `CloseIdleConnections` on the replaced or cleaned-up
client. This is safe for in-flight requests because it closes only idle
connections.

### 6. Text-block prompts are fully allocated before truncation

**Evidence:** `router/router.go:227-249`

For block-based messages, `ExtractLastUserText` collects every text block and
joins the complete prompt before applying `max_prompt_chars`. A large request
therefore duplicates the full text in memory even when only a small prefix is
sent to the classifier.

**Recommendation:** build the classifier prompt incrementally and stop once the
rune limit is reached. Preserve the current UTF-8-safe behavior.

### 7. Classifier concurrency is unbounded

**Evidence:** `main.go:67-77`, `cmd/dashboard/main.go:86-110`

Each eligible request creates an outbound classifier call. The larger idle
connection pool improves reuse but does not limit concurrency. A traffic burst
can saturate the classifier and hold gateway requests until their timeouts
expire.

**Recommendation:** benchmark expected concurrency, then consider a small
in-flight semaphore. When saturated, the plugin should fail open immediately
rather than queueing requests. Apply a separate bound to the dashboard test
endpoint if it can be shared.

## Other improvements

- Add `/healthz` and graceful signal-based shutdown to the dashboard.
- Stop returning raw Bifrost response bodies to dashboard clients; log detailed
  upstream errors server-side and return a bounded generic message.
- Compare dashboard tokens in constant time.
- Pin `govulncheck` rather than running `@latest`, pin GitHub Actions to reviewed
  commit SHAs, declare minimal workflow permissions, and set job timeouts.
- Add an end-to-end browser test for configuration load/save, authentication,
  and hostile classifier/config strings.
- Add malformed-route, request-cancellation, plugin-reload transport cleanup,
  and large text-block tests.
- Add a classifier load benchmark before choosing connection and concurrency
  limits.
- Document explicitly that the last user message is sent to the configured
  classifier, which may be remote.
- Document that callers able to set `x-tumnus-bypass` can defeat routing and
  cost policy; strip or protect that header at the gateway when necessary.
- Mark `docs/2026-07-13-project-audit.md` as superseded. It still reports
  findings fixed by revision `8e620a2`.
- Update `docs/2026-07-13-tumnus-ui-public-release-design.md`, whose status still
  says `Awaiting user review` even though v0.1.0 has shipped.
- Record the hardening work and the eventual Go version bump in `CHANGELOG.md`.

## Confirmed fixes and strengths

- Dashboard values interpolated into `innerHTML` are escaped, and security
  headers are present.
- Dashboard saves reload fresh plugin metadata and no longer share mutable
  cached state between handlers.
- Non-positive timeout and prompt limits are rejected.
- Prompt truncation is rune-aware.
- Plugin classification inherits request cancellation.
- The dashboard applies the deployed prompt limit before classification.
- Non-loopback dashboard binds require a token.
- The dashboard server defines header, read, write, and idle timeouts.
- Plugin state reloads use atomic snapshots and pass race testing.
- Classification failures leave the original request unchanged.
- Classifier response bodies are bounded.
- The decision ladder and default fallbacks match the canonical contract for
  valid classifier output.
- Dependabot covers Go modules and GitHub Actions.

## Verification performed

- `go test -race ./...`: 30 tests passed across three packages.
- `go vet ./...`: passed.
- `go mod verify`: all modules verified.
- Plugin release build: passed locally.
- Dashboard release build: passed locally.
- Statement coverage: 78.4%.
- Credential-pattern source scan: no credential-like values found.
- Main-branch GitHub Actions: failed at `govulncheck` because Go 1.26.4 is
  affected by `GO-2026-5856`.

No live Bifrost gateway, classifier, browser session, load benchmark, or
deployment rollback drill was available. No external vulnerability scanner was
run locally; the vulnerability finding comes from the repository's own CI
`govulncheck` result and the Go vulnerability database.

## Recommended sequence

1. Upgrade to Go 1.26.5 and restore green CI.
2. Fix dashboard default-value round-tripping and add a regression test.
3. Enforce strict classifier route values so malformed verdicts fail open.
4. Harden browser access against DNS rebinding and require TLS off-host.
5. Close idle classifier connections on plugin reload.
6. Bound prompt-block allocation and benchmark classifier concurrency.
7. Add operational endpoints, missing tests, and documentation status updates.
