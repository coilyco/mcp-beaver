#!/usr/bin/env bash
set -euo pipefail

# Images published either side of the rename all live under this path and stay
# pullable. Deploy pins a source SHA, so nothing here decides adoption.
registry="ghcr.io"
image_name="coilyco/mcp-beaver"

if [ -z "${GITHUB_TOKEN:-}" ]; then
  echo "GITHUB_TOKEN is required for the trusted image-publish lane." >&2
  exit 1
fi

sha="${GITHUB_SHA:-$(git rev-parse HEAD)}"
case "${sha}" in
  *[!0-9a-f]*|"")
    echo "mcp-beaver source sha is not a lowercase hexadecimal commit id." >&2
    exit 1
    ;;
esac
if [ "${#sha}" -ne 40 ]; then
  echo "mcp-beaver source sha must be a full 40-character commit id." >&2
  exit 1
fi

image="${registry}/${image_name}:${sha}"
docker_config="$(mktemp -d)"
trap 'rm -rf "${docker_config}"' EXIT
chmod 700 "${docker_config}"
export DOCKER_CONFIG="${docker_config}"

printf '%s' "${GITHUB_TOKEN}" \
  | docker login "${registry}" --username "${GITHUB_ACTOR:-coilyco-ops}" --password-stdin

echo "==> building ${image}"
docker build --pull \
  --label "org.opencontainers.image.source=https://github.com/coilyco/mcp-beaver" \
  -t "${image}" .

echo "==> publishing ${image}"
docker push "${image}"

docker manifest inspect "${image}" >/dev/null
echo "verified immutable manifest ${image}"
