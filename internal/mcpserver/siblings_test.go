package mcpserver

import (
	"os"
	"strings"
	"testing"

	"github.com/coilyco/umbra/http/mcpverb"
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

// umbraSiblings restates a name umbra owns, so pin the claim to the pinned
// umbra: if it stops reading `description`, this fails and the allowlist is
// the thing to fix. The upstream shape is the fixture because umbra reads the
// node there and on spec-mode guardfiles, and never on the inline grammar.
func TestUmbraSiblingsAreReadByUmbra(t *testing.T) {
	for name := range umbraSiblings {
		if name != "description" {
			t.Fatalf("umbraSiblings holds %q, which this test does not know how to pin", name)
		}
	}
	up, err := mcpverb.ParseUpstream([]byte(`description "Docs."
mcp-upstream "ac.tandem/docs-mcp" {
    url "https://tandem.ac/mcp"
    can "search_docs"
}`))
	if err != nil {
		t.Fatalf("mcpverb.ParseUpstream: %v", err)
	}
	if up.Description != "Docs." {
		t.Errorf("umbra read description = %q, want it consumed by umbra", up.Description)
	}
}
