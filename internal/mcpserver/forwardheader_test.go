package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// forwardSpec states a static x-agent-origin naming this server as the
// fallback, and forwards the caller's own value over it.
func forwardSpec(baseURL, sibling string) string {
	return sibling + `
wrap ward mcp test {
    base-url "` + baseURL + `"
    auth header-token {
        header "x-agent-origin"
        value literal "test-mcp"
    }
    can get thing {
        path "/things/{id}"
    }
}`
}

type originRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (o *originRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.seen = append(o.seen, r.Header.Get("X-Agent-Origin"))
		o.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (o *originRecorder) calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.seen...)
}

type headerTransport struct {
	name, value string
}

func (h headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.Header.Set(h.name, h.value)
	return http.DefaultTransport.RoundTrip(out)
}

func forwardServer(t *testing.T, upstream, sibling string) *httptest.Server {
	t.Helper()
	s, err := New("test", "test.mcp.kdl", []byte(forwardSpec(upstream, sibling)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func callOverMCP(t *testing.T, ts *httptest.Server, origin string) *mcp.CallToolResult {
	t.Helper()
	httpClient := ts.Client()
	if origin != "" {
		httpClient = &http.Client{Transport: headerTransport{name: "x-agent-origin", value: origin}}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "forward-test", Version: "0.1.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_thing",
		Arguments: map[string]any{"id": "1"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	return result
}

func TestForwardHeaderCarriesTheCallersOriginOverMCP(t *testing.T) {
	var rec originRecorder
	upstream := rec.server(t)
	ts := forwardServer(t, upstream.URL, `forward-header "x-agent-origin"`)

	if result := callOverMCP(t, ts, "eng-platform/beetle-ox:fixture"); result.IsError {
		t.Fatalf("call failed: %v", result.Content)
	}
	if result := callOverMCP(t, ts, ""); result.IsError {
		t.Fatalf("call without origin failed: %v", result.Content)
	}
	got := rec.calls()
	want := []string{"eng-platform/beetle-ox:fixture", "test-mcp"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("upstream saw x-agent-origin %q, want %q (forwarded, then the static fallback)", got, want)
	}
}

func TestForwardHeaderCarriesTheCallersOriginOverTheHTTPAPI(t *testing.T) {
	var rec originRecorder
	upstream := rec.server(t)
	ts := forwardServer(t, upstream.URL, `forward-header "x-agent-origin"`)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/api/get_thing", strings.NewReader(`{"id":"1"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Origin", "sysadmin-senior/turtle-ox:fixture")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, decodeAPIResult(t, resp))
	}
	resp.Body.Close()
	if got := rec.calls(); len(got) != 1 || got[0] != "sysadmin-senior/turtle-ox:fixture" {
		t.Fatalf("upstream saw x-agent-origin %q", got)
	}
}

func TestWithoutForwardHeaderTheCallersOriginStopsAtThisServer(t *testing.T) {
	var rec originRecorder
	upstream := rec.server(t)
	ts := forwardServer(t, upstream.URL, "")

	if result := callOverMCP(t, ts, "eng-platform/beetle-ox:fixture"); result.IsError {
		t.Fatalf("call failed: %v", result.Content)
	}
	if got := rec.calls(); len(got) != 1 || got[0] != "test-mcp" {
		t.Fatalf("upstream saw x-agent-origin %q, want only the static value", got)
	}
}

func TestRequiredForwardHeaderRefusesACallWithoutItBeforeUpstream(t *testing.T) {
	var rec originRecorder
	upstream := rec.server(t)
	ts := forwardServer(t, upstream.URL, `forward-header "x-agent-origin" required=#true`)

	result := callOverMCP(t, ts, "")
	if !result.IsError {
		t.Fatalf("a call without the required header succeeded")
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "X-Agent-Origin") {
		t.Fatalf("refusal %q does not name the header", text)
	}
	if got := rec.calls(); len(got) != 0 {
		t.Fatalf("upstream was called %d times, want 0", len(got))
	}
	if result := callOverMCP(t, ts, "eng-platform/beetle-ox:fixture"); result.IsError {
		t.Fatalf("a call with the header failed: %v", result.Content)
	}
}

func TestForwardHeaderRefusesWhatItMustNotForward(t *testing.T) {
	cases := map[string]string{
		"credential":       `forward-header "Authorization"`,
		"duplicate":        "forward-header \"x-agent-origin\"\nforward-header \"X-Agent-Origin\"",
		"unknown property": `forward-header "x-agent-origin" rename="x"`,
		"non-bool":         `forward-header "x-agent-origin" required="yes"`,
		"no name":          `forward-header`,
	}
	for label, sibling := range cases {
		if _, err := New("test", "test.mcp.kdl", []byte(forwardSpec("http://127.0.0.1:1", sibling))); err == nil {
			t.Errorf("%s: New accepted %q", label, sibling)
		}
	}
}

func TestForwardHeaderRequiredOnlyTightensAcrossInherit(t *testing.T) {
	sources := []guardSource{
		{src: []byte(`forward-header "x-agent-origin" required=#true`), dir: "."},
		{src: []byte(`forward-header "x-agent-origin" required=#false`), dir: "."},
	}
	cfg, err := parseForwardHeaders(sources)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg["X-Agent-Origin"] {
		t.Fatalf("a child relaxed its base's required=#true: %v", cfg)
	}
}
