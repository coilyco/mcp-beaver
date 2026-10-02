package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/coilyco/umbra/http/opcore"
	pdf "github.com/dslipak/pdf"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxPDFBytes bounds the document this runtime will open at all. Parsing is
// the one place a granted upstream hands this process an arbitrarily large,
// arbitrarily structured input, so the size gate comes before the parser
// rather than from it.
const maxPDFBytes = 32 << 20

// defaultExtractPages is the page bound when a guardfile states none. PDFs run
// to hundreds of pages routinely, and returning a whole document's text would
// blow any agent's context - so the bound is the default and the guardfile
// raises it, never the other way round.
const defaultExtractPages = 20

// maxExtractPages ceilings what a guardfile may raise the bound to. Past this
// the answer is a different tool rather than a bigger number.
const maxExtractPages = 200

// pdfToolSuccess replaces the PDF bytes with the text an agent can read, and
// reports pages shown of pages total in the coverage block so a bounded read
// cannot be mistaken for the whole document (mcp-beaver#68).
func pdfToolSuccess(ctx context.Context, resp opcore.Response, x *extractSpec) *mcp.CallToolResult {
	if len(resp.Raw) > maxPDFBytes {
		return toolError(fmt.Errorf("mcp-beaver: PDF is %d bytes, over the %d-byte extraction ceiling", len(resp.Raw), maxPDFBytes))
	}
	text, shown, total, err := extractPDFText(ctx, resp.Raw, x.maxPages)
	if err != nil {
		return toolError(err)
	}
	// Bytes measures the extracted text rather than the source document: it is
	// what the consumer carries, and it is what its cap applies to.
	payload := toolPayload{
		Coverage: coverage{
			Truncated:  false,
			Bytes:      len(text),
			OverBudget: len(text) > consumerBudgetBytes,
			Pages:      &pageCoverage{Shown: shown, Total: total},
		},
		Result: text,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return toolError(fmt.Errorf("serialize tool result: %w", err))
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: payload,
	}
}

// pdfOutcome is one parse attempt, carried off the parser goroutine.
type pdfOutcome struct {
	text         string
	shown, total int
	err          error
}

// extractPDFText reads up to maxPages pages and reports how many the document
// held.
//
// The parse runs on its own goroutine and the caller selects on ctx, so a
// document that wedges the parser returns a stated timeout rather than holding
// the request open - #49 recorded a 180s hang inside two healthy pods, and a
// slow parse is exactly that shape. The goroutine can outlive the call, which
// is the deliberate trade: an abandoned parse is bounded by maxPDFBytes and by
// the pod's memory limit, where a blocked handler is bounded by nothing.
//
// A panic inside the parser becomes an error. rsc.io/pdf is pure Go, so a
// malformed document cannot corrupt memory, but it can and does panic on
// structures it does not handle, and a served pod must not die of a document
// someone linked.
func extractPDFText(ctx context.Context, raw []byte, maxPages int) (text string, shown, total int, err error) {
	done := make(chan pdfOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- pdfOutcome{err: fmt.Errorf("mcp-beaver: PDF is malformed or unsupported: %v", r)}
			}
		}()
		done <- readPDFText(raw, maxPages)
	}()
	select {
	case <-ctx.Done():
		return "", 0, 0, fmt.Errorf("mcp-beaver: PDF extraction did not finish: %w", ctx.Err())
	case got := <-done:
		return got.text, got.shown, got.total, got.err
	}
}

// readPDFText walks pages in order, stopping at the page bound, and reports
// how many the document held so the caller can say shown-of-total.
//
// Fonts are cached across pages the way the fork's own whole-document reader
// does: a charmap is parsed per font, and a report reusing one font on forty
// pages would otherwise parse it forty times.
func readPDFText(raw []byte, maxPages int) (out pdfOutcome) {
	reader, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		// Encrypted documents land here too. Naming the document rather than
		// the library keeps the message actionable for whoever wrote the grant.
		out.err = fmt.Errorf("mcp-beaver: cannot read PDF: %w", err)
		return out
	}
	out.total = reader.NumPage()
	fonts := map[string]*pdf.Font{}
	var b strings.Builder
	for i := 1; i <= out.total && out.shown < maxPages; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		for _, name := range page.Fonts() {
			if _, seen := fonts[name]; !seen {
				font := page.Font(name)
				fonts[name] = &font
			}
		}
		text, err := page.GetPlainText(fonts)
		if err != nil {
			out.err = fmt.Errorf("mcp-beaver: cannot read PDF page %d: %w", i, err)
			return out
		}
		out.shown++
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(text))
	}
	out.text = b.String()
	return out
}
