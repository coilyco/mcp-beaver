"""Emit a beaver guardfile per probed upstream.

The file carries policy, never schemas. beaver's DESIGN.md: "no per-tool input
schema, because the engine derives it". serve-upstream already snapshots the
upstream contracts at connect time, so restating them here would duplicate a
source of truth that drifts.

Allow/withhold comes from the upstream's own annotations.readOnlyHint, never
from the tool name. Measured on 120 tools with a declared hint, name-based
screening caught 6 to 10 of 24 real mutators and the misses were payment tools,
so an undeclared tool is withheld rather than guessed at.
"""
import json, sys, pathlib

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
    L.append(f"// {s['name']} - generated from the MCP registry and a live tools/list.")
    L.append(f"// {len(tools)} tools: {len(allow)} declared read-only, {len(deny)} declared mutating,")
    L.append(f"// {len(unknown)} undeclared. Only declared read-only tools are exposed.")
    L.append("")
    L.append(f"wrap mcp upstream {kdl_str(s['name'])} {{")
    L.append(f"    url {kdl_str(s['url'])}")
    L.append('    transport "streamable-http"')
    L.append("")
    L.append("    // Machine-readable, so a consumer can filter on it rather than")
    L.append("    // parse the comments above.")
    L.append(f"    annotation-coverage {kdl_str(cov)} annotated={len(allow) + len(deny)} silent={len(unknown)}")
    if s.get("description"):
        L.append("")
        L.append("    instructions {")
        L.append(f"        text {kdl_str(s['description'][:180])}")
        L.append("    }")
    if allow:
        L.append("")
        L.append("    // readOnlyHint: true, declared by the upstream")
        for t in allow:
            L.append(f"    can {kdl_str(t['name'])}")
    # No withhold blocks. `withhold` mints a discoverable stub that appears in
    # tools/list, so generating one per excluded tool would advertise 112 tools
    # that refuse every call, 54% of the surface, and a consumer pays for that
    # list every turn. The allowlist is closed by construction: anything absent
    # is denied without being named, and a denial list only goes stale when the
    # upstream adds a tool. withhold stays a hand-authoring device for the two
    # or three omissions a caller will actually reach for.
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
    D = [s for s in json.load(open("probe.json")) if s["state"] == "ok"]
    out = pathlib.Path("guardfiles"); out.mkdir(exist_ok=True)
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
