#!/bin/sh
# Cross-compile the release binaries into dist/, stamped with the current tag.
#
# CGO is off so every target is a static binary that runs on a bare host: the
# whole dependency set (pgx, the AWS SDK, redis, the PDF reader) is pure Go, so
# nothing here needs a C toolchain per platform.
set -e
VERSION="$(git describe --tags --exact-match 2>/dev/null || git describe --tags --always)"
STAMP="github.com/coilyco/mcp-beaver/internal/mcpserver.Version=${VERSION}"
mkdir -p dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
    GOOS="${target%/*}"
    GOARCH="${target#*/}"
    out="dist/mcp-beaver-${GOOS}-${GOARCH}"
    GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
        go build -trimpath -ldflags "-s -w -X ${STAMP}" \
        -o "$out" ./cmd/mcp-beaver
    echo "$out"
done
(cd dist && sha256sum \
    mcp-beaver-darwin-arm64 \
    mcp-beaver-darwin-amd64 \
    mcp-beaver-linux-amd64 \
    mcp-beaver-linux-arm64 > SHA256SUMS)
echo "version: ${VERSION}"
