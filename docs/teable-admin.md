# `teable-admin`

A separate binary, not a `mcp-beaver` subcommand. It runs the three field-schema
verbs the guarded MCP surface withholds — `create-table-field`, `edit-table-field`,
`convert-table-field` — because the safety they need is imperative
read-verify-restore logic, not a `can` grant's single-call allowlist.

## Why these three are withheld from the guard

Teable's field API has confirmed defects: `POST /field` accepts unknown
properties and silently discards them, `PATCH /field` returns 200 and applies
nothing, and `PUT /field/{id}/convert` once emptied 6,536 stored values while
reporting the type change it was asked for. A `can` grant validates shape and
forwards a call; none of that catches a write that succeeds at the HTTP layer
and lies about what happened underneath.

## What each verb does about it

`create-field` posts the field, then re-reads it through a fresh,
independent `GET` — never trusting the `POST`'s own response body — and
diffs every requested property against what came back. A refusal names which
properties did not survive. There is no `delete-field` verb, so a refused
create leaves a stray field only the Teable UI can remove.

`edit-field` snapshots every row's value for the field first, `PATCH`es it,
re-reads the definition independently, and snapshots the values again. The
honest default outcome is refusal with no value changes: Teable's own PATCH
usually validates nothing and applies nothing, so a fresh read-back shows the
field never moved, and this says so instead of trusting the 200.

`convert-field` cannot prevent server-side damage — by the time its first
read-back runs, Teable has already done whatever it did. What it can do:
snapshot every row's value before the call, diff after, and flag any row that
held a real value and does not anymore. With `--restore-on-data-loss`, it
writes the pre-convert values back through the record API, which is reliable
where the field API is not. This is the same practice the guardfile's own
`withhold` text already told an operator to do by hand — "do it in the UI
with an export in hand" — made automatic instead of dependent on remembering.

## Running it

```sh
TEABLE_BASE_URL=https://teable.internal/api TEABLE_API_TOKEN=... \
  teable-admin create-field --table tblXXX --spec field.json
```

`--spec` and `--patch` both take a path to a JSON file with the field
properties Teable's API expects, never inline JSON: a spec worth reviewing
before it runs is worth being a file a diff can show.

Never run this from an agent turn. It talks to Teable directly, not through a
guarded proxy, and `convert-field` is irreversible on Teable's side regardless
of what recovery this tool attempts afterward.
