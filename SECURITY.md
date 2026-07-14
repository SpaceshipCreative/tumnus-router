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
