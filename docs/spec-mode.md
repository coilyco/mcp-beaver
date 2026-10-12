# spec mode: resolving grants against an API document

A `.mcp.kdl` reaches its operations two ways. Both end at the same `opcore.Descriptor` and guarded `Execute`, so nothing after the parse changes.

- **inline grammar** - the frozen default. Each grant states its own `path`, `query`, and `body`. No spec file, no resolution.
- **spec mode** - the guardfile states `spec <file>` and each grant names a verb and a resource. umbra resolves those against the API document by convention.

`parseSource` picks between them: `inherit` composes first ([inherit.md](inherit.md)), then a `spec` node in the result selects resolution. A guardfile with neither takes exactly the path it took before, byte for byte.

## Why

The inline grammar makes every consumer restate the upstream API. Two guardfiles against one service drift into two vocabularies for the same endpoints, and no tool can say whether one surface is weaker than the other. Resolving both against the same document makes them diffable (`coilyco-flight-deck/agentic-os#1365`).

```kdl
wrap ward mcp forgejo {
    spec forgejo.swagger.v1.json.gz
    base-url "https://forgejo.coilysiren.me/api/v1"
    auth header-token { header Authorization; prefix "token "; value env "FORGEJO_TOKEN" }
    can get repo          // resolves to GET /repos/{owner}/{repo}
    can list issue        // resolves to GET /repos/{owner}/{repo}/issues
}
```

The document is a **sibling** of the guardfile, named by file and never by path. A `.gz` is decompressed on read, bounded at 16 MiB, so a vendored spec fits in a ConfigMap.

## Deploying one

The chart's volume already lists the API document, so it lands beside the guardfile with no chart change:

```sh
helm upgrade --install forgejo-mcp mcp-beaver \
  --set-file spec=forgejo.flat.mcp.kdl \
  --set-file apiDocument=forgejo.swagger.v1.json.gz \
  --set apiDocumentName=forgejo.swagger.v1.json.gz
```

`apiDocumentName` must be spelled exactly as the `spec` node spells it, because the runtime resolves the sibling by that name. It rides in `binaryData`, since a gzipped document is not valid UTF-8. A document without a name fails the render. The chart parses neither file.

**Known gap.** An inherited guardfile drops its parent's `action` nodes, so a leaf the parent replaced comes back in generated form and `lint` does not warn (#108). See [serve.md](serve.md) and [chart.md](chart.md).
