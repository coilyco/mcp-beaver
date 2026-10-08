"""Emit a beaver guardfile per probed upstream.

Policy only, never schemas: the engine derives the input schema, and
serve-upstream snapshots the upstream contract at connect time. Allow/withhold
comes from the upstream's own readOnlyHint, never the tool name, so an
undeclared tool is withheld. The measurement behind that: docs/pull.md.
"""
import json, sys, pathlib

HERE = pathlib.Path(__file__).resolve().parent

def kdl_str(s):
    return '"' + str(s).replace("\\", "\\\\").replace('"', '\\"') + '"'

def coverage(s):
    """Kai's call: a server that annotates nothing still belongs in the index,
    marked. Excluding it loses the content, and the mark is the content."""
    h = [t.get("readOnlyHint") for t in s["tools"]]
    if all(x is None for x in h): return "undeclared"
    if any(x is None for x in h): return "partial"
    return "declared"


def kin(name, allow):
    """A substitute only counts if it shares the tool's own namespace. A generic
    fallback reads as a suggestion and is not one."""
    head = name.split("_")[0]
    same = [a["name"] for a in allow if a["name"].split("_")[0] == head]
    return same[0] if same else None


def guardfile(s):
    tools = s["tools"]
    allow  = [t for t in tools if t.get("readOnlyHint") is True]
    deny   = [t for t in tools if t.get("readOnlyHint") is False]
    unknown= [t for t in tools if t.get("readOnlyHint") is None]
    L = []
    cov = coverage(s)
    L.append(f"// {s['name']}")
    L.append(f"// Generated: {len(tools)} tools, {len(allow)} read-only (exposed), {len(deny)} mutating, {len(unknown)} undeclared.")
    L.append("")
    # `instructions` is a sibling node, as in every other beaver guardfile.
    if s.get("description"):
        L.append("instructions {")
        L.append(f"    text {kdl_str(' '.join(s['description'].split())[:180])}")
        L.append("}")
        L.append("")
    L.append(f"mcp-upstream {kdl_str(s['name'])} {{")
    L.append(f"    url {kdl_str(s['url'])}")
    L.append('    transport "streamable-http"')
    L.append("")
    L.append("    // Machine-readable, so a consumer can filter on it rather than")
    L.append("    // parse the comments above.")
    L.append(f"    annotation-coverage {kdl_str(cov)} annotated={len(allow) + len(deny)} silent={len(unknown)}")
    if allow:
        L.append("")
        L.append("    // readOnlyHint: true, declared by the upstream")
        for t in allow:
            L.append(f"    can {kdl_str(t['name'])}")
    # No withhold blocks, on purpose: docs/registry-pull.md says why.
    if deny or unknown:
        L.append("")
        L.append(f"    // Not exposed: {len(deny)} declared mutating, {len(unknown)} undeclared.")
        L.append("    // Names deliberately omitted, see annotation-coverage above.")
    L.append("}")
    return "\n".join(L)

def invocation(s):
    allow = [t["name"] for t in s["tools"] if t.get("readOnlyHint") is True]
    if not allow:
        return f"# {s['name']}: no tool declares readOnlyHint true, nothing to allow"
    flags = " \\\n    ".join(f"--tool {t}" for t in allow)
    return (f"mcp-beaver serve-upstream \\\n    --upstream {s['url']} \\\n    {flags} \\\n"
            f"    --read-only strict")

if __name__ == "__main__":
    D = [s for s in json.load((HERE / "probe.json").open()) if s["state"] == "ok"]
    out = HERE / "guardfiles"; out.mkdir(exist_ok=True)
    withhint = [s for s in D if any(t.get("readOnlyHint") is not None for t in s["tools"])]
    for s in D:
        out.joinpath(s["name"].replace("/", "__") + ".mcp.kdl").write_text(guardfile(s) + "\n")
    print(f"wrote {len(D)} guardfiles, {len(withhint)} of them from declared hints")
    pick = sys.argv[1] if len(sys.argv) > 1 else max(
        withhint, key=lambda s: sum(1 for t in s["tools"] if t.get("readOnlyHint") is not None))["name"]
    s = next(x for x in D if x["name"] == pick)
    print("\n" + "=" * 66 + f"\n{pick}\n" + "=" * 66)
    print(guardfile(s))
    print("\n--- what runs today, no new grammar needed ---\n")
    print(invocation(s))
