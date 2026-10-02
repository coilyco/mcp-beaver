package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coilyco/umbra/http/opcore"
)

// buildPDF writes a minimal, valid, uncompressed PDF carrying one line of text
// per page.
//
// Built here rather than committed as a binary fixture: the cross-reference
// offsets have to be computed from the bytes, so a generator is both shorter
// than the file it replaces and readable by whoever debugs it next.
func buildPDF(lines ...string) []byte {
	var buf bytes.Buffer
	offsets := []int{}
	object := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	buf.WriteString("%PDF-1.4\n")
	// 1 catalog, 2 pages, 3 font, then one page object and one content stream
	// per line.
	firstPage := 4
	kids := make([]string, 0, len(lines))
	for i := range lines {
		kids = append(kids, fmt.Sprintf("%d 0 R", firstPage+i*2))
	}
	object("<< /Type /Catalog /Pages 2 0 R >>")
	object(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(lines)))
	object("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, line := range lines {
		object(fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>",
			firstPage+i*2+1))
		stream := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (%s) Tj ET", line)
		object(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes()
}

func pdfSpec(baseURL, siblings string) string {
	return siblings + `
wrap ward mcp docs {
    base-url "` + baseURL + `"
    auth bearer { value literal "unused" }
    can get report {
        path "/report.pdf"
        raw-response
    }
}`
}

func servePDF(t *testing.T, body []byte, siblings string) map[string]any {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	s, err := New("docs", "docs.mcp.kdl", []byte(pdfSpec(upstream.URL, siblings)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	return callTool(t, ts, initResp.Header.Get("Mcp-Session-Id"), "2", "get_report", `{}`)
}

// The ask: a PDF reachable from a granted upstream becomes text an agent can
// read. Without this it returns bytes no model can do anything with, which is
// what made PDF-only sources invisible to the whole fleet.
func TestExtractTurnsAPDFIntoText(t *testing.T) {
	result := servePDF(t, buildPDF("Annual emissions summary", "Methodology notes"), `extract "get_report" as="pdf-text"`)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("extraction reported isError; content=%v", result["content"])
	}
	var payload struct {
		Coverage coverage `json:"coverage"`
		Result   string   `json:"result"`
	}
	if err := json.Unmarshal([]byte(firstText(t, result)), &payload); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	for _, want := range []string{"Annual emissions summary", "Methodology notes"} {
		if !strings.Contains(payload.Result, want) {
			t.Errorf("extracted text missing %q: %q", want, payload.Result)
		}
	}
	if payload.Coverage.Pages == nil {
		t.Fatalf("no page coverage: %+v", payload.Coverage)
	}
	if got := *payload.Coverage.Pages; got.Shown != 2 || got.Total != 2 {
		t.Errorf("pages = %+v, want 2 of 2", got)
	}
}

// A large document cannot return unbounded text, and the model has to be able
// to tell that it is reading a prefix - a bounded read that looks complete is
// the failure #68 is about, arriving through a different door.
func TestExtractBoundsPagesAndSaysSo(t *testing.T) {
	lines := make([]string, 0, 12)
	for i := range 12 {
		lines = append(lines, fmt.Sprintf("Section %d body", i+1))
	}
	result := servePDF(t, buildPDF(lines...), `extract "get_report" as="pdf-text" max-pages="3"`)
	var payload struct {
		Coverage coverage `json:"coverage"`
		Result   string   `json:"result"`
	}
	if err := json.Unmarshal([]byte(firstText(t, result)), &payload); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if payload.Coverage.Pages == nil {
		t.Fatalf("no page coverage: %+v", payload.Coverage)
	}
	if got := *payload.Coverage.Pages; got.Shown != 3 || got.Total != 12 {
		t.Fatalf("pages = %+v, want 3 of 12", got)
	}
	if strings.Contains(payload.Result, "Section 4 body") {
		t.Errorf("text ran past the page bound: %q", payload.Result)
	}
	if !strings.Contains(payload.Result, "Section 3 body") {
		t.Errorf("text stopped short of the page bound: %q", payload.Result)
	}
}

// A malformed document is a clean tool error rather than a crash or a hang.
// rsc.io/pdf panics on structures it does not handle, and a served pod must
// not die of a document someone linked.
func TestExtractFailsCleanlyOnAMalformedPDF(t *testing.T) {
	result := servePDF(t, []byte("%PDF-1.4\nthis is not a pdf at all\n%%EOF\n"), `extract "get_report" as="pdf-text"`)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("a malformed PDF returned success: %v", result)
	}
	if got := firstText(t, result); !strings.Contains(got, "PDF") {
		t.Errorf("error = %q, want it to name the PDF", got)
	}
}

func TestExtractFailsClosedAtBuild(t *testing.T) {
	for name, tc := range map[string]struct {
		siblings string
		want     string
	}{
		"unknown tool":   {`extract "no_such_tool" as="pdf-text"`, "not a grant-backed tool"},
		"missing as":     {`extract "get_report"`, "needs `as="},
		"unknown kind":   {`extract "get_report" as="ocr"`, "needs `as="},
		"items on a pdf": {`extract "get_report" as="pdf-text" max-items="5"`, "bounds pages rather than items"},
		"bad max-pages":  {`extract "get_report" as="pdf-text" max-pages="lots"`, "must be a whole number"},
		"zero max-pages": {`extract "get_report" as="pdf-text" max-pages="0"`, "must be between 1 and 200"},
		"over ceiling":   {`extract "get_report" as="pdf-text" max-pages="500"`, "must be between 1 and 200"},
		"unknown prop":   {`extract "get_report" as="pdf-text" pages="3"`, `unknown ` + "`extract`" + ` property "pages"`},
		"duplicate":      {"extract \"get_report\" as=\"pdf-text\"\nextract \"get_report\" as=\"pdf-text\"", "duplicate `extract`"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New("docs", "docs.mcp.kdl", []byte(pdfSpec("http://127.0.0.1:1", tc.siblings)))
			if err == nil {
				t.Fatalf("New accepted %q", tc.siblings)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// Without `raw-response` opcore decodes the body as JSON and the call fails on
// the first byte of `%PDF`, long before extraction runs. Catching that at
// build is the difference between a spec that cannot work and a spec that
// fails only when something calls it.
func TestExtractRequiresRawResponse(t *testing.T) {
	spec := `extract "get_report" as="pdf-text"
wrap ward mcp docs {
    base-url "http://127.0.0.1:1"
    auth bearer { value literal "unused" }
    can get report {
        path "/report.pdf"
    }
}`
	_, err := New("docs", "docs.mcp.kdl", []byte(spec))
	if err == nil {
		t.Fatalf("New accepted an extract without raw-response")
	}
	if !strings.Contains(err.Error(), "raw-response") {
		t.Fatalf("error = %q, want it to name raw-response", err)
	}
}

// A cancelled request returns a stated timeout rather than holding the handler
// open. #49 recorded a 180s hang inside two healthy pods, and a slow parse is
// exactly that shape.
func TestExtractHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := extractPDFText(ctx, buildPDF("anything"), 5)
	if err == nil {
		t.Fatalf("extraction ignored a cancelled context")
	}
	if !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("error = %q, want it to state the timeout", err)
	}
}

// The size gate sits before the parser, so an oversize document is refused
// without being opened at all. Exercised directly rather than over the wire:
// serving 32MB through httptest would test the loopback, not the gate.
func TestExtractRefusesAnOversizeDocument(t *testing.T) {
	oversize := opcore.Response{Raw: make([]byte, maxPDFBytes+1)}
	result := pdfToolSuccess(context.Background(), oversize, &extractSpec{kind: kindPDFText, maxPages: 5})
	if !result.IsError {
		t.Fatalf("an oversize document was accepted")
	}
}
