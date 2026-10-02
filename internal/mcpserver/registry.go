package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coilyco/umbra/http/mcpverb"
)

// registryPageSize is the registry's own maximum page, so a full enumeration
// costs as few round trips as it can.
const registryPageSize = 100

// maxRegistryPages bounds a cursor loop the registry never ends.
const maxRegistryPages = 100

// RegistryEntry is one server the registry lists, before any connection.
type RegistryEntry struct {
	Name        string
	URL         string
	Description string
	// Published is the registry's publishedAt date, YYYY-MM-DD, or empty.
	Published string
	// Repository is the source URL the publisher declared, or empty.
	Repository string
}

// EnumerateOptions carries the optional inputs of an enumeration.
type EnumerateOptions struct {
	// Registry is the base URL of the registry. Empty means DefaultRegistry.
	Registry string
	// Limit caps the entries returned, in registry order. Zero means all.
	Limit int
	// HTTPClient overrides the default client, for tests.
	HTTPClient *http.Client
}

// EnumerateRegistry pages `/v0/servers` and keeps the entries a proxy could
// front: latest, active, and publishing a streamable-HTTP remote. A
// packages-only server would have to be installed and run, and nothing here
// does either, so it is not an entry rather than a silent zero.
func EnumerateRegistry(ctx context.Context, opts EnumerateOptions) ([]RegistryEntry, error) {
	registry := opts.Registry
	if registry == "" {
		registry = DefaultRegistry
	}
	client := boundedUpstreamClient(opts.HTTPClient)
	base := strings.TrimRight(registry, "/") + "/v0/servers"
	seen := map[string]bool{}
	var out []RegistryEntry
	cursor := ""
	for page := 0; page < maxRegistryPages; page++ {
		endpoint := base + "?limit=" + strconv.Itoa(registryPageSize)
		if cursor != "" {
			endpoint += "&cursor=" + url.QueryEscape(cursor)
		}
		body, err := readRegistryPage(ctx, client, endpoint)
		if err != nil {
			return nil, err
		}
		for _, item := range body.Servers {
			official := item.Meta.Official
			if !official.IsLatest || official.Status != registryStatusActive || seen[item.Server.Name] {
				continue
			}
			remote := ""
			for _, r := range item.Server.Remotes {
				if r.Type == mcpverb.UpstreamTransport && r.URL != "" {
					remote = r.URL
					break
				}
			}
			if remote == "" {
				continue
			}
			seen[item.Server.Name] = true
			published := official.PublishedAt
			if len(published) > 10 {
				published = published[:10]
			}
			out = append(out, RegistryEntry{
				Name:        item.Server.Name,
				URL:         remote,
				Description: strings.TrimSpace(item.Server.Description),
				Published:   published,
				Repository:  item.Server.Repository.URL,
			})
			if opts.Limit > 0 && len(out) >= opts.Limit {
				return out, nil
			}
		}
		cursor = body.Metadata.NextCursor
		if cursor == "" {
			break
		}
	}
	return out, nil
}

type registryPage struct {
	Servers []struct {
		Server struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Remotes     []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"remotes"`
			Repository struct {
				URL string `json:"url"`
			} `json:"repository"`
		} `json:"server"`
		Meta struct {
			Official struct {
				IsLatest    bool   `json:"isLatest"`
				Status      string `json:"status"`
				PublishedAt string `json:"publishedAt"`
			} `json:"io.modelcontextprotocol.registry/official"`
		} `json:"_meta"`
	} `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

func readRegistryPage(ctx context.Context, client *http.Client, endpoint string) (registryPage, error) {
	var page registryPage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return page, fmt.Errorf("mcp-beaver: registry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return page, fmt.Errorf("mcp-beaver: registry %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return page, fmt.Errorf("mcp-beaver: registry %s answered HTTP %d", endpoint, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSpecBytes)).Decode(&page); err != nil {
		return page, fmt.Errorf("mcp-beaver: registry page does not parse: %w", err)
	}
	return page, nil
}

// SweepStateOK marks a server that answered `tools/list`. Every other state
// is the refusal in the upstream's own terms.
const SweepStateOK = "ok"

// SweepStateTimeout marks a server that ran past the per-upstream deadline.
// An upstream that answers `initialize` and then holds `tools/list` open
// would otherwise pin a worker for the life of the sweep (mcp-beaver#123).
const SweepStateTimeout = "timeout"

// SweptServer is one registry entry after the sweep reached for it.
type SweptServer struct {
	RegistryEntry
	// State is SweepStateOK or the refusal: `HTTP 401` when the upstream
	// answered a status, otherwise the tail of the transport error.
	State string
	// Tools is the surface served, in upstream order, and nil unless ok.
	Tools []PulledTool
}

// Answered reports whether the sweep recorded a tool surface.
func (s SweptServer) Answered() bool { return s.State == SweepStateOK }

// Pulled is the sweep's record in the shape RenderUpstreamGuardfile reads.
func (s SweptServer) Pulled() *Pulled {
	return &Pulled{Name: s.Name, URL: s.URL, Description: s.Description, Tools: s.Tools}
}

// SweepOptions carries the optional inputs of a sweep.
type SweepOptions struct {
	// Concurrency bounds the upstreams probed at once. Zero means 8.
	Concurrency int
	// Timeout bounds one upstream, handshake and list together. Zero means 30s.
	Timeout time.Duration
	// Progress receives one line per server as it settles, or is nil.
	Progress io.Writer
	// HTTPClient overrides the default client, for tests.
	HTTPClient *http.Client
}

const (
	defaultSweepConcurrency = 8
	defaultSweepTimeout     = 30 * time.Second
)

// Sweep probes every entry the way Pull does and records each answer or
// refusal in place. One server refusing is that server's state rather than
// the sweep's failure: half the registry refuses anonymously, and a directory
// that stopped at the first 401 would never be written.
func Sweep(ctx context.Context, entries []RegistryEntry, opts SweepOptions) []SweptServer {
	workers := opts.Concurrency
	if workers <= 0 {
		workers = defaultSweepConcurrency
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultSweepTimeout
	}
	out := make([]SweptServer, len(entries))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var progress sync.Mutex
	for i, entry := range entries {
		wg.Add(1)
		go func(i int, entry RegistryEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = sweepOne(ctx, entry, timeout, opts.HTTPClient)
			if opts.Progress != nil {
				progress.Lock()
				fmt.Fprintf(opts.Progress, "%s\t%s\t%d\n", entry.Name, out[i].State, len(out[i].Tools))
				progress.Unlock()
			}
		}(i, entry)
	}
	wg.Wait()
	return out
}

func sweepOne(ctx context.Context, entry RegistryEntry, timeout time.Duration, base *http.Client) SweptServer {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{}
	if base != nil {
		copied := *base
		client = &copied
	}
	// The SDK opens its standalone SSE stream on a context of its own, so a
	// deadline on ctx never reaches a server that holds that GET open. A
	// client timeout does, and a probe never needs a long-lived stream.
	if client.Timeout == 0 || client.Timeout > timeout {
		client.Timeout = timeout
	}
	status := &statusCapture{base: client.Transport}
	client.Transport = status
	swept := SweptServer{RegistryEntry: entry}
	pulled, err := pullWithin(ctx, entry, client, timeout)
	if err != nil {
		swept.State = refusalState(status.last(), err)
		if swept.State != SweepStateTimeout && status.last() == 0 && (errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded) {
			swept.State = SweepStateTimeout
		}
		return swept
	}
	swept.State = SweepStateOK
	swept.Tools = pulled.Tools
	return swept
}

// statusCapture remembers the last error status an upstream answered. The
// SDK reports a refused handshake by its reason phrase alone, and a directory
// wants the code: `HTTP 401` is the finding, not "Unauthorized".
type statusCapture struct {
	base http.RoundTripper
	mu   sync.Mutex
	code int
}

func (c *statusCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	base := c.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err == nil && resp.StatusCode >= http.StatusBadRequest {
		c.mu.Lock()
		c.code = resp.StatusCode
		c.mu.Unlock()
	}
	return resp, err
}

func (c *statusCapture) last() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code
}

// maxRefusalLength keeps a state to one readable cell.
const maxRefusalLength = 60

func refusalState(code int, err error) string {
	if code > 0 {
		return "HTTP " + strconv.Itoa(code)
	}
	text := err.Error()
	if i := strings.LastIndex(text, ": "); i >= 0 {
		text = text[i+2:]
	}
	text = strings.TrimSpace(text)
	if len(text) > maxRefusalLength {
		text = text[:maxRefusalLength]
	}
	if text == "" {
		return "no result"
	}
	return text
}

// pullWithin runs Pull and gives up at the deadline even when Pull has not
// returned. The client timeout above makes that the rare case, and a
// goroutine parked on a stream the SDK will eventually drop is a bounded
// leak in a command-line run rather than a pinned worker.
func pullWithin(ctx context.Context, entry RegistryEntry, client *http.Client, timeout time.Duration) (*Pulled, error) {
	type result struct {
		pulled *Pulled
		err    error
	}
	done := make(chan result, 1)
	go func() {
		pulled, err := Pull(ctx, entry.Name, PullOptions{Upstream: entry.URL, HTTPClient: client})
		done <- result{pulled, err}
	}()
	select {
	case r := <-done:
		return r.pulled, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("mcp-beaver: upstream %q ran past %s: %w", entry.URL, timeout, context.DeadlineExceeded)
	}
}
