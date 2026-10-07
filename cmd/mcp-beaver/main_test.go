package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coilyco/mcp-beaver/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLintPrintsProjectedToolNames(t *testing.T) {
	var out bytes.Buffer
	if err := runLint(&out, []string{filepath.Join("testdata", "valid.mcp.kdl")}); err != nil {
		t.Fatalf("runLint: %v", err)
	}
	// Sorted, one per line: the list a consumer diffs against a reviewed
	// expectation instead of reimplementing the parse to derive it. The info
	// tool is on by default and counts itself, so lint and tools/list agree.
	if got, want := out.String(), "get_issue\nlist_issue\nmcp_beaver_info\n"; got != want {
		t.Fatalf("runLint stdout = %q, want %q", got, want)
	}
}

// #55: an unknown verb still mints a tool, so lint has to say which tools got
// their method from the table and which fell through to POST. Warnings go to
// stderr precisely so this stays true - stdout is unchanged.
func TestLintWarnsOnVerbFallthrough(t *testing.T) {
	var out, warn bytes.Buffer
	if err := runLintTo(&out, &warn, []string{filepath.Join("testdata", "fallthrough-verb.mcp.kdl")}); err != nil {
		t.Fatalf("runLintTo: %v", err)
	}
	if got, want := out.String(), "close_issue\nmcp_beaver_info\ntransfer_issue\n"; got != want {
		t.Errorf("stdout = %q, want %q: a warning must not edit the diffable surface", got, want)
	}
	warning := warn.String()
	if !strings.Contains(warning, "transfer_issue") || !strings.Contains(warning, "fallthrough") {
		t.Errorf("stderr = %q, want it to name transfer_issue and the fallthrough", warning)
	}
	if !strings.Contains(warning, "POST") {
		t.Errorf("stderr = %q, want it to name the method the verb resolved to", warning)
	}
	if strings.Contains(warning, "close_issue") {
		t.Errorf("stderr = %q, want no warning for a verb with an explicit table entry", warning)
	}
}

// The control: a spec whose verbs are all in the table warns about nothing.
func TestLintSilentWhenEveryVerbIsMapped(t *testing.T) {
	var out, warn bytes.Buffer
	if err := runLintTo(&out, &warn, []string{filepath.Join("testdata", "valid.mcp.kdl")}); err != nil {
		t.Fatalf("runLintTo: %v", err)
	}
	if warn.Len() != 0 {
		t.Errorf("stderr = %q, want silence for get and list", warn.String())
	}
}

// --methods is the other half: the resolved method was invisible from every
// surface this project exposed, including the MCP tool schema.
func TestLintMethodsColumn(t *testing.T) {
	var out, warn bytes.Buffer
	if err := runLintTo(&out, &warn, []string{"--methods", filepath.Join("testdata", "fallthrough-verb.mcp.kdl")}); err != nil {
		t.Fatalf("runLintTo: %v", err)
	}
	// close is the case #55 was filed over: reopen and close share an endpoint
	// and a method, and only one of them was in the table at the time.
	want := "close_issue\tPATCH\nmcp_beaver_info\t-\ntransfer_issue\tPOST\n"
	if got := out.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// reopen was the verb that prompted #55: it fell through to POST while Forgejo
// wants the same PATCH close already used. It is in the table now, and this
// pins that so the regression cannot return silently.
func TestReopenResolvesToPatch(t *testing.T) {
	spec := `wrap ward mcp reopen-fixture {
    base-url "example.invalid/api/v1"
    auth bearer { value env "LINT_FIXTURE_TOKEN" }
    can reopen issue {
        path "/repos/{owner}/{repo}/issues/{index}"
        set state="open"
    }
}`
	srv, err := mcpserver.New("reopen-fixture", "reopen.mcp.kdl", []byte(spec))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var found bool
	for _, m := range srv.ToolMethods() {
		if m.Tool != "reopen_issue" {
			continue
		}
		found = true
		if m.Method != "PATCH" {
			t.Errorf("reopen_issue method = %s, want PATCH", m.Method)
		}
		if m.Fallthrough {
			t.Error("reopen resolved by fallthrough, want an explicit table entry")
		}
	}
	if !found {
		t.Fatal("reopen_issue not minted")
	}
}

func TestLintRejectsMalformedSpec(t *testing.T) {
	var out bytes.Buffer
	err := runLint(&out, []string{filepath.Join("testdata", "malformed.mcp.kdl")})
	if err == nil {
		t.Fatal("runLint accepted a malformed spec")
	}
	if !strings.Contains(err.Error(), "malformed.mcp.kdl") || !strings.Contains(err.Error(), "parse KDL") {
		t.Fatalf("runLint error = %q, want the spec path and the parse failure", err)
	}
	if out.Len() != 0 {
		t.Fatalf("runLint wrote %q to stdout for a rejected spec", out.String())
	}
}

// A spec can be well-formed KDL and still mint a broken surface. Linting
// through mcpserver.New rather than opcore.ParseInline is what catches it.
func TestLintRejectsDuplicateToolProjection(t *testing.T) {
	var out bytes.Buffer
	err := runLint(&out, []string{filepath.Join("testdata", "duplicate-tool.mcp.kdl")})
	if err == nil {
		t.Fatal("runLint accepted two grants projecting one tool name")
	}
	if !strings.Contains(err.Error(), `duplicate tool name "get_issue"`) {
		t.Fatalf("runLint error = %q, want the duplicate tool name", err)
	}
	if out.Len() != 0 {
		t.Fatalf("runLint wrote %q to stdout for a rejected spec", out.String())
	}
}

func TestLintRejectsUnreadableSpec(t *testing.T) {
	var out bytes.Buffer
	err := runLint(&out, []string{filepath.Join("testdata", "does-not-exist.mcp.kdl")})
	if err == nil {
		t.Fatal("runLint accepted a missing spec path")
	}
	if !strings.Contains(err.Error(), "read spec") {
		t.Fatalf("runLint error = %q, want a read failure", err)
	}
}

func TestLintNeedsExactlyOneSpecPath(t *testing.T) {
	for name, argv := range map[string][]string{
		"none": {},
		"two":  {filepath.Join("testdata", "valid.mcp.kdl"), filepath.Join("testdata", "valid.mcp.kdl")},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runLint(&out, argv); err == nil {
				t.Fatal("runLint accepted the wrong number of spec paths")
			}
		})
	}
}

// The shipped examples are real configuration, so they are validated through
// this surface rather than restated by a second parser. Only the exit status
// is asserted: pinning the exact tool names here would be the antipattern the
// command exists to remove. examples/ holds serve-mode guardfiles; an SSM
// policy uses a different grammar and is not lintable through this path.
func TestLintAcceptsShippedExamples(t *testing.T) {
	specs, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.mcp.kdl"))
	if err != nil {
		t.Fatalf("glob examples: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("found no example specs to lint")
	}
	for _, spec := range specs {
		t.Run(filepath.Base(spec), func(t *testing.T) {
			var out bytes.Buffer
			if err := runLint(&out, []string{spec}); err != nil {
				t.Fatalf("runLint: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("runLint reported no tools for a shipped example")
			}
		})
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	err := run([]string{"validate"})
	if err == nil {
		t.Fatal("run accepted an unknown command")
	}
	if !strings.Contains(err.Error(), "lint") {
		t.Fatalf("run error = %q, want lint listed among the commands", err)
	}
}

func TestServeHTTPShutsDownOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serveHTTP(ctx, "127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err != nil {
		t.Fatalf("serveHTTP: %v", err)
	}
}

// A connection outliving the shutdown timeout is warned about and cut, never
// returned: returned, it ended the process as a crash (MCP-BEAVER-1).
func TestShutdownHTTPDeadlineIsAWarningNotAnError(t *testing.T) {
	var logs strings.Builder
	mcpserver.SetLogOutput(&logs, "debug")
	t.Cleanup(func() { mcpserver.SetLogOutput(io.Discard, "error") })

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	go func() {
		if resp, err := http.Get("http://" + listener.Addr().String()); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started

	if err := shutdownHTTP(server, 50*time.Millisecond); err != nil {
		t.Fatalf("shutdownHTTP = %v, want nil so the process does not report a crash", err)
	}
	if out := logs.String(); !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "HTTP shutdown deadline reached") {
		t.Errorf("logs = %s, want the deadline at WARN", out)
	}
}

type stubTelemetryRuntime struct{ err error }

func (s stubTelemetryRuntime) Shutdown(context.Context) error { return s.err }

// An OTel flush that misses the shutdown deadline is warned about and dropped:
// returned, withTelemetry hands it to ReportCrash. Any other flush error still
// returns, including one joined with a deadline from another provider.
func TestShutdownTelemetryDeadlineIsAWarningNotAnError(t *testing.T) {
	var logs strings.Builder
	mcpserver.SetLogOutput(&logs, "debug")
	t.Cleanup(func() { mcpserver.SetLogOutput(io.Discard, "error") })

	deadline := fmt.Errorf("traces: %w", context.DeadlineExceeded)
	if err := shutdownTelemetry(stubTelemetryRuntime{err: errors.Join(deadline, deadline)}, time.Second); err != nil {
		t.Fatalf("shutdownTelemetry = %v, want nil so the process does not report a crash", err)
	}
	if out := logs.String(); !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "OpenTelemetry flush deadline reached") {
		t.Errorf("logs = %s, want the deadline at WARN", out)
	}

	other := errors.New("exporter refused")
	for name, in := range map[string]error{
		"alone":                other,
		"joined with deadline": errors.Join(deadline, other),
	} {
		err := shutdownTelemetry(stubTelemetryRuntime{err: in}, time.Second)
		if err == nil || !errors.Is(err, other) {
			t.Errorf("%s: shutdownTelemetry = %v, want the exporter error returned", name, err)
		}
		if err != nil && errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: shutdownTelemetry = %v, want the deadline dropped from what is returned", name, err)
		}
	}

	if err := shutdownTelemetry(stubTelemetryRuntime{}, time.Second); err != nil {
		t.Errorf("clean flush = %v, want nil", err)
	}
}

func TestConnectProxyWithRetryRecovers(t *testing.T) {
	attempts := 0
	srv, err := connectProxyWithRetry(context.Background(), time.Second, time.Millisecond, func(context.Context) (*mcpserver.Server, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("upstream is warming")
		}
		return &mcpserver.Server{}, nil
	})
	if err != nil {
		t.Fatalf("connectProxyWithRetry: %v", err)
	}
	if srv == nil {
		t.Fatal("connectProxyWithRetry returned a nil server")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestConnectProxyWithRetryTimesOut(t *testing.T) {
	_, err := connectProxyWithRetry(context.Background(), 20*time.Millisecond, time.Millisecond, func(context.Context) (*mcpserver.Server, error) {
		return nil, errors.New("upstream unavailable")
	})
	if err == nil {
		t.Fatal("connectProxyWithRetry succeeded, want timeout")
	}
	if !strings.Contains(err.Error(), "timed out after") || !strings.Contains(err.Error(), "upstream unavailable") {
		t.Fatalf("connectProxyWithRetry error = %q", err)
	}
}

func TestLintUpstreamPrintsSortedAllowlist(t *testing.T) {
	var out bytes.Buffer
	argv := []string{"--tool", "signoz_list_metrics", "--tool", "signoz_get_alert"}
	if err := runLintUpstream(context.Background(), &out, argv); err != nil {
		t.Fatalf("runLintUpstream: %v", err)
	}
	// Same contract as `lint`: the reviewed surface, sorted, one per line.
	if got, want := out.String(), "signoz_get_alert\nsignoz_list_metrics\n"; got != want {
		t.Fatalf("runLintUpstream stdout = %q, want %q", got, want)
	}
}

func TestLintUpstreamRejectsMalformedAllowlist(t *testing.T) {
	for name, argv := range map[string][]string{
		"no tools":  {},
		"duplicate": {"--tool", "a", "--tool", "a"},
		"empty":     {"--tool", "a", "--tool", "  "},
		"stray arg": {"--tool", "a", "values.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runLintUpstream(context.Background(), &out, argv); err == nil {
				t.Fatal("runLintUpstream accepted a malformed allowlist")
			}
			if out.Len() != 0 {
				t.Fatalf("runLintUpstream wrote %q to stdout for a rejected allowlist", out.String())
			}
		})
	}
}

// The offline gate the issue asked for: a mutation tool entering a read-only
// allowlist fails review without any upstream connection.
func TestLintUpstreamHeuristicRejectsMutationNames(t *testing.T) {
	var out bytes.Buffer
	argv := []string{
		"--read-only", "heuristic",
		"--tool", "signoz_list_metrics",
		"--tool", "signoz_create_dashboard",
	}
	err := runLintUpstream(context.Background(), &out, argv)
	if err == nil {
		t.Fatal("runLintUpstream accepted a mutation tool in a read-only allowlist")
	}
	if !strings.Contains(err.Error(), "signoz_create_dashboard") {
		t.Fatalf("error = %q, want it to name the offending tool", err)
	}
	if !strings.Contains(err.Error(), "--read-only strict") {
		t.Fatalf("error = %q, want it to point at the authoritative check", err)
	}
}

func TestLintUpstreamHeuristicAcceptsReadOnlyNames(t *testing.T) {
	var out bytes.Buffer
	argv := []string{
		"--read-only", "heuristic",
		"--tool", "signoz_list_metrics",
		"--tool", "signoz_get_field_values",
	}
	if err := runLintUpstream(context.Background(), &out, argv); err != nil {
		t.Fatalf("runLintUpstream rejected a read-only allowlist: %v", err)
	}
}

// strict is upstream truth, so without an upstream it is not a weaker check,
// it is no check. Failing beats silently downgrading to the heuristic.
func TestLintUpstreamStrictNeedsAnUpstream(t *testing.T) {
	var out bytes.Buffer
	err := runLintUpstream(context.Background(), &out, []string{"--read-only", "strict", "--tool", "a"})
	if err == nil {
		t.Fatal("runLintUpstream accepted --read-only strict without --upstream")
	}
	if !strings.Contains(err.Error(), "--upstream") {
		t.Fatalf("error = %q, want it to name the missing flag", err)
	}
}

func TestLintUpstreamRejectsUnknownReadOnlyMode(t *testing.T) {
	var out bytes.Buffer
	err := runLintUpstream(context.Background(), &out, []string{"--read-only", "yes", "--tool", "a"})
	if err == nil {
		t.Fatal("runLintUpstream accepted an unknown --read-only mode")
	}
	if !strings.Contains(err.Error(), "heuristic") {
		t.Fatalf("error = %q, want it to list the valid modes", err)
	}
}

// upstreamWithAnnotations serves two tools, only one annotated read-only, so
// the strict path has something real to reject.
func upstreamWithAnnotations(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.1.0"}, nil)
	for _, tc := range []struct {
		name     string
		readOnly bool
	}{{"fetch_report", true}, {"rotate_key", false}} {
		tool := &mcp.Tool{
			Name:        tc.name,
			Description: tc.name,
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}
		if tc.readOnly {
			tool.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
		}
		mcp.AddTool(srv, tool, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	return httptest.NewServer(mux)
}

// The authoritative check: `rotate_key` clears the name heuristic but the
// upstream never annotates it readOnlyHint, so strict rejects it.
func TestLintUpstreamStrictRejectsUnannotatedTool(t *testing.T) {
	ts := upstreamWithAnnotations(t)
	defer ts.Close()

	var out bytes.Buffer
	argv := []string{
		"--upstream", ts.URL + "/mcp",
		"--read-only", "strict",
		"--tool", "fetch_report",
		"--tool", "rotate_key",
	}
	err := runLintUpstream(context.Background(), &out, argv)
	if err == nil {
		t.Fatal("runLintUpstream accepted a tool the upstream never marked read-only")
	}
	if !strings.Contains(err.Error(), "rotate_key") {
		t.Fatalf("error = %q, want it to name the unannotated tool", err)
	}
	if strings.Contains(err.Error(), "fetch_report") {
		t.Fatalf("error = %q, want it to spare the annotated tool", err)
	}
}

func TestLintUpstreamStrictAcceptsAnnotatedAllowlist(t *testing.T) {
	ts := upstreamWithAnnotations(t)
	defer ts.Close()

	var out bytes.Buffer
	argv := []string{
		"--upstream", ts.URL + "/mcp",
		"--read-only", "strict",
		"--tool", "fetch_report",
	}
	if err := runLintUpstream(context.Background(), &out, argv); err != nil {
		t.Fatalf("runLintUpstream rejected an annotated read-only allowlist: %v", err)
	}
	if got, want := out.String(), "fetch_report\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

// Connecting also inherits the proxy's existence check, so a typo'd tool name
// fails against the upstream even without --read-only.
func TestLintUpstreamRejectsToolMissingUpstream(t *testing.T) {
	ts := upstreamWithAnnotations(t)
	defer ts.Close()

	var out bytes.Buffer
	argv := []string{"--upstream", ts.URL + "/mcp", "--tool", "fetch_reprot"}
	err := runLintUpstream(context.Background(), &out, argv)
	if err == nil {
		t.Fatal("runLintUpstream accepted a tool the upstream does not expose")
	}
	if !strings.Contains(err.Error(), "fetch_reprot") {
		t.Fatalf("error = %q, want it to name the missing tool", err)
	}
}

// A resource with no `audience` serves correctly and is pulled in by no host
// that gates on the annotation, which is how one landed correct and unread.
func TestLintWarnsOnAResourceWithNoAudience(t *testing.T) {
	var out, warn bytes.Buffer
	if err := runLintTo(&out, &warn, []string{
		filepath.Join("testdata", "resource-audience.mcp.kdl"),
	}); err != nil {
		t.Fatalf("runLintTo: %v", err)
	}
	if got, want := out.String(), "get_issue\nmcp_beaver_info\n"; got != want {
		t.Errorf("stdout = %q, want %q: a warning must not edit the diffable surface", got, want)
	}
	warning := warn.String()
	// `priority` alone builds the annotations block and answers nobody, which
	// is the state most likely to be mistaken for a configured resource.
	for _, name := range []string{"says-nothing", "ordered-only"} {
		if !strings.Contains(warning, name) {
			t.Errorf("stderr = %q, want it to name %q", warning, name)
		}
	}
	// Both exits, so the author is told how to make the silence deliberate
	// rather than only how to make the resource readable.
	for _, role := range []string{"assistant", "user"} {
		if !strings.Contains(warning, role) {
			t.Errorf("stderr = %q, want it to offer %q", warning, role)
		}
	}
}

// An author who stated an audience answered the question, whichever role they
// named. Warning there would train a reader to skip the one that matters.
func TestLintIsSilentOnAnyStatedAudience(t *testing.T) {
	var out, warn bytes.Buffer
	if err := runLintTo(&out, &warn, []string{
		filepath.Join("testdata", "resource-audience.mcp.kdl"),
	}); err != nil {
		t.Fatalf("runLintTo: %v", err)
	}
	for _, name := range []string{"for-the-model", "for-people"} {
		if strings.Contains(warn.String(), name) {
			t.Errorf("stderr = %q, want no warning for %q", warn.String(), name)
		}
	}
}
