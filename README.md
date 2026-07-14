# Tumnus 🏮

**Every prompt deserves the right model. Tumnus makes sure it gets there.**

Most LLM traffic is mismatched: trivial questions burning frontier-model money,
hard problems timing out on small local models, code prompts landing anywhere.
Tumnus fixes the mismatch at the gateway. A tiny 51M-parameter router model
reads each chat request in less than 100 ms (hardware-dependent) and
automatically routes it to the model best suited for your request —
chit-chat to your fast local model, code to your cheap code model, send genuinely
hard problems to the frontier. No routing rules to write, no regexes to
maintain. Configure which model plays which role and you're done.

Tumnus ships as a [Bifrost](https://github.com/maximhq/bifrost) plugin plus a
standalone dashboard, with a portable routing contract for future adapters.

## Why "Tumnus"?

In *The Lion, the Witch and the Wardrobe*, Mr. Tumnus is the faun who meets
Lucy at the lamppost — the first face travellers see in a strange land, the
one who knows the terrain and guides you where you need to go. That's this
router: it stands just inside your gateway, meets every request as it arrives,
and walks it to the right destination.

And like the faun, it would rather fail you kindly than hand you over to the
wrong power: **any classification problem fails open**. Timeout, garbled
verdict, classifier down — the request passes through untouched, as if Tumnus
were never installed. Worst case, you find your own way home.

## How it routes

The router model classifies the last user message — domain, complexity 1–5,
code/math flags, small/big verdict — and Tumnus walks a fixed decision ladder
("the ladder is code, not config"); first match wins:

1. code && complexity >= threshold (default 5) -> `frontier` (fallback: `code_cheap`)
2. code -> `code_cheap` (fallback: `frontier`)
3. big-model verdict or math -> `local_big` (fallbacks: `code_cheap`, `frontier`)
4. otherwise -> `local_small` (fallback: `local_big`)

Fallback chains are derived from the ladder automatically; override per role if
you like.

## Quickstart

Serve the router model (any OpenAI-compatible completions server works):

```bash
vllm serve SupraLabs/Supra-Router-51M --port 8000
```

Build the plugin against the core version your Bifrost binary uses:

```bash
make build CORE_VERSION=v1.7.0   # produces tumnus.so and tumnus-dashboard
```

Register it in Bifrost's `config.json` (requires a dynamically-built Bifrost
binary — see Bifrost's `docs/plugins/building-dynamic-binary.mdx`).
`pre_builtin` placement makes the routing decision land before governance, so
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

Good to know:

- All four role providers must already exist in Bifrost's provider config.
- Send header `x-tumnus-bypass: true` to skip routing for one request.
- Any classification failure fails open: the request proceeds unchanged.
- Decisions appear in Bifrost's Routing Decision Logs (`routing-rule` engine).

## Classifier support

Any classifier served over an OpenAI-compatible completions API that emits the
Tumnus output format — see [docs/CONTRACT.md](docs/CONTRACT.md). Default:
[SupraLabs/Supra-Router-51M](https://huggingface.co/SupraLabs/Supra-Router-51M).

## The dashboard

`tumnus-dashboard` is a standalone control panel: edit the deployed router
config in a form, and paste any prompt into the test console to see exactly
where it would route (domain, complexity, chosen role, target, fallbacks,
latency) — live, against your running gateway. It talks to Bifrost's
management API server-side; the plugin itself serves no HTTP.

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

## Compatibility

| Tumnus | Bifrost core |
|--------|--------------|
| v0.1.1 | v1.7.0 |
| v0.1.0 | v1.7.0 |

Go plugins are exact-version constructs: the `github.com/maximhq/bifrost/core`
version compiled into `tumnus.so` must match the Bifrost binary loading it, and
the Go toolchain version must match your Bifrost build (see `go.mod`).
macOS/Linux only, no cross-compilation. Use `make build CORE_VERSION=<version>`
to re-pin before building.

## Security

The dashboard binds `127.0.0.1` by default and refuses non-loopback binds
unless `--token` is set — and never expose the dashboard or the gateway
management API publicly. The bypass header (`x-tumnus-bypass`) lets any caller
skip routing for a request; treat it as an operator convenience, not an access
control.

Editing the router config is operator-level power: whoever can save it can
point the classifier endpoint at any URL the plugin and dashboard can reach
(SSRF-equivalent). Restrict config access accordingly; see
[SECURITY.md](SECURITY.md) for the full trust model.

## Roadmap

- Configurable prompt template/parser for other router models.
- Adapters for LiteLLM, OpenCode, Hermes Agent, and OpenClaw per
  [docs/CONTRACT.md](docs/CONTRACT.md).
- Webhook-plugin adapter when Bifrost ships webhook plugins.

## License

[Apache-2.0](LICENSE).
