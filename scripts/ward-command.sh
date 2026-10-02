#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
export GOPRIVATE=forgejo.coilysiren.me

action="${1:-}"
shift || true

case "$action" in
  build)
    go build ./...
    ;;
  test)
    go test ./...
    ;;
  vet)
    go vet ./...
    ;;
  tidy)
    go mod tidy
    ;;
  fmt)
    gofmt -w cmd internal
    ;;
  serve-example)
    go run ./cmd/mcp-beaver serve examples/skillsmp.mcp.kdl --http "${1:-:18080}"
    ;;
  directory)
    go run ./cmd/mcp-beaver directory "$@"
    ;;
  lint-examples)
    # A broken committed example is worse than a missing one, and --methods
    # surfaces verb-fallthrough warnings on stderr as it goes.
    status=0
    for spec in examples/*.mcp.kdl; do
      echo "== ${spec}"
      go run ./cmd/mcp-beaver lint --methods "${spec}" || status=1
    done
    exit "${status}"
    ;;
  pin-umbra)
    ref="${1:-v0.122.0}"
    go get "github.com/coilyco/umbra@${ref}"
    go mod tidy
    ;;
  *)
    echo "unknown Ward action: $action" >&2
    exit 2
    ;;
esac
