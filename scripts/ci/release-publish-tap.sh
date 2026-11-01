#!/bin/sh
# Push the refreshed Homebrew formula to coilyco/homebrew-tap with the App token.
set -eu
: "${TAP_TOKEN:?TAP_TOKEN is required}"
git clone --depth 1 \
  "https://x-access-token:${TAP_TOKEN}@github.com/coilyco/homebrew-tap.git" tap
cp dist/mcp-beaver.rb tap/Formula/mcp-beaver.rb
cd tap
if git diff --quiet; then
  exit 0
fi
git config user.name "coilyco-ops"
git config user.email "coilyco-ops@coilysiren.me"
git add Formula/mcp-beaver.rb
git commit -m "chore(mcp-beaver): bump formula to ${TAG} [skip ci]"
git push origin HEAD:main
