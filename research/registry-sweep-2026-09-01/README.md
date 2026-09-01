# Registry sweep, 2026-09-01

Delphi's prototype for [#119](https://forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver/issues/119), preserved by Angie because it lived only in `/tmp/beaver-gen/` on kais-macbook-pro and would not have survived the session.

**This is a record, not a shipped component.** Nothing here is on an import path, nothing runs in CI, and the Go implementation of `pull` does not read it. It is here because the numbers #119 rests on came from one network run that a second run would not reproduce.

## What the numbers cost

60 registry entries enumerated, 30 answered anonymously, 30 refused, 21 of those with HTTP 401. So `probe.json` is not re-derivable on demand: half the population declines, and which half can change.

## Files

* `probe.py` - enumerates `registry.modelcontextprotocol.io/v0/servers`, initializes each remote, and reads `tools/list`. SSE-framed replies, and one retry on an empty first read.
* `classify.py` - splits tools by `annotations.readOnlyHint` into the three scopes.
* `gen_kdl.py` - emits a guardfile per server.
* `render*.py` - the artifact renderings, kept for provenance rather than use.
* `probe.json` - the 208-tool dataset across 30 servers.
* `guardfiles/` - the 30 generated files, all of which `kdlfmt format` accepted.

## The caveat that travels with it

One run, one host, one day. `readOnlyHint` is self-declared, so an upstream that mislabels is trusted, and nothing in this pipeline calls a tool to find out.
