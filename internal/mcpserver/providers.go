package mcpserver

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/coilyco/umbra/pkg/tokenmint"
	"github.com/coilyco/umbra/pkg/valuesource"
)

// ProviderSet is the ONE value registry a server resolves and validates
// through. Before it, six sites called `valuesource.Builtins()` for themselves
// and two of those were validators, so a consumer-registered provider would
// resolve at runtime and be refused at validation - the shape where a
// capability exists and cannot be reached (#83).
//
// Held rather than rebuilt per call because a minted provider carries a token
// cache: rebuilding would re-mint on every request and stampede the token
// endpoint that tokenmint exists to protect.
type ProviderSet struct {
	providers map[string]valuesource.Provider
	// names is the declared oauth2 client set, for validating an address
	// offline and for the describe surface. Names only.
	names []string
}

// BaseProviders is the registry for a caller that declares no minted client:
// umbra's built-in readers and nothing else. It is also the zero value's
// behaviour, so a caller that never thinks about this gets the readers.
func BaseProviders() ProviderSet {
	return ProviderSet{providers: valuesource.Builtins()}
}

// NewProviderSet layers a minted `oauth2` provider over the built-in readers.
// The http.Client is the caller's, so a mint inherits whatever bounds that
// client carries rather than dialing unbounded on its own.
func NewProviderSet(clients []tokenmint.Client, httpClient *http.Client) (ProviderSet, error) {
	if len(clients) == 0 {
		return BaseProviders(), nil
	}
	registry, err := tokenmint.New(clients, valuesource.Builtins(), httpClient)
	if err != nil {
		return ProviderSet{}, fmt.Errorf("mcp-beaver: build oauth2 clients: %w", err)
	}
	return ProviderSet{
		providers: valuesource.Merge(map[string]valuesource.Provider{oauth2Provider: registry.Provider()}),
		names:     registry.Names(),
	}, nil
}

// known reports whether a provider name resolves in this registry. This is the
// validator's question, and it is why the registry has to be threaded rather
// than reconstructed: `valuesource.Builtins()` answers false for `oauth2` even
// when the server has one.
func (p ProviderSet) known(provider string) bool {
	_, ok := p.registry()[provider]
	return ok
}

// registry is the resolved map, filling in the built-in readers for a zero
// ProviderSet so a caller that passes one by accident reads secrets rather
// than failing every lookup closed for the wrong reason.
func (p ProviderSet) registry() map[string]valuesource.Provider {
	if p.providers == nil {
		return valuesource.Builtins()
	}
	return p.providers
}

// checkSource reports why a (provider, address) pair cannot resolve, or nil.
//
// Two distinct failures, kept distinct: an unknown provider is a name this
// server has never heard of, and an undeclared minted address is a real
// provider pointed at a client nobody declared. Collapsing them would tell an
// author to check their provider spelling when the provider is fine.
//
// For every reader the address is a runtime lookup and cannot be judged here.
// A minted address is a DECLARED client name, so a typo is catchable at lint
// instead of at the first call in production.
func (p ProviderSet) checkSource(provider, address string) error {
	if !p.known(provider) {
		return fmt.Errorf("names unknown provider %q (want %s; fail-closed)", provider, strings.Join(p.providerNames(), " | "))
	}
	if provider != oauth2Provider {
		return nil
	}
	for _, name := range p.names {
		if name == address {
			return nil
		}
	}
	return fmt.Errorf("names oauth2 client %q, which no `oauth2-client` declares (declared: %s; fail-closed)", address, strings.Join(p.names, " | "))
}

// providerNames lists what a value source may name, for a fail-closed error
// that tells an author the actual set rather than a hardcoded three.
func (p ProviderSet) providerNames() []string {
	registry := p.registry()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
