# Releasing mcp-beaver

Two artifacts leave this repo on two cadences. **The OCI image** is published by `ci.yml` on every main push, keyed by full source sha, and is how the fleet runs beaver ([ci.md](ci.md)). **The binaries** are published by `release.yml` when a revision changes what the binary ships, and are how a person installs beaver. This page covers the binaries.

## Installing

Take a binary and `SHA256SUMS` from the [releases page](https://forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver/releases):

```sh
curl -fsSLO https://forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver/releases/latest/download/mcp-beaver-linux-amd64
shasum -a 256 -c SHA256SUMS --ignore-missing && chmod +x mcp-beaver-linux-amd64 && ./mcp-beaver-linux-amd64 version
```

macOS (Apple Silicon, Intel) and Linux (x86-64, arm64), all static (`CGO_ENABLED=0`). **Homebrew is not wired yet**: every release builds and attaches `mcp-beaver.rb` and the tap step runs, but skips with a warning because the repo has no `TAP_WRITE_TOKEN` secret (mcp-beaver#125). Once it is set, the next release publishes the formula.

## What the train does

`release.yml` runs on every push to `main` and validates with `just test`. Whether it publishes is `just release-impact`'s answer. A revision touching `cmd/`, `internal/`, `go.mod`, `go.sum`, or either release script publishes, and anything else validates and stops, so a docs or chart commit never mints an identical version. It compares from the previous release tag, not the pushed range, so a shipped change after an unpublished revision still publishes. Publishing bumps the tag, cross-compiles and checksums four binaries, creates a Forgejo release with the artifacts, and pushes the Homebrew formula to the tap. The tag and release steps are agentic-os's shared composite actions, consumed by ref.

## Versions

Every automatic release is a **minor** bump, and nothing in commit history can escalate it. A **major** is hand-driven by dispatching with `bump: major`:

```sh
aosguard ops forgejo workflow dispatch coilyco-flight-deck mcp-beaver release.yml --ref main --inputs '{"bump":"major"}'
```

A dispatch always publishes whatever the classifier says, so it is also the retry path after a run that failed post-tagging (pass an explicit `tag` to retry an existing one). While a `.release-major` file exists, a push validates and stops so a minor cannot take the next number, and a dispatch ignores the hold. Delete the file once the major is out.

**The version stamp.** `internal/mcpserver.Version` is `dev` except in release builds, which stamp the tag with `-X`. One target feeds `mcp-beaver version` and the `serverInfo` version in every handshake, so they cannot disagree.
