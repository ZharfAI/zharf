# `zharf launch`: coding agents on ژرف models

`zharf launch <app> --model <id>` starts a coding agent with platform.zharf.ai as its model
provider, your saved key, and the model you pick. zharf then replaces itself with the app, so
the app owns the terminal; anything after `--` goes to the app.

```bash
zharf launch                                   # the apps, and which are installed
zharf launch codex --model gpt-5-mini
zharf launch opencode -m qwen3.5-plus
zharf launch qwen -m qwen3.5-plus
zharf launch hermes -m deepseek-v4-flash -- --yolo
```

zharf checks that the model exists and is a text model before it starts anything. If the app
is not installed, it prints the install command and exits with status 1.

## What each app gets

### Codex CLI (`codex`)

Only command-line overrides; `~/.codex/config.toml` is not touched:

```
codex -c model_provider="zharf" \
      -c model="<id>" \
      -c model_providers.zharf.name="Zharf" \
      -c model_providers.zharf.base_url="https://platform.zharf.ai/v1" \
      -c model_providers.zharf.env_key="ZHARF_API_KEY" \
      -c model_providers.zharf.wire_api="responses" \
      [your arguments]
```

with `ZHARF_API_KEY` set in its environment. Codex speaks the Responses API, which the
platform serves at `/v1/responses`.

Install: `npm install -g @openai/codex`

### OpenCode (`opencode`)

OpenCode 2 reads one more config source from the `OPENCODE_CONFIG_CONTENT` variable, after
your global and project `opencode.json`. zharf puts the provider and the model there for this
run only:

```json
{
  "providers": {
    "zharf": {
      "name": "Zharf",
      "env": ["ZHARF_API_KEY"],
      "package": "@opencode/ai/providers/openai-compatible",
      "settings": { "baseURL": "https://platform.zharf.ai/v1" },
      "models": { "<id>": { "name": "<id>" } }
    }
  },
  "model": "zharf/<id>"
}
```

- No file is changed. Your own providers and settings still load; for this run the model is
  `zharf/<id>`.
- The key is not in the config: OpenCode reads it from `ZHARF_API_KEY`, which zharf sets for
  the run.
- To use the same setup without zharf, add the JSON above to your `opencode.json` and export
  `ZHARF_API_KEY`.
- OpenCode 1 is refused; upgrade with the command below.

`OPENCODE_CONFIG_CONTENT` is read by OpenCode 2.4 but not yet described in its documentation.
If a later OpenCode drops it, the model picker will not list `zharf`; use the JSON above in
`opencode.json` instead.

Install: `curl -fsSL https://opencode.ai/v2/install | bash` (or `npm install -g @opencode/cli`)

### Qwen Code (`qwen`)

Environment only:

```
OPENAI_API_KEY=<your key>
OPENAI_BASE_URL=https://platform.zharf.ai/v1
OPENAI_MODEL=<id>
qwen [your arguments]
```

Install: `npm install -g @qwen-code/qwen-code@latest`

### Hermes Agent (`hermes`)

Hermes's built-in `custom` provider, with no change to `~/.hermes/config.yaml`:

```
CUSTOM_BASE_URL=https://platform.zharf.ai/v1
ZHARF_API_KEY=<your key>
hermes --provider custom -m <id> [your arguments]
```

For a custom endpoint Hermes sends the key it finds in `<VENDOR>_API_KEY`, the vendor being
taken from the host name (`platform.zharf.ai` → `ZHARF_API_KEY`). Hermes loads `~/.hermes/.env`
over the environment, so a `CUSTOM_BASE_URL` or `ZHARF_API_KEY` set there wins.

Install: `curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash`

## Apps that are not launched

### OpenClaw

OpenClaw runs its models in a long-lived Gateway service, so a one-off launch cannot point it
at another provider. Add zharf as a provider in `~/.openclaw/openclaw.json`, in the format
OpenClaw documents for OpenAI-compatible endpoints, with `zharf serve` running:

```json5
{
  agents: { defaults: { model: { primary: "zharf/qwen3.5-plus" } } },
  models: {
    mode: "merge",
    providers: {
      zharf: {
        baseUrl: "http://127.0.0.1:11434/v1",
        apiKey: "zharf-serve",          // any value: zharf serve sends your key
        api: "openai-completions",
        models: [{ id: "qwen3.5-plus", name: "Qwen 3.5 Plus" }]
      }
    }
  }
}
```

Then restart the Gateway.

### Claude Code

Not offered: Claude models are not available on platform.zharf.ai
(`GET /v1/models/not-offered` lists them with the reason).

## Adding an app

An app is one entry in `launchApps()` in `cmd/launch.go`: its binary name, an install command,
and a function that fills in the arguments and environment. Add a test in
`cmd/launch_test.go` that pins the exact command line and environment, and a section here.
