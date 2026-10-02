package mcpserver

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/coilyco/umbra/http/guardfile"
	"github.com/coilyco/umbra/http/opcore"
	"github.com/coilyco/umbra/http/specverb"
)

// maxSpecBytes bounds a decompressed spec, so a crafted archive cannot exhaust
// memory before the parser sees it. Forgejo's pruned Swagger is under 200 KiB.
const maxSpecBytes = 16 << 20

// parseSource resolves one guardfile into descriptors. `inherit` flattens
// first, then `spec` selects swagger resolution over the inline grammar.
// See docs/spec-mode.md.
func parseSource(specPath string, src []byte) ([]opcore.Descriptor, opcore.RuntimeConfig, error) {
	effective, err := flattenSource(specPath, src)
	if err != nil {
		return nil, opcore.RuntimeConfig{}, err
	}
	specMode, err := declaresWrapNode(effective, "spec")
	if err != nil {
		return nil, opcore.RuntimeConfig{}, err
	}
	if !specMode {
		return opcore.ParseInline(effective)
	}
	gf, err := guardfile.Parse(effective)
	if err != nil {
		return nil, opcore.RuntimeConfig{}, fmt.Errorf("mcp-beaver: parse spec-mode guardfile: %w", err)
	}
	raw, err := readSpecFile(specPath, gf.Spec)
	if err != nil {
		return nil, opcore.RuntimeConfig{}, err
	}
	return specverb.Descriptors(specverb.DescriptorConfig{Guardfile: gf, Spec: raw})
}

// flattenSource resolves `inherit` when the guardfile declares one, and returns
// the source untouched when it does not, so the disk is read only on request.
func flattenSource(specPath string, src []byte) ([]byte, error) {
	inherits, err := declaresWrapNode(src, "inherit")
	if err != nil {
		return nil, err
	}
	if !inherits {
		return src, nil
	}
	if specPath == "" {
		return nil, fmt.Errorf("mcp-beaver: `inherit` resolves against the guardfile's own path, and this source was supplied without one")
	}
	flat, err := guardfile.Flatten(specPath)
	if err != nil {
		return nil, fmt.Errorf("mcp-beaver: resolve inherit: %w", err)
	}
	return flat, nil
}

// declaresWrapNode reports whether the wrap body states the named node.
func declaresWrapNode(src []byte, name string) (bool, error) {
	doc, err := parseInlineDoc(src, name)
	if err != nil {
		return false, err
	}
	wrap := doc.GetNode("wrap")
	if wrap == nil {
		return false, fmt.Errorf("mcp-beaver: missing top-level `wrap` node")
	}
	for _, n := range wrap.Children().Nodes {
		if n.Name() == name {
			return true, nil
		}
	}
	return false, nil
}

// readSpecFile reads the `spec` file named beside the guardfile, decompressing
// a `.gz` so a vendored spec can stay small enough for a ConfigMap.
func readSpecFile(specPath, name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("mcp-beaver: spec mode needs a `spec <file>` node naming the API document")
	}
	if specPath == "" {
		return nil, fmt.Errorf("mcp-beaver: `spec %s` resolves beside the guardfile, and this source was supplied without a path", name)
	}
	// Confined to the guardfile's own directory: a spec is a sibling artifact.
	if filepath.Base(name) != name {
		return nil, fmt.Errorf("mcp-beaver: `spec %s` must name a file beside the guardfile, not a path", name)
	}
	full := filepath.Join(filepath.Dir(specPath), name)
	raw, err := os.ReadFile(full) //nolint:gosec // operator-supplied trusted policy path
	if err != nil {
		return nil, fmt.Errorf("mcp-beaver: read spec %q: %w", full, err)
	}
	if filepath.Ext(name) != ".gz" {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("mcp-beaver: decompress spec %q: %w", full, err)
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(io.LimitReader(zr, maxSpecBytes+1))
	if err != nil {
		return nil, fmt.Errorf("mcp-beaver: decompress spec %q: %w", full, err)
	}
	if len(out) > maxSpecBytes {
		return nil, fmt.Errorf("mcp-beaver: spec %q exceeds %d bytes decompressed", full, maxSpecBytes)
	}
	return out, nil
}
