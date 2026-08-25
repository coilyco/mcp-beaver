package mcpserver

import (
	"context"
	"net/url"
	"strings"
	"sync"

	"forgejo.coilysiren.me/coilyco-flight-deck/umbra/http/opcore"
)

// secretPaths holds the base-url path prefixes this process must never emit.
// Package scope beside the process logger, because the two redactors it feeds
// are package functions. See docs/logs.md.
var secretPaths struct {
	mu       sync.RWMutex
	prefixes []string
}

// registerSecretPath marks one base-url path prefix as unemittable.
//
// An operator only puts a credential in a base URL when the upstream forces it,
// so the whole operator-supplied path prefix is treated as sensitive rather
// than a pattern guessed from what a token looks like.
func registerSecretPath(prefix string) {
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if prefix == "" || prefix == "/" {
		return
	}
	secretPaths.mu.Lock()
	defer secretPaths.mu.Unlock()
	for _, existing := range secretPaths.prefixes {
		if existing == prefix {
			return
		}
	}
	secretPaths.prefixes = append(secretPaths.prefixes, prefix)
}

// resetSecretPaths clears the registry between tests.
func resetSecretPaths() {
	secretPaths.mu.Lock()
	defer secretPaths.mu.Unlock()
	secretPaths.prefixes = nil
}

// redactSecretPaths masks every registered base-url path prefix in text.
//
// The whole prefix goes rather than a guessed span inside it, because a
// credential that has to be split out of its neighbours is a credential a
// future upstream will format differently.
func redactSecretPaths(text string) string {
	secretPaths.mu.RLock()
	prefixes := append([]string(nil), secretPaths.prefixes...)
	secretPaths.mu.RUnlock()
	for _, prefix := range prefixes {
		text = strings.ReplaceAll(text, prefix, "/<redacted>")
	}
	return text
}

// registerBaseURLPath resolves the runtime's base URL once and registers its
// path, best effort.
//
// Resolution failure is not fatal here. A base-url provider that cannot answer
// at boot fails the first real call with its own error, and turning that into a
// startup failure would take a server down for a redaction it does not need.
func registerBaseURLPath(ctx context.Context, rt *opcore.Runtime) {
	if rt == nil {
		return
	}
	base, err := rt.BaseForRequest(ctx, false)
	if err != nil {
		return
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return
	}
	registerSecretPath(parsed.EscapedPath())
}
