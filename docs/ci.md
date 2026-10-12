# CI, image, and packaging

[`.forgejo/workflows/ci.yml`](../.forgejo/workflows/ci.yml) is the whole automation surface: a **gate** that builds, vets, and tests the runtime and validates the chart and examples on every push and pull request, and a **publish** step that, on a push to `main` or a manual dispatch of `main`, builds the image, publishes it under the full source sha, and verifies the remote manifest.

**gate.** It runs on the `docker` label, the only one this instance's runners advertise, in the moving `:release` aos dev-base image (Go, helm, yq, just, Docker CLI). `GOPRIVATE=forgejo.coilysiren.me` keeps umbra, a private module fetched anonymously, off the public proxy and sumdb. After `go build`, `go vet`, and `go test` come three `just` verbs, so nothing passes on a laptop and differs here: `lint-examples`, `helm-lint-chart` (plain and `app`-bearing values), and `check-app-mount`, which renders the chart, projects the mount as kubelet does, and lints it with the real runtime, because a chart or guardfile change reaches production without touching Go ([apps.md](apps.md)).

**publish.** It runs on the trusted `deploy` label after the gate, on `push` or `workflow_dispatch` at `refs/heads/main`, never for a pull request. The manual path recovers a commit whose push did not queue Actions. `scripts/publish-image.sh` authenticates through password-stdin with a temporary Docker config, pushes one source-sha tag, and `docker manifest inspect` must resolve that exact reference before the job succeeds. There is no `:latest`, since the fleet keys rollouts by sha.

**If CI never runs** (mcp-beaver#10): the Actions unit must be active when the push lands (`has_actions`, toggled by the forgejo operator verbs), and the gate must name a label a runner advertises. `ubuntu-latest` is a GitHub-mirror label no Forgejo runner carries.

## Image

One [`Dockerfile`](../Dockerfile) builds the generic distroless nonroot runtime. The spec is mounted or COPYed in and named on the command line, so one binary drives every `.mcp.kdl`. The image is `forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver:<full-source-sha>`, private and single-architecture, published only when runtime source changes. The deploy runner holds the package-write credential and hands the exact reference to `deploy`, while fleet consumers use a read-only one.

Examples: `forgejo-issues.mcp.kdl` (five guarded issue tools scoped to `coilyco-*` and `kai`), `skillsmp.mcp.kdl`, `*.values.yaml` (a ClusterIP read surface, an optional NodePort write surface), and `upstream.values.yaml` (an allowlisted proxy with a co-located MCP container).

**Not yet built.** `action` composition, one tool chaining ops, waits on opcore exposing a composed chain. Tool-name disambiguation is open, since `verb_resource` is lossy when a resource has its own separator and unprefixed across mounted servers.

See [release.md](release.md) for binaries and [chart.md](chart.md) for the chart.
