# mcp-beaver

An MCP server generator with a natural flow

![mcp-beaver // .mcp.kdl - An MCP server generator with a natural flow](assets/banner/mcp-beaver-banner.jpg)

mcp-beaver turns one [umbra](https://github.com/coilyco/umbra)
guardfile into a running MCP server. Write a grant, get a tool.

```kdl
can get issue {
    path "/repos/{owner}/{repo}/issues/{index}"
}
```

That grant is the tool `get_issue`, the endpoint `POST /api/get_issue`, and an
input schema of `owner`, `repo`, and `index` that umbra derives from the path
itself. Five grants like it are the entire Forgejo server in
[`examples/forgejo-issues.mcp.kdl`](examples/forgejo-issues.mcp.kdl), which you
can read end to end in a minute.

One file, four surfaces:

- an **MCP server** a host connects to over HTTP or SSE
- an **HTTP tool API**, one `POST /api/{tool-name}` per grant, through the same
  handler and returning the same `CallToolResult` shape
- an **MCP App**, when the file carries an `app` node: a widget at a `ui://`
  resource that calls those same tools back through the host
- a **helm release**, because one generic OCI image runs every guardfile

One runtime, many guardfiles.

## Install

```sh
curl -fsSL -o mcp-beaver https://forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver/releases/latest/download/mcp-beaver-darwin-arm64
chmod +x mcp-beaver && ./mcp-beaver version
```

macOS and Linux, on Intel or arm64: swap the asset name for your platform.
Every binary is static and `SHA256SUMS` ships beside it. The Homebrew formula
is built and attached but not in the tap yet (#125). Release train and
verification: [docs/release.md](docs/release.md).

## Quickstart

```sh
curl -fsSLO https://forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver/raw/branch/main/examples/forgejo-issues.mcp.kdl
FORGEJO_TOKEN=... mcp-beaver serve forgejo-issues.mcp.kdl --http :8080

curl -s -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' localhost:8080/mcp
```

The same grant over the HTTP projection:

```sh
curl -s -H 'Content-Type: application/json' \
  -d '{"owner":"coilyco-flight-deck","repo":"mcp-beaver","index":"41"}' \
  localhost:8080/api/get_issue
```

## Four ways to serve

- **`serve`** reads a `.mcp.kdl` and guards an HTTP upstream. The general case.
- **`serve-upstream`** wraps a private MCP behind an exact tool allowlist,
  stated as flags or as an `mcp-upstream` guardfile, which `pull` writes
  from a registry entry and `directory` writes for the whole registry. See
  [docs/upstream.md](docs/upstream.md), [docs/pull.md](docs/pull.md), and
  [docs/directory.md](docs/directory.md).
- **`serve-ssm`** is the AWS SDK-backed exact-parameter reader. The policy names
  one parameter, the general getter answers for that name alone before AWS sees
  a request, and IAM bounds the principal independently. Two bounds rather than
  one. See [docs/ssm.md](docs/ssm.md).
- **`serve-s3`** is the asset publisher and the first write-capable mode. Its
  policy fixes one bucket, the media types it will serve, the public base URL,
  and an optional key prefix. See [docs/s3.md](docs/s3.md).

A guardfile may also name an API document with `spec <file>` and let umbra
resolve `can get repo` against it ([docs/spec-mode.md](docs/spec-mode.md)), and
compose from another guardfile with `inherit`. A tier narrows its base unless it
writes `override` by name, and a base tier's `confirm` and `withhold` bind on
every tier below it, so a weaker surface is structural rather than asserted.
`flatten` resolves the composition into the one guardfile a runtime mounts. See
[docs/inherit.md](docs/inherit.md).

`lint` and `lint-upstream` are the same paths minus the listener, for validating
a guardfile in CI. See [docs/lint.md](docs/lint.md).

## Results lead with coverage

Every grant-backed result is `{"coverage": {...}, "result": ...}`, in that
order, in both the text and the structured content. Coverage leads because a
consuming harness bounds a tool result by keeping the front and discarding the
tail, so a caveat serialized last is the first thing destroyed, and the model
then reads rows with no caveat and answers as though the view were complete.
Coverage names every array in the payload and its length, because a count in
meaning is what changes an answer. See [docs/DESIGN.md](docs/DESIGN.md).

An `oauth2-client` node declares a `client_credentials` client, and
`value oauth2 "<name>"` presents a token this runtime **mints** rather than
reads. Every other credential is read from somewhere holding it already. An
OAuth upstream holds nothing until one is fetched. See
[docs/oauth2.md](docs/oauth2.md).

## The surface is exactly the grants

You declare the operations an agent may reach, down to the leaf, and the served
surface is those grants. The Forgejo example writes five, so the agent gets five
tools, and `restrict owner matches "coilyco-*"` bounds every path among them.
Auditing that server means reading one file of 85 lines, most of which is
comment.

**The claim is about the running server rather than the image.** The image is
deliberately generic and carries no guardfile, so a consumer mounts the spec at
deploy time. In spec mode the runtime serves the declared handlers and holds
nothing else to serve. In upstream-proxy mode the container holds credentials
for an endpoint whose own tool list is wider, and the runtime re-checks
allowlist membership on every call, so the bound there is enforcement rather
than absence. Both are stated so a deployment can pick which one it wants.

## Authentication is the deployment's job

Caller identity, TLS, ingress, and network reachability belong to the consuming
deployment, and mcp-beaver expects to sit behind them. It authenticates nothing
inbound itself. Guardfile `auth` configures mcp-beaver's own credential to the
upstream service, which is a different direction from the caller's credential to
mcp-beaver.

## Ships as image plus chart

```sh
helm upgrade --install skillsmp mcp-beaver \
  -f skillsmp.values.yaml \
  --set-file spec=skillsmp.mcp.kdl \
  --set image.tag=<built-runtime-sha>
```

Deploying an MCP is a values file plus `helm upgrade`. One generic image serves
every guardfile, so a new service reuses the image the last one built. The chart
templates the auth-neutral runtime layer, and in spec mode it stays spec-opaque
and leaves the guardfile unparsed.
[`deploy`](https://forgejo.coilysiren.me/coilyco-bridge/deploy) owns public
ingress, authentication, TLS, DNS, and rollout. See
[docs/chart.md](docs/chart.md).

Every push to canonical `main` publishes the runtime as
`forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver:<full-source-sha>`, and
the trusted publisher verifies the remote manifest.

## Naming

**A dam is not a wall. It decides what gets through.**

mcp-beaver shares no code with
[ward](https://forgejo.coilysiren.me/coilyco-flight-deck/ward), whose name this
project used to carry as `ward-mcp`. One `ward` spelling survives on purpose:
`wrap ward mcp <name>` opens every guardfile, because that line is umbra's
inline grammar rather than anything this runtime owns, and it moves when umbra
moves.

## License

MIT. See [LICENSE](LICENSE).

## See also

- [AGENTS.md](AGENTS.md) - agent operating rules for this repository.
- [docs/FEATURES.md](docs/FEATURES.md) - what ships today.
- [docs/DESIGN.md](docs/DESIGN.md) - why it is shaped this way.
- [justfile](justfile) - dev verbs.
- [.ward/ward.yaml](.ward/ward.yaml) - catalog metadata only.
