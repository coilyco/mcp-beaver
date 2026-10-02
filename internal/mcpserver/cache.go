package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coilyco/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxCacheTTL bounds how stale an author may declare an answer may be. A
// longer window is almost always a typo, and the cost of a wrong long TTL is
// paid by a reader who has no way to see it.
const maxCacheTTL = 24 * time.Hour

// maxCacheEntries bounds one tool's cache. A community asks overlapping
// questions, which is what makes caching worth anything here, but the tail of
// distinct queries is unbounded and this runs in a pod with a memory limit.
const maxCacheEntries = 256

// cacheConfig maps a projected tool name to how long its responses may be
// reused.
type cacheConfig map[string]time.Duration

// parseCaches reads top-level `cache` nodes, siblings of `wrap`:
//
//	cache "get_store_app_details" ttl="15m"
//
// The argument is the PROJECTED TOOL NAME, matching `confirm` and `pin`,
// because that is the name a client dispatches on.
//
// Every tool call was a live upstream request, so N identical questions from a
// Discord community produced N identical upstream calls from one pod IP. That
// was a politeness problem while every upstream was a keyless public good. A
// metered upstream makes it a spend problem, and caching is the only control
// that reduces cost without reducing capability - a rate limit only makes the
// tool refuse (mcp-beaver#73).
//
// Opt-in per grant and off by default, because correctness varies entirely by
// upstream: a stale answer is worse than a slow one for anything
// time-sensitive, and no default can know which this is.
//
// Stated beside `wrap` rather than inside it, like `rate-limit` and for the
// same reason: the wrap body is opcore's frozen grammar.
func parseCaches(sources []guardSource) (cacheConfig, error) {
	nodes, err := parseInlineNodes(sources, "cache")
	if err != nil {
		return nil, err
	}
	out := cacheConfig{}
	for _, sn := range nodes {
		n := sn.node
		if n.Name() != "cache" {
			continue
		}
		tool, err := oneStringArg(n, "cache")
		if err != nil {
			return nil, err
		}
		if _, dup := out[tool]; dup {
			return nil, fmt.Errorf("mcp-beaver: duplicate `cache` for tool %q", tool)
		}
		ttl := time.Duration(0)
		for key, value := range n.Properties() {
			if key != "ttl" {
				return nil, fmt.Errorf("mcp-beaver: unknown `cache` property %q (want ttl; fail-closed)", key)
			}
			ttl, err = time.ParseDuration(value.String())
			if err != nil {
				return nil, fmt.Errorf("mcp-beaver: `cache` %q ttl %q must be a duration, e.g. \"15m\"", tool, value.String())
			}
		}
		switch {
		case ttl == 0:
			return nil, fmt.Errorf("mcp-beaver: `cache` %q needs a ttl, e.g. `cache %q ttl=\"15m\"`", tool, tool)
		case ttl < 0:
			return nil, fmt.Errorf("mcp-beaver: `cache` %q ttl must be positive", tool)
		case ttl > maxCacheTTL:
			return nil, fmt.Errorf("mcp-beaver: `cache` %q ttl %s is over the %s ceiling", tool, ttl, maxCacheTTL)
		}
		out[tool] = ttl
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// validateCaches rejects a `cache` that would serve a stale answer where a
// fresh one is the whole contract. Build-time rather than call-time: an author
// who believes a call is cached and one who believes it is not should not both
// be able to run.
func validateCaches(cfg cacheConfig, descs []opcore.Descriptor, confirmations confirmConfig) error {
	if cfg == nil {
		return nil
	}
	byTool := make(map[string]opcore.Descriptor, len(descs))
	for _, d := range descs {
		byTool[toolName(d)] = d
	}
	for tool := range cfg {
		desc, ok := byTool[tool]
		if !ok {
			// Naming a tool the spec does not mint, or naming the info tool or
			// a withheld stub: none of those reach an upstream, so a cache on
			// one saves nothing and hides that the author aimed at the wrong
			// name.
			return fmt.Errorf("mcp-beaver: `cache` names %q, which is not a grant-backed tool this spec serves", tool)
		}
		if desc.Destructive {
			return fmt.Errorf("mcp-beaver: `cache` names %q, which is destructive: replaying a destructive call from a cache is not a saving, it is a lie about what happened", tool)
		}
		if _, gated := confirmations[tool]; gated {
			return fmt.Errorf("mcp-beaver: %q is both `confirm`-gated and `cache`d: a cache hit would skip the human gate the confirmation exists to impose", tool)
		}
	}
	return nil
}

type cacheEntry struct {
	result  *mcp.CallToolResult
	expires time.Time
}

// responseCache holds one tool's upstream answers for its declared window.
//
// In-process and per-pod, so it dies with the pod and two replicas keep two
// copies. That is the same objection mcp-beaver#69 raises about the rate
// limiter, and the same answer applies: if #69 lands a durable store, this
// should share it rather than grow a second one.
//
// SAFE HERE BECAUSE THE CREDENTIAL IS SERVER-SIDE. This runtime performs no
// inbound authentication and holds one upstream credential for the whole
// process, so every caller is the same principal upstream and a response
// cached for one is a response any other would have received. That is a
// property of this deployment shape rather than of caching, and a runtime that
// ever gained per-caller credentials would have to key on the caller or stop
// caching.
type responseCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cacheEntry
}

func newResponseCache(ttl time.Duration) *responseCache {
	return &responseCache{ttl: ttl, entries: map[string]cacheEntry{}}
}

func (c *responseCache) get(key string, now time.Time) (*mcp.CallToolResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.result, true
}

func (c *responseCache) put(key string, result *mcp.CallToolResult, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, k)
		}
	}
	// Still full after purging: drop whatever expires soonest. Nothing here
	// tracks access order, and the entry closest to being useless is the
	// cheapest one to lose.
	for len(c.entries) >= maxCacheEntries {
		oldest, found := "", time.Time{}
		for k, entry := range c.entries {
			if found.IsZero() || entry.expires.Before(found) {
				oldest, found = k, entry.expires
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = cacheEntry{result: result, expires: now.Add(c.ttl)}
}

// cacheKey is the tool plus its arguments, canonicalised. Two callers that
// spell the same object with different key order must hit the same entry, and
// encoding/json sorts map keys, so a decode-and-re-encode is the canonical
// form. Undecodable arguments return false and the call goes upstream: a key
// that cannot be computed is not a key that may collide.
//
// The tool name stands in for the method and path template, since a projected
// tool is exactly one grant. Server-side `pin` values are deliberately absent:
// they resolve identically for every caller in a process, so they cannot
// distinguish two calls, and the TTL bounds a `file` pin that changes
// underneath.
func cacheKey(name string, arguments json.RawMessage) (string, bool) {
	if len(arguments) == 0 {
		return name + "\x00{}", true
	}
	var decoded any
	if err := json.Unmarshal(arguments, &decoded); err != nil {
		return "", false
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return "", false
	}
	return name + "\x00" + string(canonical), true
}

// withResponseCache serves a repeated identical call from memory.
//
// Wrapped OUTSIDE the rate limiter on purpose. A cache hit that spent a
// rate-limit slot would throttle the community on behalf of a request that was
// never made, which is the opposite of what this is for.
//
// A failed call is never stored. An upstream that 5xxed for fifteen seconds
// must not answer for the next fifteen minutes.
func withResponseCache(cache *responseCache, name string, next mcp.ToolHandler) mcp.ToolHandler {
	if cache == nil {
		return next
	}
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key, ok := cacheKey(name, req.Params.Arguments)
		if !ok {
			return next(ctx, req)
		}
		now := time.Now()
		if hit, found := cache.get(key, now); found {
			return hit, nil
		}
		result, err := next(ctx, req)
		if err != nil || result == nil || result.IsError {
			return result, err
		}
		cache.put(key, result, now)
		return result, nil
	}
}
