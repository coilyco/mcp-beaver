// Command mcp-beaver is the generic runtime that renders one `.mcp.kdl` spec into
// a guarded MCP server and matching HTTP tool API. It is the single static
// binary baked into every mcp-beaver image; the spec is the only thing that
// varies. There is no per-guardfile Go and no per-server handler.
//
//	mcp-beaver serve /spec/<name>.mcp.kdl --http :8080
//	mcp-beaver lint /spec/<name>.mcp.kdl [--methods]
//	mcp-beaver serve-upstream --upstream <url> --tool <name> [--pin <tool>.<arg>=<value>] [--upstream-header <name>=<template>]
//	mcp-beaver serve-upstream /spec/<name>.mcp.kdl
//	mcp-beaver lint-upstream --tool <name> --read-only heuristic
//	mcp-beaver pull <registry-name> [--scope read-only|read-write|all] [-o <out>]
//
// It parses the spec through umbra's opcore engine, projects one MCP tool
// plus one POST /api/{tool-name} endpoint per grant, and binds one HTTP
// listener. It never binds stdio: these run as remote pods reached by URL.
//
// A `sql` grant reaches a database through the drivers this binary registers.
// Postgres (`database pgx { ... }`) is the only one linked today.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/coilyco/mcp-beaver/internal/mcpserver"
	internaltelemetry "github.com/coilyco/mcp-beaver/internal/telemetry"
	"github.com/coilyco/umbra/http/mcpverb"
	"github.com/coilyco/umbra/pkg/tokenmint"

	// Registers the "pgx" database/sql driver a `database pgx { ... }` guardfile
	// names. umbra deliberately imports no driver, so the choice of which
	// databases this image can reach is made HERE, in the binary, and is the
	// only reason a `sql` grant runs rather than erroring on an unknown driver.
	//
	// Postgres alone on purpose: it is what the fleet runs, and every driver
	// linked is image weight and supply-chain surface for a database nobody has.
	_ "github.com/jackc/pgx/v5/stdlib"
)

const shutdownTimeout = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runContext(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-beaver:", err)
		os.Exit(1)
	}
}

// run dispatches the subcommand. The serving verbs bind a listener; `lint` is
// the one offline verb, so a consumer can validate a guardfile without starting
// anything. The pipeline's build/lock steps remain umbra's and deploy's,
// not this runtime's.
func run(argv []string) error {
	return runContext(context.Background(), argv)
}

func runContext(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("usage: mcp-beaver serve <spec.mcp.kdl> [--http :8080] | mcp-beaver serve-ssm <spec.mcp.kdl> [--http :8080] | mcp-beaver serve-s3 <spec.mcp.kdl> [--http :8080] | mcp-beaver serve-upstream (<spec.mcp.kdl> | --upstream <mcp-url> --tool <name> [--tool <name> ...]) | mcp-beaver lint <spec.mcp.kdl> | mcp-beaver lint-upstream (<spec.mcp.kdl> | --tool <name>) [--read-only heuristic|strict] [--upstream <mcp-url>] | mcp-beaver pull <registry-name> [--scope read-only|read-write|all] [-o <out>] | mcp-beaver directory -o <dir> [--scope read-only|read-write|all] [--limit <n>] [--timeout <d>] [--from <sweep.json>] | mcp-beaver flatten <spec.mcp.kdl> [-o <out>] [--check] | mcp-beaver version")
	}
	switch argv[0] {
	case "serve":
		return runServe(ctx, argv[1:])
	case "serve-upstream":
		return runServeUpstream(ctx, argv[1:])
	case "serve-ssm":
		return runServeSSM(ctx, argv[1:])
	case "serve-s3":
		return runServeS3(ctx, argv[1:])
	case "lint":
		return runLint(os.Stdout, argv[1:])
	case "lint-upstream":
		return runLintUpstream(ctx, os.Stdout, argv[1:])
	case "flatten":
		return runFlatten(os.Stdout, argv[1:])
	case "pull":
		return runPull(ctx, os.Stdout, argv[1:])
	case "directory":
		return runDirectory(ctx, os.Stdout, argv[1:])
	case "version":
		_, err := fmt.Fprintln(os.Stdout, mcpserver.Version)
		return err
	default:
		return fmt.Errorf("unknown command %q (want: serve, serve-ssm, serve-s3, serve-upstream, lint, lint-upstream, pull, directory, flatten, version)", argv[0])
	}
}

// runLintUpstream is the validation surface for a `serve-upstream` allowlist,
// the counterpart to what `lint` gives a guardfile. An allowlist has no spec
// file to build, so the checks are the allowlist's own shape plus, optionally,
// what the upstream says about the tools it names.
//
// Offline by default so it runs in CI and a sealed clone: shape only, plus the
// mutation-name heuristic behind --read-only. With --upstream it connects and
// checks against upstream truth, which is a rollout or smoke step, not CI.
func runLintUpstream(ctx context.Context, out io.Writer, argv []string) error {
	fs := flag.NewFlagSet("lint-upstream", flag.ContinueOnError)
	upstream := fs.String("upstream", "", "connect to this MCP upstream and check the allowlist against its advertised tools")
	readOnly := fs.String("read-only", "", "assert the allowlist is read-only, as `mode`: heuristic screens tool names offline, strict requires --upstream and checks readOnlyHint")
	connectTimeout := fs.Duration("connect-timeout", 0, "retry upstream startup until this timeout (0 fails immediately)")
	var tools stringSliceFlag
	fs.Var(&tools, "tool", "allowlisted upstream tool to check (repeatable)")
	var headerFlags stringSliceFlag
	fs.Var(&headerFlags, "upstream-header", "present a header to the upstream, as `<name>=<template>` where {env:VAR} resolves server-side (repeatable)")
	var oauth2Flags stringSliceFlag
	fs.Var(&oauth2Flags, "oauth2-client", "declare a client_credentials client a header may address as {oauth2:<name>}, as `name=..,token-url=..,client-id=..,client-secret={env:VAR}` (repeatable)")
	if err := fs.Parse(reorderFlagsFirst(argv)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("lint-upstream takes at most one guardfile path, plus --tool entries when there is none")
	}
	inputs, err := resolveProxyInputs("lint-upstream", fs.Arg(0), upstreamFlagInputs{upstream: *upstream, tools: tools, headers: headerFlags, oauth2: oauth2Flags})
	if err != nil {
		return err
	}
	mode, err := parseReadOnlyMode(*readOnly, inputs.upstream)
	if err != nil {
		return err
	}
	// The same authority the serving path calls, so the offline check cannot
	// drift from what serve-upstream will actually accept.
	allowlist, err := mcpserver.ValidateAllowlist(inputs.tools)
	if err != nil {
		return err
	}
	if mode == readOnlyHeuristic {
		if suspects := mcpserver.MutationSuspects(allowlist); len(suspects) > 0 {
			return fmt.Errorf(
				"read-only allowlist names mutation tools: %s. If these are genuinely read-only, re-run with --upstream and --read-only strict to check the upstream's own readOnlyHint instead of their names",
				strings.Join(suspects, ", "),
			)
		}
	}
	// A guardfile always names its upstream, so a file lint connects unless
	// nothing asks it to: --read-only strict is the ask, and a bare file lint
	// stays offline like `lint`.
	if inputs.upstream != "" && (fs.Arg(0) == "" || mode == readOnlyStrict) {
		if err := checkUpstreamAllowlist(ctx, inputs.upstream, allowlist, inputs.options, mode, *connectTimeout); err != nil {
			return err
		}
	}
	for _, name := range sortedCopy(allowlist) {
		if _, err := fmt.Fprintln(out, name); err != nil {
			return err
		}
	}
	return nil
}

// checkUpstreamAllowlist builds the same proxy serve-upstream builds. That
// connect already fails when an allowlisted tool is absent upstream, so the
// only check added here is the readOnlyHint one.
func checkUpstreamAllowlist(
	ctx context.Context,
	upstream string,
	allowlist []string,
	opts mcpserver.ProxyOptions,
	mode readOnlyMode,
	connectTimeout time.Duration,
) error {
	srv, err := connectProxyWithRetry(ctx, connectTimeout, time.Second, func(ctx context.Context) (*mcpserver.Server, error) {
		return mcpserver.NewProxyWithOptions(ctx, "mcp-beaver-lint", "", upstream, allowlist, opts)
	})
	if err != nil {
		return fmt.Errorf("connect upstream %q: %w", upstream, err)
	}
	defer func() { _ = srv.Close() }()
	if mode != readOnlyStrict {
		return nil
	}
	if mutable := srv.NotReadOnly(); len(mutable) > 0 {
		return fmt.Errorf(
			"upstream %q does not annotate these allowlisted tools readOnlyHint: %s",
			upstream, strings.Join(mutable, ", "),
		)
	}
	return nil
}

type readOnlyMode int

const (
	readOnlyOff readOnlyMode = iota
	readOnlyHeuristic
	readOnlyStrict
)

func parseReadOnlyMode(value, upstream string) (readOnlyMode, error) {
	switch value {
	case "":
		return readOnlyOff, nil
	case "heuristic":
		return readOnlyHeuristic, nil
	case "strict":
		if upstream == "" {
			return readOnlyOff, fmt.Errorf("--read-only strict needs --upstream: readOnlyHint is upstream's to declare")
		}
		return readOnlyStrict, nil
	default:
		return readOnlyOff, fmt.Errorf("unknown --read-only mode %q (want: heuristic, strict)", value)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// runLint is runServe minus serveHTTP and minus withTelemetry: it reads the
// spec, builds the same server, prints the minted tool names one per line, and
// exits. No listener, no telemetry, no network, so it runs in a sealed clone
// and in CI.
//
// It goes through mcpserver.New rather than opcore.ParseInline directly on
// purpose. That validates the grant-to-tool projection as well as the KDL
// parse, so the check covers what the runtime will actually mint rather than
// only what the file says.
func runLint(out io.Writer, argv []string) error {
	return runLintTo(out, os.Stderr, argv)
}

// runLintTo splits the two streams so the warning channel is testable. stdout
// stays the machine-readable surface a consumer diffs; warnings go to stderr
// so adding one never edits that diff.
func runLintTo(out, warn io.Writer, argv []string) error {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	methods := fs.Bool("methods", false, "print the resolved HTTP method beside each tool, as `name<TAB>METHOD`")
	apps := fs.Bool("apps", false, "print the MCP App widget uri beside each tool that carries one, as `name<TAB>URI`")
	if err := fs.Parse(reorderFlagsFirst(argv)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("lint needs exactly one spec path, e.g. `lint /spec/forgejo.mcp.kdl`")
	}
	specPath := fs.Arg(0)
	src, err := os.ReadFile(specPath) //nolint:gosec // operator-supplied trusted policy path
	if err != nil {
		return fmt.Errorf("read spec %q: %w", specPath, err)
	}
	if *methods && *apps {
		return fmt.Errorf("lint takes --methods or --apps, not both: each owns the second column")
	}
	shape, err := mcpserver.ClassifyGuardfile(src)
	if err != nil {
		return fmt.Errorf("invalid spec %q: %w", specPath, err)
	}
	if shape == mcpverb.ShapeUpstream {
		return lintUpstreamSpec(out, specPath, src, *methods, *apps)
	}
	// "invalid spec" rather than serve's "parse spec": a failure here is just as
	// often a projection failure (two grants minting one tool name) as a KDL
	// parse failure, and this message is the whole product of the command.
	srv, err := mcpserver.New(serverName(specPath), specPath, src)
	if err != nil {
		return fmt.Errorf("invalid spec %q: %w", specPath, err)
	}
	if err := lintWarnFallthrough(warn, srv.ToolMethods()); err != nil {
		return err
	}
	if err := lintWarnResourceAudience(warn, srv.ResourcesWithoutAudience()); err != nil {
		return err
	}
	if err := lintWarnVacatedControls(warn, srv.VacatedControls()); err != nil {
		return err
	}
	if *methods {
		return lintPrintMethods(out, srv)
	}
	if *apps {
		return lintPrintApps(out, srv)
	}
	for _, name := range srv.ToolNames() {
		if _, err := fmt.Fprintln(out, name); err != nil {
			return err
		}
	}
	return nil
}

// lintWarnFallthrough is the always-on half of #55. An unknown verb silently
// becoming POST is the defect: the tool mints, lint reads identically to the
// working case, and the grant fails only when something calls it. Warning is
// not an error because the fallthrough is legitimate for a child
// sub-collection - the author just has to be the one deciding that.
func lintWarnFallthrough(warn io.Writer, methods []mcpserver.ToolMethod) error {
	for _, m := range methods {
		if !m.Fallthrough {
			continue
		}
		_, err := fmt.Fprintf(warn,
			"mcp-beaver: warning: %s: verb %q is not in opcore's method table and resolved to %s by fallthrough. Confirm the upstream expects %s on this path.\n",
			m.Tool, m.Verb, m.Method, m.Method,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// lintWarnVacatedControls reports a control an inherited guardfile stated on a
// tool this tier narrowed away. Dropping it is correct, since there is nothing
// left to gate, and staying quiet would not be: the author wrote it in another
// file and cannot see from this one that it stopped applying.
func lintWarnVacatedControls(warn io.Writer, controls []string) error {
	for _, name := range controls {
		_, err := fmt.Fprintf(warn,
			"mcp-beaver: warning: inherited control on %q was dropped, because this "+
				"tier does not mint that tool. Nothing is left ungated, and the base "+
				"guardfile still reads as though the control applies here.\n",
			name,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// lintWarnResourceAudience is the fallthrough warning's shape applied to
// resources. A resource with no `audience` serves correctly and is included by
// no host that gates on the annotation, so it reads identically to a working
// one from every surface this project exposes.
//
// Warning rather than error, because a resource meant for a person to open by
// hand is legitimate. The author just has to be the one deciding that, which is
// why `audience "user"` silences this and silence alone does not.
func lintWarnResourceAudience(warn io.Writer, resources []string) error {
	for _, name := range resources {
		_, err := fmt.Fprintf(warn,
			"mcp-beaver: warning: resource %q states no `audience`, so a host that "+
				"decides context inclusion from it will skip this resource. Add "+
				"`audience \"assistant\"` for a model to read it, or `audience "+
				"\"user\"` to state it is for people.\n",
			name,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// lintPrintApps is the MCP App inventory, kept off the default stdout listing
// for the same reason --methods is: that listing is the diff a consumer pins,
// and a second column is a change to it. A tool with no widget prints "-", so
// the surface reads as a whole rather than as the linked subset.
func lintPrintApps(out io.Writer, srv *mcpserver.Server) error {
	widgets := srv.AppTools()
	for _, name := range srv.ToolNames() {
		uri, linked := widgets[name]
		if !linked {
			uri = "-"
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\n", name, uri); err != nil {
			return err
		}
	}
	return nil
}

func lintPrintMethods(out io.Writer, srv *mcpserver.Server) error {
	methods := make(map[string]string)
	for _, m := range srv.ToolMethods() {
		methods[m.Tool] = m.Method
	}
	for _, name := range srv.WithheldTools() {
		methods[name] = "WITHHELD"
	}
	for _, name := range srv.ToolNames() {
		method, ok := methods[name]
		if !ok {
			// The info tool and proxy grants reach no upstream by verb.
			method = "-"
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\n", name, method); err != nil {
			return err
		}
	}
	return nil
}

// runServe parses the spec and binds the HTTP listener. The spec path is the one
// positional; --http sets the bind address (default :8080).
func runServe(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("http", ":8080", "HTTP listen address for the MCP server (/mcp streamable HTTP)")
	requestTimeout := fs.Duration("request-timeout", mcpserver.DefaultRequestTimeout, "bound one request end to end, including its upstream call (0 disables)")
	// The documented entrypoint writes the spec before --http
	// (`serve /spec/x.mcp.kdl --http :8080`), so reorder the flags ahead of the
	// positional - flag.Parse stops at the first non-flag argument otherwise.
	if err := fs.Parse(reorderFlagsFirst(argv)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("serve needs exactly one spec path, e.g. `serve /spec/forgejo.mcp.kdl --http :8080`")
	}
	specPath := fs.Arg(0)
	name := serverName(specPath)
	return withTelemetry(ctx, name, func() error {
		src, err := os.ReadFile(specPath) //nolint:gosec // operator-supplied trusted policy path
		if err != nil {
			return fmt.Errorf("read spec %q: %w", specPath, err)
		}
		srv, err := mcpserver.New(name, specPath, src)
		if err != nil {
			return fmt.Errorf("parse spec %q: %w", specPath, err)
		}
		srv.SetRequestTimeout(*requestTimeout)

		// Structured rather than a banner: an operator reading a fleet of
		// these needs the bound config queryable, not greppable (#78).
		mcpserver.Log().Info("serving spec-backed MCP",
			"mode", "spec",
			"server", name,
			"spec", specPath,
			"addr", *addr,
			"tools", len(srv.ToolNames()),
			"request_timeout", requestTimeout.String())
		return serveHTTP(ctx, *addr, srv.Handler())
	})
}

// defaultUpstreamName is the flag form's server name. A guardfile names the
// server after its file instead, the way `serve` does.
const defaultUpstreamName = "mcp-beaver-upstream"

// runServeUpstream binds the HTTP listener for a proxy server that exposes a
// selected subset of an upstream streamable-HTTP MCP server, stated by flags
// or by an `mcp-upstream` guardfile.
func runServeUpstream(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("serve-upstream", flag.ContinueOnError)
	addr := fs.String("http", ":8080", "HTTP listen address for the MCP server (/mcp streamable HTTP)")
	name := fs.String("name", defaultUpstreamName, "MCP server name (a guardfile's own name when one is given)")
	upstream := fs.String("upstream", "", "streamable-HTTP MCP upstream endpoint")
	connectTimeout := fs.Duration("connect-timeout", 0, "retry upstream startup until this timeout (0 fails immediately)")
	requestTimeout := fs.Duration("request-timeout", mcpserver.DefaultRequestTimeout, "bound one request end to end, including its upstream call (0 disables)")
	var tools stringSliceFlag
	fs.Var(&tools, "tool", "allowlisted upstream tool to expose (repeatable)")
	var pinFlags stringSliceFlag
	fs.Var(&pinFlags, "pin", "fix one argument of one tool server-side, as `<tool>.<arg>=<value>` (repeatable)")
	var headerFlags stringSliceFlag
	fs.Var(&headerFlags, "upstream-header", "present a header to the upstream, as `<name>=<template>` where {env:VAR} resolves server-side (repeatable)")
	var oauth2Flags stringSliceFlag
	fs.Var(&oauth2Flags, "oauth2-client", "declare a client_credentials client a header may address as {oauth2:<name>}, as `name=..,token-url=..,client-id=..,client-secret={env:VAR}` (repeatable)")
	if err := fs.Parse(reorderFlagsFirst(argv)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("serve-upstream takes at most one guardfile path, plus flags when there is none")
	}
	specPath := fs.Arg(0)
	inputs, err := resolveProxyInputs("serve-upstream", specPath, upstreamFlagInputs{upstream: *upstream, tools: tools, headers: headerFlags, oauth2: oauth2Flags})
	if err != nil {
		return err
	}
	if inputs.upstream == "" {
		return fmt.Errorf("serve-upstream needs --upstream <mcp-url> or an `mcp-upstream` guardfile")
	}
	if len(inputs.tools) == 0 {
		return fmt.Errorf("serve-upstream needs at least one allowlisted tool: a guardfile exposing nothing is a statement, not a server")
	}
	serverName := *name
	if specPath != "" && serverName == defaultUpstreamName {
		serverName = inputs.name
	}
	pins, err := parseArgPins(pinFlags)
	if err != nil {
		return err
	}
	// A guardfile states its own `pin` nodes now, and two allowlists with no
	// reviewable answer is the shape resolveProxyInputs already refuses for
	// --upstream and --tool.
	if len(pins) > 0 && len(inputs.options.Pins) > 0 {
		return fmt.Errorf("serve-upstream takes a guardfile's `pin` nodes or --pin, not both: the file already states them")
	}
	// Before the retry loop, not inside it: an unset secret is a configuration
	// error, and --connect-timeout would otherwise spend minutes reporting it
	// as an upstream that will not answer.
	if err := mcpserver.PreflightUpstreamHeaders(ctx, inputs.headers, inputs.providers); err != nil {
		return err
	}
	return withTelemetry(ctx, serverName, func() error {
		opts := inputs.options
		if len(pins) > 0 {
			opts.Pins = pins
		}
		srv, err := connectProxyWithRetry(ctx, *connectTimeout, time.Second, func(ctx context.Context) (*mcpserver.Server, error) {
			return mcpserver.NewProxyWithOptions(ctx, serverName, specPath, inputs.upstream, inputs.tools, opts)
		})
		if err != nil {
			return fmt.Errorf("connect upstream %q: %w", inputs.upstream, err)
		}
		defer func() { _ = srv.Close() }()
		srv.SetRequestTimeout(*requestTimeout)
		mcpserver.Log().Info("serving upstream proxy",
			"mode", "upstream",
			"server", serverName,
			"spec", specPath,
			"addr", *addr,
			"tools", len(inputs.tools),
			"request_timeout", requestTimeout.String())
		return serveHTTP(ctx, *addr, srv.Handler())
	})
}

// connectProxyWithRetry lets a proxy container start alongside an upstream
// sidecar without crash-looping while the sidecar publishes its MCP listener.
// A zero timeout retains the command's fail-fast behavior for direct use.
func connectProxyWithRetry(
	ctx context.Context,
	timeout time.Duration,
	retryInterval time.Duration,
	connect func(context.Context) (*mcpserver.Server, error),
) (*mcpserver.Server, error) {
	if timeout < 0 {
		return nil, fmt.Errorf("connect timeout must not be negative")
	}
	if timeout == 0 {
		return connect(ctx)
	}
	if retryInterval <= 0 {
		return nil, fmt.Errorf("retry interval must be positive")
	}

	retryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		srv, err := connect(retryCtx)
		if err == nil {
			return srv, nil
		}
		lastErr = err

		timer := time.NewTimer(retryInterval)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return nil, fmt.Errorf("timed out after %s: %w", timeout, lastErr)
		case <-timer.C:
		}
	}
}

// runServeSSM serves the spec-declared, exact-parameter SSM reader. AWS SDK
// credential discovery reads the static key injected into the container.
func runServeSSM(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("serve-ssm", flag.ContinueOnError)
	addr := fs.String("http", ":8080", "HTTP listen address for the MCP server (/mcp streamable HTTP)")
	if err := fs.Parse(reorderFlagsFirst(argv)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("serve-ssm needs exactly one spec path")
	}
	specPath := fs.Arg(0)
	name := serverName(specPath)
	return withTelemetry(ctx, name, func() error {
		src, err := os.ReadFile(specPath) //nolint:gosec // operator-supplied trusted policy path
		if err != nil {
			return fmt.Errorf("read spec %q: %w", specPath, err)
		}
		srv, err := mcpserver.NewSSM(ctx, name, specPath, src)
		if err != nil {
			return fmt.Errorf("parse SSM spec %q: %w", specPath, err)
		}
		mcpserver.Log().Info("serving guarded SSM reader",
			"mode", "ssm",
			"server", name,
			"spec", specPath,
			"addr", *addr)
		return serveHTTP(ctx, *addr, srv.Handler())
	})
}

// runServeS3 serves the spec-declared publisher. Credential discovery is the
// same static-key path serve-ssm uses, and the guardfile carries every bound
// that IAM cannot state: the content-type allowlist, the key shape, and the
// size cap.
func runServeS3(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("serve-s3", flag.ContinueOnError)
	addr := fs.String("http", ":8080", "HTTP listen address for the MCP server (/mcp streamable HTTP)")
	if err := fs.Parse(reorderFlagsFirst(argv)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("serve-s3 needs exactly one spec path")
	}
	specPath := fs.Arg(0)
	name := serverName(specPath)
	return withTelemetry(ctx, name, func() error {
		src, err := os.ReadFile(specPath) //nolint:gosec // operator-supplied trusted policy path
		if err != nil {
			return fmt.Errorf("read spec %q: %w", specPath, err)
		}
		srv, err := mcpserver.NewS3(ctx, name, specPath, src)
		if err != nil {
			return fmt.Errorf("parse S3 spec %q: %w", specPath, err)
		}
		mcpserver.Log().Info("serving guarded S3 publisher",
			"mode", "s3",
			"server", name,
			"spec", specPath,
			"addr", *addr)
		return serveHTTP(ctx, *addr, srv.Handler())
	})
}

func withTelemetry(ctx context.Context, serviceName string, run func() error) (err error) {
	if _, crashErr := internaltelemetry.StartCrashReporting(serviceName); crashErr != nil {
		// The type only: a DSN parse error can carry the DSN.
		mcpserver.Log().Warn("crash reporting disabled", "error_type", fmt.Sprintf("%T", crashErr))
	}
	defer internaltelemetry.RecoverCrash()
	// Every serving verb ends here, so an error returned from run is the process dying.
	defer func() { internaltelemetry.ReportCrash(err) }()
	runtime, err := internaltelemetry.Setup(ctx, serviceName)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if shutdownErr := runtime.Shutdown(shutdownCtx); shutdownErr != nil {
			err = errors.Join(err, fmt.Errorf("shutdown OpenTelemetry: %w", shutdownErr))
		}
	}()
	return run()
}

// readHeaderTimeout bounds how long a client may take to send its request
// headers. Separate from the per-request deadline, which covers work the
// runtime does once it has a request: this one covers a peer that connects and
// then stalls, and it is the guard against a slow-loris holding a connection.
const readHeaderTimeout = 10 * time.Second

// idleTimeout bounds a kept-alive connection between requests. Without it a
// pod accumulates idle sockets from callers that never close.
const idleTimeout = 120 * time.Second

func serveHTTP(ctx context.Context, addr string, handler http.Handler) error {
	// No WriteTimeout on purpose: it is absolute from the start of the request
	// and would cut a legitimately slow upstream mid-response. The per-request
	// context deadline in mcpserver.Handler is the bound on that axis, and it
	// aborts the outbound call rather than only the write.
	server := &http.Server{
		Addr:              addr,
		Handler:           internaltelemetry.RecoverHandler(handler),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		if err := shutdownHTTP(server, shutdownTimeout); err != nil {
			return err
		}
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// shutdownHTTP drains the server for up to timeout. Connections still open at
// the deadline are cut and warned about, never returned: a returned error ends
// the process as a crash, and a pod roll with a request in flight hit that
// every time (MCP-BEAVER-1).
func shutdownHTTP(server *http.Server, timeout time.Duration) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := server.Shutdown(shutdownCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		mcpserver.Log().Warn("HTTP shutdown deadline reached, closing open connections", "timeout", timeout.String())
		_ = server.Close()
		return nil
	}
	if err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	return nil
}

// reorderFlagsFirst moves flag tokens (and the value of the one known
// value-taking flag, --http) ahead of positional arguments, so `serve <spec>
// --http :8080` parses the same as `serve --http :8080 <spec>`. A bare `--`
// terminator passes through untouched.
func reorderFlagsFirst(argv []string) []string {
	var flags, positional []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			positional = append(positional, argv[i:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			// A separated value (`--http :8080`) rides along with its flag;
			// the `--http=:8080` form already carries its value inline.
			if !strings.Contains(a, "=") && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				flags = append(flags, argv[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

// parseArgPins lifts the repeatable --pin flag into the runtime's own type, so
// the CLI never carries a second understanding of the pin format.
func parseUpstreamHeaders(raw []string, providers mcpserver.ProviderSet) ([]mcpserver.UpstreamHeader, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]mcpserver.UpstreamHeader, 0, len(raw))
	for _, entry := range raw {
		header, err := mcpserver.ParseUpstreamHeader(entry, providers)
		if err != nil {
			return nil, err
		}
		out = append(out, header)
	}
	if err := mcpserver.ValidateUpstreamHeaders(out); err != nil {
		return nil, err
	}
	return out, nil
}

// upstreamProviders lifts the repeatable --oauth2-client flag into the value
// registry every header then validates and resolves against. Built before the
// headers on purpose: a header naming {oauth2:x} is only valid once x is
// declared, and validating in the other order would refuse a live capability.
func upstreamProviders(raw []string) (mcpserver.ProviderSet, error) {
	if len(raw) == 0 {
		return mcpserver.BaseProviders(), nil
	}
	clients := make([]tokenmint.Client, 0, len(raw))
	for _, entry := range raw {
		client, err := mcpserver.ParseOAuth2Client(entry)
		if err != nil {
			return mcpserver.ProviderSet{}, err
		}
		clients = append(clients, client)
	}
	if err := mcpserver.ValidateOAuth2Clients(clients); err != nil {
		return mcpserver.ProviderSet{}, err
	}
	return mcpserver.NewProviderSet(clients, nil)
}

func parseArgPins(raw []string) ([]mcpserver.ArgPin, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]mcpserver.ArgPin, 0, len(raw))
	for _, entry := range raw {
		pin, err := mcpserver.ParseArgPin(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, pin)
	}
	return out, nil
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// serverName derives the MCP serverInfo name from the spec filename, stripping
// the `.mcp.kdl` (or `.kdl`) suffix: `/spec/forgejo-issues.mcp.kdl` -> `forgejo-issues`.
func serverName(specPath string) string {
	base := filepath.Base(specPath)
	base = strings.TrimSuffix(base, ".kdl")
	base = strings.TrimSuffix(base, ".mcp")
	if base == "" {
		return "mcp-beaver"
	}
	return base
}
