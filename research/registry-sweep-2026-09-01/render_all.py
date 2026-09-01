"""Render every generated guardfile into one page, grouped by annotation coverage."""
import json, html, pathlib, datetime, collections

D = [s for s in json.load(open("probe.json")) if s["state"] == "ok"]
GF = pathlib.Path("guardfiles")
e = lambda x: html.escape(str(x or ""))
STAMP = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M UTC")

def coverage(s):
    h = [t.get("readOnlyHint") for t in s["tools"]]
    if all(x is None for x in h): return "undeclared"
    if any(x is None for x in h): return "partial"
    return "declared"

ORDER = ["declared", "partial", "undeclared"]
BLURB = {
 "declared": "Every tool carries <code>readOnlyHint</code>. The allowlist is the upstream's own answer, not a guess.",
 "partial":  "Some tools declare, some do not. The declared ones are allowed, the silent ones are withheld alongside the mutating ones.",
 "undeclared": "No tool declares anything. Every tool is withheld, and the entry stays in the index carrying that fact. The mark is the content.",
}
groups = collections.OrderedDict((k, []) for k in ORDER)
for s in sorted(D, key=lambda s: -len(s["tools"])):
    groups[coverage(s)].append(s)

def card(s):
    path = GF / (s["name"].replace("/", "__") + ".mcp.kdl")
    kdl = path.read_text()
    allow = sum(1 for t in s["tools"] if t.get("readOnlyHint") is True)
    return f"""<article class="gf">
  <header class="gf__h">
    <span class="gf__n">{e(s['name'])}</span>
    <span class="gf__m">{len(s['tools'])} tools &middot; {allow} allowed &middot; {len(s['tools'])-allow} withheld</span>
  </header>
  <pre>{e(kdl)}</pre>
</article>"""

sections = []
for k in ORDER:
    ss = groups[k]
    tools = sum(len(s["tools"]) for s in ss)
    sections.append(f"""<section class="grp grp--{k}">
  <div class="sec-head">
    <p class="eyebrow">{k} &nbsp;//&nbsp; {len(ss)} servers &nbsp;//&nbsp; {tools} tools</p>
    <h2>{'Allowlist from the upstream' if k=='declared' else ('Mixed' if k=='partial' else 'Nothing declared, everything withheld')}</h2>
    <p>{BLURB[k]}</p>
  </div>
  {''.join(card(s) for s in ss)}
</section>""")

TPL = pathlib.Path("shell_all.html").read_text()
out = (TPL.replace("{{STAMP}}", STAMP)
          .replace("{{N}}", str(len(D)))
          .replace("{{NDEC}}", str(len(groups['declared'])))
          .replace("{{NPART}}", str(len(groups['partial'])))
          .replace("{{NUND}}", str(len(groups['undeclared'])))
          .replace("{{NTOOLS}}", str(sum(len(s['tools']) for s in D)))
          .replace("{{NALLOW}}", str(sum(1 for s in D for t in s['tools'] if t.get('readOnlyHint') is True)))
          .replace("{{SECTIONS}}", "\n".join(sections)))
pathlib.Path("/tmp/beaver-guardfiles.html").write_text(out)
print(f"rendered {len(D)} guardfiles: {len(groups['declared'])} declared, {len(groups['partial'])} partial, {len(groups['undeclared'])} undeclared")
