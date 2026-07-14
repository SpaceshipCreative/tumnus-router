# Changelog

## Unreleased

- Validate fallback entries in ParseConfig (empty provider/model rejected).
- Treat out-of-range classifier Complexity (outside 1-5) as the zero default.
- Dashboard tokenless mode rejects cross-site requests (Origin / Sec-Fetch-Site
  checks) and non-JSON API writes; token prompt no longer loops on cancel or
  repeated bad tokens.
- Malformed-route log messages bound the embedded classifier output excerpt.

## v0.1.1

- Go 1.26.5 (fixes GO-2026-5856 / CVE-2026-42505 in crypto/tls).
- Security hardening from the 2026-07-13 audits: dashboard XSS escaping and
  security headers, fresh-load saves, numeric config validation, rune-aware
  truncation, request-cancellation propagation, loopback/token bind guard,
  Host-header check against DNS rebinding, constant-time token comparison,
  strict classifier Route validation (unknown routes fail open), idle-
  connection cleanup on plugin reload, minimal-config round-trip fix in the
  dashboard form.

## v0.1.0

- Initial public release: Bifrost routing plugin (Supra-Router-51M classification,
  four-role decision ladder, fail-open) and standalone tumnus-dashboard.
