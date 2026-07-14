# Tumnus

## What is Tumnus

Tumnus is an LLM-classified request router for [Bifrost](https://github.com/maximhq/bifrost):
a small router model reads each chat request and routes it to the right model.
No routing rules to maintain — configure which model plays which role and done.
It is named for the faun who meets travellers at the lamppost and guides them
where they need to go.

The plugin classifies the last user message (domain, complexity 1-5, code/math
flags, small/big verdict) and rewrites the request's provider, model, and
fallbacks before Bifrost dispatches it.

## How it routes

The decision ladder is fixed by design ("the ladder is code, not config");
first match wins:

1. code && complexity >= threshold (default 5) -> `frontier` (fallback: `code_cheap`)
2. code -> `code_cheap` (fallback: `frontier`)
3. big-model verdict or math -> `local_big` (fallbacks: `code_cheap`, `frontier`)
4. otherwise -> `local_small` (fallback: `local_big`)

## Classifier support

Any classifier served over an OpenAI-compatible completions API that emits the
Tumnus output format — see [docs/CONTRACT.md](docs/CONTRACT.md). Default:
[SupraLabs/Supra-Router-51M](https://huggingface.co/SupraLabs/Supra-Router-51M).

## Quickstart

Serve the router model:

```bash
vllm serve SupraLabs/Supra-Router-51M --port 8000
```

Build the plugin against the core version your Bifrost binary uses:

```bash
make build CORE_VERSION=v1.7.0   # produces tumnus.so and tumnus-dashboard
```

Register in Bifrost's `config.json` (requires a dynamically-built Bifrost
binary — see Bifrost's `docs/plugins/building-dynamic-binary.mdx`).
`pre_builtin` placement makes the routing decision land before governance —
any CEL routing rule you add later still overrides it (manual escape hatch):

```json
{
  "plugins": [
    {
      "name": "tumnus",
      "enabled": true,
      "path": "/path/to/tumnus.so",
      "placement": "pre_builtin",
      "order": 0,
      "config": {
        "endpoint": "http://classifier-host:8000/v1/completions",
        "roles": {
          "local_small": { "provider": "my-vllm", "model": "your-fast-local-model" },
          "local_big":   { "provider": "my-vllm", "model": "your-big-local-model" },
          "code_cheap":  { "provider": "openai", "model": "your-cheap-code-model" },
          "frontier":    { "provider": "anthropic", "model": "your-frontier-model" }
        }
      }
    }
  ]
}
```

Optional config keys (defaults): `router_model` (`SupraLabs/Supra-Router-51M`),
`timeout_ms` (150), `max_prompt_chars` (4000), `bypass_header` (`x-tumnus-bypass`),
`frontier_complexity_threshold` (5), per-role `fallbacks` array (derived from
the ladder when omitted).

Notes:

- All four role providers must already exist in Bifrost's provider config.
- Send header `x-tumnus-bypass: true` to skip routing for one request.
- Any classification failure fails open: the request proceeds unchanged.
- Decisions appear in Bifrost's Routing Decision Logs (`routing-rule` engine).

## Compatibility

| Tumnus | Bifrost core |
|--------|--------------|
| v0.1.0 | v1.7.0 |

Go plugins are exact-version constructs: the `github.com/maximhq/bifrost/core`
version compiled into `tumnus.so` must match the Bifrost binary loading it, and
the Go toolchain version must match your Bifrost build (see `go.mod`).
macOS/Linux only, no cross-compilation. Use `make build CORE_VERSION=<version>`
to re-pin before building.

## Dashboard

`tumnus-dashboard` is a standalone control panel: a config editor plus a live
classification test console. It talks to Bifrost's management API server-side;
the plugin itself serves no HTTP.

```bash
go build -o tumnus-dashboard ./cmd/dashboard
./tumnus-dashboard --bifrost-url http://localhost:8080 --bifrost-token $TOKEN
```

| Flag | Default | Purpose |
|------|---------|---------|
| `--listen` | `127.0.0.1:9090` | Dashboard listen address |
| `--bifrost-url` | `http://localhost:8080` | Bifrost gateway base URL |
| `--bifrost-token` | `$TUMNUS_BIFROST_TOKEN` | Bifrost management API token (sent as `Authorization: Bearer`) |
| `--token` | (none) | Require `x-tumnus-token` on all dashboard endpoints |

## Security

The dashboard binds `127.0.0.1` by default. If you move it beyond localhost,
set `--token` — and never expose the dashboard or the gateway management API
publicly. The bypass header (`x-tumnus-bypass`) lets any caller skip routing
for a request; treat it as an operator convenience, not an access control.

## Roadmap

- Configurable prompt template/parser for other router models.
- Adapters for LiteLLM, OpenCode, Hermes Agent, and OpenClaw per
  [docs/CONTRACT.md](docs/CONTRACT.md).
- Webhook-plugin adapter when Bifrost ships webhook plugins.
