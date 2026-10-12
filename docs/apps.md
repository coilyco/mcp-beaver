# MCP Apps

An MCP App is an HTML widget a host renders in place of a tool's text result. The `app` node declares one, and mcp-beaver serves it at a `ui://` URI (mime `text/html;profile=mcp-app`) with `_meta.ui.resourceUri` on each linked tool.

```kdl
app "thing-card" uri="ui://thing-card/mcp-app.html" file="widgets/things.html" prefers-border=#true {
    tool "get_thing"
    tool "search_things" visibility="app"
    csp { connect "https://*.cesium.com"; resource "https://cdn.example.com" }
    permission "clipboardWrite"
}
```

`lint --apps <spec>` prints the widget uri beside each tool (`-` for none).

**Ownership.** Beaver declares the widget and the link. The host owns the iframe, the postMessage bridge, and rendering, and the widget is the author's code. A widget calls tools back through the host, and those are the `can` grants, so its blast radius is the audited file.

**The body is a file.** `file` resolves against the directory of the guardfile that declared it, so a spec and its widgets move together. An absolute path is refused, since the container has no such path and the failure would land at startup, not lint. Unlike [`resource`](guardfile-siblings.md), which is inline-only to avoid an unguarded egress path, a local file read once reaches no network. A body is bounded at 10 MB.

**Linking tools.** `tool` is what makes a host fetch the widget, so these are build errors: an `app` with no `tool`, a tool this spec does not mint, or two apps claiming one tool. `visibility` says who may reach the tool (`model`, `app`, or both), which the host acts on and the runtime does not enforce.

**csp and permissions** ride on the resource, never the tool, and an omitted csp list is `'none'`. Lists: `connect` (fetch, XHR, WebSocket), `resource` (images, scripts, styles, fonts, media), `frame`, `base-uri`. `permission` takes `camera`, `microphone`, `geolocation`, or `clipboardWrite`, which a host MAY honour. `domain` asks for a dedicated sandbox origin.

## Deploying one

The chart mounts the guardfile at `/spec/<specName>.mcp.kdl` and each widget beside it, one `widgets` entry per `app file=`:

```sh
helm upgrade --install things mcp-beaver --set-file spec=things.mcp.kdl \
  --set-file widgets[0].content=widgets/things.html --set widgets[0].path=widgets/things.html
```

`path` is spelled as the guardfile spells it. A missing `content`, a `..`, or two paths colliding on one ConfigMap key fail at template time. Widgets are base64-encoded into the ConfigMap, a third over their size against its 1 MiB cap, and the chart refuses first. `just check-app-mount` renders the chart, projects the mount as kubelet does, and lints it with the real runtime. A flattened guardfile is no different ([inherit.md](inherit.md)).

Example: `examples/guardfile-siblings.mcp.kdl` with `examples/widgets/things.html`. Wire shapes follow `@modelcontextprotocol/ext-apps` (2026-01-26), and `_meta` carries the flat `ui/resourceUri` too, for older hosts.
