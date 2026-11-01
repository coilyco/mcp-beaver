# Per-repo task manifest. Run `just` (or `just --list`) to see every verb.
#
# Recipes take trailing arguments directly: `just <verb> a b`, where the
# retired form was `ward exec <verb> -- a b`.
#
# One line of comment per recipe on purpose: just reads only the LAST comment
# line above a recipe, so a wrapped description silently truncates to its tail.
#
# `ward exec` is retired. `.ward/ward.yaml` survives carrying catalog metadata
# only, because the catalog hooks upstream in agentic-os pin that exact path.

set positional-arguments

# Default target: list every available recipe.
default:
    @just --list --unsorted

# Build all Go packages.
build *ARGS:
    @bash scripts/ward-command.sh build "$@"

# Run the Go test suite.
test *ARGS:
    @bash scripts/ward-command.sh test "$@"

# Run go vet across the tree.
vet *ARGS:
    @bash scripts/ward-command.sh vet "$@"

# Reconcile Go module dependencies.
tidy *ARGS:
    @bash scripts/ward-command.sh tidy "$@"

# Format the Go source tree.
fmt *ARGS:
    @bash scripts/ward-command.sh fmt "$@"

# Build the generic mcp-beaver runtime image locally.
image-build *ARGS:
    @docker build -t mcp-beaver:local . "$@"

# Validate the trusted GHCR publisher shell contract.
check-publish *ARGS:
    @bash -n scripts/publish-image.sh "$@"

# Pin umbra and reconcile Go module dependencies. Pass an optional version after `--`.
pin-umbra *ARGS:
    @bash scripts/ward-command.sh pin-umbra "$@"

# Serve the skillsmp example spec on localhost:18080 for local MCP handshake checks.
serve-example *ARGS:
    @bash scripts/ward-command.sh serve-example "$@"

# Lint every committed example guardfile, so a shipped reference cannot rot.
lint-examples *ARGS:
    @bash scripts/ward-command.sh lint-examples "$@"

# Cross-compile the release binaries into dist/, stamped with the current tag.
release-build *ARGS:
    @sh scripts/release-build.sh "$@"

# Render the brew formula from the dist/ binaries. Needs an exact release tag.
release-package *ARGS:
    @sh scripts/render-packaging.sh "$@"

# Decide whether one validated revision should publish a release.
release-impact *ARGS:
    @sh scripts/release-impact.sh "$@"

# Sweep the MCP registry into a directory of guardfiles and pages. Pass `-o <dir>` and any `directory` flag after `--`.
directory *ARGS:
    @bash scripts/ward-command.sh directory "$@"

# Lint the auth-neutral mcp-beaver chart across the spec, app-widget, and guardfile-stated upstream shapes.
helm-lint-chart *ARGS:
    @helm lint chart --set-file spec=examples/skillsmp.mcp.kdl -f examples/skillsmp.values.yaml "$@"
    @helm lint chart --set-file spec=examples/guardfile-siblings.mcp.kdl --set-file widgets[0].content=examples/widgets/things.html --set widgets[0].path=widgets/things.html "$@"
    @helm lint chart --set-file spec=examples/registry-upstream.mcp.kdl -f examples/registry-upstream.values.yaml "$@"

# Render the app-bearing chart, materialize its mount, and lint it as the pod would.
check-app-mount *ARGS:
    @bash scripts/check-app-mount.sh "$@"

# Render the chart for the app-bearing example, so a widget mount is readable.
helm-template-app *ARGS:
    @helm template mcp-beaver chart --set-file spec=examples/guardfile-siblings.mcp.kdl --set-file widgets[0].content=examples/widgets/things.html --set widgets[0].path=widgets/things.html "$@"

# Render the default ClusterIP chart shape.
helm-template-clusterip *ARGS:
    @helm template mcp-beaver chart --namespace mcp-beaver --set-file spec=examples/skillsmp.mcp.kdl -f examples/skillsmp.values.yaml "$@"

# Render the optional NodePort chart shape.
helm-template-nodeport *ARGS:
    @helm template mcp-beaver chart --namespace mcp-beaver --set-file spec=examples/forgejo-issues.mcp.kdl -f examples/forgejo-issues.values.yaml "$@"

# Render the allowlisted upstream-proxy chart shape.
helm-template-upstream *ARGS:
    @helm template mcp-beaver chart --namespace mcp-beaver -f examples/upstream.values.yaml "$@"

# Render the upstream-proxy chart shape stated as a mounted guardfile.
helm-template-upstream-spec *ARGS:
    @helm template mcp-beaver chart --namespace mcp-beaver --set-file spec=examples/registry-upstream.mcp.kdl -f examples/registry-upstream.values.yaml "$@"

# Render the authenticated hosted-upstream chart shape.
helm-template-upstream-authed *ARGS:
    @helm template mcp-beaver chart --namespace mcp-beaver -f examples/upstream-authed.values.yaml "$@"

# Render the spec-mode sidecar shape, wrapping a co-located non-MCP process.
helm-template-sidecar *ARGS:
    @helm template mcp-beaver chart --namespace mcp-beaver --set-file spec=examples/sidecar.mcp.kdl -f examples/sidecar.values.yaml "$@"

# Probe the MCP registry live and rewrite scripts/registry-probe/probe.json. Pass a server count after `--`.
registry-probe *ARGS:
    @python3 scripts/registry-probe/probe.py "$@"

# Regenerate the prototype guardfiles from the committed probe, offline, then format them.
registry-guardfiles *ARGS:
    @python3 scripts/registry-probe/classify.py
    @python3 scripts/registry-probe/gen_kdl.py "$@"
    @kdlfmt format scripts/registry-probe/guardfiles/*.mcp.kdl

# Render the prototype directory pages from the committed probe into scripts/registry-probe/out/.
registry-render *ARGS:
    @python3 scripts/registry-probe/render.py
    @python3 scripts/registry-probe/render_all.py
    @python3 scripts/registry-probe/render_interactive.py
