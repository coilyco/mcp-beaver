package mcpserver

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/coilyco/umbra/http/guardfile"
)

// The reported shape (COI-2106): a misspelled control parsed as nothing and
// guarded nothing, with `lint` exiting 0.
func TestNewRefusesAnUnknownTopLevelSibling(t *testing.T) {
	for name, tc := range map[string]struct{ spec, want string }{
		"typo of reject-empty": {`reject-emtpy "get_thing"`, "reject-emtpy"},
		"unknown control":      {`ignore-undeclared-argument "get_thing"`, "ignore-undeclared-argument"},
		"unknown block":        {`banana { text "x" }`, "banana"},
		"typo beside a valid":  {`confirm "create_thing" message="ok"` + "\n" + `confim "get_thing"`, "confim"},
		"typo of description":  {`descripton "Things."`, "descripton"},
		"umbra-shaped sibling": {`wrap-extra "x"`, "wrap-extra"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New("test", "test.mcp.kdl", []byte(cachedSpec("http://127.0.0.1:1", tc.spec)))
			if err == nil {
				t.Fatal("New accepted a top-level node nothing reads")
			}
			if !strings.Contains(err.Error(), "`"+tc.want+"`") {
				t.Errorf("error = %q, want it to name `%s`", err, tc.want)
			}
		})
	}
}

// Every name the allowlist holds must still build, `description` included,
// since umbra reads it and beaver's parsers never do.
func TestNewKeepsEverySiblingItParses(t *testing.T) {
	spec := `description "Things."
confirm "create_thing" message="ok"
cache "get_thing" ttl="1m"
reject-empty "get_thing"
instructions { text "Use things." }
`
	if _, err := New("test", "test.mcp.kdl", []byte(cachedSpec("http://127.0.0.1:1", spec))); err != nil {
		t.Fatalf("New refused valid siblings: %v", err)
	}
}

// A typo in a base tier is read by nothing in the child either, and inherit
// exists so a child cannot be weaker than its base.
func TestNewRefusesAnUnknownSiblingInAnInheritedBase(t *testing.T) {
	path := inheritPair(t, `reject-emtpy "get_thing"`, parentWrapBody, bareChild)
	src, err := os.ReadFile(path) //nolint:gosec // test fixture written above
	if err != nil {
		t.Fatalf("read child: %v", err)
	}
	if _, err := New("test", path, src); err == nil || !strings.Contains(err.Error(), "`reject-emtpy`") {
		t.Errorf("err = %v, want the base tier's typo named", err)
	}
}

// The refusal names what umbra exports, so a sibling umbra adds is allowed and
// listed without an edit here (COI-2429).
func TestUmbraSiblingsAreAllowedAndNamed(t *testing.T) {
	names := restSiblingNames()
	for _, name := range guardfile.SiblingNodes() {
		if !slices.Contains(names, name) {
			t.Errorf("refusal omits umbra sibling %q", name)
		}
	}
}
