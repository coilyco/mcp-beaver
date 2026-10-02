package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coilyco/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// roundTripSpec points its base-url at a mock upstream (set per-test) and
// grants one read and one write tool plus an owner restriction, so a single spec
// covers projection, aliased query routing, typed body routing, and the guard.
func roundTripSpec(baseURL string) string {
	return `wrap ward mcp test {
    base-url "` + baseURL + `"
    auth bearer { value env "MCP_BEAVER_TEST_TOKEN" }
    restrict owner matches "coilyco-*"
    can get thing {
        path "/owners/{owner}/things/{id}"
        query "search_query" upstream="query"
    }
    can create thing {
        path "/owners/{owner}/things"
        body {
            field "title" type="string" required=#true
        }
    }
}`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// newTestHandler builds a handler over a two-tool spec pointed at a dead
// upstream. The transport tests exercise initialize, tools/list, and tools/call
// through the SDK-backed /mcp endpoint.
func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	s, err := New("test", "test.mcp.kdl", []byte(roundTripSpec("http://127.0.0.1:1")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s.Handler()
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(newTestHandler(t))
}

func newUpstreamServer(t *testing.T, server *mcp.Server) *httptest.Server {
	t.Helper()
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// These fixtures answer on the POST, so they have nothing to deliver on
		// a standalone stream and decline it, which the spec allows. Left open
		// it would outlive the test: httptest waits for outstanding requests.
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		inner.ServeHTTP(w, r)
	})
	return httptest.NewServer(mux)
}

func upstreamTool(t *testing.T, name, description, schema string, handler func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error)) *mcp.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: name, Version: "0.1.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(schema),
	}, handler)
	return srv
}

// postJSON posts one JSON-RPC message to path and returns the raw response.
func postToServer(t *testing.T, client *http.Client, url, sessionID, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-03-26")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func decodeRPCResponse(t *testing.T, resp *http.Response) rpcResponse {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		raw = sseData(t, raw, "message")
	}
	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode rpc response: %v\nbody: %s", err, raw)
	}
	return out
}

func sseData(t *testing.T, raw []byte, wantEvent string) []byte {
	t.Helper()
	lines := bytes.Split(raw, []byte("\n"))
	var event string
	for _, line := range lines {
		line = bytes.TrimRight(line, "\r")
		switch {
		case bytes.HasPrefix(line, []byte("event: ")):
			event = string(bytes.TrimPrefix(line, []byte("event: ")))
		case bytes.HasPrefix(line, []byte("data: ")):
			if event == wantEvent {
				return bytes.TrimPrefix(line, []byte("data: "))
			}
		}
	}
	t.Fatalf("no %q event in SSE body: %s", wantEvent, raw)
	return nil
}

func toolList(t *testing.T, resp rpcResponse) []map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	rawTools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools field is %T, want []any", result["tools"])
	}
	out := make([]map[string]any, 0, len(rawTools))
	for _, item := range rawTools {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("tool item is %T, want map", item)
		}
		out = append(out, m)
	}
	return out
}

// TestInitializeHandshake proves a pre-2026 client still negotiates through the
// stateless handler: version, tools capability, server info, and instructions.
func TestInitializeHandshake(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// No session id: the handler is stateless so 2026-07-28 clients are not
	// rejected, and that revision removed the header. A pre-2026 client still
	// negotiates its own version, which is what the rest of this asserts.
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("stateless handler minted a session id %q", got)
	}
	out := decodeRPCResponse(t, resp)
	var result map[string]any
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if result["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocolVersion = %v, want 2025-03-26", result["protocolVersion"])
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities missing tools; got %v", caps)
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "test" {
		t.Errorf("serverInfo.name = %v, want test", serverInfo["name"])
	}
	if got, _ := result["instructions"].(string); !strings.Contains(got, "policy-approved tools") {
		t.Errorf("instructions = %q, want policy boundary guidance", got)
	}
}

// containsAny reports whether a JSON-decoded `required` list names a field.
func containsAny(list []any, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

// TestToolsListFromSpec proves the tool list is derived from a `.mcp.kdl`: the
// committed forgejo example projects exactly its five grants, one tool each,
// named verb_resource, with no delete tool (deny-by-absence).
func TestToolsListFromSpec(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "forgejo-issues.mcp.kdl"))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	s, err := New("forgejo-issues", filepath.Join("..", "..", "examples", "forgejo-issues.mcp.kdl"), src)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	out := decodeRPCResponse(t, initResp)
	if out.Error != nil {
		t.Fatalf("initialize error: %+v", out.Error)
	}
	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := toolList(t, decodeRPCResponse(t, resp))
	got := map[string]bool{}
	for _, tl := range tools {
		if name, _ := tl["name"].(string); name != "" {
			got[name] = true
		}
	}
	// The info tool rides along on every spec-mode server by default; it is
	// the one non-grant entry, and it reaches no upstream.
	want := []string{"create_issue", "get_issue", "list_issue", "comment_issue", "close_issue", defaultServerInfoTool}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing tool %q; got %v", w, got)
		}
	}
	if got["delete_issue"] {
		t.Error("delete_issue was minted; deny-by-absence should have withheld it")
	}
	if got["admin_describe"] || got["admin_reload"] {
		t.Fatalf("admin endpoints leaked into tools/list: %v", got)
	}
	if len(tools) != len(want) {
		t.Errorf("tool count = %d, want %d (%v)", len(tools), len(want), got)
	}
	create := findTool(t, tools, "create_issue")
	var schema map[string]any
	if err := toJSON(create["inputSchema"], &schema); err != nil {
		t.Fatalf("create_issue schema not JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, field := range []string{"owner", "repo", "title", "body", "labels"} {
		if _, ok := props[field]; !ok {
			t.Errorf("create_issue schema missing property %q; got %v", field, props)
		}
	}
	// The block body form carries what the flat shorthand cannot: a required
	// flag and a typed array. labels rides along on creation so filing is one
	// call rather than two (#53).
	labels, _ := props["labels"].(map[string]any)
	if labels["type"] != "array" {
		t.Errorf("labels = %v, want an array", labels)
	}
	if items, _ := labels["items"].(map[string]any); items["type"] != "integer" {
		t.Errorf("labels items = %v, want integer ids: string ids arrive quoted and label nothing", items)
	}
	required, _ := schema["required"].([]any)
	if !containsAny(required, "title") {
		t.Errorf("required = %v, want title required", required)
	}
	if containsAny(required, "body") || containsAny(required, "labels") {
		t.Errorf("required = %v, want body and labels optional", required)
	}
	assertToolMetadata(t, create, "Create issue", false, false, false, true)
	assertResultOutputSchema(t, create)

	// The flat `body "..."` shorthand still mints optional strings. Pinned on
	// comment_issue, which is the grant in this example that still uses it.
	comment := findTool(t, tools, "comment_issue")
	var commentSchema map[string]any
	if err := toJSON(comment["inputSchema"], &commentSchema); err != nil {
		t.Fatalf("comment_issue schema not JSON: %v", err)
	}
	commentProps, _ := commentSchema["properties"].(map[string]any)
	if prop, _ := commentProps["body"].(map[string]any); prop["type"] != "string" {
		t.Errorf("flat body property = %v, want optional string", prop)
	}
	commentRequired, _ := commentSchema["required"].([]any)
	if containsAny(commentRequired, "body") {
		t.Errorf("flat body property became required: %v", commentRequired)
	}

	get := findTool(t, tools, "get_issue")
	assertToolMetadata(t, get, "Get issue", true, false, true, true)
	assertResultOutputSchema(t, get)
}

func TestToolMetadataClassifiesHTTPBehavior(t *testing.T) {
	tests := []struct {
		name            string
		desc            opcore.Descriptor
		wantReadOnly    bool
		wantDestructive bool
		wantIdempotent  bool
	}{
		{
			name:           "read",
			desc:           opcore.Descriptor{Leaf: "get", Group: "thing", Method: http.MethodGet},
			wantReadOnly:   true,
			wantIdempotent: true,
		},
		{
			name: "additive write",
			desc: opcore.Descriptor{Leaf: "create", Group: "thing", Method: http.MethodPost},
		},
		{
			name:            "destructive idempotent write",
			desc:            opcore.Descriptor{Leaf: "delete", Group: "thing", Method: http.MethodDelete, Destructive: true},
			wantDestructive: true,
			wantIdempotent:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := toolSpec(test.desc)
			if tool.Title == "" || tool.OutputSchema == nil {
				t.Fatalf("metadata incomplete: %#v", tool)
			}
			got := tool.Annotations
			if got == nil {
				t.Fatal("annotations are nil")
			}
			if got.ReadOnlyHint != test.wantReadOnly || got.IdempotentHint != test.wantIdempotent {
				t.Errorf("annotations = %#v", got)
			}
			if got.DestructiveHint == nil || *got.DestructiveHint != test.wantDestructive {
				t.Errorf("destructiveHint = %#v, want %t", got.DestructiveHint, test.wantDestructive)
			}
			if got.OpenWorldHint == nil || !*got.OpenWorldHint {
				t.Errorf("openWorldHint = %#v, want true", got.OpenWorldHint)
			}
		})
	}
}

// TestToolCallRoundTrip drives one tool call end to end against a mock upstream:
// arguments split onto path/query/body, the token is signed in, and the decoded
// response renders back as MCP tool content with isError false.
func TestToolCallRoundTrip(t *testing.T) {
	t.Setenv("MCP_BEAVER_TEST_TOKEN", "s3cr3t")

	var gotMethod, gotPath, gotQuery, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"name":"widget"}`))
	}))
	defer upstream.Close()

	s, err := New("test", "test.mcp.kdl", []byte(roundTripSpec(upstream.URL)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	listResp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := toolList(t, decodeRPCResponse(t, listResp))
	get := findTool(t, tools, "get_thing")
	var schema map[string]any
	if err := toJSON(get["inputSchema"], &schema); err != nil {
		t.Fatalf("get_thing schema not JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["search_query"]; !ok {
		t.Fatalf("get_thing schema omitted local query alias: %v", props)
	}
	if _, leaked := props["query"]; leaked {
		t.Fatalf("get_thing schema leaked reserved upstream query name: %v", props)
	}

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_thing","arguments":{"owner":"coilyco-flight-deck","id":"42","search_query":"platform engineer"}}}`)
	out := decodeRPCResponse(t, resp)
	var result map[string]any
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("tool call reported isError; content=%v", result["content"])
	}
	if gotMethod != http.MethodGet {
		t.Errorf("upstream method = %q, want GET", gotMethod)
	}
	if gotPath != "/owners/coilyco-flight-deck/things/42" {
		t.Errorf("upstream path = %q", gotPath)
	}
	if gotQuery != "query=platform+engineer" {
		t.Errorf("upstream query = %q, want query=platform+engineer", gotQuery)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Errorf("upstream auth = %q, want Bearer s3cr3t", gotAuth)
	}
	text := firstText(t, result)
	if !strings.Contains(text, `"name":"widget"`) {
		t.Errorf("tool content = %q, want the upstream body", text)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	value, _ := structured["result"].(map[string]any)
	if value["id"] != float64(42) || value["name"] != "widget" {
		t.Errorf("structuredContent = %v, want decoded upstream result", structured)
	}
}

// TestToolCallBodyRoundTrip proves a write tool sends its body fields as JSON
// and path params still route, distinct from the GET path.
func TestToolCallBodyRoundTrip(t *testing.T) {
	t.Setenv("MCP_BEAVER_TEST_TOKEN", "s3cr3t")

	var gotBody map[string]any
	var gotMethod string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	s, err := New("test", "test.mcp.kdl", []byte(roundTripSpec(upstream.URL)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_thing","arguments":{"owner":"coilyco-x","title":"hello"}}}`)
	out := decodeRPCResponse(t, resp)
	var result map[string]any
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("create call reported isError; content=%v", result["content"])
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody["title"] != "hello" {
		t.Errorf("upstream body = %v, want title=hello", gotBody)
	}
}

// TestToolCallRestrictDenied proves the guard is opcore's, not re-implemented:
// a path value outside the `restrict owner matches coilyco-*` allowlist comes
// back as a tool error and never reaches the upstream.
func TestToolCallRestrictDenied(t *testing.T) {
	t.Setenv("MCP_BEAVER_TEST_TOKEN", "s3cr3t")

	hit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	s, err := New("test", "test.mcp.kdl", []byte(roundTripSpec(upstream.URL)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_thing","arguments":{"owner":"someone-else","id":"1"}}}`)
	out := decodeRPCResponse(t, resp)
	var result map[string]any
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("restrict-denied call should be an isError result; got %v", result)
	}
	if hit {
		t.Error("upstream was called despite a restrict denial")
	}
}

// TestUnknownToolDenied proves deny-by-absence at call time: a tool the spec
// never granted is a method-not-found, not a silent pass.
func TestUnknownToolDenied(t *testing.T) {
	s, err := New("test", "test.mcp.kdl", []byte(roundTripSpec("http://127.0.0.1:1")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delete_thing","arguments":{}}}`)
	out := decodeRPCResponse(t, resp)
	if out.Error == nil {
		t.Fatal("calling an ungranted tool should be a JSON-RPC error")
	}
	if out.Error.Code != -32601 {
		t.Errorf("error code = %d, want method-not-found -32601", out.Error.Code)
	}
}

// TestHealth proves the runtime still exposes the pod liveness probe.
func TestHealth(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("health body = %v, want status ok", body)
	}
}

// TestAdminDescribe proves the runtime exposes non-MCP operator inspection
// without leaking the upstream host or secret material.
func TestAdminDescribe(t *testing.T) {
	t.Setenv("MCP_BEAVER_TEST_TOKEN", "s3cr3t")

	ts := httptest.NewServer(newTestHandler(t))
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + adminDescribePath)
	if err != nil {
		t.Fatalf("GET %s: %v", adminDescribePath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode describe body: %v", err)
	}
	server, _ := body["server"].(map[string]any)
	if server["name"] != "test" {
		t.Errorf("server.name = %v, want test", server["name"])
	}
	if server["spec"] != "test" {
		t.Errorf("server.spec = %v, want test", server["spec"])
	}
	if server["specPath"] != "test.mcp.kdl" {
		t.Errorf("server.specPath = %v, want test.mcp.kdl", server["specPath"])
	}
	projection, _ := body["projection"].(map[string]any)
	if got, _ := projection["toolCount"].(float64); got != 3 {
		t.Errorf("toolCount = %v, want 3 (two grants plus the default info tool)", got)
	}
	transport, _ := body["transport"].(map[string]any)
	if transport["mode"] != transportMode {
		t.Errorf("transport.mode = %v, want %s", transport["mode"], transportMode)
	}
	if transport["apiPrefix"] != apiPrefix {
		t.Errorf("transport.apiPrefix = %v, want %s", transport["apiPrefix"], apiPrefix)
	}
	if transport["inboundAuth"] != "deployment-owned" {
		t.Errorf("transport.inboundAuth = %v, want deployment-owned", transport["inboundAuth"])
	}
	config, _ := body["config"].(map[string]any)
	if config["baseUrlMode"] != "static" {
		t.Errorf("config.baseUrlMode = %v, want static", config["baseUrlMode"])
	}
	if config["restrictCount"] == nil {
		t.Fatal("config.restrictCount missing")
	}
	upstreams, _ := body["upstreams"].([]any)
	if len(upstreams) != 1 {
		t.Fatalf("upstreams = %v, want 1 configured upstream", body["upstreams"])
	}
	upstream, _ := upstreams[0].(map[string]any)
	if upstream["kind"] != "base-url" || upstream["mode"] != "static" {
		t.Fatalf("upstream = %v, want base-url/static", upstream)
	}
	reload, _ := body["reload"].(map[string]any)
	if reload["mode"] != "restart-only" || reload["status"] != "restart-required" {
		t.Fatalf("reload = %v, want restart-only/restart-required", reload)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal describe body: %v", err)
	}
	for _, forbidden := range []string{"127.0.0.1", "s3cr3t"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("describe response leaked %q: %s", forbidden, raw)
		}
	}
}

// TestAdminReloadRestartOnly proves the runtime exposes an explicit operator
// reload endpoint and says restart-only when the process cannot reload safely.
func TestAdminReloadRestartOnly(t *testing.T) {
	ts := httptest.NewServer(newTestHandler(t))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+adminReloadPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", adminReloadPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode reload body: %v", err)
	}
	if body["mode"] != "restart-only" {
		t.Errorf("reload.mode = %v, want restart-only", body["mode"])
	}
	if body["status"] != "restart-required" {
		t.Errorf("reload.status = %v, want restart-required", body["status"])
	}
}

// TestUpstreamProxyRoundTrip proves the new streamable-HTTP backend can proxy a
// selected upstream tool, preserve its schema, and forward the call.
func TestUpstreamProxyRoundTrip(t *testing.T) {
	upstream := upstreamTool(t, "browse", "browse upstream", `{
		"type":"object",
		"properties":{"q":{"type":"string"}},
		"required":["q"]
	}`, func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "upstream:" + args["q"].(string)}}}, nil, nil
	})
	upstreamTS := newUpstreamServer(t, upstream)
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
	out := decodeRPCResponse(t, initResp)
	if out.Error != nil {
		t.Fatalf("initialize error: %+v", out.Error)
	}

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := toolList(t, decodeRPCResponse(t, resp))
	if len(tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(tools))
	}
	tool := tools[0]
	if tool["name"] != "browse" {
		t.Fatalf("tool = %v, want browse", tool)
	}
	schema, _ := tool["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["q"]; !ok {
		t.Fatalf("browse schema missing q: %v", schema)
	}

	callResp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"browse","arguments":{"q":"ramen"}}}`)
	callOut := decodeRPCResponse(t, callResp)
	var callResult map[string]any
	if err := json.Unmarshal(callOut.Result, &callResult); err != nil {
		t.Fatalf("call result: %v", err)
	}
	if isErr, _ := callResult["isError"].(bool); isErr {
		t.Fatalf("browse call reported isError; content=%v", callResult["content"])
	}
	if got := firstText(t, callResult); !strings.Contains(got, "upstream:ramen") {
		t.Fatalf("browse result = %q, want upstream:ramen", got)
	}
}

// TestUpstreamProxySchemaDriftFailsClosed proves the proxy compares upstream
// tool contracts on call and returns an MCP tool error if the upstream schema
// drifts.
// The drift is applied to the same upstream server rather than by swapping the
// instance behind the endpoint. The proxy now checks drift over its long-lived
// session (mcp-beaver#67), and an instance swap is invisible to an already
// established session - as it is in production, where replacing the served
// binary means replacing the pod.
func TestUpstreamProxySchemaDriftFailsClosed(t *testing.T) {
	upstream := upstreamTool(t, "browse", "browse upstream", `{
		"type":"object",
		"properties":{"q":{"type":"string"}},
		"required":["q"]
	}`, func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "v1:" + args["q"].(string)}}}, nil, nil
	})
	upstreamTS := newUpstreamServer(t, upstream)
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
	if out := decodeRPCResponse(t, initResp); out.Error != nil {
		t.Fatalf("initialize error: %+v", out.Error)
	}

	upstream.RemoveTools("browse")
	mcp.AddTool(upstream, &mcp.Tool{
		Name:        "browse",
		Description: "browse upstream",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{"q":{"type":"integer"}},
			"required":["q"]
		}`),
	}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "v2"}}}, nil, nil
	})

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browse","arguments":{"q":"ramen"}}}`)
	out := decodeRPCResponse(t, resp)
	var result map[string]any
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("schema drift should return isError; got %v", result)
	}
	if got := firstText(t, result); !strings.Contains(got, "schema drift") {
		t.Fatalf("drift error = %q, want schema drift", got)
	}
}

func findTool(t *testing.T, tools []map[string]any, name string) map[string]any {
	t.Helper()
	for _, tl := range tools {
		if tl["name"] == name {
			return tl
		}
	}
	t.Fatalf("no tool %q", name)
	return nil
}

func assertToolMetadata(
	t *testing.T,
	tool map[string]any,
	wantTitle string,
	wantReadOnly, wantDestructive, wantIdempotent, wantOpenWorld bool,
) {
	t.Helper()
	if tool["title"] != wantTitle {
		t.Errorf("%s title = %v, want %q", tool["name"], tool["title"], wantTitle)
	}
	annotations, ok := tool["annotations"].(map[string]any)
	if !ok {
		t.Fatalf("%s annotations = %T, want object", tool["name"], tool["annotations"])
	}
	want := map[string]bool{
		"readOnlyHint":    wantReadOnly,
		"destructiveHint": wantDestructive,
		"idempotentHint":  wantIdempotent,
		"openWorldHint":   wantOpenWorld,
	}
	for field, expected := range want {
		got, _ := annotations[field].(bool)
		if got != expected {
			t.Errorf("%s annotations.%s = %v, want %t", tool["name"], field, annotations[field], expected)
		}
	}
}

func assertResultOutputSchema(t *testing.T, tool map[string]any) {
	t.Helper()
	schema, ok := tool["outputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("%s outputSchema = %T, want object", tool["name"], tool["outputSchema"])
	}
	if schema["type"] != "object" {
		t.Errorf("%s outputSchema.type = %v, want object", tool["name"], schema["type"])
	}
	properties, _ := schema["properties"].(map[string]any)
	if _, ok := properties["result"]; !ok {
		t.Errorf("%s outputSchema omitted result: %v", tool["name"], schema)
	}
	if _, ok := properties["coverage"]; !ok {
		t.Errorf("%s outputSchema omitted coverage: %v", tool["name"], schema)
	}
	// Both required, so a consumer can rely on the caveat being there rather
	// than treating its absence as "nothing to say" (mcp-beaver#68).
	required, _ := schema["required"].([]any)
	if len(required) != 2 || !containsAny(required, "result") || !containsAny(required, "coverage") {
		t.Errorf("%s outputSchema.required = %v, want coverage and result", tool["name"], schema["required"])
	}
}

func firstText(t *testing.T, res map[string]any) string {
	t.Helper()
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("content is %T / empty: %v", res["content"], res["content"])
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

func toJSON(v any, out any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// The regression this guards: a session-backed handler rejects a 2026-07-28
// client outright with 400, so mcp-beaver was unreachable from any client that
// negotiated the current revision.
func TestStatelessAcceptsCurrentProtocolRevision(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
	))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/list")
	req.Header.Set("Mcp-Name", "tools/list")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	out := decodeRPCResponse(t, resp)
	if out.Error != nil {
		t.Fatalf("tools/list error: %+v", out.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if result["resultType"] != "complete" {
		t.Errorf("resultType = %v, want complete", result["resultType"])
	}
	if len(result["tools"].([]any)) == 0 {
		t.Error("tools/list returned no tools")
	}
}

// 2026-07-28 requires ttlMs on cacheable list results. The SDK leaves it 0,
// which tells clients the list is immediately stale, so mcp-beaver stamps it.
func TestToolsListCarriesCacheHints(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	decodeRPCResponse(t, initResp)
	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var result map[string]any
	if err := json.Unmarshal(decodeRPCResponse(t, resp).Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if got, want := result["ttlMs"], float64(specListTTLMs); got != want {
		t.Errorf("ttlMs = %v, want %v", got, want)
	}
	if got := result["cacheScope"]; got != "public" {
		t.Errorf("cacheScope = %v, want public", got)
	}
}

// A proxied list mirrors an upstream that can change under us, so it carries
// the shorter hint.
func TestProxyToolsListUsesShorterCacheHint(t *testing.T) {
	upstreamTS := newUpstreamServer(t, upstreamTool(t, "browse", "browse upstream", `{"type":"object","properties":{}}`,
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		}))
	defer upstreamTS.Close()
	s, err := NewProxy(context.Background(), "proxy", "", upstreamTS.URL+"/mcp", []string{"browse"}, upstreamTS.Client())
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var result map[string]any
	if err := json.Unmarshal(decodeRPCResponse(t, resp).Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if got, want := result["ttlMs"], float64(upstreamListTTLMs); got != want {
		t.Errorf("ttlMs = %v, want %v", got, want)
	}
}

// 2026-07-28 deprecates Logging, Roots, and Sampling. mcp-beaver emits
// OpenTelemetry instead, so it should not claim the deprecated capability.
func TestCapabilitiesOmitDeprecatedLogging(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	var result map[string]any
	if err := json.Unmarshal(decodeRPCResponse(t, resp).Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["logging"]; ok {
		t.Errorf("capabilities still advertise deprecated logging: %v", caps)
	}
	// The inferred tools capability must survive dropping the default.
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities lost tools: %v", caps)
	}
}
