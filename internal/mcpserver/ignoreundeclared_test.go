package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// alertmanagerPayload is the shape SigNoz posts: the one annotation the `map`
// reads, plus every top-level key the grant never declares (teable:coilyco/deploy#8419).
const alertmanagerPayload = `{"receiver":"telegram","status":"firing","alerts":[{"status":"firing"}],` +
	`"groupLabels":{},"commonLabels":{"alertname":"HighErrorRate"},` +
	`"commonAnnotations":{"summary":"API error rate is high"},` +
	`"externalURL":"http://signoz","version":"4","groupKey":"{}:{}","truncatedAlerts":0}`

func TestIgnoreUndeclaredArgumentsDeliversAWebhookPayload(t *testing.T) {
	var gotBodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		gotBodies = append(gotBodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	spec := `ignore-undeclared-arguments "create_message"` + "\n" + bodyMappingSpec(upstream.URL)
	s, err := New("mapper", "mapper.mcp.kdl", []byte(spec))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postAPI(t, ts.Client(), ts.URL+"/api/create_message", "application/json", alertmanagerPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("API status = %d, want 200; body = %v", resp.StatusCode, decodeAPIResult(t, resp))
	}
	want := []map[string]any{{"text": "API error rate is high"}}
	if !reflect.DeepEqual(gotBodies, want) {
		t.Errorf("upstream bodies = %#v, want %#v: undeclared keys must be dropped, not forwarded", gotBodies, want)
	}
}

// The same payload without the directive is still refused, so the #94 guard is
// off only where a guardfile says so.
func TestAlertmanagerPayloadIsRefusedWithoutTheDirective(t *testing.T) {
	upstream := bodyUpstream(t, `{"ok":true}`)
	s, err := New("mapper", "mapper.mcp.kdl", []byte(bodyMappingSpec(upstream.URL)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postAPI(t, ts.Client(), ts.URL+"/api/create_message", "application/json", alertmanagerPayload)
	result := decodeAPIResult(t, resp)
	if resp.StatusCode != http.StatusBadGateway || result["isError"] != true {
		t.Fatalf("status/body = %d %v, want 502 and isError=true", resp.StatusCode, result)
	}
}

// A grant without a body `map` is where a dropped key is a dropped filter, so
// naming one is a build error rather than a quieter #94.
func TestIgnoreUndeclaredArgumentsRefusesAGrantWithoutAMap(t *testing.T) {
	cases := map[string]string{
		"query grant": `ignore-undeclared-arguments "get_thing"`,
		"field grant": `ignore-undeclared-arguments "create_thing"`,
		"unserved":    `ignore-undeclared-arguments "delete_thing"`,
		"property":    `ignore-undeclared-arguments "get_thing" loud=#true`,
	}
	for name, sibling := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New("test", "test.mcp.kdl", []byte(cachedSpec("http://127.0.0.1:1", sibling)))
			if err == nil || !strings.Contains(err.Error(), "ignore-undeclared-arguments") {
				t.Fatalf("New error = %v, want a refusal naming the directive", err)
			}
		})
	}
}
