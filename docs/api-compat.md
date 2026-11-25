# `zharf serve`: Ollama and OpenAI compatibility

`zharf serve` listens on `http://127.0.0.1:11434`, Ollama's address, and answers two APIs with
the key saved by `zharf login` (or `ZHARF_API_KEY`):

- **the Ollama API** under `/api/*`, translated to the platform's chat API;
- **the OpenAI API** under `/v1/*`, passed through to `https://platform.zharf.ai/v1` unchanged,
  with the saved key put in place of whatever key the client sends.

Every call is charged to your account, exactly as if you had called the platform yourself.

## Ollama routes

| Route | What zharf does |
| --- | --- |
| `GET /` | `Ollama is running` |
| `GET /api/version` | `{"version": "0.12.6", "zharf": "<zharf version>"}`: the Ollama API version these routes follow |
| `GET /api/tags` | The platform's chat and embedding models, most used first (as `zharf models`). `--models` narrows the list |
| `POST /api/show` | Details for one model, with `capabilities` (`completion`, `tools`, `vision`, `thinking`, `embedding`) and `model_info["<family>.context_length"]` |
| `GET /api/ps` | `{"models": []}`: nothing is loaded locally |
| `POST /api/chat` | Chat, streamed as NDJSON unless `"stream": false` |
| `POST /api/generate` | One prompt (with optional `system` and `images`), streamed as NDJSON unless `"stream": false` |
| `POST /api/embed` | Embeddings for one input or many (`embeddings`) |
| `POST /api/embeddings` | The older single-input form (`embedding`) |
| `POST /api/pull` | `{"status": "success"}` for a model the platform has; `404` otherwise. Nothing is downloaded |
| other `/api/*` (`create`, `copy`, `delete`, `push`, `blobs`…) | `501`: models live on the platform, nothing is stored locally |

A `/api/chat` with no messages, or a `/api/generate` with an empty prompt, answers at once with
`"done_reason": "load"` (or `"unload"` when `keep_alive` is 0), as Ollama does, and calls nothing.

Model names may carry Ollama's `:latest` tag; it is removed. An unknown model gets `404` before
any call is made.

## Request mapping

zharf always asks the platform for a stream (it carries the cost) and builds the reply the
client asked for.

### Options

| Ollama `options` | Platform (OpenAI) field |
| --- | --- |
| `temperature` | `temperature` |
| `top_p` | `top_p` |
| `top_k` | `top_k` |
| `num_predict` | `max_tokens` (`-1` and `-2`, Ollama's "no limit", send nothing) |
| `stop` (string or list) | `stop` |
| `seed` | `seed` |
| `presence_penalty`, `frequency_penalty` | the same names |
| `num_ctx`, `repeat_penalty`, `num_gpu`, `mirostat`… | ignored: they tune a local runner |

### Other fields

| Ollama | Platform (OpenAI) |
| --- | --- |
| `format: "json"` | `response_format: {"type": "json_object"}` |
| `format: {JSON schema}` | `response_format: {"type": "json_schema", "json_schema": {"name": "response", "schema": …}}` |
| `tools` | `tools` (the same shape) |
| `think: "low" \| "medium" \| "high"` | `reasoning_effort` |
| `think: false` | reasoning is left out of the reply |
| `keep_alive` | ignored, apart from the load/unload replies above |
| `system` (generate) | a `system` message before the prompt |

### Messages

| Ollama message | Platform message |
| --- | --- |
| `{"role", "content"}` | the same |
| `images: [base64…]` | `content` becomes `[{"type": "text"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,…"}}]`; the image type is read from its first bytes (PNG, JPEG, GIF, WebP, HEIC, BMP) |
| assistant `tool_calls: [{"function": {"name", "arguments": {…}}}]` | `tool_calls` with ids `call_1`, `call_2`… and `arguments` as a JSON string |
| `{"role": "tool", "tool_name", "content"}` | `{"role": "tool", "tool_call_id"}` matching the earlier call by name, else in order |

## Reply mapping

| Platform stream | Ollama reply |
| --- | --- |
| `delta.content` | `message.content` (chat) or `response` (generate), one NDJSON line per piece |
| `delta.reasoning` / `reasoning_content` | `message.thinking` / `thinking` |
| `delta.tool_calls` fragments | joined, then sent as one `message.tool_calls` line with `arguments` as an object |
| `finish_reason` | `done_reason`: `stop`, or `length` |
| `usage.prompt_tokens`, `completion_tokens` | `prompt_eval_count`, `eval_count` |
| `usage.cost_zharf` | `cost_zharf` on the final line (an addition; Ollama clients ignore it) |

`total_duration` is the whole call, `prompt_eval_duration` the wait for the first token, and
`eval_duration` the rest, in nanoseconds. `load_duration` is 0.

Errors come back in Ollama's shape, `{"error": "…"}`, with the platform's HTTP status and its
English and Persian messages joined. An error in the middle of a stream arrives as a last
NDJSON line `{"error": "…"}`.

## OpenAI routes

Everything under `/v1/` goes to `https://platform.zharf.ai/v1/` as it is, streamed without
buffering. zharf removes the client's `Authorization`, `Cookie` and `Origin` headers and sends
your key. So any OpenAI SDK works with:

```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:11434/v1", api_key="unused")
```

## Who may call it

The server spends your credit, so it checks every request:

- **Browser pages:** a request with an `Origin` header must come from a local page
  (`localhost`, `127.0.0.1`, `[::1]`, `0.0.0.0`) or an app (`app://`, `tauri://`,
  `vscode-webview://`, `vscode-file://`). Add others with `--origins` or `ZHARF_ORIGINS`
  (comma-separated; `*` allows any). Anything else gets `403`.
- **Host names:** when listening on loopback, the `Host` header must name this computer
  (`localhost`, a loopback or private address, its own host name, `*.local`, `*.localhost`,
  `*.internal` such as `host.docker.internal`). This blocks DNS rebinding.
- **`--host 0.0.0.0`** (and the Docker image) turns the host-name check off, as Ollama does,
  because clients on the network use names this computer cannot know. Anyone who can reach the
  port can then spend your credit; publish it only where you mean to.

Each request is logged to stderr: time, method, path, status, duration, model and cost.
