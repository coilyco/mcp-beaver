package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/coilyco/mcp-beaver/internal/mcpserver"
)

func TestVersionCommandPrintsTheStampedVersion(t *testing.T) {
	stdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = write
	runErr := run([]string{"version"})
	_ = write.Close()
	os.Stdout = stdout
	if runErr != nil {
		t.Fatalf("run version: %v", runErr)
	}
	out := make([]byte, 64)
	n, _ := read.Read(out)
	if got := strings.TrimSpace(string(out[:n])); got != mcpserver.Version {
		t.Fatalf("version printed %q, want %q", got, mcpserver.Version)
	}
}

// The release build stamps the version through -X, which names the symbol as a
// string. A package move or a rename would leave that string pointing at
// nothing, the linker would not complain, and every released binary would ship
// saying "dev". So the path is asserted against the real one.
func TestReleaseBuildStampsTheRealVersionSymbol(t *testing.T) {
	pkg := reflect.TypeOf(mcpserver.Pulled{}).PkgPath()
	want := pkg + ".Version="
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "release-build.sh"))
	if err != nil {
		t.Fatalf("read release-build.sh: %v", err)
	}
	if !strings.Contains(string(script), want) {
		t.Fatalf("release-build.sh does not stamp %q; a rename left the release unstamped", want)
	}
}

// A build no release produced says so, rather than claiming a version.
func TestVersionDefaultsToDev(t *testing.T) {
	if mcpserver.Version != "dev" {
		t.Skipf("running from a stamped build: %s", mcpserver.Version)
	}
}
