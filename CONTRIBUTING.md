# Contributing to zharf

Thanks for helping. Bug reports, fixes, new `zharf launch` apps and better docs are all welcome.

## Getting started

You need Go 1.25 or newer. Nothing else: zharf uses only the standard library.

```bash
git clone https://github.com/ZharfAI/zharf
cd zharf
go build .              # ./zharf
go test ./...
go vet ./...
```

`scripts/build.sh` builds every release binary into `dist/`, with `version.json`, the install
scripts and `SHA256SUMS`:

```bash
VERSION=0.2.0 scripts/build.sh
```

## Layout

| Path | What is there |
| --- | --- |
| `main.go` | The program's entry point |
| `cmd/` | Every command: `login`, `models`, `run`, `serve`, `launch`, `update`… |
| `cmd/serve.go`, `cmd/ollama.go` | The Ollama-compatible server and the mapping to the OpenAI API |
| `cmd/sse.go`, `cmd/chat.go` | Reading streamed answers |
| `version/` | The version, set at build time |
| `scripts/` | `build.sh`, `install.sh`, `install.ps1` |
| `docs/` | `api-compat.md` (what `zharf serve` implements), `launch.md` |
| `examples/` | Shell scripts that use `zharf run` and `zharf serve` |

## Testing against a server

Tests never call the real platform: they use `httptest` servers that stand in for it. To try a
change by hand against another server, point zharf at it:

```bash
ZHARF_BASE_URL=http://127.0.0.1:8080 ZHARF_API_KEY=zk-... ./zharf run some-model "hi"
```

`ZHARF_CONFIG_DIR` keeps a test sign-in apart from your real one, and `ZHARF_KEYRING=file`
keeps it out of the system keychain.

## Guidelines

- **Standard library only.** A new dependency needs a strong reason.
- **The interface is in English**, for developers. Persian appears where it helps: the sign-in
  code line, and the platform's `message_fa` shown under its English error message.
- **Persian text must stay whole.** Anything that streams text works on bytes up to a line end
  or on whole strings, never on byte counts; there is a test that cuts Persian characters
  across reads.
- **stdout is for answers.** Costs, progress and hints go to stderr, so `zharf run … > file`
  stays clean.
- **No colour when it is not a terminal**, and none when `NO_COLOR` is set.
- **Only the public API.** zharf talks to `/v1/*`, `/api/cli/*` and `/cli/*` on the platform,
  nothing else.
- `gofmt` your code, and add a test for every bug you fix. `.golangci.yaml` holds the lint
  settings if you use golangci-lint.

## Pull requests

Keep each pull request to one change, describe what it does and how you tested it, and make
sure `go vet ./...` and `go test ./...` pass on your system; CI runs them on Linux, macOS and
Windows.

Security problems go to security@zharf.ai, not to a pull request or an issue
([SECURITY.md](SECURITY.md)).

By contributing you agree that your work is released under the [MIT License](LICENSE), and to
follow the [Code of Conduct](CODE_OF_CONDUCT.md).
