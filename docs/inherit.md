# `inherit`: composing a guardfile from tiers

A guardfile can build on another. `inherit` names a base by relative path, and the composition is resolved before anything is served. Spec resolution is in [spec-mode.md](spec-mode.md).

```kdl
wrap ward mcp forgejo {
    inherit "operator/forgejo.kdl"
    auth header-token { header Authorization; prefix "token "; value env "FORGEJO_TOKEN" }
    never delete repo
    never create repo
}
```

Grants merge. `spec`, `base-url`, and `auth` are child-wins singletons, so a tier states its own credential and inherits the policy. `restrict` dedupes by param.

A child can only **narrow**. Its `never` shadows an inherited `can`, and a bare `can` that crosses an inherited `never` is a parse error naming `override can <verb> <resource>` as the way to escalate on purpose. That makes "this tier is weaker than its base" structural instead of a claim to re-check across two files. **A denied leaf mints no tool**: a `never` removes the operation from `tools/list` and `/api/` entirely. The inline grammar has no `never`, so it can inherit to add but not to subtract, and a strict-subset tier needs spec mode.

## Siblings compose too

The chain is read base-first, so a parser sees a grandparent's declarations, then a parent's, then the child's.

- **Union** is the default, and each node's own duplicate check arbitrates. A base tier's `confirm` and `withhold` therefore bind: a child cannot drop them, and restating one by name is a collision, not an override.
- **Child-wins** are `instructions`, `server-info`, and `rate-limit`, as `spec`, `base-url`, and `auth` are in the body. A nearer guardfile replaces its base's, and two in one file stay the typo-catching error.
- **An `app`** names its widget beside the guardfile that declared it, so a parent's widget resolves against the parent's directory.
- **A control on a tool this tier narrowed away** is dropped and `lint` warns, since nothing is left to gate. One a guardfile states on a tool it never minted stays an error: a typo.

Before mcp-beaver#113 a parent's siblings were silently dropped while its grants survived, widening the child.

## Flatten before you mount

A pod mounts one guardfile and cannot follow an `inherit` path, so the composition is resolved at author time and committed:

```sh
mcp-beaver flatten services/forgejo-mcp/forgejo.mcp.kdl -o services/forgejo-mcp/forgejo.flat.mcp.kdl [--check]
```

The flattened artifact is what the chart mounts and a reviewer reads, with a banner naming the source to edit. `--check` regenerates and compares without writing, failing on a stale file. Comments do not survive (KDL re-emission keeps data), so rationale belongs in `describe`. An `app` keeps its `file=` reference and the widget reaches the pod through the chart's `widgets` values, so `--check` covers the composition and not the widgets. `just check-app-mount` covers the widget in CI ([apps.md](apps.md)). The mounted shape is in [chart.md](chart.md).
