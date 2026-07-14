# bifrost-tumnus

A [Bifrost](https://github.com/maximhq/bifrost) plugin that routes every chat
request through [SupraLabs/Supra-Router-51M](https://huggingface.co/SupraLabs/Supra-Router-51M):
the 51M-param router LLM classifies the prompt (domain, complexity 1-5, code/math
flags, small/big verdict) and the plugin picks one of four role-assigned models.
No routing rules to maintain — configure which model plays which role and done.

Design spec and implementation plan: `docs/`.

## Serve the router model

```bash
vllm serve SupraLabs/Supra-Router-51M --port 8000
```

## Decision ladder (fixed)

1. code && complexity >= threshold (default 5) -> `frontier` (fallback: `code_cheap`)
2. code -> `code_cheap` (fallback: `frontier`)
3. big-model verdict or math -> `local_big` (fallbacks: `code_cheap`, `frontier`)
4. otherwise -> `local_small` (fallback: `local_big`)

## Build

```bash
make test && make build   # produces suprarouter.so
```

Constraints: Go version must match your Bifrost build (see `go.mod`), macOS/Linux
only, no cross-compilation, and the `github.com/maximhq/bifrost/core` version must
match the Bifrost binary loading the plugin.

## Register in Bifrost `config.json`

Requires a dynamically-built Bifrost binary (see Bifrost's
`docs/plugins/building-dynamic-binary.mdx`). `pre_builtin` placement makes the
routing decision land before governance — any CEL routing rule you add later
still overrides it (manual escape hatch).

```json
{
  "plugins": [
    {
      "name": "suprarouter",
      "enabled": true,
      "path": "/path/to/suprarouter.so",
      "placement": "pre_builtin",
      "order": 0,
      "config": {
        "endpoint": "http://spark-1:8000/v1/completions",
        "roles": {
          "local_small": { "provider": "vllm-local", "model": "qwen3.6-35b-a3b" },
          "local_big":   { "provider": "vllm-local", "model": "deepseek-v4-flash" },
          "code_cheap":  { "provider": "ollama-cloud", "model": "glm-5.2" },
          "frontier":    { "provider": "anthropic", "model": "claude-fable-5" }
        }
      }
    }
  ]
}
```

Optional config keys (defaults): `router_model` (`SupraLabs/Supra-Router-51M`),
`timeout_ms` (150), `max_prompt_chars` (4000), `bypass_header` (`x-supra-bypass`),
`frontier_complexity_threshold` (5), per-role `fallbacks` array (derived from
the ladder when omitted).

Notes:

- All four role providers must already exist in Bifrost's provider config.
- Send header `x-supra-bypass: true` to skip routing for one request.
- Any classification failure fails open: the request proceeds unchanged.
- Decisions appear in Bifrost's Routing Decision Logs (`routing-rule` engine).
