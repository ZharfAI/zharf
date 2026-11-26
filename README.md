<div align="center">

# zharf

**Every model on [platform.zharf.ai](https://platform.zharf.ai) from your terminal, paid in ژرف.**

Chat with GPT-5, Gemini 3, DeepSeek V4, Qwen 3.5, GLM-5, Kimi K2 and 500 more models, and
serve them on your own computer over the Ollama and OpenAI APIs, so Open WebUI, Continue and
every other Ollama client can use them without a key of their own.

[فارسی](#راهنمای-فارسی) · [Install](#install) · [Commands](#commands) · [zharf serve](#zharf-serve) · [zharf launch](#zharf-launch)

</div>

```text
$ zharf run gemma-3-4b-it "سلام، یک جمله"
امروز هوا خیلی زیباست. 😊
0.000379 ژرف · 13 in, 10 out tokens
```

## Install

**macOS and Linux**

```bash
curl -fsSL https://platform.zharf.ai/cli/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://platform.zharf.ai/cli/install.ps1 | iex
```

The script puts `zharf` in `/usr/local/bin` when it can write there and in `~/.local/bin`
otherwise (`%LOCALAPPDATA%\zharf` on Windows), and tells you if that folder is not on your
`PATH`. Downloads come from platform.zharf.ai itself. Behind a proxy, set `HTTPS_PROXY` first.

**With Go**

```bash
go install github.com/ZharfAI/zharf@latest
```

**Binaries** for macOS (Apple silicon and Intel), Linux (x86-64 and ARM64) and Windows are on
the [releases page](https://github.com/ZharfAI/zharf/releases), with `SHA256SUMS`.

## Quick start

```bash
zharf login                          # sign in in the browser; the key goes in your keychain
zharf models --search qwen           # what there is, and what it costs
zharf run qwen3.5-plus               # chat
zharf serve                          # Ollama API on http://127.0.0.1:11434
```

Balance and top-ups are on [platform.zharf.ai/wallet](https://platform.zharf.ai/wallet).

## Commands

| Command | |
| --- | --- |
| `zharf login` | Sign in through the browser and save an API key |
| `zharf logout` | Forget the saved key |
| `zharf whoami` | Which key and account are in use, and whether the key works |
| `zharf models` (`list`, `ls`) | The models and their prices in ژرف |
| `zharf show <model>` | One model's details |
| `zharf run <model> [prompt]` | Ask once, or chat |
| `zharf serve` | Serve the models over the Ollama and OpenAI APIs |
| `zharf launch <app> --model <id>` | Start Claude Code, OpenCode, Codex, Qwen Code or Hermes on a ژرف model |
| `zharf usage` | Where to see spending and balance |
| `zharf update` | Install the latest zharf |
| `zharf version`, `zharf help [command]` | |

### `zharf login`

```text
$ zharf login

  Your one-time code:

      WDJB-MJHT

  این صفحه را در مرورگر باز کنید و اگر همین کد را نشان داد، ورود را تأیید کنید.
  Open this page and approve the sign-in if it shows the same code:

      https://platform.zharf.ai/cli?code=WDJB-MJHT

  Waiting for approval… (Ctrl+C to stop)

Signed in to platform.zharf.ai as Sara (09121234567).
Key zk-AbC12345… saved in the macOS keychain.
```

Like `gh auth login`: zharf opens the page, you approve the code shown, and the platform makes
an API key named «zharf CLI · <this computer>». The key is kept in the macOS keychain, the Linux
secret service (`secret-tool`), or `~/.config/zharf/credentials` with mode 0600.

- `zharf login --no-browser` prints the link without opening a browser (for SSH sessions).
- `echo "$KEY" | zharf login --with-key` saves a key you made on the
  [keys page](https://platform.zharf.ai/keys), after checking it.
- `ZHARF_API_KEY`, when set, is used instead of the saved key, by every command.

### `zharf whoami` and `zharf logout`

```text
$ zharf whoami
platform.zharf.ai
  API       https://platform.zharf.ai/v1
  Key       zk-AbC12345… from the macOS keychain
  Account   Sara · 09121234567 (as recorded at sign-in)
  Check     the platform accepts this key
  Balance   see https://platform.zharf.ai/wallet
```

The check is a request that costs nothing. API keys cannot read the balance, so it links to the
wallet. `zharf logout` removes the saved key from this computer; revoke the key itself on the
[keys page](https://platform.zharf.ai/keys).

### `zharf models`

```bash
zharf models                         # all of them, the most used families first
zharf models --search gpt            # id, name or maker contains every word
zharf models --type image            # text, image, audio, video, embedding or other
zharf models -s "qwen 3.5" -n 5      # at most five
zharf models --type text --json      # as JSON
```

```text
$ zharf models --search gpt-5.4
MODEL            MAKER   TYPE   CONTEXT  IN/1M  OUT/1M
gpt-5.4-pro      OpenAI  text        1M  7,639  45,834
gpt-5.4-nano     OpenAI  text      400K  50.93   318.3
gpt-5.4-mini     OpenAI  text      400K    191   1,146
gpt-5.4          OpenAI  text        1M  636.6   3,819
gpt-5.4-image-2  OpenAI  image        -  2,037   3,819
```

Prices are in ژرف per million input and output tokens; media models show their price per image,
second or job. `zharf show <model>` gives one model's context, features, prices and description.

### `zharf run`

```bash
zharf run gpt-5-mini "Explain CRDTs in three sentences"     # one answer, then exit
zharf run deepseek-v4-flash                                 # a chat
cat error.log | zharf run qwen3.5-plus "What went wrong?"   # piped text comes first
zharf run glm-5.1 --system "Answer in Persian" "What is a monad?"
```

The answer streams to stdout as it is written; the cost of each answer goes dimly to stderr, so
`zharf run … > answer.md` saves only the answer. A model's reasoning shows dimmed while it thinks
(`--hide-thinking` hides it).

In a chat:

| | |
| --- | --- |
| `"""` | Start and end a multi-line message |
| `/model <id>` | Switch model, keeping the conversation |
| `/system <text>` | Set the system message (`/system` alone shows it) |
| `/clear` | Forget the conversation |
| `/cost` | What this chat has cost |
| `/bye` | Leave (or Ctrl+D). Ctrl+C stops an answer |

Flags: `--system`, `--temperature`, `--max-tokens`, `--effort low|medium|high`,
`--hide-thinking`, `-q/--quiet` (no cost line).

## `zharf serve`

```bash
zharf serve                          # http://127.0.0.1:11434, Ollama's address
zharf serve --port 11435             # if Ollama itself is running
zharf serve --models "gpt-5*,qwen3.5-plus"   # list only these in /api/tags
```

It answers the **Ollama API** (`/api/tags`, `/api/chat`, `/api/generate`, `/api/show`,
`/api/embed`, `/api/version`, `/api/ps`) and passes the **OpenAI API** (`/v1/*`) straight to the
platform, with your saved key in place of whatever key the tool sends. Each call is charged to
your account as usual. [docs/api-compat.md](docs/api-compat.md) lists every route and how
Ollama's options map.

**Open WebUI**

```bash
docker run -d -p 3000:8080 -e OLLAMA_BASE_URL=http://host.docker.internal:11434 \
  -v open-webui:/app/backend/data --name open-webui ghcr.io/open-webui/open-webui:main
```

On Linux use `--network host -e OLLAMA_BASE_URL=http://127.0.0.1:11434` instead
([examples/open-webui.sh](examples/open-webui.sh)).

**Continue** (`~/.continue/config.yaml`)

```yaml
models:
  - name: Qwen 3.5 Plus (ژرف)
    provider: ollama
    model: qwen3.5-plus
    apiBase: http://127.0.0.1:11434
```

**Any Ollama client:** point it at `http://127.0.0.1:11434` (or set
`OLLAMA_HOST=http://127.0.0.1:11434`).

**Any OpenAI SDK:** base URL `http://127.0.0.1:11434/v1`, any API key.

```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:11434/v1", api_key="unused")
print(client.chat.completions.create(model="gpt-5-mini",
      messages=[{"role": "user", "content": "سلام"}]).choices[0].message.content)
```

**Docker**

```bash
docker build -t zharf .
docker run -d -p 127.0.0.1:11434:11434 -e ZHARF_API_KEY=zk-... zharf
```

**Safety.** The server spends your credit, so it listens on 127.0.0.1, refuses browser requests
from other web sites, and refuses requests addressed to other host names (DNS rebinding).
`--host 0.0.0.0` opens it to your network: anyone who can reach the port can spend your credit.
Allow a browser origin with `--origins https://my.app` or `ZHARF_ORIGINS`.

## `zharf launch`

Starts a coding agent with platform.zharf.ai as its provider, your key and the model you choose,
like `ollama launch`:

```bash
zharf launch                                  # the apps, and which are installed
zharf launch claude --model glm-5.4
zharf launch codex --model gpt-5-mini
zharf launch opencode -m qwen3.5-plus
zharf launch qwen -m qwen3.5-plus
zharf launch hermes -m deepseek-v4-flash -- --yolo     # after --: the app's own arguments
```

| App | How it is pointed at ژرف |
| --- | --- |
| `claude` (Claude Code) | the selected model and the platform Messages endpoint, authenticated with your saved Zharf key |
| `codex` (Codex CLI) | `-c model_provider=zharf …` overrides; your `config.toml` is not changed |
| `opencode` (OpenCode 2) | a `zharf` provider and the model in `OPENCODE_CONFIG_CONTENT` for this run; `opencode.json` is not changed |
| `qwen` (Qwen Code) | `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` for this run |
| `hermes` (Hermes Agent) | `--provider custom` with `CUSTOM_BASE_URL`; `config.yaml` is not changed |

If the app is missing, zharf prints its install command. OpenClaw can use zharf through
`zharf serve`.
Details: [docs/launch.md](docs/launch.md).

## `zharf usage`, `zharf update`

```bash
zharf usage            # links to the usage, wallet and keys pages (keys cannot read usage)
zharf update           # download the latest zharf for this system and replace this one
zharf update --check   # only say whether there is a newer version
```

`zharf update` reads `https://platform.zharf.ai/cli/version.json`, downloads
`/cli/zharf-<os>-<arch>`, checks its size and that it is a program for this system, runs it
once, then swaps it in with a rename.

## Environment

| Variable | |
| --- | --- |
| `ZHARF_API_KEY` | Use this key instead of the saved one |
| `ZHARF_BASE_URL` | The platform (default `https://platform.zharf.ai`; `--base-url` too) |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | Send requests through a proxy |
| `ZHARF_HOST`, `ZHARF_PORT` | `zharf serve`'s address and port |
| `ZHARF_ORIGINS` | Extra browser origins `zharf serve` accepts (comma-separated) |
| `ZHARF_CONFIG_DIR` | Where zharf keeps its files (default `~/.config/zharf`, `%APPDATA%\zharf`) |
| `ZHARF_KEYRING=file` | Keep the key in the credentials file, not the system keychain |
| `NO_COLOR` | No colours (`--no-color` too) |

Connections time out after 15 seconds; answers have no time limit, so long streams are never cut.

## Building from source

```bash
git clone https://github.com/ZharfAI/zharf && cd zharf
go build .                  # needs Go 1.25; standard library only
go test ./...
VERSION=0.2.0 scripts/build.sh   # every release binary, version.json and SHA256SUMS in dist/
```

See [CONTRIBUTING.md](CONTRIBUTING.md). Report security problems to security@zharf.ai
([SECURITY.md](SECURITY.md)).

## License

[MIT](LICENSE)

---

<div dir="rtl">

## راهنمای فارسی

**zharf** ابزار خط فرمان [پلتفرم ژرف](https://platform.zharf.ai) است. با آن از ترمینال به بیش از پانصد مدل هوش مصنوعی دسترسی دارید، از GPT-5 و Gemini 3 تا DeepSeek V4 و Qwen 3.5 و GLM-5 و Kimi K2. اعتبار حساب در پلتفرم با واحدی به نام «ژرف» حساب می‌شود و هزینه‌ی هر درخواست از همین اعتبار کم می‌شود.

zharf این مدل‌ها را روی کامپیوتر خودتان هم با API اولاما (Ollama) و OpenAI در اختیار برنامه‌های دیگر می‌گذارد. یعنی Open WebUI و Continue و هر برنامه‌ی دیگری که با اولاما کار می‌کند، بدون کلید جداگانه می‌تواند از مدل‌های ژرف استفاده کند.

### نصب

در مک و لینوکس:

</div>

```bash
curl -fsSL https://platform.zharf.ai/cli/install.sh | sh
```

<div dir="rtl">

در ویندوز (PowerShell):

</div>

```powershell
irm https://platform.zharf.ai/cli/install.ps1 | iex
```

<div dir="rtl">

فایل‌ها از خود پلتفرم ژرف دانلود می‌شوند. اگر از پراکسی استفاده می‌کنید، پیش از نصب متغیر `HTTPS_PROXY` را تنظیم کنید. zharf هم برای همه‌ی درخواست‌هایش از همین پراکسی استفاده می‌کند.

### شروع کار

</div>

```bash
zharf login                         # ورود با مرورگر
zharf models --search qwen          # فهرست مدل‌ها و قیمتشان به ژرف
zharf run qwen3.5-plus "سلام"       # یک پرسش و یک پاسخ
zharf run qwen3.5-plus              # گفت‌وگو
```

<div dir="rtl">

- **ورود:** `zharf login` یک کد یک‌بارمصرف نشان می‌دهد و صفحه‌ی ورود را در مرورگر باز می‌کند. اگر کد صفحه همان کد ترمینال بود، ورود را تأیید کنید. کلید API در Keychain مک، Secret Service لینوکس یا فایلی با دسترسی محدود ذخیره می‌شود. اگر کلید را از قبل دارید: `echo "$KEY" | zharf login --with-key`
- **هزینه:** پس از هر پاسخ، هزینه‌اش به ژرف با رنگ کم‌رنگ زیر پاسخ می‌آید. در گفت‌وگو، دستور `/cost` مجموع هزینه‌ها را نشان می‌دهد.
- **متن طولانی:** در گفت‌وگو، پیام چندخطی را میان `"""` بنویسید. متن یک فایل را هم می‌توانید به zharf بدهید: `cat report.txt | zharf run gpt-5-mini "خلاصه کن"`

### استفاده در برنامه‌های دیگر

با دستور `zharf serve` روی نشانی `http://127.0.0.1:11434`، همان نشانی اولاما، یک سرور محلی راه می‌افتد. در Open WebUI یا Continue یا هر برنامه‌ای که با اولاما کار می‌کند همین نشانی را بدهید. برای برنامه‌هایی که با API سازگار با OpenAI کار می‌کنند، نشانی `http://127.0.0.1:11434/v1` را بدهید و در جای کلید هر چیزی بنویسید. zharf خودش کلید شما را می‌فرستد.

چون این سرور از اعتبار شما خرج می‌کند، فقط به درخواست‌های همین کامپیوتر جواب می‌دهد و درخواست سایت‌های دیگر را از داخل مرورگر قبول نمی‌کند.

### اجرای دستیارهای برنامه‌نویسی

دستور `zharf launch` دستیارهای برنامه‌نویسی را روی مدل‌های ژرف اجرا می‌کند. از Claude Code و OpenCode و Codex و Qwen Code و Hermes پشتیبانی می‌کند:

</div>

```bash
zharf launch claude --model glm-5.4
zharf launch codex --model gpt-5-mini
zharf launch opencode -m qwen3.5-plus
```

<div dir="rtl">

جزئیات هر برنامه در [docs/launch.md](docs/launch.md) آمده است.

### متغیرهای محیطی

- `ZHARF_API_KEY`: به جای کلید ذخیره‌شده از این کلید استفاده می‌شود.
- `HTTPS_PROXY`: همه‌ی درخواست‌ها از این پراکسی می‌گذرند.
- `ZHARF_BASE_URL`: نشانی پلتفرم، اگر نشانی دیگری لازم دارید.

### گزارش مشکل و مشارکت

مشکل‌ها و پیشنهادها را در [Issues](https://github.com/ZharfAI/zharf/issues) بنویسید. مشکل‌های امنیتی را به security@zharf.ai بفرستید. این پروژه متن‌باز است و با مجوز MIT منتشر شده است. راهنمای مشارکت در [CONTRIBUTING.md](CONTRIBUTING.md) است.

</div>
