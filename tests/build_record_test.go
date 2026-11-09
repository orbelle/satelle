//go:build integration

package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
)

// Every satelle run records its own build for the binary on disk, and the
// footer's lookup of the CLI beside satelled finds it (sto_49b6e762).
func TestEveryRunRecordsItsBuild(t *testing.T) {
	s := newSandbox(t)
	line := strings.TrimSpace(s.must("version"))
	home := s.env["SATELLE_HOME"]
	if got := buildinfo.Lookup(home, buildinfo.CLIBeside(satelledBin)); got != line {
		t.Fatalf("the CLI beside satelled reads %q, want the line it printed %q (record %s)", got, line, filepath.Join(home, buildinfo.RecordsFile))
	}
}
