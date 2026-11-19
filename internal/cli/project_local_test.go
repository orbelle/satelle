package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

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
	help := strings.Join(strings.Fields(must(t, "help", "projects")), " ")
	for _, want := range []string{
		"satelled's Projects page lists this machine's projects",
		"when a satelle command there writes its ledger",
		"A repo under the temp dir (the system temp dir and `/tmp`) pushes nothing, added or not",
		"marks those `not-added`",
		"is bound to a satelle.dev project but is not one of this machine's projects says so",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help projects lacks %q", want)
		}
	}
}

func regexpLine(out string, fields ...string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= len(fields) && strings.Join(f[:len(fields)], " ") == strings.Join(fields, " ") {
			return true
		}
	}
	return false
}

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

func TestPartitionsMarkStrayAndPruneDropsThem(t *testing.T) {
	m := newMachineWithHomeUI(t)
	if out := runOn(t, m, "project", "add"); !strings.Contains(out, "seeded satelled at") {
		t.Fatalf("project add: %s", out)
	}
	key := config.RepoKey(m.dir)
	strayPath := t.TempDir()
	ctx := context.Background()
	if _, err := m.ms.TouchPartition(ctx, "stray-0000beef", "stray", time.Now()); err != nil {
		t.Fatal(err)
	}
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: "stray", RepoRoot: strayPath})
	if err := m.ms.ReplaceKind(ctx, "stray-0000beef", "identity", []mirror.ItemRow{{ID: "meta", Payload: string(ident)}}, time.Now()); err != nil {
		t.Fatal(err)
	}

	out := must(t, "project", "partitions")
	if !regexpLine(out, "REPO_KEY", "SLUG", "STORY", "TASK", "PROJECT", "PATH") {
		t.Errorf("partitions header: %q", out)
	}
	var own, stray []string
	notAdded := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && f[0] != "REPO_KEY" && slices.Contains(f, "not-added") {
			notAdded++
		}
		switch {
		case len(f) >= 6 && f[0] == key:
			own = f
		case len(f) >= 6 && f[0] == "stray-0000beef":
			stray = f
		}
	}
	if len(own) < 6 || own[4] != "added" {
		t.Errorf("this machine's project is not marked added: %v\n%s", own, out)
	}
	if len(stray) < 6 || stray[4] != "not-added" || stray[5] != strayPath {
		t.Errorf("the stray partition is not marked not-added: %v\n%s", stray, out)
	}
	if !strings.Contains(out, fmt.Sprintf("%d partition(s) are not one of this machine's projects; satelle project prune <repo_key> drops one", notAdded)) {
		t.Errorf("partitions does not name the stray count:\n%s", out)
	}

	if out := must(t, "project", "prune", "stray-0000beef"); !strings.Contains(out, "dropped partition stray-0000beef") {
		t.Errorf("prune of a not-added partition whose path exists: %q", out)
	}
	if _, err := run(t, "", "project", "prune", key); err == nil || !strings.Contains(err.Error(), "is one of this machine's projects; satelle project remove drops it") {
		t.Errorf("prune of an added project's partition: %v", err)
	}
	if out := must(t, "project", "prune", key, "--force"); !strings.Contains(out, "dropped partition "+key) {
		t.Errorf("prune --force: %q", out)
	}
}
