package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coilyco/umbra/http/mcpverb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const upstreamSpecFull = `instructions {
    text "Search the published Tandem docs index."
}

mcp-upstream "ac.tandem/docs-mcp" {
    url "https://tandem.ac/mcp"
    transport streamable-http
    annotation-coverage partial annotated=7 silent=6

    auth header-token {
        header "Authorization"
        prefix "Bearer "
        value literal "unused"
    }

    can "search_docs"
    can get_doc
}
`

func TestParseUpstreamSpecReadsEveryNode(t *testing.T) {
	spec, err := ParseUpstreamSpec("tandem.mcp.kdl", []byte(upstreamSpecFull))
	if err != nil {
		t.Fatalf("ParseUpstreamSpec: %v", err)
	}
	if spec.Name != "ac.tandem/docs-mcp" || spec.URL != "https://tandem.ac/mcp" {
		t.Fatalf("name/url = %q %q", spec.Name, spec.URL)
	}
	// Stated order, quoted and bare alike: the proxy serves it as written.
	if got := strings.Join(spec.Tools, ","); got != "search_docs,get_doc" {
		t.Fatalf("tools = %q", got)
	}
	if spec.Coverage == nil || *spec.Coverage != (AnnotationCoverage{Kind: "partial", Annotated: 7, Silent: 6}) {
		t.Fatalf("coverage = %+v", spec.Coverage)
	}
	if len(spec.Headers) != 1 || spec.Headers[0].Name != "Authorization" {
		t.Fatalf("headers = %+v", spec.Headers)
	}
	if spec.Instructions != "Search the published Tandem docs index." {
		t.Fatalf("instructions = %q", spec.Instructions)
	}
}

func TestParseUpstreamSpecAllowsAnEmptyAllowlist(t *testing.T) {
	spec, err := ParseUpstreamSpec("", []byte(`mcp-upstream "x/y" {
    url "https://example.invalid/mcp"
    annotation-coverage undeclared annotated=0 silent=3
}`))
	if err != nil {
		t.Fatalf("a guardfile that exposes nothing is a valid statement: %v", err)
	}
	if len(spec.Tools) != 0 || spec.Coverage.Kind != "undeclared" {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestClassifyGuardfileTellsTheShapesApart(t *testing.T) {
	for src, want := range map[string]mcpverb.Shape{
		`mcp-upstream "x/y" { url "https://e.invalid/mcp" }`:     mcpverb.ShapeUpstream,
		`wrap ward mcp forgejo { base-url "https://e.invalid" }`: mcpverb.ShapeCommand,
		`instructions { text "hi" }
mcp-upstream "x/y" { url "https://e.invalid/mcp" }`: mcpverb.ShapeUpstream,
		// The boolean shorthand opcore accepts and strict KDL does not. A
		// guardfile that will not parse classifies as nothing, so the
		// normalization has to happen before umbra sees the bytes.
		`wrap ward mcp things {
    base-url "https://e.invalid"
    can list things { path "/things"; query "q" required=true }
}`: mcpverb.ShapeCommand,
	} {
		got, err := ClassifyGuardfile([]byte(src))
		if err != nil {
			t.Fatalf("ClassifyGuardfile(%q): %v", src, err)
		}
		if got != want {
			t.Fatalf("ClassifyGuardfile(%q) = %v, want %v", src, got, want)
		}
	}
	// Neither node is not a default, it is an error.
	if _, err := ClassifyGuardfile([]byte(`instructions { text "hi" }`)); err == nil {
		t.Fatal("a guardfile carrying neither shape must refuse rather than pick one")
	}
}

// The guardfile-wide auth grammar is umbra's now, so `bearer` and `none`
// reach the proxy where the hand-rolled parser took header-token alone.
func TestParseUpstreamSpecServesTheWiderAuthGrammar(t *testing.T) {
	spec, err := ParseUpstreamSpec("", []byte(`description "Docs."

mcp-upstream "x/y" {
    url "https://e.invalid/mcp"
    auth bearer {
        value literal "unused"
    }
}`))
	if err != nil {
		t.Fatalf("ParseUpstreamSpec: %v", err)
	}
	if len(spec.Headers) != 1 || spec.Headers[0].Name != "Authorization" {
		t.Fatalf("headers = %+v, want the bearer shorthand's Authorization", spec.Headers)
	}
	// umbra owns `description` beside the node, so it parses rather than
	// meeting the unprojected-sibling refusal.
	if spec.Description != "Docs." {
		t.Fatalf("description = %q", spec.Description)
	}
	none, err := ParseUpstreamSpec("", []byte(`mcp-upstream "x/y" { url "https://e.invalid/mcp"; auth none }`))
	if err != nil {
		t.Fatalf("`auth none` is a statement, not an omission: %v", err)
	}
	if len(none.Headers) != 0 {
		t.Fatalf("headers = %+v, want none", none.Headers)
	}
}

func TestParseUpstreamSpecFailsClosed(t *testing.T) {
	body := func(inner string) string {
		return "mcp-upstream \"x/y\" {\n    url \"https://e.invalid/mcp\"\n" + inner + "\n}"
	}
	cases := map[string]string{
		"no url":                        `mcp-upstream "x/y" { can "a" }`,
		"relative url":                  body(`url "e.invalid/mcp"`),
		"other transport":               body(`transport stdio`),
		"duplicate can":                 body("    can \"a\"\n    can \"a\""),
		"can with children":             body(`can "a" { path "/x" }`),
		"rest node in body":             body(`base-url "https://e.invalid"`),
		"inherit in body":               body(`inherit "base.mcp.kdl"`),
		"coverage unknown kind":         body(`annotation-coverage some annotated=1 silent=1`),
		"coverage declared silent":      body(`annotation-coverage declared annotated=3 silent=1`),
		"coverage undeclared annotated": body(`annotation-coverage undeclared annotated=1 silent=0`),
		"coverage partial zero":         body(`annotation-coverage partial annotated=0 silent=2`),
		"coverage missing count":        body(`annotation-coverage declared annotated=3`),
		"coverage not a number":         body(`annotation-coverage declared annotated="3" silent=0`),
		"auth query-param":              body("    auth query-param {\n        param key { value literal \"x\" }\n    }"),
		"auth value chain":              body("    auth bearer {\n        value { env \"A\"; literal \"b\" }\n    }"),
		"auth missing value":            body(`auth header-token { header "Authorization" }`),
		"auth brace in prefix":          body(`auth header-token { header "A"; prefix "{"; value env "T" }`),
		"unprojected sibling":           body(`can "a"`) + "\ncache \"a\" ttl=\"1m\"",
		"unprojected forward-header":    body(`can "a"`) + "\nforward-header \"x-agent-origin\"",
		"confirm naming no minted tool": body(`can "a"`) + "\nconfirm \"b\" message=\"sure?\"",
		"server-info shadowing a grant": body(`can "a"`) + "\nserver-info name=\"a\"",
		"pin naming no allowlisted tool": body(`can "a"`) +
			"\npin \"b\" { argument \"scope\" literal \"x\" }",
		"pin with a REST query child": body(`can "a"`) +
			"\npin \"a\" { query \"scope\" literal \"x\" }",
		"pin that fixes nothing":     body(`can "a"`) + "\npin \"a\"",
		"pin on an unknown provider": body(`can "a"`) + "\npin \"a\" { argument \"scope\" nope \"x\" }",
		"pin with an empty source":   body(`can "a"`) + "\npin \"a\" { argument \"scope\" literal \"\" }",
		"duplicate rate-limit":       body(`can "a"`) + "\nrate-limit \"1/1s\"\nrate-limit \"2/1s\"",
		"rate-limit with no window":  body(`can "a"`) + "\nrate-limit \"1\"",
		"icon with no src":           body(`can "a"`) + "\nicon",
		"withhold with no reason":    body(`can "a"`) + "\nwithhold \"b\" { alternative \"a\" }",
		"withhold shadowing a grant": body(`can "a"`) + "\nwithhold \"a\" { reason \"no\" }",
		"withhold naming a missing alternative": body(`can "a"`) +
			"\nwithhold \"b\" { reason \"no\"; alternative \"c\" }",
		"property on the node":    `mcp-upstream "x/y" foo=1 { url "https://e.invalid/mcp" }`,
		"two names":               `mcp-upstream "x/y" "z" { url "https://e.invalid/mcp" }`,
		"both shapes in one file": `wrap ward mcp x { base-url "https://e.invalid" }` + "\n" + body(""),
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseUpstreamSpec("", []byte(src)); err == nil {
				t.Fatalf("accepted:\n%s", src)
			}
		})
	}
}

// New serves REST grants and must not swallow a proxy guardfile into an
// opcore parse error nobody can act on.
func TestNewPointsAProxyGuardfileAtServeUpstream(t *testing.T) {
	_, err := New("x", "x.mcp.kdl", []byte(`mcp-upstream "x/y" { url "https://e.invalid/mcp" }`))
	if err == nil || !strings.Contains(err.Error(), "serve-upstream") {
		t.Fatalf("New error = %v, want it to name serve-upstream", err)
	}
}

// annotatedUpstream serves three tools that cover the whole evidence axis:
// declared read-only, declared mutating, and silent.
func hintedUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/mcp", hintedUpstreamHandler(t))
	return httptest.NewServer(mux)
}

func hintedUpstreamHandler(t *testing.T) http.Handler {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.1.0"}, nil)
	for _, tc := range []struct {
		name        string
		annotations *mcp.ToolAnnotations
	}{
		{"get_thing", &mcp.ToolAnnotations{ReadOnlyHint: true}},
		{"delete_thing", &mcp.ToolAnnotations{ReadOnlyHint: false}},
		{"mystery", nil},
	} {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        tc.name,
			Description: "the " + tc.name + " tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Annotations: tc.annotations,
		}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
}

// The guardfile's own text has to reach the handshake, which is the one
// thing a flag could never state (mcp-beaver#109).
func TestUpstreamSpecServesItsInstructions(t *testing.T) {
	ts := hintedUpstream(t)
	// Closed after the proxy below (LIFO), because the proxy's session holds
	// the upstream's standalone stream open and httptest waits for it.
	defer ts.Close()
	src := strings.Replace(upstreamSpecFull, "https://tandem.ac/mcp", ts.URL+"/mcp", 1)
	src = strings.Replace(src, "can \"search_docs\"\n    can get_doc", "can \"get_thing\"", 1)
	spec, err := ParseUpstreamSpec("tandem.mcp.kdl", []byte(src))
	if err != nil {
		t.Fatalf("ParseUpstreamSpec: %v", err)
	}
	s, err := NewProxyWithOptions(context.Background(), "tandem", "tandem.mcp.kdl", spec.URL, spec.Tools, spec.Options())
	if err != nil {
		t.Fatalf("NewProxyWithOptions: %v", err)
	}
	defer func() { _ = s.Close() }()
	if got := strings.Join(s.ToolNames(), ","); got != "get_thing,mcp_beaver_info" {
		t.Fatalf("tools = %q", got)
	}
	proxy := httptest.NewServer(s.Handler())
	defer proxy.Close()
	resp := postToServer(t, proxy.Client(), proxy.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	var result struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(decodeRPCResponse(t, resp).Result, &result); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if !strings.Contains(result.Instructions, "Tandem docs index") {
		t.Fatalf("instructions = %q, want the guardfile's own text", result.Instructions)
	}
}

// withheldUpstreamSpec grants one read-only tool the hinted upstream serves and
// withholds the mutating one beside it, which is the whole hand-authoring case
// #119 named: the allowlist says what is served, and the stub says why the
// obvious neighbour is not.
func withheldUpstreamSpec(url string) string {
	return `withhold "delete_thing" {
    reason "This surface is read-only by policy."
    alternative "get_thing"
}

mcp-upstream "test/things" {
    url "` + url + `"
    can "get_thing"
}
`
}

func TestUpstreamSpecReadsWithhold(t *testing.T) {
	spec, err := ParseUpstreamSpec("things.mcp.kdl", []byte(withheldUpstreamSpec("https://e.invalid/mcp")))
	if err != nil {
		t.Fatalf("ParseUpstreamSpec: %v", err)
	}
	if got := spec.WithheldTools(); len(got) != 1 || got[0] != "delete_thing" {
		t.Fatalf("WithheldTools = %v", got)
	}
	// Carried on the options the serving path takes, not just parsed and
	// dropped on the floor.
	if len(spec.Options().Withheld) != 1 {
		t.Fatalf("Options().Withheld = %+v", spec.Options().Withheld)
	}
}

// The stub reaches the served surface a client sees, refuses every call, and
// stays out of the allowlist that the read-only checks screen.
func TestProxyServesWithheldStub(t *testing.T) {
	ts := hintedUpstream(t)
	defer ts.Close()
	spec, err := ParseUpstreamSpec("things.mcp.kdl", []byte(withheldUpstreamSpec(ts.URL+"/mcp")))
	if err != nil {
		t.Fatalf("ParseUpstreamSpec: %v", err)
	}
	s, err := NewProxyWithOptions(context.Background(), "things", "things.mcp.kdl", spec.URL, spec.Tools, spec.Options())
	if err != nil {
		t.Fatalf("NewProxyWithOptions: %v", err)
	}
	defer func() { _ = s.Close() }()
	if got := strings.Join(s.ToolNames(), ","); got != "get_thing,mcp_beaver_info,delete_thing" {
		t.Fatalf("ToolNames = %q, want the grant, the info tool, then the stub", got)
	}
	if got := s.WithheldTools(); len(got) != 1 || got[0] != "delete_thing" {
		t.Fatalf("WithheldTools = %v", got)
	}
	// The upstream annotates delete_thing readOnlyHint:false, so a stub that
	// borrowed the upstream contract would fail `--read-only strict` here. It
	// mints its own instead, and reaches no upstream to borrow from.
	if got := s.NotReadOnly(); len(got) != 0 {
		t.Fatalf("NotReadOnly = %v, want the stub spared", got)
	}

	proxy := httptest.NewServer(s.Handler())
	defer proxy.Close()
	call := postToServer(t, proxy.Client(), proxy.URL+"/mcp", "",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_thing","arguments":{}}}`)
	var result map[string]any
	if err := json.Unmarshal(decodeRPCResponse(t, call).Result, &result); err != nil {
		t.Fatalf("call result: %v", err)
	}
	if result["isError"] != true {
		t.Fatalf("isError = %v, want the stub to refuse", result["isError"])
	}
	structured, _ := result["structuredContent"].(map[string]any)
	if structured["error"] != withheldErrorCode || structured["alternative"] != "get_thing" {
		t.Fatalf("structured = %v", structured)
	}
}

// controlledUpstreamSpec states every sibling #6859 projected, so one parse
// covers the whole set rather than five near-identical fixtures.
func controlledUpstreamSpec(url string) string {
	return `icon "data:image/png;base64,aGVsbG8="

server-info name="things_info"

confirm "delete_thing" message="This deletes the thing upstream. Continue?"

pin "get_thing" {
    argument "scope" literal "sirens-deep"
}

rate-limit "10/1s"

mcp-upstream "test/things" {
    url "` + url + `"
    can "get_thing"
    can "delete_thing"
}
`
}

func TestParseUpstreamSpecReadsTheProjectedControls(t *testing.T) {
	spec, err := ParseUpstreamSpec("things.mcp.kdl", []byte(controlledUpstreamSpec("https://e.invalid/mcp")))
	if err != nil {
		t.Fatalf("ParseUpstreamSpec: %v", err)
	}
	opts := spec.Options()
	if len(opts.Icons) != 1 || opts.Icons[0].MIMEType != "image/png" {
		t.Errorf("Icons = %+v, want the data URI's own mime", opts.Icons)
	}
	if spec.ServerInfoTool() != "things_info" {
		t.Errorf("ServerInfoTool = %q, want the renamed tool", spec.ServerInfoTool())
	}
	if opts.Confirmations["delete_thing"] == "" {
		t.Errorf("Confirmations = %v, want the stated message", opts.Confirmations)
	}
	if len(opts.Pins) != 1 || opts.Pins[0].Arg != "scope" || opts.Pins[0].Provider != literalProvider {
		t.Errorf("Pins = %+v, want one literal pin on scope", opts.Pins)
	}
	if opts.RateLimit == nil || opts.RateLimit.count != 10 {
		t.Errorf("RateLimit = %+v, want the stated rate", opts.RateLimit)
	}
}

// The whole point of the node: a value the deployment holds rather than the
// tracked guardfile, resolved at call time like `auth`.
func TestProxyAppliesAGuardfilePinFromEnv(t *testing.T) {
	t.Setenv("THINGS_SCOPE", "sirens-deep")
	ts := scopedUpstream(t)
	defer ts.Close()
	src := `pin "echo_scope" {
    argument "scope" env "THINGS_SCOPE"
}

mcp-upstream "test/things" {
    url "` + ts.URL + `/mcp"
    can "echo_scope"
}
`
	s := proxyFromSpec(t, "things", src)
	defer func() { _ = s.Close() }()
	proxy := httptest.NewServer(s.Handler())
	defer proxy.Close()

	body := decodeAPIResult(t, postAPI(t, proxy.Client(), proxy.URL+"/api/echo_scope", "application/json", `{}`))
	if got := firstText(t, body); got != "scope=sirens-deep" {
		t.Errorf("upstream saw %q, want the env-resolved pin applied", got)
	}

	// Refused rather than corrected, and the refusal never echoes the pinned
	// value: an env-sourced pin is where a secret would leak.
	refused := decodeAPIResult(t, postAPI(t, proxy.Client(), proxy.URL+"/api/echo_scope", "application/json", `{"scope":"kube-system"}`))
	if refused["isError"] != true {
		t.Fatalf("body = %v, want the override refused", refused)
	}
	text := firstText(t, refused)
	if !strings.Contains(text, "pinned") || !strings.Contains(text, "kube-system") {
		t.Errorf("error = %q, want it to name the pin and the rejected value", text)
	}
	if strings.Contains(text, "sirens-deep") {
		t.Errorf("error = %q, want the resolved pin kept out of the message", text)
	}
}

// A confirmed proxy tool reaches the upstream only after a human accepts, and
// a decline reaches it not at all. The gate is the whole reason `confirm` had
// to project: an agent calling a destructive proxied verb unattended is the
// case the node exists for.
func TestProxyConfirmsAGuardfileTool(t *testing.T) {
	var hits int
	ts := newUpstreamServer(t, upstreamTool(t, "delete_thing", "delete a thing", `{"type":"object","properties":{}}`,
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			hits++
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deleted"}}}, nil, nil
		}))
	defer ts.Close()
	s := proxyFromSpec(t, "things", `confirm "delete_thing" message="This deletes the thing upstream. Continue?"

mcp-upstream "test/things" {
    url "`+ts.URL+`/mcp"
    can "delete_thing"
}
`)
	defer func() { _ = s.Close() }()
	proxy := httptest.NewServer(s.Handler())
	defer proxy.Close()

	var prompted string
	answer := "decline"
	session := connectSDKClient(t, proxy, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		prompted = req.Params.Message
		return &mcp.ElicitResult{Action: answer}, nil
	})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "delete_thing"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("result = %+v, want a declined confirmation refused", res)
	}
	if !strings.Contains(prompted, "deletes the thing") {
		t.Errorf("prompt = %q, want the guardfile's own message", prompted)
	}
	if hits != 0 {
		t.Fatalf("upstream hits = %d, want a declined call to reach it not at all", hits)
	}

	answer = "accept"
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "delete_thing"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if hits != 1 {
		t.Fatalf("upstream hits = %d, want the accepted call through exactly once", hits)
	}
}

// The initialize response carries the brand mark, which is the whole reason
// the node exists: a connector tile renders it or shows a placeholder.
func TestProxyServesGuardfileIcons(t *testing.T) {
	ts := hintedUpstream(t)
	defer ts.Close()
	src := `icon "data:image/png;base64,aGVsbG8="

mcp-upstream "test/things" {
    url "` + ts.URL + `/mcp"
    can "get_thing"
}
`
	s := proxyFromSpec(t, "things", src)
	defer func() { _ = s.Close() }()
	proxy := httptest.NewServer(s.Handler())
	defer proxy.Close()

	resp := postToServer(t, proxy.Client(), proxy.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	var result struct {
		ServerInfo struct {
			Icons []mcp.Icon `json:"icons"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(decodeRPCResponse(t, resp).Result, &result); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if len(result.ServerInfo.Icons) != 1 || result.ServerInfo.Icons[0].MIMEType != "image/png" {
		t.Fatalf("icons = %+v, want the guardfile's own", result.ServerInfo.Icons)
	}
}

// On by default, renameable, and disableable - the same three states spec mode
// has, so absence of the info tool carries one meaning across both modes.
func TestProxyServerInfoStates(t *testing.T) {
	ts := hintedUpstream(t)
	defer ts.Close()
	upstream := `

mcp-upstream "test/things" {
    url "` + ts.URL + `/mcp"
    can "get_thing"
}
`
	for name, tc := range map[string]struct{ node, want string }{
		"default":  {"", "get_thing,mcp_beaver_info"},
		"renamed":  {`server-info name="things_info"`, "get_thing,things_info"},
		"disabled": {"server-info disabled", "get_thing"},
	} {
		t.Run(name, func(t *testing.T) {
			s := proxyFromSpec(t, "things", tc.node+upstream)
			defer func() { _ = s.Close() }()
			if got := strings.Join(s.ToolNames(), ","); got != tc.want {
				t.Fatalf("ToolNames = %q, want %q", got, tc.want)
			}
		})
	}
}

// The info tool reports the proxy's own shape and reaches no upstream, so it
// answers on a surface whose upstream is unreachable.
func TestProxyServerInfoReportsUpstreamMode(t *testing.T) {
	ts := hintedUpstream(t)
	defer ts.Close()
	s := proxyFromSpec(t, "things", `mcp-upstream "test/things" {
    url "`+ts.URL+`/mcp"
    can "get_thing"
}
`)
	defer func() { _ = s.Close() }()
	proxy := httptest.NewServer(s.Handler())
	defer proxy.Close()

	body := decodeAPIResult(t, postAPI(t, proxy.Client(), proxy.URL+"/api/mcp_beaver_info", "application/json", `{}`))
	structured, _ := body["structuredContent"].(map[string]any)
	if structured["mode"] != "upstream" || structured["server"] != "things" {
		t.Fatalf("payload = %v, want the proxy's own identity", structured)
	}
}

// proxyFromSpec parses a guardfile and connects the proxy it states, which is
// the pair every control test below runs.
func proxyFromSpec(t *testing.T, name, src string) *Server {
	t.Helper()
	spec, err := ParseUpstreamSpec(name+".mcp.kdl", []byte(src))
	if err != nil {
		t.Fatalf("ParseUpstreamSpec: %v", err)
	}
	s, err := NewProxyWithOptions(context.Background(), name, name+".mcp.kdl", spec.URL, spec.Tools, spec.Options())
	if err != nil {
		t.Fatalf("NewProxyWithOptions: %v", err)
	}
	return s
}

// scopedUpstream echoes the scope argument back, so a pin is visible in the
// answer rather than only in the request the test cannot see.
func scopedUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := upstreamTool(t, "echo_scope", "echo the scope", `{
		"type":"object",
		"properties":{"scope":{"type":"string"}}
	}`, func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		scope, _ := args["scope"].(string)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "scope=" + scope}}}, nil, nil
	})
	return newUpstreamServer(t, srv)
}

// The bucket is what a public-good upstream publishes a limit for, and the
// proxy is the shape most likely to sit in front of one. It charges proxied
// calls and spares the info tool, which reaches no upstream and doubles as the
// fleet's liveness probe.
func TestProxyRateLimitsProxiedCallsOnly(t *testing.T) {
	var mu sync.Mutex
	var arrivals []time.Time
	ts := newUpstreamServer(t, upstreamTool(t, "get_thing", "get a thing", `{"type":"object","properties":{}}`,
		func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			mu.Lock()
			arrivals = append(arrivals, time.Now())
			mu.Unlock()
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		}))
	defer ts.Close()
	s := proxyFromSpec(t, "things", `rate-limit "1/200ms"

mcp-upstream "test/things" {
    url "`+ts.URL+`/mcp"
    can "get_thing"
}
`)
	defer func() { _ = s.Close() }()
	proxy := httptest.NewServer(s.Handler())
	defer proxy.Close()

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postAPI(t, proxy.Client(), proxy.URL+"/api/get_thing", "application/json", `{}`)
		}()
	}
	wg.Wait()

	mu.Lock()
	spread := arrivals[len(arrivals)-1].Sub(arrivals[0])
	hits := len(arrivals)
	mu.Unlock()
	if hits != 3 {
		t.Fatalf("upstream saw %d requests, want 3", hits)
	}
	// Burst of 1 spaces the second and third by the window. Generous slack for
	// scheduling; an unbucketed proxy would be near-zero.
	if spread < 300*time.Millisecond {
		t.Errorf("three calls spread over %s, want them serialised by the 200ms bucket", spread)
	}

	start := time.Now()
	for range 3 {
		postAPI(t, proxy.Client(), proxy.URL+"/api/mcp_beaver_info", "application/json", `{}`)
	}
	if waited := time.Since(start); waited > 150*time.Millisecond {
		t.Errorf("three info calls took %s, want the liveness probe outside the upstream's bucket", waited)
	}
}

// The pin sits outside the confirmation gate, so a call contradicting it is
// refused before a human is asked to approve what was going to be refused
// anyway - the ordering spec mode already uses for its argument refusals.
func TestProxyPinRefusalPrecedesTheConfirmation(t *testing.T) {
	var hits int
	ts := newUpstreamServer(t, upstreamTool(t, "echo_scope", "echo the scope", `{
		"type":"object",
		"properties":{"scope":{"type":"string"}}
	}`, func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		hits++
		scope, _ := args["scope"].(string)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "scope=" + scope}}}, nil, nil
	}))
	defer ts.Close()
	s := proxyFromSpec(t, "things", `confirm "echo_scope" message="Continue?"

pin "echo_scope" {
    argument "scope" literal "sirens-deep"
}

mcp-upstream "test/things" {
    url "`+ts.URL+`/mcp"
    can "echo_scope"
}
`)
	defer func() { _ = s.Close() }()
	proxy := httptest.NewServer(s.Handler())
	defer proxy.Close()

	var prompts int
	session := connectSDKClient(t, proxy, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		prompts++
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "echo_scope",
		Arguments: map[string]any{"scope": "kube-system"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("result = %+v, want the contradicting call refused", res)
	}
	if prompts != 0 || hits != 0 {
		t.Fatalf("prompts = %d, upstream hits = %d, want neither spent on a refused call", prompts, hits)
	}
}
