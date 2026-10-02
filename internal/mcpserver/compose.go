package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	kdl "github.com/calico32/kdl-go"
	"github.com/coilyco/umbra/http/mcpverb"
)

// maxInheritDepth bounds the chain a guardfile may compose. A cycle is caught
// by the visited set below, so this only stops a legitimately absurd tower and
// gives the error a number rather than a stack overflow.
const maxInheritDepth = 16

// guardSource is one guardfile in an inherit chain: its bytes, and the
// directory a path stated inside it resolves against. The directory travels
// because `app file=` names a file beside the guardfile that DECLARED it, and
// after composition that is not the guardfile the runtime was pointed at.
type guardSource struct {
	src []byte
	dir string
}

// sourceNode is one top-level node with the directory of the guardfile it came
// from, so a parser that reads a file resolves it against the right one.
type sourceNode struct {
	node  *kdl.Node
	dir   string
	index int
}

// singletonSiblings names the sibling nodes a server states at most once. A
// child restating one REPLACES its base's rather than colliding with it,
// matching `spec`, `base-url`, and `auth` inside the wrap body. Two of them in
// one file stays the fail-closed error it already was: this only decides what
// happens across an inherit edge.
var singletonSiblings = map[string]bool{
	"instructions": true,
	"server-info":  true,
	"rate-limit":   true,
}

// inheritedSources walks the `inherit` chain and returns every guardfile in it,
// root-most first and the child last.
//
// The wrap body is composed by umbra's own Flatten. This exists because the
// nodes BESIDE wrap are parsed from raw bytes, so before this a parent's
// `confirm` and `withhold` were dropped in silence while its grants survived -
// a child came out WIDER than its base, against the one invariant inherit
// exists to hold (mcp-beaver#113).
func inheritedSources(specPath string, src []byte) ([]guardSource, error) {
	visited := map[string]bool{}
	if specPath != "" {
		if abs, err := filepath.Abs(specPath); err == nil {
			visited[abs] = true
		}
	}
	return collectInherited(specPath, src, visited, 0)
}

func collectInherited(specPath string, src []byte, visited map[string]bool, depth int) ([]guardSource, error) {
	if depth > maxInheritDepth {
		return nil, fmt.Errorf("mcp-beaver: `inherit` nests deeper than %d guardfiles", maxInheritDepth)
	}
	dir := filepath.Dir(specPath)
	if specPath == "" {
		dir = "."
	}
	parents, err := inheritPaths(src)
	if err != nil {
		return nil, err
	}
	var out []guardSource
	for _, rel := range parents {
		if specPath == "" {
			return nil, fmt.Errorf("mcp-beaver: `inherit %s` resolves against the guardfile's own path, and this source was supplied without one", rel)
		}
		path := filepath.Join(dir, rel)
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("mcp-beaver: resolve `inherit %s`: %w", rel, err)
		}
		if visited[abs] {
			return nil, fmt.Errorf("mcp-beaver: `inherit` cycles back to %q", path)
		}
		visited[abs] = true
		body, err := os.ReadFile(path) //nolint:gosec // operator-supplied trusted policy path, the same one umbra reads
		if err != nil {
			return nil, fmt.Errorf("mcp-beaver: read `inherit %s`: %w", rel, err)
		}
		ancestors, err := collectInherited(path, body, visited, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, ancestors...)
	}
	return append(out, guardSource{src: src, dir: dir}), nil
}

// inheritPaths reads the `inherit` nodes out of the wrap body, in stated order.
func inheritPaths(src []byte) ([]string, error) {
	doc, err := parseInlineDoc(src, "inherit")
	if err != nil {
		return nil, err
	}
	wrap := doc.GetNode("wrap")
	if wrap == nil {
		return nil, fmt.Errorf("mcp-beaver: missing top-level `wrap` node")
	}
	var out []string
	for _, n := range wrap.Children().Nodes {
		if n.Name() != "inherit" {
			continue
		}
		path, err := oneStringArg(n, "inherit")
		if err != nil {
			return nil, err
		}
		out = append(out, path)
	}
	return out, nil
}

// parseInlineNodes returns the top-level nodes across a composed chain, in
// root-most-first order, so every sibling parser sees a base's declarations
// before the child's and its existing duplicate check does the arbitrating.
//
// A singleton is pruned to its last occurrence across the chain, which is the
// child-wins half. Everything else unions, which is the half that matters:
// a child cannot drop a gate its base set, because the base's node is still
// here and redefining it is the duplicate error the parser already raises.
func parseInlineNodes(sources []guardSource, what string) ([]sourceNode, error) {
	var all []sourceNode
	for i, source := range sources {
		doc, err := parseInlineDoc(source.src, what)
		if err != nil {
			return nil, err
		}
		for _, n := range doc.Nodes {
			// Both top-level shapes are the file's subject, never a sibling of it.
			if n.Name() == "wrap" || n.Name() == mcpverb.UpstreamNode {
				continue
			}
			all = append(all, sourceNode{node: n, dir: source.dir, index: i})
		}
	}
	return pruneSingletons(all), nil
}

// pruneSingletons drops a singleton stated by an ancestor once a nearer
// guardfile states its own. It keeps EVERY node from the winning file rather
// than the last node overall, so two `instructions` in one guardfile still meet
// the duplicate error that catches a typo. Only the inherit edge is decided
// here, never what one file says.
func pruneSingletons(nodes []sourceNode) []sourceNode {
	winner := map[string]int{}
	for _, sn := range nodes {
		name := sn.node.Name()
		if singletonSiblings[name] && sn.index > winner[name] {
			winner[name] = sn.index
		}
	}
	out := make([]sourceNode, 0, len(nodes))
	for _, sn := range nodes {
		name := sn.node.Name()
		if singletonSiblings[name] && winner[name] != sn.index {
			continue
		}
		out = append(out, sn)
	}
	return out
}

// singleSource is the chain for a guardfile that inherits nothing: itself, and
// the directory it sits in.
func singleSource(src []byte, dir string) []guardSource {
	return []guardSource{{src: src, dir: dir}}
}

// toolScopedSiblings names the sibling nodes whose single argument is a
// projected tool name. Each is a control ON a tool, so it means nothing once
// the tool is gone.
var toolScopedSiblings = map[string]bool{
	"confirm":                     true,
	"cache":                       true,
	"reject-empty":                true,
	"reject-empty-argument":       true,
	"ignore-undeclared-arguments": true,
	"pin":                         true,
	"extract":                     true,
}

// inheritedToolControls names every tool an ANCESTOR states a control on. It
// is what separates a base's declaration from the child's own, which the
// parsed configs no longer remember: they are keyed by tool and say nothing
// about which guardfile put them there.
func inheritedToolControls(sources []guardSource) (map[string]bool, error) {
	if len(sources) < 2 {
		return nil, nil
	}
	nodes, err := parseInlineNodes(sources[:len(sources)-1], "inherited controls")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, sn := range nodes {
		if !toolScopedSiblings[sn.node.Name()] || len(sn.node.Arguments()) != 1 {
			continue
		}
		out[sn.node.Arg(0).String()] = true
	}
	return out, nil
}

// dropVacantControls removes a control a base tier stated on a tool the child
// narrowed away, and reports what it removed.
//
// Without this, `inherit` gains a dead end the moment it starts composing
// siblings: a base states `confirm "delete_thing"`, a child states
// `never delete thing`, and the child cannot build because a gate it has no
// way to remove names a tool it correctly removed. A vacated control is not a
// weakened one - there is no tool left to gate, and deny-by-absence already
// covers it - so this drops rather than refuses. A control the child states
// ITSELF on a tool it does not mint stays the error it was: that one is a typo.
func dropVacantControls[T any](cfg map[string]T, inherited, minted map[string]bool) []string {
	var dropped []string
	for name := range cfg {
		if minted[name] || !inherited[name] {
			continue
		}
		delete(cfg, name)
		dropped = append(dropped, name)
	}
	sort.Strings(dropped)
	return dropped
}
