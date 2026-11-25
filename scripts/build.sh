#!/usr/bin/env bash
# Builds zharf for every supported system into dist/, with version.json and the install
# scripts, ready to serve from https://platform.zharf.ai/cli/ or attach to a release.
#
#   scripts/build.sh            # version from $VERSION, else 0.1.0
#   VERSION=0.2.0 scripts/build.sh
set -euo pipefail

cd "$(dirname "$0")/.."
VERSION="${VERSION:-0.1.0}"
VERSION="${VERSION#v}"
GO="${GO:-go}"
DIST=dist
TARGETS="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64"

rm -rf "$DIST"
mkdir -p "$DIST"

files=""
for target in $TARGETS; do
  os="${target%/*}"
  arch="${target#*/}"
  name="zharf-$os-$arch"
  [ "$os" = windows ] && name="$name.exe"
  echo "building $name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$GO" build -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/ZharfAI/zharf/version.Version=$VERSION" \
    -o "$DIST/$name" .
  files="$files${files:+,
}    \"$os-$arch\": \"$name\""
done

cat > "$DIST/version.json" <<EOF
{
  "version": "$VERSION",
  "files": {
$files
  }
}
EOF

cp scripts/install.sh scripts/install.ps1 "$DIST/"
chmod +x "$DIST/install.sh"

(
  cd "$DIST"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum zharf-* install.sh install.ps1 version.json > SHA256SUMS
  else
    shasum -a 256 zharf-* install.sh install.ps1 version.json > SHA256SUMS
  fi
)

echo
ls -l "$DIST"
