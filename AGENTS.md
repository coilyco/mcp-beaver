---
ward:
  workflow: merge-remote-main
---
# Agent instructions

## Scope

`mcp-beaver` turns a umbra Guardfile into a guarded MCP server with an
automatic matching HTTP tool API, and ships the generic runtime as an OCI
image plus Helm chart. The module, the binary, the chart, and the served
`mcp_beaver_info` tool all carry that name.

## The old name, and where it still belongs

This was `ward-mcp`, and the rename is done here. Two places keep the old
spelling on purpose and are not stragglers to sweep:

- **`wrap ward mcp <name>`** opens every guardfile. It is umbra's inline
  grammar rather than this project's name, and every deployed spec starts with
  it. It moves when umbra moves, not before.
- **A live resource name.** A Kubernetes selector label is immutable, so a
  release installed before the rename keeps its old names through
  `nameOverride: ward-mcp`. See [the chart](docs/chart.md).

Package installers name the old binary so they can mark it outdated and
uninstall it. That is the tap's and the bucket's business, not this repo's.

## Project shape

The Go runtime lives at `cmd/mcp-beaver` and `internal/`, the generic Helm
chart at `chart/`, and the guardfile examples under `examples/`. `docs/` holds
the design and feature pages the README points at.

## Repo boundaries

Keep the runtime spec-driven and deny by absence. `umbra` owns guarded HTTP
execution. `mcp-beaver` owns MCP and HTTP tool projection plus transport.
`deploy` owns service configuration, inbound identity and authentication,
ingress, credentials, and rollout.

## Commands

Route development through Ward using the verbs in [`.ward/ward.yaml`](.ward/ward.yaml).

## Validation

Run `just test` and the relevant chart or image checks before landing a
change. Never bypass commit hooks.

## Safety

Do not widen a Guardfile grant, tool allowlist, path restriction, or secret
boundary as incidental cleanup. Keep tokens and deployment identifiers out of
tracked files and logs.

## Cross-repo contracts

umbra provides the guarded-CLI engine this builds on, consumed as a private
Forgejo module. Deploy consumes the published image by full source sha and owns
rollout. The catalog pre-commit hooks are authored in agentic-os and consumed
here by upstream rev, never forked.

## Agent rules

<!-- BEGIN managed by agentic-os/scripts/apply-git-workflow.py -->
### Git workflow

**This repo runs the `merge-remote-main` lane**, declared as `ward.workflow` in this file's frontmatter. The agent commits, pushes straight to `main`, and closes the issue. Pushing `main` here is the expected path, not an escalation.

The fleet runs two lanes, and both authorize the same core actions:

* `merge-remote-main` - the agent commits, pushes to `main`, and closes the issue. No branch and no pull request.
* `pull-request-and-merge` - the agent commits to a task branch, pushes it, opens a pull request, and merges that pull request itself once it is green.

**Every lane slug names what the AGENT does, never what someone else does.** `pull-request-and-merge` carries the merge because the agent that authored the code merges its own pull request. `pull-request` drops `-and-merge` because the author stops at the pull request and the director merge lane takes over. Reading `pull-request-and-merge` as "someone else merges it later" inverts the two lanes and leaves finished work sitting unmerged.

**These actions are pre-authorized on every lane, and the agent MUST take them without asking first.** Committing, creating a branch, pushing a branch, pushing the lane's own destination, and opening a pull request are ordinary reversible work, not the destructive wall that earns a question. Stopping to ask is how a turn ends with the work stranded in a dirty worktree.

* **ALWAYS commit** in-scope work and **ALWAYS push** it to the canonical remote before pausing, reporting a checkpoint, handing off, or ending a turn. A local-only commit is not a checkpoint.
* **ALWAYS open the pull request** in the same turn as the branch's first push, on every lane except `remote-branch-only`. A pushed branch with no pull request is litter nobody reviews.
* **NEVER `--no-verify`** and **NEVER force-push**. Those two are the real walls, and they stay closed.
* **ALWAYS merge your own pull request on `pull-request-and-merge`**, in the same turn, as soon as it is green. Reporting it as open and awaiting someone is the failure this lane exists to prevent.
* **NEVER merge on `pull-request` or `remote-branch-only`.** Those two stop where they stop, and the director merge lane carries a `pull-request` from there.
<!-- END managed by agentic-os/scripts/apply-git-workflow.py -->

Use she/her for Kai. No em dashes, italics, or semicolons in prose. Name the
actor in every action sentence.

## Release

Push validated work to canonical Forgejo `main`. CI publishes an immutable
source-SHA image. `deploy` consumes that exact reference.

## Checkout residency

This repo is not in Agent Compose's `repository-plan.yaml`, so it has no
resident checkout under `~/projects/<owner>/`. That is intentional. Work it
from a task-scoped temporary clone, and remove that clone once the work lands.

A temporary root can be purged at any time, so commit and push before pausing,
switching tasks, or ending a session. The remote is the only durable artifact.

## See also

* [README.md](README.md) - product boundary and quickstart.
* [docs/FEATURES.md](docs/FEATURES.md) - shipped runtime and chart inventory.
* [`.ward/ward.yaml`](.ward/ward.yaml) - allowlisted commands.
