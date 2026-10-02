package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/coilyco/umbra/http/opcore"
)

func structuredBodySpec(baseURL string) string {
	return `wrap ward mcp test {
    base-url "` + baseURL + `"
    auth bearer { value env "MCP_BEAVER_TEST_TOKEN" }
    can create report {
        path "/reports"
        body {
            field "title" type="string" required=true
            object "options" required=true {
                field "enabled" type="boolean" required=true
                field "limit" type="integer"
            }
            array "labels" items="string"
            object "payload" raw=true
            array "events" raw=true required=true
        }
    }
}`
}

func TestToolsListProjectsStructuredBodySchemaUnchanged(t *testing.T) {
	src := []byte(structuredBodySpec("http://127.0.0.1:1"))
	descs, _, err := opcore.ParseInline(src)
	if err != nil {
		t.Fatalf("ParseInline: %v", err)
	}
	if len(descs) != 1 {
		t.Fatalf("descriptor count = %d, want 1", len(descs))
	}

	s, err := New("test", "test.mcp.kdl", src)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	if out := decodeRPCResponse(t, initResp); out.Error != nil {
		t.Fatalf("initialize error: %+v", out.Error)
	}

	listResp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tool := findTool(t, toolList(t, decodeRPCResponse(t, listResp)), "create_report")
	var got map[string]any
	if err := toJSON(tool["inputSchema"], &got); err != nil {
		t.Fatalf("tools/list inputSchema: %v", err)
	}
	var want map[string]any
	if err := json.Unmarshal(descs[0].InputSchema().JSONSchema(), &want); err != nil {
		t.Fatalf("opcore input schema: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools/list inputSchema changed opcore schema:\ngot  %v\nwant %v", got, want)
	}

	props := got["properties"].(map[string]any)
	options := props["options"].(map[string]any)
	if options["type"] != "object" {
		t.Fatalf("options schema = %v, want object", options)
	}
	optionProps := options["properties"].(map[string]any)
	if optionProps["enabled"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("options.enabled schema = %v, want boolean", optionProps["enabled"])
	}
	optionRequired := options["required"].([]any)
	if len(optionRequired) != 1 || optionRequired[0] != "enabled" {
		t.Fatalf("options.required = %v, want [enabled]", optionRequired)
	}
	labels := props["labels"].(map[string]any)
	if labels["type"] != "array" || labels["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("labels schema = %v, want string array", labels)
	}
	for _, name := range []string{"payload", "events"} {
		prop := props[name].(map[string]any)
		if prop["x-opcore-raw"] != true {
			t.Errorf("%s raw marker = %v, want true", name, prop["x-opcore-raw"])
		}
	}
	if _, ok := props["events"].(map[string]any)["items"]; ok {
		t.Errorf("raw events array constrained its items: %v", props["events"])
	}
	required := got["required"].([]any)
	if !reflect.DeepEqual(required, []any{"events", "options", "title"}) {
		t.Errorf("required = %v, want [events options title]", required)
	}
}

func TestStructuredBodyRoundTripsThroughArgsAndUpstreamJSON(t *testing.T) {
	t.Setenv("MCP_BEAVER_TEST_TOKEN", "test-token")

	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	src := []byte(structuredBodySpec(upstream.URL))
	descs, _, err := opcore.ParseInline(src)
	if err != nil {
		t.Fatalf("ParseInline: %v", err)
	}
	arguments := map[string]any{
		"title": "weekly",
		"options": map[string]any{
			"enabled": true,
			"limit":   float64(25),
		},
		"labels": []any{"operations", "weekly"},
		"payload": map[string]any{
			"filters": []any{
				map[string]any{"field": "service", "value": "api"},
			},
			"window": map[string]any{"start": float64(10), "end": float64(20)},
		},
		"events": []any{
			map[string]any{"kind": "opened"},
			[]any{"raw", "nested", "array"},
		},
	}
	args := splitArgs(descs[0].InputSchema(), arguments)
	if !reflect.DeepEqual(args.Body, arguments) {
		t.Fatalf("Args.Body changed structured values:\ngot  %v\nwant %v", args.Body, arguments)
	}

	s, err := New("test", "test.mcp.kdl", src)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	if out := decodeRPCResponse(t, initResp); out.Error != nil {
		t.Fatalf("initialize error: %+v", out.Error)
	}
	call, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "create_report",
			"arguments": arguments,
		},
	})
	if err != nil {
		t.Fatalf("marshal tools/call: %v", err)
	}
	resp := postToServer(t, ts.Client(), ts.URL+"/mcp", sessionID, string(call))
	var result map[string]any
	out := decodeRPCResponse(t, resp)
	if out.Error != nil {
		t.Fatalf("tools/call error: %+v", out.Error)
	}
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatalf("decode tools/call result: %v", err)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("structured body call reported isError: %v", result["content"])
	}
	if !reflect.DeepEqual(gotBody, arguments) {
		t.Fatalf("upstream JSON changed structured values:\ngot  %v\nwant %v", gotBody, arguments)
	}
}
