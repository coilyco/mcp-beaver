package mcpserver

import (
	"context"
	"fmt"
	"strconv"

	kdl "github.com/calico32/kdl-go"
	"github.com/coilyco/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// extractKind names a projection. The set is closed and an unknown value is a
// build error, so a typo in `as` cannot silently degrade to pass-through.
type extractKind string

const (
	kindPDFText     extractKind = "pdf-text"
	kindFeedEntries extractKind = "feed-entries"
)

// extractSpec is one grant's declared projection. Each kind reads only its own
// bound, and parseExtracts refuses the other one rather than ignoring it.
type extractSpec struct {
	kind     extractKind
	maxPages int
	maxItems int
}

// extractConfig maps a projected tool name to its extraction.
type extractConfig map[string]*extractSpec

// parseExtracts reads top-level `extract` nodes, siblings of `wrap`:
//
//	extract "get_report" as="pdf-text" max-pages="20"
//	extract "get_subreddit_rss" as="feed-entries" max-items="25"
//
// A large amount of authoritative reference material - government statistics,
// standards bodies, regulatory filings, equipment documentation - is published
// only as PDF, with no JSON API and often no HTML equivalent. Syndicated
// sources have the same shape one format over: a feed is XML an agent cannot
// read. A grant that reaches either returns bytes no model can use, so those
// sources are invisible to the whole fleet (mcp-beaver#60, #81).
//
// The projection lives here rather than in the guardfile grammar because
// turning an upstream response into tool content is this runtime's half of the
// boundary: umbra owns guarded execution, mcp-beaver owns projection.
//
// `as` names the extraction. Table extraction and OCR remain absent because
// they are three very different amounts of work with three very different
// dependency footprints - the property exists to make the next one additive,
// which is what `feed-entries` then was.
func parseExtracts(sources []guardSource) (extractConfig, error) {
	nodes, err := parseInlineNodes(sources, "extract")
	if err != nil {
		return nil, err
	}
	out := extractConfig{}
	for _, sn := range nodes {
		n := sn.node
		if n.Name() != "extract" {
			continue
		}
		tool, err := oneStringArg(n, "extract")
		if err != nil {
			return nil, err
		}
		if _, dup := out[tool]; dup {
			return nil, fmt.Errorf("mcp-beaver: duplicate `extract` for tool %q", tool)
		}
		spec, err := parseExtractProperties(n, tool)
		if err != nil {
			return nil, err
		}
		out[tool] = spec
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseExtractProperties reads one node's properties. The bounds are read
// before the kind is known, so which of them is legal is settled afterwards.
//
// Whether a bound was STATED is tracked separately from its value, because
// zero is a value an author can write and it has to stay a build error rather
// than falling through to the default.
func parseExtractProperties(n *kdl.Node, tool string) (*extractSpec, error) {
	var kind extractKind
	var pages, items int
	var sawPages, sawItems bool
	for key, value := range n.Properties() {
		var err error
		switch key {
		case "as":
			kind = extractKind(value.String())
		case "max-pages":
			if pages, err = strconv.Atoi(value.String()); err != nil {
				return nil, fmt.Errorf("mcp-beaver: `extract` %q max-pages %q must be a whole number", tool, value.String())
			}
			sawPages = true
		case "max-items":
			if items, err = strconv.Atoi(value.String()); err != nil {
				return nil, fmt.Errorf("mcp-beaver: `extract` %q max-items %q must be a whole number", tool, value.String())
			}
			sawItems = true
		default:
			return nil, fmt.Errorf("mcp-beaver: unknown `extract` property %q (want as | max-pages | max-items; fail-closed)", key)
		}
	}
	switch kind {
	case kindPDFText:
		if sawItems {
			return nil, fmt.Errorf("mcp-beaver: `extract` %q is %q, which bounds pages rather than items: use max-pages", tool, kind)
		}
		if !sawPages {
			pages = defaultExtractPages
		}
		if pages < 1 || pages > maxExtractPages {
			return nil, fmt.Errorf("mcp-beaver: `extract` %q max-pages must be between 1 and %d, got %d", tool, maxExtractPages, pages)
		}
		return &extractSpec{kind: kind, maxPages: pages}, nil
	case kindFeedEntries:
		if sawPages {
			return nil, fmt.Errorf("mcp-beaver: `extract` %q is %q, which bounds items rather than pages: use max-items", tool, kind)
		}
		if !sawItems {
			items = defaultFeedItems
		}
		if items < 1 || items > maxFeedItems {
			return nil, fmt.Errorf("mcp-beaver: `extract` %q max-items must be between 1 and %d, got %d", tool, maxFeedItems, items)
		}
		return &extractSpec{kind: kind, maxItems: items}, nil
	default:
		return nil, fmt.Errorf("mcp-beaver: `extract` %q needs `as=%q` or `as=%q`, got %q", tool, kindPDFText, kindFeedEntries, kind)
	}
}

// validateExtracts rejects an `extract` that could never run. Both cases are
// build errors because an author who believes a document is being read and one
// who believes it is not should not both be able to run the same spec.
func validateExtracts(cfg extractConfig, descs []opcore.Descriptor) error {
	if cfg == nil {
		return nil
	}
	byTool := make(map[string]opcore.Descriptor, len(descs))
	for _, d := range descs {
		byTool[toolName(d)] = d
	}
	for tool := range cfg {
		desc, ok := byTool[tool]
		if !ok {
			return fmt.Errorf("mcp-beaver: `extract` names %q, which is not a grant-backed tool this spec serves", tool)
		}
		// Without `raw-response` opcore decodes the body as JSON and the call
		// fails on the first byte of `%PDF` or `<`, long before anything here
		// runs.
		if !desc.RawResponse {
			return fmt.Errorf("mcp-beaver: `extract` names %q, whose grant does not declare `raw-response`: opcore would decode the document as JSON and fail the call before extraction", tool)
		}
	}
	return nil
}

// extractToolSuccess routes an upstream response to the projection its grant
// declared. The kind is validated at build, so an unreachable default here is
// a programming error rather than a spec error.
func extractToolSuccess(ctx context.Context, resp opcore.Response, x *extractSpec) *mcp.CallToolResult {
	switch x.kind {
	case kindPDFText:
		return pdfToolSuccess(ctx, resp, x)
	case kindFeedEntries:
		return feedToolSuccess(resp, x)
	default:
		return toolError(fmt.Errorf("mcp-beaver: unknown extraction %q", x.kind))
	}
}
