package mcpserver

import (
	"fmt"
	"math/big"
	"strings"

	kdl "github.com/calico32/kdl-go"
)

// parseInlineDoc parses a `.mcp.kdl` document for the nodes that ride BESIDE
// `wrap` at the top level. `opcore.ParseInline` reads only the `wrap` node, so
// these siblings never touch the frozen wrap-body grammar or the umbra pin.
// See docs/guardfile-siblings.md for the rule and the nodes that use it.
func parseInlineDoc(src []byte, what string) (*kdl.Document, error) {
	doc, err := kdl.ParseString(string(normalizeInlineBooleans(src)))
	if err != nil {
		return nil, fmt.Errorf("mcp-beaver: parse KDL for %s: %w", what, err)
	}
	return doc, nil
}

// normalizeInlineBooleans mirrors opcore's own: the wrap body spells booleans
// as `required=true` shorthand, which kdl-go alone rejects. Every reader of
// these bytes goes through here, umbra's strict parsers included, because a
// document that will not parse classifies as nothing rather than as itself.
func normalizeInlineBooleans(src []byte) []byte {
	repl := strings.NewReplacer(
		"required=true", "required=#true",
		"required=false", "required=#false",
		"raw=true", "raw=#true",
		"raw=false", "raw=#false",
		"keyed=true", "keyed=#true",
		"keyed=false", "keyed=#false",
	)
	return []byte(repl.Replace(string(src)))
}

// oneStringArg reads the single required string argument a sibling node names
// itself with, so every node reports the same shape error.
func oneStringArg(n *kdl.Node, node string) (string, error) {
	if len(n.Arguments()) != 1 {
		return "", fmt.Errorf("mcp-beaver: `%s` wants exactly one name argument, got %d", node, len(n.Arguments()))
	}
	value := n.Arg(0).String()
	if value == "" {
		return "", fmt.Errorf("mcp-beaver: `%s` name must be non-empty", node)
	}
	return value, nil
}

// boolProp reads a KDL boolean property. Value.String() renders a bool as
// "<kdl.Bool true>" and Value.Bool() panics on a non-bool, so comparing the
// rendered string silently reads every boolean as false - which would turn a
// `required=#true` argument into an optional one. Fail closed on a non-bool
// instead, and never panic on operator-supplied config.
func boolProp(value kdl.Value, node, key string) (bool, error) {
	if value.Kind() != kdl.Bool {
		return false, fmt.Errorf("mcp-beaver: `%s` property %q wants a boolean (#true or #false), got %s", node, key, value.String())
	}
	return value.Bool(), nil
}

// floatProp reads a KDL numeric property. kdl-go spreads numbers across four
// kinds and every accessor panics on the other three, so all four are handled
// rather than the two an author would guess: a plain `priority=1` arrives as
// Int, but the decimal `priority=0.9` arrives as BigFloat, not Float. Missing
// that reads a well-formed number as a type error. A genuine non-number still
// fails closed, because annotating as zero would mean "least important"
// instead of saying the spec is wrong.
func floatProp(value kdl.Value, node, key string) (float64, error) {
	switch value.Kind() {
	case kdl.Int:
		return float64(value.Int()), nil
	case kdl.Float:
		return value.Float(), nil
	case kdl.BigInt:
		got, _ := new(big.Float).SetInt(value.BigInt()).Float64()
		return got, nil
	case kdl.BigFloat:
		got, _ := value.BigFloat().Float64()
		return got, nil
	default:
		return 0, fmt.Errorf("mcp-beaver: `%s` property %q wants a number, got %s", node, key, value.String())
	}
}

// joinTextChildren collects repeated `text "..."` children into one body.
// KDL has no ergonomic multi-line string here, so a body is written as one
// `text` node per line and joined with newlines.
func joinTextChildren(n *kdl.Node, node string) (string, error) {
	var lines []string
	for _, child := range n.Children().Nodes {
		if child.Name() != "text" {
			continue
		}
		if len(child.Arguments()) != 1 {
			return "", fmt.Errorf("mcp-beaver: `%s` child `text` wants exactly one argument, got %d", node, len(child.Arguments()))
		}
		lines = append(lines, child.Arg(0).String())
	}
	return strings.Join(lines, "\n"), nil
}
