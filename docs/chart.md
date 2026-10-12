# The mcp-beaver Helm chart

The auth-neutral distribution vehicle: one chart, one runtime image, many releases. It mounts a `.mcp.kdl` guardfile or wraps an existing streamable-HTTP MCP with an exact tool allowlist, so adding an MCP runtime is a values file plus `helm upgrade`.

```sh
helm upgrade --install skillsmp mcp-beaver -f skillsmp.values.yaml \
  --set-file spec=skillsmp.mcp.kdl --set image.tag=<built-runtime-sha>
```

The chart is spec-opaque: it never parses the `.mcp.kdl`, and the spec never reaches into the chart. `deploy` composes this contract with its exposure charts and owns rollout. Values: [chart-values.md](chart-values.md).

## What it templates

- `configmap-spec.yaml` wherever a `spec` is mounted, putting the `.mcp.kdl` at `/spec/<name>.mcp.kdl` beside any `widgets` ([apps.md](apps.md)). The volume lists what it projects, so a key without a matching item stays unmounted.
- `deployment.yaml` renders the runtime in spec or upstream mode, injects application tokens when asked, and appends optional co-located containers.
- `service.yaml` renders a ClusterIP, or a NodePort when `service.nodePort` is set.
- `externalsecret.yaml` pulls each SSM-path `secret` entry into the environment variable the guardfile names.

## Upstream mode, as values or as a guardfile

`runtime.mode: upstream` carries both ways to reach a passthrough proxy. Set the `upstream` block and the chart renders `serve-upstream` with `--upstream`, `--tool`, `--upstream-header`, and `--oauth2-client`. Set `spec` to an `mcp-upstream` guardfile instead and it mounts as in spec mode and runs `serve-upstream /spec/<specName>.mcp.kdl`. The file names the endpoint, allowlist, credential, and server, so the chart refuses `upstream.url`, `.name`, `.tools`, `.headers`, and `.oauth2Clients` beside it, and `upstream.connectTimeout` still applies. `just helm-template-upstream-spec` renders it from `examples/` ([upstream.md](upstream.md)).

## Sidecars, upgrades, exposure

- **`extraContainers`** works in both modes: the chart appends it with no mode condition, and only the runtime args, the `/spec` mount, and the volumes are mode-conditional. **Upstream mode** wraps a sidecar that already speaks MCP, which `serve-upstream` snapshots and proxies. **Spec mode** wraps a sidecar speaking plain HTTP JSON, with `base-url` at `127.0.0.1` since containers share a network namespace. That serves a bundled dataset with no third-party limits or uptime.
- **Upgrading a release installed as `ward-mcp`** cannot be in place if `nameOverride` changes, because the Deployment's `spec.selector` is immutable. Reinstall, or keep the old `nameOverride`.
- **Exposure is the consumer's.** The chart renders no Ingress, auth proxy, identity provider, certificate, DNS record, or RFC 9728 metadata endpoint. `deploy`'s shared `charts/ingress-public-authed` owns the fleet gate.
- **Verify** with `helm lint chart/` and `helm template` offline in both modes, then `GET /healthz` and one `tools/list` live.
