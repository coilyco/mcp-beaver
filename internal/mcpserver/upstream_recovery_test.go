package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sessionDroppingUpstream serves normally until drop is set, then answers every
// request carrying a session id with 404 `session not found` - which is how a
// real upstream answers a session it has forgotten. Clearing drop lets a fresh
// handshake through again.
func sessionDroppingUpstream(t *testing.T, server *mcp.Server) (*httptest.Server, *atomic.Bool, *atomic.Int64) {
	t.Helper()
	drop := &atomic.Bool{}
	handshakes := &atomic.Int64{}
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// Answered on the POST, so no standalone stream is offered. Holding
		// one open would outlive the test, since httptest waits for
		// outstanding requests before it closes.
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "" && drop.Load() {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Mcp-Session-Id") == "" && r.Method == http.MethodPost {
			handshakes.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
	return httptest.NewServer(mux), drop, handshakes
}

// One lost session must not brick the pod. A playwright deployment recorded 0
// successes over 24h because a single aborted call left the upstream session
// gone and nothing ever reconnected (mcp-beaver#79).
func TestUpstreamProxyRecoversALostSession(t *testing.T) {
	upstream := upstreamTool(t, "browse", "browse upstream", `{
		"type":"object",
		"properties":{"q":{"type":"string"}},
		"required":["q"]
	}`, func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "upstream:" + args["q"].(string)}}}, nil, nil
	})
	upstreamTS, drop, handshakes := sessionDroppingUpstream(t, upstream)
	defer upstreamTS.Close()

	s, err := NewProxy(context.Background(), "proxy", "", upstreamTS.URL+"/mcp", []string{"browse"}, upstreamTS.Client())
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	call := func(id string) map[string]any {
		t.Helper()
		resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID,
			`{"jsonrpc":"2.0","id":`+id+`,"method":"tools/call","params":{"name":"browse","arguments":{"q":"ramen"}}}`)
		var result map[string]any
		if err := json.Unmarshal(decodeRPCResponse(t, resp).Result, &result); err != nil {
			t.Fatalf("call %s: %v", id, err)
		}
		return result
	}

	if isErr, _ := call("2")["isError"].(bool); isErr {
		t.Fatalf("the first call failed before anything was dropped")
	}

	// The upstream forgets the session. The in-flight call is allowed to fail;
	// what must not happen is every later call failing forever.
	drop.Store(true)
	if isErr, _ := call("3")["isError"].(bool); !isErr {
		t.Fatalf("a dropped session did not surface as an error")
	}

	drop.Store(false)
	result := call("4")
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("the proxy never recovered; content=%v", result["content"])
	}
	if got := firstText(t, result); !strings.Contains(got, "upstream:ramen") {
		t.Fatalf("recovered call returned %q", got)
	}
	if got := handshakes.Load(); got < 2 {
		t.Errorf("handshakes = %d, want the startup one plus a reconnect", got)
	}
}

// Schema drift is a decision about the upstream, not a transport failure.
// Reconnecting past it would ask the same question twice and adopt the answer.
func TestUpstreamProxyDoesNotReconnectPastSchemaDrift(t *testing.T) {
	upstream := upstreamTool(t, "browse", "browse upstream", `{
		"type":"object",
		"properties":{"q":{"type":"string"}},
		"required":["q"]
	}`, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	upstreamTS, _, handshakes := sessionDroppingUpstream(t, upstream)
	defer upstreamTS.Close()

	s, err := NewProxy(context.Background(), "proxy", "", upstreamTS.URL+"/mcp", []string{"browse"}, upstreamTS.Client())
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	upstream.RemoveTools("browse")
	mcp.AddTool(upstream, &mcp.Tool{
		Name:        "browse",
		Description: "browse upstream",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"integer"}},"required":["q"]}`),
	}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})

	before := handshakes.Load()
	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browse","arguments":{"q":"ramen"}}}`)
	var result map[string]any
	if err := json.Unmarshal(decodeRPCResponse(t, resp).Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("schema drift did not fail closed: %v", result)
	}
	if got := firstText(t, result); !strings.Contains(got, "schema drift") {
		t.Fatalf("drift error = %q", got)
	}
	if got := handshakes.Load(); got != before {
		t.Errorf("drift triggered %d reconnect(s); it must not", got-before)
	}
}

// One drifted tool must not take its unchanged neighbours down with it, and a
// refusal names the tool that drifted. The latch used to be one error for the
// whole backend, so every allowlisted tool answered with the first drift's name.
func TestUpstreamProxySchemaDriftIsPerTool(t *testing.T) {
	text := func(s string) func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
		}
	}
	upstream := upstreamTool(t, "browse", "browse upstream", `{"type":"object","properties":{"q":{"type":"string"}}}`, text("browse:ok"))
	mcp.AddTool(upstream, &mcp.Tool{
		Name:        "lookup",
		Description: "lookup upstream",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`),
	}, text("lookup:ok"))
	upstreamTS := newUpstreamServer(t, upstream)
	defer upstreamTS.Close()

	s, err := NewProxy(context.Background(), "proxy", "", upstreamTS.URL+"/mcp", []string{"browse", "lookup"}, upstreamTS.Client())
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	upstream.RemoveTools("lookup")
	mcp.AddTool(upstream, &mcp.Tool{
		Name:        "lookup",
		Description: "lookup upstream",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`),
	}, text("lookup:v2"))

	call := func(id int, tool string) (bool, string) {
		body := `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"` + tool + `","arguments":{}}}`
		var result map[string]any
		if err := json.Unmarshal(decodeRPCResponse(t, postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, body)).Result, &result); err != nil {
			t.Fatalf("%s result: %v", tool, err)
		}
		isErr, _ := result["isError"].(bool)
		return isErr, firstText(t, result)
	}

	if isErr, got := call(2, "lookup"); !isErr || !strings.Contains(got, `schema drift for tool "lookup"`) {
		t.Fatalf("drifted tool = (%v, %q), want a refusal naming lookup", isErr, got)
	}
	if isErr, got := call(3, "browse"); isErr || got != "browse:ok" {
		t.Fatalf("unchanged tool = (%v, %q), want it served after its neighbour drifted", isErr, got)
	}
	if isErr, got := call(4, "lookup"); !isErr || !strings.Contains(got, `schema drift for tool "lookup"`) {
		t.Fatalf("drifted tool, second call = (%v, %q), want it latched", isErr, got)
	}
}
