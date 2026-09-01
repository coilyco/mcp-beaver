"""Render probe.json into the directory page. Chrome is authored, every fact is
generated: nothing about a server is typed by hand."""
import json, html, datetime, collections, pathlib

D = json.load(open("probe.json"))
OK = sorted([s for s in D if s["state"] == "ok"], key=lambda s: -len(s["tools"]))
BAD = [s for s in D if s["state"] != "ok"]
TOOLS = [t for s in OK for t in s["tools"]]
K = collections.Counter(t["klass"] for t in TOOLS)
STATES = collections.Counter(s["state"] for s in BAD)
STAMP = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
e = lambda x: html.escape(str(x or ""))

def mut(s):  return sum(1 for t in s["tools"] if t["klass"] == "mutates")
FEATURE = max(OK, key=lambda s: (mut(s), len(s["tools"])))

def pill(k):
    c = {"reads": "p-ok", "mutates": "p-risk", "unknown": "p-none"}[k]
    return f'<span class="pill {c}">{k}</span>'

rows = "\n".join(
    f'<div class="td"><span class="td__n">{e(s["name"])}</span>'
    f'<span class="num">{len(s["tools"])}</span>'
    f'<span class="num" style="color:var(--risk)">{mut(s) or "&middot;"}</span>'
    f'<span class="muted">{e(s["description"][:78])}</span></div>'
    for s in OK)

badrows = "\n".join(
    f'<div class="td td--bad"><span class="td__n">{e(s["name"])}</span>'
    f'<span class="pill p-lock">{e(s["state"])}</span>'
    f'<span class="muted" style="grid-column:3/-1">{e(s["description"][:90])}</span></div>'
    for s in sorted(BAD, key=lambda s: s["state"]))

def toollist(kind):
    items = [t for t in FEATURE["tools"] if t["klass"] == kind]
    lis = "\n".join(f'<li>{e(t["name"])}<span>{e(t["description"][:72])}</span></li>' for t in items)
    return len(items), lis

nr, reads = toollist("reads"); nm, muts = toollist("mutates"); nu, unk = toollist("unknown")
denies = " ".join(f'"{t["name"]}"' for t in FEATURE["tools"] if t["klass"] == "mutates")
allows = " ".join(f'"{t["name"]}"' for t in FEATURE["tools"] if t["klass"] == "reads")

statefreq = " &middot; ".join(f"{v}&times; {e(k)}" for k, v in STATES.most_common())

TPL = pathlib.Path("shell.html").read_text()
out = (TPL
  .replace("{{STAMP}}", STAMP)
  .replace("{{NPROBED}}", str(len(D)))
  .replace("{{NOK}}", str(len(OK)))
  .replace("{{NBAD}}", str(len(BAD)))
  .replace("{{NTOOLS}}", str(len(TOOLS)))
  .replace("{{NREAD}}", str(K["reads"]))
  .replace("{{NMUT}}", str(K["mutates"]))
  .replace("{{NUNK}}", str(K["unknown"]))
  .replace("{{PCTOK}}", f"{100*len(OK)/len(D):.0f}")
  .replace("{{ROWS}}", rows)
  .replace("{{BADROWS}}", badrows)
  .replace("{{STATEFREQ}}", statefreq)
  .replace("{{FNAME}}", e(FEATURE["name"]))
  .replace("{{FURL}}", e(FEATURE["url"]))
  .replace("{{FDESC}}", e(FEATURE["description"]))
  .replace("{{FTOTAL}}", str(len(FEATURE["tools"])))
  .replace("{{NR}}", str(nr)).replace("{{READS}}", reads)
  .replace("{{NM}}", str(nm)).replace("{{MUTS}}", muts)
  .replace("{{NU}}", str(nu)).replace("{{UNKS}}", unk or '<li class="empty">none</li>')
  .replace("{{DENIES}}", e(denies)).replace("{{ALLOWS}}", e(allows)))
pathlib.Path("/tmp/beaver-directory.html").write_text(out)
print(f"rendered {len(OK)} servers, {len(TOOLS)} tools, feature={FEATURE['name']}")
