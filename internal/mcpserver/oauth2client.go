package mcpserver

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	kdl "github.com/calico32/kdl-go"

	"github.com/coilyco/umbra/pkg/tokenmint"
	"github.com/coilyco/umbra/pkg/valuesource"
)

// oauth2Provider is the registry name a guardfile or a header template
// addresses. Its ADDRESS is a declared client name, never an endpoint, so a
// spec carries no URL and no credential.
const oauth2Provider = "oauth2"

// parseOAuth2Clients reads top-level `oauth2-client` nodes, siblings of `wrap`:
//
//	oauth2-client "moxn" {
//	    token-url "https://example.moxn.dev/api/oauth/token"
//	    client-id "mcp-beaver"
//	    client-secret env "MOXN_CLIENT_SECRET"
//	    scope "profile" "email"
//	}
//
// Every other value in this runtime is READ from somewhere that already holds
// it. An OAuth `client_credentials` upstream holds nothing until a token is
// fetched, so the value has to be minted. umbra's `pkg/tokenmint` does the
// minting and this declares which clients exist, keeping the endpoint and the
// secret reference in the audited file rather than in argv.
//
// The secret is a `<provider> "<address>"` pair like `auth`'s own `value`, so
// it lands from a Secret exactly as every other credential does and rotating
// it takes effect without a restart.
func parseOAuth2Clients(sources []guardSource) ([]tokenmint.Client, error) {
	nodes, err := parseInlineNodes(sources, "oauth2-client")
	if err != nil {
		return nil, err
	}
	var out []tokenmint.Client
	seen := map[string]bool{}
	for _, sn := range nodes {
		n := sn.node
		if n.Name() != "oauth2-client" {
			continue
		}
		client, err := parseOAuth2Client(n)
		if err != nil {
			return nil, err
		}
		if seen[client.Name] {
			return nil, fmt.Errorf("mcp-beaver: duplicate `oauth2-client` %q", client.Name)
		}
		seen[client.Name] = true
		out = append(out, client)
	}
	return out, nil
}

func parseOAuth2Client(n *kdl.Node) (tokenmint.Client, error) {
	name, err := oneStringArg(n, "oauth2-client")
	if err != nil {
		return tokenmint.Client{}, err
	}
	if len(n.Properties()) > 0 {
		return tokenmint.Client{}, fmt.Errorf("mcp-beaver: `oauth2-client` %q takes no properties, only children", name)
	}
	client := tokenmint.Client{Name: name}
	for _, child := range n.Children().Nodes {
		switch child.Name() {
		case "token-url":
			if client.TokenURL, err = oneStringArg(child, "token-url"); err != nil {
				return tokenmint.Client{}, err
			}
		case "client-id":
			if client.ClientID, err = oneStringArg(child, "client-id"); err != nil {
				return tokenmint.Client{}, err
			}
		case "client-secret":
			source, err := parseValueSource(child, name)
			if err != nil {
				return tokenmint.Client{}, err
			}
			client.ClientSecret = source
		case "scope":
			if len(child.Arguments()) == 0 {
				return tokenmint.Client{}, fmt.Errorf("mcp-beaver: `oauth2-client` %q `scope` wants at least one scope", name)
			}
			for i := range child.Arguments() {
				scope := child.Arg(i).String()
				if scope == "" {
					return tokenmint.Client{}, fmt.Errorf("mcp-beaver: `oauth2-client` %q has an empty scope", name)
				}
				client.Scopes = append(client.Scopes, scope)
			}
		case "auth-style":
			style, err := oneStringArg(child, "auth-style")
			if err != nil {
				return tokenmint.Client{}, err
			}
			// RFC 6749 requires a server to support basic, so that stays the
			// default. Naming a third style would be a silent downgrade to
			// whatever the library picks, so it fails closed.
			if style != tokenmint.AuthStyleBasic && style != tokenmint.AuthStylePost {
				return tokenmint.Client{}, fmt.Errorf("mcp-beaver: `oauth2-client` %q auth-style %q is not %s or %s (fail-closed)", name, style, tokenmint.AuthStyleBasic, tokenmint.AuthStylePost)
			}
			client.AuthStyle = style
		default:
			return tokenmint.Client{}, fmt.Errorf("mcp-beaver: unknown `oauth2-client` child %q on %q (want token-url | client-id | client-secret | scope | auth-style; fail-closed)", child.Name(), name)
		}
	}
	return client, validateOAuth2Client(client)
}

// validateOAuth2Client refuses an incomplete client here rather than at first
// call. tokenmint.New refuses a missing name, url, or id too, and this adds the
// secret, whose absence would otherwise mint an anonymous request the token
// endpoint rejects with something less specific than the real cause.
func validateOAuth2Client(client tokenmint.Client) error {
	switch {
	case client.TokenURL == "":
		return fmt.Errorf("mcp-beaver: `oauth2-client` %q needs a `token-url`", client.Name)
	case !secureTokenURL(client.TokenURL):
		return fmt.Errorf("mcp-beaver: `oauth2-client` %q token-url %q must be https, or http to a loopback address, since the client secret crosses it", client.Name, client.TokenURL)
	case client.ClientID == "":
		return fmt.Errorf("mcp-beaver: `oauth2-client` %q needs a `client-id`", client.Name)
	case client.ClientSecret.Provider == "":
		return fmt.Errorf("mcp-beaver: `oauth2-client` %q needs a `client-secret <provider> \"<address>\"`", client.Name)
	}
	return nil
}

// parseValueSource reads the `<provider> "<address>"` pair `auth`'s own `value`
// node uses, so a credential is addressed the same way wherever it appears.
func parseValueSource(n *kdl.Node, owner string) (valuesource.Source, error) {
	args := n.Arguments()
	if len(args) != 2 {
		return valuesource.Source{}, fmt.Errorf("mcp-beaver: `oauth2-client` %q `%s` wants `<provider> \"<address>\"`, got %d argument(s)", owner, n.Name(), len(args))
	}
	source := valuesource.Source{Provider: n.Arg(0).String(), Address: n.Arg(1).String()}
	if source.Provider == "" || source.Address == "" {
		return valuesource.Source{}, fmt.Errorf("mcp-beaver: `oauth2-client` %q `%s` needs a non-empty provider and address", owner, n.Name())
	}
	// The base registry only: a client secret resolved through the minted
	// provider would be circular, and a minted token is not a client secret.
	if _, ok := valuesource.Builtins()[source.Provider]; !ok {
		return valuesource.Source{}, fmt.Errorf("mcp-beaver: `oauth2-client` %q `%s` names unknown provider %q (want a built-in reader; a minted value cannot seed a mint)", owner, n.Name(), source.Provider)
	}
	return source, nil
}

// OAuth2ClientNames reports the declared client names for `/admin` and `lint`.
// Names only: never an endpoint, never a credential, never a token.
func (s *Server) OAuth2ClientNames() []string {
	return append([]string(nil), s.oauth2Clients...)
}

// oauth2ClientFields are the keys the CLI form accepts, listed once so the
// parser and its error message cannot drift.
var oauth2ClientFields = []string{"name", "token-url", "client-id", "client-secret", "scope", "auth-style"}

// ParseOAuth2Client reads the `serve-upstream` CLI form, which is the guardfile
// node flattened into one flag because upstream mode mounts no guardfile:
//
//	--oauth2-client 'name=moxn,token-url=https://x/api/oauth/token,client-id=beaver,client-secret={env:MOXN_SECRET}'
//
// The secret is a `{provider:address}` span for the reason `--upstream-header`
// requires one: a literal here would put the client secret in argv, visible in
// `ps` and in the pod spec. Unlike that flag there is no literal escape hatch,
// because a constant client secret in argv is never what someone means.
func ParseOAuth2Client(raw string) (tokenmint.Client, error) {
	client := tokenmint.Client{}
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, value, found := strings.Cut(field, "=")
		if !found {
			return tokenmint.Client{}, fmt.Errorf("mcp-beaver: oauth2 client %q: %q must be <key>=<value> (want %s)", raw, field, strings.Join(oauth2ClientFields, " | "))
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "name":
			client.Name = value
		case "token-url":
			client.TokenURL = value
		case "client-id":
			client.ClientID = value
		case "client-secret":
			source, err := parseSpanSource(value, client.Name)
			if err != nil {
				return tokenmint.Client{}, err
			}
			client.ClientSecret = source
		case "scope":
			for _, scope := range strings.Fields(value) {
				client.Scopes = append(client.Scopes, scope)
			}
		case "auth-style":
			if value != tokenmint.AuthStyleBasic && value != tokenmint.AuthStylePost {
				return tokenmint.Client{}, fmt.Errorf("mcp-beaver: oauth2 client %q auth-style %q is not %s or %s (fail-closed)", client.Name, value, tokenmint.AuthStyleBasic, tokenmint.AuthStylePost)
			}
			client.AuthStyle = value
		default:
			return tokenmint.Client{}, fmt.Errorf("mcp-beaver: oauth2 client %q names unknown field %q (want %s; fail-closed)", raw, key, strings.Join(oauth2ClientFields, " | "))
		}
	}
	if client.Name == "" {
		return tokenmint.Client{}, fmt.Errorf("mcp-beaver: oauth2 client %q needs a `name=`, which is what a header addresses as {oauth2:<name>}", raw)
	}
	return client, validateOAuth2Client(client)
}

// parseSpanSource reads the `{provider:address}` span the header grammar uses,
// so a credential is addressed one way across both flags.
func parseSpanSource(value, name string) (valuesource.Source, error) {
	body, ok := strings.CutPrefix(value, string(upstreamHeaderOpen))
	if !ok {
		return valuesource.Source{}, fmt.Errorf("mcp-beaver: oauth2 client %q: client-secret must be a {provider:address} span, e.g. {env:MOXN_CLIENT_SECRET}, so the secret never sits in argv", name)
	}
	body, ok = strings.CutSuffix(body, string(upstreamHeaderClose))
	if !ok {
		return valuesource.Source{}, fmt.Errorf("mcp-beaver: oauth2 client %q: client-secret span is missing its closing %q", name, string(upstreamHeaderClose))
	}
	provider, address, found := strings.Cut(body, ":")
	if !found {
		return valuesource.Source{}, fmt.Errorf("mcp-beaver: oauth2 client %q: client-secret {%s} must be {<provider>:<address>}", name, body)
	}
	provider, address = strings.TrimSpace(provider), strings.TrimSpace(address)
	if provider == "" || address == "" {
		return valuesource.Source{}, fmt.Errorf("mcp-beaver: oauth2 client %q: client-secret needs a non-empty provider and address", name)
	}
	if _, ok := valuesource.Builtins()[provider]; !ok {
		return valuesource.Source{}, fmt.Errorf("mcp-beaver: oauth2 client %q: client-secret names unknown provider %q (a minted value cannot seed a mint)", name, provider)
	}
	return valuesource.Source{Provider: provider, Address: address}, nil
}

// ValidateOAuth2Clients rejects a set naming one client twice, offline, so
// `lint-upstream` runs it where no secret is reachable.
func ValidateOAuth2Clients(clients []tokenmint.Client) error {
	seen := make(map[string]bool, len(clients))
	for _, client := range clients {
		if seen[client.Name] {
			return fmt.Errorf("mcp-beaver: oauth2 client %q is declared twice; the second would silently win", client.Name)
		}
		seen[client.Name] = true
	}
	return nil
}

// secureTokenURL requires https, or http to a loopback address.
//
// A client secret crosses this hop, so plain http to anywhere routable puts it
// on the wire in clear. Loopback is exempt because it never reaches a wire and
// because a co-located auth sidecar is a shape this runtime already serves -
// examples/sidecar.mcp.kdl is exactly that, an upstream reachable only inside
// the pod. Refusing it would push an operator to terminate TLS against
// themselves for a hop that cannot be observed.
func secureTokenURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch parsed.Scheme {
	case "https":
		return parsed.Host != ""
	case "http":
		host := parsed.Hostname()
		return host == "localhost" || net.ParseIP(host).IsLoopback()
	default:
		return false
	}
}
