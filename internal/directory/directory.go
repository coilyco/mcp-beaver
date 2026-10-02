// Package directory writes what `mcp-beaver directory` produces: a sweep
// record, one guardfile per answering server, and the two pages that index
// them. See docs/directory.md.
package directory

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coilyco/mcp-beaver/internal/mcpserver"
)

//go:embed templates/index.html templates/guardfiles.html
var templates embed.FS

// Record is a sweep as `sweep.json` stores it: every server the registry
// listed, answered or not, with the raw hint each tool declared. It is the
// only input the pages and guardfiles need, so `--from` renders offline.
type Record struct {
	GeneratedAt time.Time `json:"generated_at"`
	Registry    string    `json:"registry"`
	Scope       string    `json:"scope"`
	Servers     []Server  `json:"servers"`
}

// Server is one registry entry and what the sweep found behind it.
type Server struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Published   string `json:"published,omitempty"`
	Repository  string `json:"repository,omitempty"`
	State       string `json:"state"`
	Tools       []Tool `json:"tools,omitempty"`
}

// Tool is one upstream tool with its hint as declared: absent stays absent.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ReadOnly    *bool  `json:"readOnlyHint,omitempty"`
}

// Answered reports whether the sweep recorded a tool surface.
func (s Server) Answered() bool { return s.State == mcpserver.SweepStateOK }

func (s Server) pulled() *mcpserver.Pulled {
	out := &mcpserver.Pulled{Name: s.Name, URL: s.URL, Description: s.Description}
	for _, tool := range s.Tools {
		out.Tools = append(out.Tools, mcpserver.PulledTool{Name: tool.Name, Description: tool.Description, ReadOnly: tool.ReadOnly})
	}
	return out
}

// FromSweep records a sweep at one scope.
func FromSweep(registry string, scope mcpserver.Scope, swept []mcpserver.SweptServer, at time.Time) Record {
	rec := Record{GeneratedAt: at.UTC(), Registry: registry, Scope: string(scope)}
	for _, s := range swept {
		server := Server{
			Name:        s.Name,
			URL:         s.URL,
			Description: s.Description,
			Published:   s.Published,
			Repository:  s.Repository,
			State:       s.State,
		}
		for _, tool := range s.Tools {
			server.Tools = append(server.Tools, Tool{Name: tool.Name, Description: tool.Description, ReadOnly: tool.ReadOnly})
		}
		rec.Servers = append(rec.Servers, server)
	}
	return rec
}

// RecordFile is the record's name inside the output directory.
const RecordFile = "sweep.json"

// GuardfilesDir is the subdirectory the guardfiles land in.
const GuardfilesDir = "guardfiles"

// ReadRecord loads a `sweep.json` an earlier run wrote.
func ReadRecord(path string) (Record, error) {
	var rec Record
	src, err := os.ReadFile(path) //nolint:gosec // operator-supplied record path
	if err != nil {
		return rec, fmt.Errorf("mcp-beaver: read sweep %q: %w", path, err)
	}
	if err := json.Unmarshal(src, &rec); err != nil {
		return rec, fmt.Errorf("mcp-beaver: sweep %q does not parse: %w", path, err)
	}
	if _, err := mcpserver.ParseScope(rec.Scope); err != nil {
		return rec, err
	}
	return rec, nil
}

// Summary is what a run reports.
type Summary struct {
	Listed, Answered, Refused, Tools, Allowed int
}

// Write lands the record, the guardfiles, and the pages under dir.
func Write(dir string, rec Record) (Summary, error) {
	scope, err := mcpserver.ParseScope(rec.Scope)
	if err != nil {
		return Summary{}, err
	}
	if err := os.MkdirAll(filepath.Join(dir, GuardfilesDir), 0o755); err != nil { //nolint:gosec // operator-chosen output directory
		return Summary{}, fmt.Errorf("mcp-beaver: create %q: %w", dir, err)
	}
	encoded, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return Summary{}, err
	}
	if err := writeFile(filepath.Join(dir, RecordFile), append(encoded, '\n')); err != nil {
		return Summary{}, err
	}
	var summary Summary
	summary.Listed = len(rec.Servers)
	kdl := map[string]string{}
	for _, server := range rec.Servers {
		if !server.Answered() {
			summary.Refused++
			continue
		}
		summary.Answered++
		summary.Tools += len(server.Tools)
		pulled := server.pulled()
		summary.Allowed += len(pulled.Select(scope))
		text, err := mcpserver.RenderUpstreamGuardfile(pulled, scope)
		if err != nil {
			return Summary{}, err
		}
		path := filepath.Join(dir, GuardfilesDir, GuardfileName(server.Name))
		// Parsed back before it is written, as `pull` does, so the directory
		// never carries a file its own `lint` would refuse.
		if _, err := mcpserver.ParseUpstreamSpec(path, []byte(text)); err != nil {
			return Summary{}, fmt.Errorf("generated guardfile for %q does not parse, which is a bug: %w", server.Name, err)
		}
		if err := writeFile(path, []byte(text)); err != nil {
			return Summary{}, err
		}
		kdl[server.Name] = text
	}
	if err := renderPage(filepath.Join(dir, "index.html"), "templates/index.html", indexView(rec, scope)); err != nil {
		return Summary{}, err
	}
	if err := renderPage(filepath.Join(dir, "guardfiles.html"), "templates/guardfiles.html", guardfilesView(rec, scope, kdl)); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // operator-chosen output directory
		return fmt.Errorf("mcp-beaver: write %q: %w", path, err)
	}
	return nil
}

// GuardfileName maps a registry name onto one file: `ac.tandem/docs-mcp`
// becomes `ac.tandem__docs-mcp.mcp.kdl`, and any byte a path could misread
// becomes `_`.
func GuardfileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/':
			b.WriteString("__")
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + ".mcp.kdl"
}

func renderPage(path, name string, data any) error {
	tmpl, err := template.ParseFS(templates, name)
	if err != nil {
		return fmt.Errorf("mcp-beaver: template %s: %w", name, err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return fmt.Errorf("mcp-beaver: render %s: %w", name, err)
	}
	return writeFile(path, []byte(b.String()))
}

func stamp(rec Record) string {
	return rec.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC")
}

// counts is the hint split of one server: what the guardfile beside it is
// built from, never a name heuristic.
type counts struct {
	Tools, ReadOnly, Mutating, Silent int
}

func countHints(tools []Tool) counts {
	var c counts
	c.Tools = len(tools)
	for _, tool := range tools {
		switch {
		case tool.ReadOnly == nil:
			c.Silent++
		case *tool.ReadOnly:
			c.ReadOnly++
		default:
			c.Mutating++
		}
	}
	return c
}

// Row is one answering server on the index page.
type Row struct {
	Name, URL, Description, File, Coverage string
	counts
}

// Refusal is one server that did not answer.
type Refusal struct {
	Name, Description, State string
}

// IndexView is what templates/index.html renders.
type IndexView struct {
	Stamp, Registry, Scope                 string
	Listed, Answered, Refused, Pct         int
	Tools, ReadOnly, Mutating, Silent      int
	Declared, Partial, Undeclared, Allowed int
	Rows                                   []Row
	Refusals                               []Refusal
	States                                 string
}

func indexView(rec Record, scope mcpserver.Scope) IndexView {
	view := IndexView{Stamp: stamp(rec), Registry: rec.Registry, Scope: string(scope), Listed: len(rec.Servers)}
	states := map[string]int{}
	for _, server := range rec.Servers {
		if !server.Answered() {
			view.Refused++
			states[server.State]++
			view.Refusals = append(view.Refusals, Refusal{Name: server.Name, Description: server.Description, State: server.State})
			continue
		}
		view.Answered++
		c := countHints(server.Tools)
		view.Tools += c.Tools
		view.ReadOnly += c.ReadOnly
		view.Mutating += c.Mutating
		view.Silent += c.Silent
		pulled := server.pulled()
		view.Allowed += len(pulled.Select(scope))
		coverage := pulled.Coverage().Kind
		switch coverage {
		case "declared":
			view.Declared++
		case "partial":
			view.Partial++
		default:
			view.Undeclared++
		}
		view.Rows = append(view.Rows, Row{
			Name:        server.Name,
			URL:         server.URL,
			Description: server.Description,
			File:        GuardfilesDir + "/" + GuardfileName(server.Name),
			Coverage:    coverage,
			counts:      c,
		})
	}
	if view.Listed > 0 {
		view.Pct = 100 * view.Answered / view.Listed
	}
	sort.SliceStable(view.Rows, func(i, j int) bool {
		if view.Rows[i].Tools != view.Rows[j].Tools {
			return view.Rows[i].Tools > view.Rows[j].Tools
		}
		return view.Rows[i].Name < view.Rows[j].Name
	})
	sort.SliceStable(view.Refusals, func(i, j int) bool {
		if view.Refusals[i].State != view.Refusals[j].State {
			return view.Refusals[i].State < view.Refusals[j].State
		}
		return view.Refusals[i].Name < view.Refusals[j].Name
	})
	names := make([]string, 0, len(states))
	for state := range states {
		names = append(names, state)
	}
	sort.Slice(names, func(i, j int) bool {
		if states[names[i]] != states[names[j]] {
			return states[names[i]] > states[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, state := range names {
		parts = append(parts, strconv.Itoa(states[state])+" x "+state)
	}
	view.States = strings.Join(parts, " // ")
	return view
}

// Card is one guardfile on the guardfiles page.
type Card struct {
	Name, File, KDL string
	Tools, Allowed  int
}

// Group is one annotation-coverage band of cards.
type Group struct {
	Kind, Heading  string
	Blurb          template.HTML
	Servers, Tools int
	Cards          []Card
}

// GuardfilesView is what templates/guardfiles.html renders.
type GuardfilesView struct {
	Stamp, Scope                     string
	N, Declared, Partial, Undeclared int
	Tools, Allowed                   int
	Groups                           []Group
}

// Authored chrome, so the HTML in it is the page's own rather than data.
var groupText = map[string][2]string{
	"declared":   {"Allowlist from the upstream", "Every tool carries <code>readOnlyHint</code>. The allowlist is the upstream's own answer rather than a guess."},
	"partial":    {"Mixed", "Some tools declare and some do not. The declared ones decide the scope, and the silent ones are absent until the scope says <code>all</code>."},
	"undeclared": {"Nothing declared, nothing allowed", "No tool declares anything, so read-only and read-write allow nothing. The entry stays in the index carrying that fact, because the mark is the content."},
}

func guardfilesView(rec Record, scope mcpserver.Scope, kdl map[string]string) GuardfilesView {
	view := GuardfilesView{Stamp: stamp(rec), Scope: string(scope)}
	groups := map[string]*Group{}
	for _, kind := range []string{"declared", "partial", "undeclared"} {
		text := groupText[kind]
		groups[kind] = &Group{Kind: kind, Heading: text[0], Blurb: template.HTML(text[1])} //nolint:gosec // authored constant, not data
	}
	answered := make([]Server, 0, len(rec.Servers))
	for _, server := range rec.Servers {
		if server.Answered() {
			answered = append(answered, server)
		}
	}
	sort.SliceStable(answered, func(i, j int) bool {
		if len(answered[i].Tools) != len(answered[j].Tools) {
			return len(answered[i].Tools) > len(answered[j].Tools)
		}
		return answered[i].Name < answered[j].Name
	})
	for _, server := range answered {
		pulled := server.pulled()
		allowed := len(pulled.Select(scope))
		group := groups[pulled.Coverage().Kind]
		group.Servers++
		group.Tools += len(server.Tools)
		group.Cards = append(group.Cards, Card{
			Name:    server.Name,
			File:    GuardfilesDir + "/" + GuardfileName(server.Name),
			KDL:     kdl[server.Name],
			Tools:   len(server.Tools),
			Allowed: allowed,
		})
		view.N++
		view.Tools += len(server.Tools)
		view.Allowed += allowed
	}
	view.Declared = groups["declared"].Servers
	view.Partial = groups["partial"].Servers
	view.Undeclared = groups["undeclared"].Servers
	for _, kind := range []string{"declared", "partial", "undeclared"} {
		view.Groups = append(view.Groups, *groups[kind])
	}
	return view
}
