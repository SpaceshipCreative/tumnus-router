# Security Policy

## Supported versions

| Version | Supported |
|---------|-----------|
| 0.1.x   | yes       |

## Reporting a vulnerability

Report vulnerabilities privately via
[GitHub security advisories](https://github.com/SpaceshipCreative/tumnus/security/advisories/new).
Please do not open public issues for security reports.

## Trust model

The dashboard and the plugin config are operator-level controls: anyone who
can edit the router config can point the classifier endpoint at any URL
(SSRF-equivalent power) and steer traffic between models. Keep the dashboard
on loopback or behind `--token`, and treat Bifrost's management API token as
an admin credential.

Two more facts operators should know:

- **Prompt privacy:** the last user message of every routed chat request is
  sent to the configured classifier endpoint, which may be remote. Point
  `endpoint` only at infrastructure you trust with prompt content.
- **Bypass header:** any caller who can set `x-tumnus-bypass: true` skips
  routing (and whatever cost policy it implements). Strip or overwrite that
  header at your edge if untrusted clients can reach the gateway. Off-host
  dashboard deployments should sit behind a TLS-terminating reverse proxy —
  `x-tumnus-token` is otherwise sent in cleartext.
