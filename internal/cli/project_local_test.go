package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

// This machine's projects are added, listed (with the satelle.dev project
// each is bound to) and removed under satelle project; satelle workspace is
// satelle.dev's workspaces only.
func TestProjectsAreLocalAndWorkspacesAreSatelleDev(t *testing.T) {
	m := newMachine(t)
	srv := fakeserver.Start(t, testToken)
	name := filepath.Base(m.dir)

	if out := must(t, "project", "add"); !strings.Contains(out, "added project "+m.dir) {
		t.Fatalf("project add = %q", out)
	}
	if out := must(t, "project", "list"); !strings.Contains(out, "STATE") || !regexpLine(out, "ok", name, "-", m.dir) {
		t.Fatalf("project list = %q", out)
	}
	must(t, "project", "create", "beta", "--bind", "--server", srv.URL)
	if out := must(t, "project", "list"); !regexpLine(out, "ok", name, "beta", m.dir) {
		t.Errorf("project list after bind = %q", out)
	}
	if out := must(t, "project", "list", "--hosted", "--server", srv.URL); !strings.Contains(out, "*  beta") {
		t.Errorf("project list --hosted = %q", out)
	}
	if _, err := run(t, "", "project", "list", "--server", srv.URL); err == nil || !strings.Contains(err.Error(), "add --hosted") {
		t.Errorf("project list --server without --hosted: %v", err)
	}
	if out := must(t, "workspace", "list", "--server", srv.URL); !regexpLine(out, "wsp_personal", "personal", "personal") {
		t.Errorf("workspace list = %q", out)
	}
	cfg := filepath.Join(os.Getenv("SATELLE_HOME"), "config.toml")
	before, _ := os.ReadFile(cfg)
	if _, err := run(t, "", "workspace", "add"); err == nil || !strings.Contains(err.Error(), "satelle project add") || !strings.Contains(err.Error(), "satelle.dev") {
		t.Errorf("workspace add: %v", err)
	}
	if after, _ := os.ReadFile(cfg); string(after) != string(before) {
		t.Error("workspace add changed the machine config")
	}
	if out := must(t, "project", "remove"); !strings.Contains(out, "removed project "+m.dir) {
		t.Errorf("project remove = %q", out)
	}
	if out := must(t, "project", "list"); strings.Contains(out, m.dir) {
		t.Errorf("project list after remove = %q", out)
	}
	if out := must(t, "help", "projects"); !strings.Contains(out, "is a repo or directory") || !strings.Contains(out, "is a collection of projects on satelle.dev") {
		t.Errorf("help projects = %q", out)
	}
}

// regexpLine reports whether some line of out holds the fields in order.
func regexpLine(out string, fields ...string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= len(fields) && strings.Join(f[:len(fields)], " ") == strings.Join(fields, " ") {
			return true
		}
	}
	return false
}

// Every command, flag and help line that says workspace means satelle.dev,
// and no local-project verb sits under satelle workspace.
func TestWorkspaceWordMeansSatelleDev(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, text := range []string{c.Short, c.Long} {
			if strings.Contains(strings.ToLower(text), "workspace") && !strings.Contains(text, "satelle.dev") {
				t.Errorf("%s: %q says workspace without satelle.dev", c.CommandPath(), text)
			}
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(strings.ToLower(f.Usage), "workspace") && !strings.Contains(f.Usage, "satelle.dev") {
				t.Errorf("%s --%s: %q", c.CommandPath(), f.Name, f.Usage)
			}
		})
		if c.Parent() != nil && c.Parent().Name() == "workspace" {
			switch c.Name() {
			case "add", "remove", "partitions", "prune":
				t.Errorf("satelle workspace %s still exists", c.Name())
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(NewRootCmd())
}
