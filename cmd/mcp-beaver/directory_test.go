package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coilyco/mcp-beaver/internal/directory"
)

// pagedRegistry lists four servers over two pages: one that answers, one that
// refuses with 401, one superseded version, and one packages-only entry.
func pagedRegistry(t *testing.T, answering, refusing string) *httptest.Server {
	t.Helper()
	entry := func(name, description string, latest bool, remote string) map[string]any {
		server := map[string]any{"name": name, "description": description, "repository": map[string]any{"url": "https://example.test/" + name}}
		if remote != "" {
			server["remotes"] = []map[string]string{{"type": "streamable-http", "url": remote}}
		} else {
			server["packages"] = []map[string]string{{"registryType": "npm", "identifier": name}}
		}
		return map[string]any{
			"server": server,
			"_meta": map[string]any{"io.modelcontextprotocol.registry/official": map[string]any{
				"isLatest": latest, "status": "active", "publishedAt": "2026-08-30T12:00:00Z",
			}},
		}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/servers" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"servers":  []any{entry("test/things", "Things.", true, answering), entry("test/old", "Old.", false, answering)},
				"metadata": map[string]any{"nextCursor": "page-two"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"servers": []any{entry("test/locked", "Needs a token.", true, refusing), entry("test/local", "Stdio only.", true, "")},
		})
	}))
}

func TestDirectoryWritesRecordGuardfilesAndPages(t *testing.T) {
	upstream := hintedUpstream(t)
	defer upstream.Close()
	locked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "token required", http.StatusUnauthorized)
	}))
	defer locked.Close()
	registry := pagedRegistry(t, upstream.URL+"/mcp", locked.URL+"/mcp")
	defer registry.Close()

	dir := filepath.Join(t.TempDir(), "out")
	var out bytes.Buffer
	if err := runDirectory(context.Background(), &out, []string{"--registry", registry.URL, "-o", dir}); err != nil {
		t.Fatalf("runDirectory: %v", err)
	}
	if got := out.String(); !strings.HasPrefix(got, "directory: 2 listed, 1 answered, 1 refused, 3 tools, 1 allowed at read-only") {
		t.Fatalf("summary = %q", got)
	}

	rec, err := directory.ReadRecord(filepath.Join(dir, directory.RecordFile))
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	states := map[string]string{}
	for _, s := range rec.Servers {
		states[s.Name] = s.State
	}
	if states["test/things"] != "ok" || states["test/locked"] != "HTTP 401" || len(states) != 2 {
		t.Fatalf("states = %v", states)
	}
	for _, s := range rec.Servers {
		if s.Name == "test/things" && (len(s.Tools) != 3 || s.Tools[2].ReadOnly != nil || s.Tools[0].ReadOnly == nil) {
			t.Fatalf("record lost the hint distinction: %+v", s.Tools)
		}
		if s.Published != "2026-08-30" || s.Repository == "" {
			t.Fatalf("record lost registry facts: %+v", s)
		}
	}

	guardfiles, _ := filepath.Glob(filepath.Join(dir, directory.GuardfilesDir, "*.mcp.kdl"))
	if len(guardfiles) != 1 || filepath.Base(guardfiles[0]) != "test__things.mcp.kdl" {
		t.Fatalf("guardfiles = %v", guardfiles)
	}
	var lint bytes.Buffer
	if err := runLint(&lint, []string{guardfiles[0]}); err != nil || lint.String() != "get_thing\nmcp_beaver_info\n" {
		t.Fatalf("lint = %q, %v", lint.String(), err)
	}

	index := readString(t, filepath.Join(dir, "index.html"))
	for _, want := range []string{"test/things", "test/locked", "HTTP 401", `href="guardfiles/test__things.mcp.kdl"`, "p-partial"} {
		if !strings.Contains(index, want) {
			t.Fatalf("index.html lacks %q", want)
		}
	}
	if strings.Contains(index, "test/old") || strings.Contains(index, "test/local") {
		t.Fatalf("index.html lists an entry the sweep should have dropped")
	}
	pages := readString(t, filepath.Join(dir, "guardfiles.html"))
	for _, want := range []string{`mcp-upstream &#34;test/things&#34;`, "can &#34;get_thing&#34;", "annotation-coverage partial annotated=2 silent=1"} {
		if !strings.Contains(pages, want) {
			t.Fatalf("guardfiles.html lacks %q", want)
		}
	}

	// The record is the only input, so a second render from it, with every
	// network fixture gone, reproduces the directory byte for byte.
	upstream.Close()
	locked.Close()
	registry.Close()
	again := filepath.Join(t.TempDir(), "again")
	out.Reset()
	if err := runDirectory(context.Background(), &out, []string{"--from", filepath.Join(dir, directory.RecordFile), "-o", again}); err != nil {
		t.Fatalf("runDirectory --from: %v", err)
	}
	for _, name := range []string{directory.RecordFile, "index.html", "guardfiles.html", filepath.Join(directory.GuardfilesDir, "test__things.mcp.kdl")} {
		if readString(t, filepath.Join(dir, name)) != readString(t, filepath.Join(again, name)) {
			t.Fatalf("%s differs between the sweep and its offline re-render", name)
		}
	}

	// A wider scope from the same record adds grants without a network.
	wide := filepath.Join(t.TempDir(), "wide")
	if err := runDirectory(context.Background(), &out, []string{"--from", filepath.Join(dir, directory.RecordFile), "--scope", "all", "-o", wide}); err != nil {
		t.Fatalf("runDirectory --from --scope all: %v", err)
	}
	if text := readString(t, filepath.Join(wide, directory.GuardfilesDir, "test__things.mcp.kdl")); strings.Count(text, "\n    can ") != 3 {
		t.Fatalf("scope all wrote %q", text)
	}
}

func TestDirectoryRejectsBadArguments(t *testing.T) {
	for name, argv := range map[string][]string{
		"no output":    {"--registry", "http://127.0.0.1:1"},
		"positional":   {"something", "-o", t.TempDir()},
		"bad scope":    {"--scope", "everything", "-o", t.TempDir(), "--registry", "http://127.0.0.1:1"},
		"missing from": {"--from", filepath.Join(t.TempDir(), "absent.json"), "-o", t.TempDir()},
	} {
		if err := runDirectory(context.Background(), &bytes.Buffer{}, argv); err == nil {
			t.Fatalf("%s: accepted %v", name, argv)
		}
	}
}

func TestGuardfileNameIsOneFilePerRegistryName(t *testing.T) {
	for in, want := range map[string]string{
		"ac.tandem/docs-mcp": "ac.tandem__docs-mcp.mcp.kdl",
		"io.example/a b:c":   "io.example__a_b_c.mcp.kdl",
	} {
		if got := directory.GuardfileName(in); got != want {
			t.Fatalf("GuardfileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// hungUpstream answers the handshake and then holds `tools/list` open until
// the client gives up, which is the shape that pinned a full sweep for an
// hour (mcp-beaver#123).
func hungUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	inner := hintedUpstream(t)
	t.Cleanup(inner.Close)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if bytes.Contains(body, []byte(`"tools/list"`)) {
			<-r.Context().Done()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		inner.Config.Handler.ServeHTTP(w, r)
	}))
}

func TestDirectoryTimesOutAHungUpstreamWithoutHoldingTheSweep(t *testing.T) {
	upstream := hintedUpstream(t)
	defer upstream.Close()
	hung := hungUpstream(t)
	defer hung.Close()
	registry := pagedRegistry(t, upstream.URL+"/mcp", hung.URL+"/mcp")
	defer registry.Close()

	dir := filepath.Join(t.TempDir(), "out")
	started := time.Now()
	var out bytes.Buffer
	if err := runDirectory(context.Background(), &out, []string{"--registry", registry.URL, "--timeout", "500ms", "-o", dir}); err != nil {
		t.Fatalf("runDirectory: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("the hung upstream held the sweep for %s", elapsed)
	}
	rec, err := directory.ReadRecord(filepath.Join(dir, directory.RecordFile))
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	states := map[string]string{}
	for _, s := range rec.Servers {
		states[s.Name] = s.State
	}
	if states["test/things"] != "ok" || states["test/locked"] != "timeout" {
		t.Fatalf("states = %v", states)
	}
}

// openStreamUpstream answers the handshake and then holds the standalone GET
// SSE stream open with headers already sent, which is the shape that parked
// five workers inside the SDK's Connect on the second full sweep.
func openStreamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	inner := hintedUpstream(t)
	t.Cleanup(inner.Close)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
}

func TestDirectoryTimesOutAnUpstreamThatHoldsItsStreamOpen(t *testing.T) {
	upstream := hintedUpstream(t)
	defer upstream.Close()
	open := openStreamUpstream(t)
	defer open.Close()
	registry := pagedRegistry(t, upstream.URL+"/mcp", open.URL+"/mcp")
	defer registry.Close()

	dir := filepath.Join(t.TempDir(), "out")
	started := time.Now()
	if err := runDirectory(context.Background(), &bytes.Buffer{}, []string{"--registry", registry.URL, "--timeout", "500ms", "-o", dir}); err != nil {
		t.Fatalf("runDirectory: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("the open stream held the sweep for %s", elapsed)
	}
	rec, err := directory.ReadRecord(filepath.Join(dir, directory.RecordFile))
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	for _, s := range rec.Servers {
		switch s.Name {
		case "test/things":
			if s.State != "ok" {
				t.Fatalf("answering upstream settled as %q", s.State)
			}
		case "test/locked":
			if s.State != "timeout" && s.State != "ok" {
				t.Fatalf("open-stream upstream settled as %q", s.State)
			}
		}
	}
}
