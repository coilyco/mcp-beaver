package mcpserver

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

var jevBaseURL = regexp.MustCompile(`base-url "[^"]*"`)

// jevSpec is the shipped example guardfile pointed at a test upstream, so the
// keyed `questions` shape under test is the owning file's, not a copy.
func jevSpec(t *testing.T, upstreamURL string) string {
	t.Helper()
	raw, err := os.ReadFile("../../examples/jev-decision.mcp.kdl")
	if err != nil {
		t.Fatalf("read example guardfile: %v", err)
	}
	return jevBaseURL.ReplaceAllString(string(raw), `base-url "`+upstreamURL+`"`)
}

// COI-2083: Echo and Deep sent `questions` as a string or an array and Jev
// 422'd each one. A malformed body must be refused before the upstream call.
func TestJevMalformedQuestionsNeverReachUpstream(t *testing.T) {
	cases := map[string]string{
		"bare string":         `"Will it rain?"`,
		"JSON-encoded string": `"[{\"question\":\"Will it rain?\"}]"`,
		"array":               `[{"question":"Will it rain?"}]`,
		"entry as string":     `{"rain":"Will it rain?"}`,
	}
	for name, questions := range cases {
		t.Run(name, func(t *testing.T) {
			upstream, calls := tallyingUpstream(t, http.StatusOK)
			ts, sessionID := serveSpec(t, jevSpec(t, upstream.URL))

			result := callTool(t, ts, sessionID, "2", "create_noul_decision",
				`{"model":"jev-latest","state":{"chance_of_rain":0.9},"questions":`+questions+`}`)

			if isErr, _ := result["isError"].(bool); !isErr {
				t.Fatalf("malformed questions was not refused: %v", result)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("upstream calls = %d, want 0 for a locally refused body", got)
			}
			if got := firstText(t, result); !strings.Contains(got, "questions") {
				t.Errorf("refusal = %q, want it to name the questions field", got)
			}
		})
	}
}

// The refusal must not turn the correct shape into an outage.
func TestJevWellFormedYesNoQuestionReachesUpstream(t *testing.T) {
	upstream, calls := tallyingUpstream(t, http.StatusOK)
	ts, sessionID := serveSpec(t, jevSpec(t, upstream.URL))

	result := callTool(t, ts, sessionID, "2", "create_noul_decision",
		`{"model":"jev-latest","state":{"chance_of_rain":0.9},"questions":{"umbrella":{"type":"noul","instructions":"Should I bring an umbrella?"}}}`)

	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("well-formed yes/no question was refused: %v", result)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}
