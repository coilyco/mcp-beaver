package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/coilyco/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sqlPayload runs one sql-shaped response through the envelope and decodes it,
// keeping the two `truncated` statements side by side.
func sqlPayload(t *testing.T, desc opcore.Descriptor, decoded map[string]any) (coverage, map[string]any) {
	t.Helper()
	res := toolSuccess(opcore.Response{Decoded: decoded, Status: "OK"}, desc)
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("unexpected content %T", res.Content[0])
	}
	var payload struct {
		Coverage coverage       `json:"coverage"`
		Result   map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	return payload.Coverage, payload.Result
}

func sqlDescriptor() opcore.Descriptor {
	return opcore.Descriptor{Leaf: "list", Group: "orders", SQL: &opcore.SQL{MaxRows: 2}}
}

// The defect #89 records: coverage led with truncated:false while the payload
// ended with truncated:true, and a slicing consumer keeps the false one.
func TestBoundedSQLReadReportsTruncationInCoverage(t *testing.T) {
	cov, result := sqlPayload(t, sqlDescriptor(), map[string]any{
		"rows":      []any{map[string]any{"id": 1}, map[string]any{"id": 2}},
		"truncated": true,
		"columns":   []any{"id"},
	})
	if !cov.Truncated {
		t.Fatalf("coverage.truncated = false on a bounded read: %+v", cov)
	}
	// The property that actually matters: the durable statement and the
	// discardable one cannot disagree.
	if tail, _ := result["truncated"].(bool); tail != cov.Truncated {
		t.Fatalf("coverage.truncated = %v but result.truncated = %v", cov.Truncated, tail)
	}
}

func TestUnboundedSQLReadReportsNoTruncation(t *testing.T) {
	cov, result := sqlPayload(t, sqlDescriptor(), map[string]any{
		"rows":      []any{map[string]any{"id": 1}},
		"truncated": false,
		"columns":   []any{"id"},
	})
	if cov.Truncated {
		t.Fatalf("coverage.truncated = true on an unbounded read: %+v", cov)
	}
	if tail, _ := result["truncated"].(bool); tail != cov.Truncated {
		t.Fatalf("coverage.truncated = %v but result.truncated = %v", cov.Truncated, tail)
	}
}

// A non-sql grant keeps the standing explicit false claim, even when the
// upstream's own payload happens to carry a field called `truncated`.
func TestNonSQLGrantKeepsTheStandingFalseClaim(t *testing.T) {
	cov, _ := sqlPayload(t, opcore.Descriptor{Leaf: "get", Group: "thing"}, map[string]any{
		"rows":      []any{map[string]any{"id": 1}},
		"truncated": true,
	})
	if cov.Truncated {
		t.Fatalf("an upstream's own `truncated` field reached coverage: %+v", cov)
	}
}
