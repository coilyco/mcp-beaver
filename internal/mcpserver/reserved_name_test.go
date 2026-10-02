package mcpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The unrestricted Exa shape: `query` is a body field beside optional ones, which a
// mapping cannot carry because it makes every field required. mcp-beaver mounts no
// CLI flags, so umbra's reserved names do not apply to it.
func unrestrictedSearchSpec(baseURL string) string {
	return `wrap ward mcp exa {
    base-url "` + baseURL + `"
    auth bearer { value literal "unused" }
    can search web {
        method "POST"
        path "/search"
        body {
            field "query" type="string" required=#true
            field "numResults" type="integer"
            object "contents" raw=#true
        }
    }
}`
}

func TestBodyFieldKeepsAReservedNameOnTheWire(t *testing.T) {
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer upstream.Close()

	s, err := New("exa", "exa.mcp.kdl", []byte(unrestrictedSearchSpec(upstream.URL)))
	if err != nil {
		t.Fatalf("a body field named query must be servable: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	result := callTool(t, ts, sessionID, "2", "search_web", `{"query":"exa vs tavily","numResults":3,"contents":{"text":true}}`)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("search_web reported isError; content=%v", result["content"])
	}
	if got["query"] != "exa vs tavily" || got["numResults"] != float64(3) {
		t.Fatalf("upstream body = %v, want query and numResults unrenamed", got)
	}
	if contents, _ := got["contents"].(map[string]any); contents["text"] != true {
		t.Fatalf("upstream body = %v, want the raw contents object", got)
	}
}
