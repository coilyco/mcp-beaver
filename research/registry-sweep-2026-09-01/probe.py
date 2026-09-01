"""Enumerate the official MCP registry, connect to every server that publishes a
remote, and record the tool surface it actually serves.

Shells out to curl rather than urllib: the system trust store resolves these
hosts and Python's bundled one does not.
"""
import json, subprocess, sys, re
from concurrent.futures import ThreadPoolExecutor

REGISTRY = "https://registry.modelcontextprotocol.io/v0/servers"
PROTO = "2025-06-18"
UA = ["-H", "Content-Type: application/json",
      "-H", "Accept: application/json, text/event-stream",
      "-H", f"MCP-Protocol-Version: {PROTO}"]

MUTATE = re.compile(r"\b(creat|updat|delet|writ|refresh|invalidat|set|send|post|"
                    r"execut|run|kill|inject|install|deploy|publish|remov|add|"
                    r"clear|purg|warmup|reset|modif|edit|upload|insert|drop|"
                    r"cancel|approv|submit|schedul|start|stop|restart)", re.I)


def curl(args, timeout=20):
    try:
        r = subprocess.run(["curl", "-s", "-m", str(timeout), *args],
                           capture_output=True, text=True, timeout=timeout + 8)
        return r.stdout
    except Exception:
        return ""


def frames(raw):
    """A streamable-http reply may be JSON or SSE. Return the first JSON object."""
    for line in raw.splitlines():
        s = line[5:].strip() if line.startswith("data:") else line.strip()
        if s.startswith("{"):
            try:
                return json.loads(s)
            except Exception:
                continue
    return {}


def enumerate_servers(pages=3, limit=100):
    out, cursor = [], None
    for _ in range(pages):
        url = f"{REGISTRY}?limit={limit}" + (f"&cursor={cursor}" if cursor else "")
        d = json.loads(curl([url]) or "{}")
        for e in d.get("servers", []):
            s, m = e["server"], e.get("_meta", {}).get(
                "io.modelcontextprotocol.registry/official", {})
            if not m.get("isLatest") or m.get("status") != "active":
                continue
            remote = next((r.get("url") for r in (s.get("remotes") or []) if r.get("url")), None)
            if remote:
                out.append({"name": s["name"], "url": remote,
                            "description": (s.get("description") or "").strip(),
                            "title": s.get("title"), "published": m.get("publishedAt", "")[:10],
                            "repository": (s.get("repository") or {}).get("url")})
        cursor = (d.get("metadata") or {}).get("nextCursor")
        if not cursor:
            break
    seen, uniq = set(), []
    for s in out:
        if s["name"] not in seen:
            seen.add(s["name"]); uniq.append(s)
    return uniq


def probe(server):
    url = server["url"]
    init_body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
        "protocolVersion": PROTO, "capabilities": {},
        "clientInfo": {"name": "beaver-probe", "version": "0.1.0"}}})
    hdr = curl(["-D", "-", "-o", "/dev/null", "-w", "%{http_code}", "-X", "POST", url,
                *UA, "-d", init_body])
    code = hdr.strip().splitlines()[-1] if hdr.strip() else "000"
    if code != "200":
        return {**server, "state": f"HTTP {code}", "tools": None}
    sid = ""
    for line in hdr.splitlines():
        if line.lower().startswith("mcp-session-id:"):
            sid = line.split(":", 1)[1].strip()
    sess = ["-H", f"Mcp-Session-Id: {sid}"] if sid else []
    curl(["-o", "/dev/null", "-X", "POST", url, *UA, *sess, "-d",
          json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"})], timeout=10)

    tools, err = None, None
    for _ in range(2):  # one retry: a single reading produces false negatives
        d = frames(curl(["-X", "POST", url, *UA, *sess, "-d",
                         json.dumps({"jsonrpc": "2.0", "id": 2, "method": "tools/list",
                                     "params": {}})]))
        if "result" in d:
            tools = d["result"].get("tools", [])
            if tools:
                break
        elif "error" in d:
            err = str(d["error"].get("message", ""))[:60]
    if tools is None:
        return {**server, "state": err or "no result", "tools": None}
    classified = [{"name": t.get("name", ""),
                   "description": (t.get("description") or "").strip(),
                   "annotations": t.get("annotations") or {},
                   "readOnlyHint": (t.get("annotations") or {}).get("readOnlyHint")}
                  for t in tools]
    return {**server, "state": "ok", "tools": classified}


if __name__ == "__main__":
    want = int(sys.argv[1]) if len(sys.argv) > 1 else 60
    servers = enumerate_servers()[:want]
    print(f"enumerated {len(servers)} latest+active servers with a remote", file=sys.stderr)
    with ThreadPoolExecutor(max_workers=12) as pool:
        results = list(pool.map(probe, servers))
    json.dump(results, open("probe.json", "w"), indent=1)
    ok = [r for r in results if r["state"] == "ok"]
    print(f"probed {len(results)}  ok {len(ok)}  tools {sum(len(r['tools']) for r in ok)}",
          file=sys.stderr)
