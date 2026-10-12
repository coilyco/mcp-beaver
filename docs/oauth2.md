# OAuth2: a credential this runtime mints

Every other value mcp-beaver presents is **read** from somewhere that already holds it. An OAuth `client_credentials` upstream holds nothing until a token is fetched, so it is minted. `oauth2-client` declares the clients, and `{oauth2:<name>}` or `value oauth2 "<name>"` presents the token. umbra's `pkg/tokenmint` posts `grant_type=client_credentials`, caches to `expires_in`, renews ahead of expiry, and serializes concurrent first calls so they do not stampede the token endpoint. This repo declares the clients and registers the provider.

## Spec mode

```kdl
oauth2-client "vendor" { token-url "https://auth.vendor.example/oauth/token"; client-id "mcp-beaver"; client-secret env "VENDOR_CLIENT_SECRET"; scope "read:things" }
wrap ward mcp things { base-url "https://api.vendor.example/v1"; auth bearer { value oauth2 "vendor" }; can get thing { path "/things/{id}" } }
```

The client secret is a `<provider> "<address>"` pair like `auth`'s `value`, so it arrives from a Secret and rotating it needs no restart. `auth-style` is `basic` (the default, since RFC 6749 requires support) or `post`.

## Upstream mode

`serve-upstream` mounts no guardfile, so the declaration is one flag, which the chart's `upstream.oauth2Clients` carries as entries:

```sh
mcp-beaver serve-upstream --upstream https://vendor.example/api/mcp/http --tool search_things \
  --oauth2-client 'name=vendor,token-url=https://auth.vendor.example/oauth/token,client-id=mcp-beaver,client-secret={env:VENDOR_CLIENT_SECRET}' \
  --upstream-header 'Authorization=Bearer {oauth2:vendor}'
```

The secret is a `{provider:address}` span, as `--upstream-header` requires, since a literal would sit in argv. There is no `{literal:...}` escape, because a constant client secret in argv is never what anyone means.

## Fails closed

- A `token-url` that is not https, and not http to loopback, is refused, since the secret crosses that hop. A co-located auth sidecar on loopback stays served.
- A client secret resolved through `oauth2` itself is refused, since a minted value cannot seed a mint.
- `{oauth2:typo}` naming no declared client is a **build error**, not a first-call 401, so `lint` catches it in CI.

**One registry.** Six sites once called `valuesource.Builtins()` separately, two of them validators, so a consumer-registered provider resolved at runtime and was rejected at validation. They now share one `ProviderSet` per server, which also stops a minted provider's token cache re-minting every call.

**Never leaves the process.** `/admin/describe` reports `oauth2Clients` by name only, with no token, secret, or endpoint. A failed mint names the endpoint with the credential redacted (tested in umbra).

Out of scope: `authorization_code` and `refresh_token` grants, which need a browser flow (#82), and a durable-password-for-token session bootstrap (#91).
