# OAuth2: a credential this runtime mints

Every other value mcp-beaver presents is **read** from somewhere that already
holds it. An OAuth `client_credentials` upstream holds nothing until a token is
fetched, so that value has to be minted. `oauth2-client` declares which clients
exist, and `{oauth2:<name>}` or `value oauth2 "<name>"` presents the token.

umbra's [`pkg/tokenmint`](https://github.com/coilyco/umbra)
does the minting: it posts `grant_type=client_credentials`, caches the token to
its own `expires_in`, renews ahead of expiry, and serializes concurrent first
calls so they do not stampede the token endpoint. This repo declares the clients
and registers the provider.

## Spec mode

```kdl
oauth2-client "vendor" {
    token-url "https://auth.vendor.example/oauth/token"
    client-id "mcp-beaver"
    client-secret env "VENDOR_CLIENT_SECRET"
    scope "read:things"
}

wrap ward mcp things {
    base-url "https://api.vendor.example/v1"
    auth bearer { value oauth2 "vendor" }
    can get thing { path "/things/{id}" }
}
```

The client secret is a `<provider> "<address>"` pair, the same shape `auth`'s own
`value` takes, so it arrives from a Secret exactly as every other credential does
and rotating it takes effect without a restart. `auth-style` is `basic` or
`post`, and `basic` is the default because RFC 6749 requires a server to support
it.

## Upstream mode

`serve-upstream` mounts no guardfile, so the same declaration is one flag:

```sh
mcp-beaver serve-upstream \
  --upstream https://vendor.example/api/mcp/http \
  --oauth2-client 'name=vendor,token-url=https://auth.vendor.example/oauth/token,client-id=mcp-beaver,client-secret={env:VENDOR_CLIENT_SECRET}' \
  --upstream-header 'Authorization=Bearer {oauth2:vendor}' \
  --tool search_things
```

The secret is a `{provider:address}` span for the reason `--upstream-header`
requires one: a literal would put the client secret in argv, visible in `ps` and
in the pod spec. Unlike that flag there is no `{literal:...}` escape, because a
constant client secret in argv is never what someone means.

In the chart, `upstream.oauth2Clients` carries the same entries.

## What fails closed, and where

A `token-url` that is not https, and not http to a loopback address, is refused:
the client secret crosses that hop. Loopback is exempt because it never reaches
a wire, and a co-located auth sidecar is a shape this runtime already serves.

A client secret resolved through `oauth2` itself is refused, because a minted
value cannot seed a mint.

`{oauth2:typo}` naming a client no `oauth2-client` declares is a **build error**,
not a 401 on the first call. That is the point of addressing a client by name
rather than by endpoint: the address is checkable offline, so `lint` catches it
in CI.

## One registry, so a validator sees what the runtime has

Six sites used to call umbra's `valuesource.Builtins()` for themselves, and two
were validators. A consumer-registered provider therefore resolved at runtime
and was rejected at validation, which is a capability that exists and cannot be
reached. They now share one `ProviderSet`, built once per server. A minted
provider carries a token cache, so rebuilding it per call would re-mint every
request and stampede the endpoint the cache exists to protect.

## What never leaves the process

`/admin/describe` reports `oauth2Clients` as **names only**: never a token, never
a client secret, never the token endpoint. A mint that fails names the endpoint
in its error with the credential redacted, which is umbra's behavior and is
tested there.

## Not this

`authorization_code` and `refresh_token` grants, which need a browser flow a pod
cannot complete, are #82. A session bootstrap that trades a durable password for
a short-lived token, which is a different protocol shape, is #91.
