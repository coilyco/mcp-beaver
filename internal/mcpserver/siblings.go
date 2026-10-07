package mcpserver

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/coilyco/umbra/http/guardfile"
)

// restSiblings names every top-level node beside `wrap` that a REST guardfile
// parses in this package, one entry per sibling parser. A parser added without
// an entry here fails its own first guardfile, which is the safe direction.
var restSiblings = map[string]bool{
	"app":                         true,
	"cache":                       true,
	"confirm":                     true,
	"extract":                     true,
	"forward-header":              true,
	"icon":                        true,
	"ignore-undeclared-arguments": true,
	"instructions":                true,
	"oauth2-client":               true,
	"pin":                         true,
	"prompt":                      true,
	"rate-limit":                  true,
	"reject-empty":                true,
	"reject-empty-argument":       true,
	"resource":                    true,
	"server-info":                 true,
	"withhold":                    true,
}

// rejectUnknownRESTSiblings refuses a top-level node no parser reads. Each
// sibling control is a gate, so a misspelled one (`reject-emtpy`) read by
// nothing leaves its author believing a tool is guarded when it is not
// (COI-2106). Runs across the whole `inherit` chain, so a typo in a base tier
// fails the child that composes it.
func rejectUnknownRESTSiblings(sources []guardSource) error {
	nodes, err := parseInlineNodes(sources, "sibling check")
	if err != nil {
		return err
	}
	for _, sn := range nodes {
		name := sn.node.Name()
		if restSiblings[name] || slices.Contains(guardfile.SiblingNodes(), name) {
			continue
		}
		return fmt.Errorf(
			"mcp-beaver: unknown top-level node `%s` beside `wrap`; it would guard nothing (fail-closed). Known: %s",
			name, strings.Join(restSiblingNames(), ", "),
		)
	}
	return nil
}

// restSiblingNames lists what a refusal may name, read off the map and umbra's
// export so a node added to either cannot leave the message stale.
func restSiblingNames() []string {
	out := guardfile.SiblingNodes()
	for name := range restSiblings {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
