import json, html, pathlib, datetime, collections
D = [s for s in json.load(open("probe.json")) if s["state"] == "ok"]
e = lambda x: html.escape(str(x or ""))
STAMP = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M UTC")

def coverage(s):
    h = [t.get("readOnlyHint") for t in s["tools"]]
    if all(x is None for x in h): return "undeclared"
    if any(x is None for x in h): return "partial"
    return "declared"

ORDER = ["declared", "partial", "undeclared"]
HEAD = {"declared": "Allowlist from the upstream",
        "partial": "Mixed",
        "undeclared": "Nothing declared"}
BLURB = {
 "declared": "Every tool carries <code>readOnlyHint</code>, so read-only scope is the upstream's own answer rather than a guess.",
 "partial":  "Some tools declare, some do not. Read-only scope keeps the declared reads and drops the rest.",
 "undeclared": "No tool declares anything, so read-only scope exposes nothing at all. The entry stays, carrying that fact.",
}
groups = collections.OrderedDict((k, []) for k in ORDER)
for s in sorted(D, key=lambda s: -len(s["tools"])):
    groups[coverage(s)].append(s)

def SEG(ax, opts):
    btns = "".join(f'<button data-v="{v}">{l}</button>' for v, l in opts)
    return f'<div class="seg" data-axis="{ax}">{btns}</div>' 

def card(s):
    reads = sum(1 for t in s["tools"] if t.get("readOnlyHint") is True)
    return f"""<article class="gf" data-server="{e(s['name'])}">
  <header class="gf__h">
    <span class="gf__n">{e(s['name'])}</span>
    <span class="gf__m">{len(s['tools'])} tools &middot; {reads} declared read-only</span>
    <span class="gf__ctl">
      {SEG('scope', [('read','read-only'),('write','read-write'),('all','+ undeclared')])}
      {SEG('voice', [('verbose','verbose'),('terse','terse')])}
    </span>
  </header>
  <pre></pre>
</article>"""

sections = []
for k in ORDER:
    ss = groups[k]
    sections.append(f"""<section class="grp grp--{k}">
  <div class="sec-head">
    <p class="eyebrow">{k} &nbsp;//&nbsp; {len(ss)} servers &nbsp;//&nbsp; {sum(len(s['tools']) for s in ss)} tools</p>
    <h2>{HEAD[k]}</h2>
    <p>{BLURB[k]}</p>
  </div>
  {''.join(card(s) for s in ss)}
</section>""")

data = pathlib.Path("embed.json").read_text()
out = (pathlib.Path("shell_interactive.html").read_text()
       .replace("{{STAMP}}", STAMP)
       .replace("{{NDEC}}", str(len(groups['declared'])))
       .replace("{{NPART}}", str(len(groups['partial'])))
       .replace("{{NUND}}", str(len(groups['undeclared'])))
       .replace("{{NTOOLS}}", str(sum(len(s['tools']) for s in D)))
       .replace("{{NALLOW}}", str(sum(1 for s in D for t in s['tools'] if t.get('readOnlyHint') is True)))
       .replace("{{N}}", str(len(D)))
       .replace("{{SECTIONS}}", "\n".join(sections))
       .replace("{{DATA}}", data))
pathlib.Path("/tmp/beaver-guardfiles.html").write_text(out)
print(f"rendered {len(D)} interactive cards, {len(data)//1024} KB of embedded data")
