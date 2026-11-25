#!/bin/sh
# Installs zharf, the command-line tool for platform.zharf.ai.
#
#   curl -fsSL https://platform.zharf.ai/cli/install.sh | sh
#
# Settings (environment):
#   ZHARF_INSTALL_DIR   where to put zharf (default: /usr/local/bin when writable, else ~/.local/bin)
#   ZHARF_BASE_URL      where to download from (default: https://platform.zharf.ai)
#   HTTPS_PROXY         a proxy for the download, if you need one
set -eu

BASE_URL="${ZHARF_BASE_URL:-https://platform.zharf.ai}"
BASE_URL="${BASE_URL%/}"
BASE_URL="${BASE_URL%/v1}"

say() { printf '%s\n' "$*"; }
fail() { printf 'zharf install: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW* | MSYS* | CYGWIN*) fail "on Windows, run in PowerShell: irm $BASE_URL/cli/install.ps1 | iex" ;;
  *) fail "no zharf build for $(uname -s) yet" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "no zharf build for $(uname -m) yet" ;;
esac

# An Intel shell under Rosetta on an Apple silicon Mac still gets the native build.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
  arch=arm64
fi

if [ -n "${ZHARF_INSTALL_DIR:-}" ]; then
  dir="$ZHARF_INSTALL_DIR"
elif [ "$(id -u)" = 0 ] || { [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; }; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi
mkdir -p "$dir" || fail "cannot create $dir"

url="$BASE_URL/cli/zharf-$os-$arch"
tmp="$(mktemp "${TMPDIR:-/tmp}/zharf.XXXXXX")"
trap 'rm -f "$tmp"' EXIT INT TERM

say "Downloading zharf for $os/$arch from $url"
if command -v curl >/dev/null 2>&1; then
  curl -fL --retry 3 --connect-timeout 15 --progress-bar -o "$tmp" "$url" || fail "download failed"
elif command -v wget >/dev/null 2>&1; then
  wget -q --show-progress -O "$tmp" "$url" || fail "download failed"
else
  fail "needs curl or wget"
fi

# Some curl versions exit 0 after a refused connection when --retry is given, so the size is
# the real check.
size="$(wc -c < "$tmp" | tr -d ' ')"
[ "$size" -gt 0 ] || fail "could not download $url; is $BASE_URL reachable? (behind a proxy, set HTTPS_PROXY)"
[ "$size" -gt 1000000 ] || fail "the download is too small ($size bytes) to be zharf"
chmod 755 "$tmp"
"$tmp" version >/dev/null 2>&1 || fail "the downloaded program does not run on this system"

mv -f "$tmp" "$dir/zharf" || fail "cannot write to $dir (try: sudo sh, or ZHARF_INSTALL_DIR=~/.local/bin)"
trap - EXIT INT TERM

say "Installed $("$dir/zharf" version) to $dir/zharf"

case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    shell_rc="~/.profile"
    case "${SHELL:-}" in
      */zsh) shell_rc="~/.zshrc" ;;
      */bash) shell_rc="~/.bashrc" ;;
      */fish) shell_rc="~/.config/fish/config.fish" ;;
    esac
    say ""
    say "$dir is not on your PATH. Add it with:"
    if [ "$shell_rc" = "~/.config/fish/config.fish" ]; then
      say "  fish_add_path $dir"
    else
      say "  echo 'export PATH=\"$dir:\$PATH\"' >> $shell_rc && export PATH=\"$dir:\$PATH\""
    fi
    ;;
esac

say ""
say "Next:  zharf login"
say "Then:  zharf run qwen3.5-9b \"سلام\""
