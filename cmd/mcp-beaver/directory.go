package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/coilyco/mcp-beaver/internal/directory"
	"github.com/coilyco/mcp-beaver/internal/mcpserver"
)

// runDirectory sweeps the registry and writes the directory: `sweep.json`,
// one guardfile per answering server, and the two pages. With `--from` it
// renders an earlier sweep offline instead. See docs/directory.md.
func runDirectory(ctx context.Context, out io.Writer, argv []string) error {
	fs := flag.NewFlagSet("directory", flag.ContinueOnError)
	registry := fs.String("registry", mcpserver.DefaultRegistry, "MCP registry base URL")
	scopeFlag := fs.String("scope", "", "which tools each guardfile allows, as `scope`: read-only (the default), read-write, or all")
	limit := fs.Int("limit", 0, "probe at most this many listed servers, in registry order; 0 means every one")
	concurrency := fs.Int("concurrency", 0, "upstreams probed at once; 0 means 8")
	timeout := fs.Duration("timeout", 0, "deadline per upstream, handshake and list together; 0 means 30s")
	from := fs.String("from", "", "render from this earlier sweep.json instead of reaching the registry")
	outDir := fs.String("o", "", "directory to write into (required)")
	if err := fs.Parse(reorderFlagsFirst(argv)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("directory takes no positional argument; name the output with -o <dir>")
	}
	if *outDir == "" {
		return fmt.Errorf("directory needs -o <dir> to write into")
	}
	var rec directory.Record
	if *from != "" {
		loaded, err := directory.ReadRecord(*from)
		if err != nil {
			return err
		}
		rec = loaded
		if *scopeFlag != "" {
			scope, err := mcpserver.ParseScope(*scopeFlag)
			if err != nil {
				return err
			}
			rec.Scope = string(scope)
		}
	} else {
		scope, err := mcpserver.ParseScope(*scopeFlag)
		if err != nil {
			return err
		}
		entries, err := mcpserver.EnumerateRegistry(ctx, mcpserver.EnumerateOptions{Registry: *registry, Limit: *limit})
		if err != nil {
			return err
		}
		swept := mcpserver.Sweep(ctx, entries, mcpserver.SweepOptions{Concurrency: *concurrency, Timeout: *timeout, Progress: os.Stderr})
		rec = directory.FromSweep(*registry, scope, swept, time.Now())
	}
	summary, err := directory.Write(*outDir, rec)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "directory: %d listed, %d answered, %d refused, %d tools, %d allowed at %s -> %s\n",
		summary.Listed, summary.Answered, summary.Refused, summary.Tools, summary.Allowed, rec.Scope, *outDir)
	return err
}
