#!/bin/sh
# Attach every built artifact to the created GitHub release. The upload is idempotent
# with --clobber, so a retry of a half-attached release finishes the job.
set -eu
gh release upload "${TAG}" --clobber \
  dist/mcp-beaver-darwin-arm64 \
  dist/mcp-beaver-darwin-amd64 \
  dist/mcp-beaver-linux-amd64 \
  dist/mcp-beaver-linux-arm64 \
  dist/SHA256SUMS \
  dist/mcp-beaver.rb
echo "attached the artifacts to ${TAG}"
