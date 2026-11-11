package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	internaltelemetry "github.com/coilyco/mcp-beaver/internal/telemetry"
)

// schemaRefreshInterval is how often an idle proxy re-reads the upstream, so a
// changed contract reaches a person without a caller tripping over it. Rules and
// alert: docs/upstream.md.
var schemaRefreshInterval = 10 * time.Minute

const (
	schemaRefreshTimeout = 30 * time.Second
	maxDriftNames        = 10
)

// schemaDriftError is one tool's refusal. It names the tool it was raised for, and
// a call to any other tool never sees it (COI-2900).
type schemaDriftError struct {
	tool   string
	reason string
}

func (e *schemaDriftError) Error() string {
	return fmt.Sprintf("mcp-beaver: upstream schema drift for tool %q: %s", e.tool, e.reason)
}

func isSchemaDrift(err error) bool {
	var drift *schemaDriftError
	return errors.As(err, &drift)
}

// driftEvent is one change worth a person's attention. admitted means the proxy
// accepted it under the re-admit rule, and otherwise the tool is refused.
type driftEvent struct {
	tool        string
	admitted    bool
	detail      string
	fingerprint string
}

// announceDrift is replaced by a test. It never blocks on the network.
var announceDrift = func(ev driftEvent) {
	level := "refused"
	if ev.admitted {
		level = "admitted"
	}
	logger.Error("upstream schema drift", "tool", ev.tool, "outcome", level, "detail", ev.detail)
	internaltelemetry.ReportDrift(ev.tool, ev.admitted, ev.detail)
}

// driftOutcome is what one reconcile decided, collected under the lock and
// announced after it is released.
type driftOutcome struct {
	admitted *mcp.Tool
	event    *driftEvent
}

// reconcileLocked compares one upstream tool with the contract this proxy accepted.
// A match clears any refusal, so an upstream that reverts heals the tool. A change
// the re-admit rule allows becomes the accepted contract, and any other change
// refuses that tool alone until the upstream matches again.
func (p *proxyBackend) reconcileLocked(tools []*mcp.Tool, name string) (driftOutcome, error) {
	var out driftOutcome
	want, ok := p.baseline[name]
	if !ok {
		return out, fmt.Errorf("mcp-beaver: tool %q is not allowlisted", name)
	}
	tool, err := findUpstreamTool(tools, name)
	if err != nil {
		return out, err
	}
	fp, err := fingerprintTool(tool)
	if err != nil {
		return out, fmt.Errorf("fingerprint upstream tool %q: %w", name, err)
	}
	if fp == want {
		delete(p.refused, name)
		delete(p.reported, name)
		return out, nil
	}
	reason, added := driftVerdict(p.acceptedLocked(name), tool)
	if reason != "" {
		return p.refuseLocked(name, fp, reason), &schemaDriftError{tool: name, reason: reason}
	}
	clone := toolSpecFromUpstream(tool)
	p.adoptLocked(name, clone, fp)
	delete(p.refused, name)
	out.admitted = cloneTool(clone)
	out.event = p.newEventLocked(name, fp, true, "added optional arguments: "+added)
	return out, nil
}

// refuseLocked records a refusal and returns the event to announce, once per
// distinct upstream contract so a busy tool does not repeat it on every call.
func (p *proxyBackend) refuseLocked(name, fp, reason string) driftOutcome {
	p.refused[name] = &schemaDriftError{tool: name, reason: reason}
	return driftOutcome{event: p.newEventLocked(name, fp, false, reason)}
}

func (p *proxyBackend) newEventLocked(name, fp string, admitted bool, detail string) *driftEvent {
	key := fmt.Sprintf("%s/%t", fp, admitted)
	if p.reported[name] == key {
		return nil
	}
	p.reported[name] = key
	return &driftEvent{tool: name, admitted: admitted, detail: detail, fingerprint: fp}
}

func (p *proxyBackend) acceptedLocked(name string) *mcp.Tool {
	for _, tool := range p.selected {
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

func (p *proxyBackend) adoptLocked(name string, tool *mcp.Tool, fp string) {
	for i, existing := range p.selected {
		if existing.Name == name {
			p.selected[i] = tool
		}
	}
	p.baseline[name] = fp
}

// announce publishes what a reconcile decided, outside the lock. An admitted tool is
// re-registered so clients see the new contract.
func (p *proxyBackend) announce(out driftOutcome) {
	p.mu.Lock()
	onAdmit := p.onAdmit
	p.mu.Unlock()
	if out.admitted != nil && onAdmit != nil {
		onAdmit(out.admitted)
	}
	if out.event != nil {
		announceDrift(*out.event)
	}
}

// refreshLoop re-reads the upstream on a timer until the backend closes.
func (p *proxyBackend) refreshLoop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.refreshAll(ctx)
		}
	}
}

// refreshAll checks every allowlisted tool from one listing, replacing the
// session once if it has gone away, as a call would.
func (p *proxyBackend) refreshAll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, schemaRefreshTimeout)
	defer cancel()
	err := p.checkAll(ctx)
	if err == nil {
		return
	}
	if rerr := p.reconnect(ctx); rerr != nil {
		logger.Warn("upstream schema refresh failed", "error", err.Error(), "reconnect", rerr.Error())
		return
	}
	if err := p.checkAll(ctx); err != nil {
		logger.Warn("upstream schema refresh failed", "error", err.Error())
	}
}

func (p *proxyBackend) checkAll(ctx context.Context) error {
	p.mu.Lock()
	session, err := p.sessionLocked()
	if err != nil {
		p.mu.Unlock()
		return err
	}
	res, err := p.probeTools(ctx, session)
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("refresh upstream tools: %w", err)
	}
	var outcomes []driftOutcome
	for _, name := range p.allowlist {
		out, rerr := p.reconcileLocked(res.Tools, name)
		if rerr != nil && !isSchemaDrift(rerr) {
			logger.Warn("upstream schema refresh", "tool", name, "error", rerr.Error())
		}
		outcomes = append(outcomes, out)
	}
	p.mu.Unlock()
	for _, out := range outcomes {
		p.announce(out)
	}
	return nil
}

// driftVerdict applies the re-admit rule. It returns an empty reason when the
// change is allowed, with the names of the arguments it added, and otherwise why
// the tool is refused. The rule: the tool is read-only before and after, and the
// only change is new properties that the required list does not name.
func driftVerdict(accepted, current *mcp.Tool) (string, string) {
	if accepted == nil || current == nil {
		return "the accepted contract is missing", ""
	}
	if !readOnly(accepted) || !readOnly(current) {
		return "the tool is not marked readOnlyHint, so a changed contract needs review", ""
	}
	if !equalJSON(withoutInput(accepted), withoutInput(current)) {
		return "its description, title, annotations or output schema changed", ""
	}
	before, errBefore := schemaObject(accepted.InputSchema)
	after, errAfter := schemaObject(current.InputSchema)
	if errBefore != nil || errAfter != nil {
		return "its input schema is not a JSON object", ""
	}
	for key, value := range before {
		if key == "properties" {
			continue
		}
		if !reflect.DeepEqual(value, after[key]) {
			return fmt.Sprintf("its input schema changed %q", key), ""
		}
	}
	for key := range after {
		if _, had := before[key]; !had && key != "properties" {
			return fmt.Sprintf("its input schema gained %q", key), ""
		}
	}
	oldProps, _ := before["properties"].(map[string]any)
	newProps, _ := after["properties"].(map[string]any)
	var added []string
	for name, value := range oldProps {
		if !reflect.DeepEqual(value, newProps[name]) {
			return fmt.Sprintf("argument %s changed or was removed", boundedName(name)), ""
		}
	}
	for name := range newProps {
		if _, had := oldProps[name]; !had {
			added = append(added, boundedName(name))
		}
	}
	sort.Strings(added)
	if len(added) > maxDriftNames {
		added = append(added[:maxDriftNames], "...")
	}
	return "", strings.Join(added, ", ")
}

func readOnly(tool *mcp.Tool) bool {
	return tool.Annotations != nil && tool.Annotations.ReadOnlyHint
}

func withoutInput(tool *mcp.Tool) *mcp.Tool {
	clone := cloneTool(tool)
	clone.InputSchema = nil
	return clone
}

func equalJSON(a, b any) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}

func schemaObject(schema any) (map[string]any, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, fmt.Errorf("not an object")
	}
	return out, nil
}

// boundedName quotes an argument name an upstream chose, cut short so a hostile name
// cannot flood a log line or an alert.
func boundedName(name string) string {
	if len(name) > 64 {
		name = name[:64] + "..."
	}
	return fmt.Sprintf("%q", name)
}
