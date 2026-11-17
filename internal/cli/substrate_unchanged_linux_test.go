//go:build linux

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
)

func TestSessionStartVerbsLeaveTheSubstrate(t *testing.T) {
	cmds := map[string][]string{
		"version":          {"version"},
		"update":           {"update"},
		"doctor":           {"doctor"},
		"doctor --resolve": {"doctor", "--resolve"},
		"hook context":     {"hook", "context"},
	}
	for name, args := range cmds {
		t.Run(name, func(t *testing.T) {
			dir := isolate(t)
			noTerminal(t)
			must(t, "init")
			gc, err := config.LoadGlobal()
			if err != nil {
				t.Fatal(err)
			}
			gc.Projects.Add(dir)
			if err := config.SaveGlobal(gc); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(dir, ".satelle")
			writeFile(t, filepath.Join(data, "skills", "ran-check.md"), "---\nname: ran-check\ntype: check\ndescription: Prints a line and changes nothing.\n---\n\n```sh\necho ran-check was here\nexit 1\n```\n")
			writeFile(t, filepath.Join(data, "doctor.toml"), "[[check]]\nskill = \"ran-check\"\nfix = \"look\"\n")
			commitAll(t, dir, "fixture")
			gittest.Git(t, dir, "rm", "-q", ".satelle/session.md")
			commitAll(t, dir, "remove session.md")
			cli, daemon := fakeInstall(t, true)
			releaseServer(t, "v2.0.0", []string{"v2.0.0", "serve-v0.2.0", "serve-v0.1.0"}, map[string]string{
				"v2.0.0/" + asset("satelle", "2.0.0"):        "#!/bin/sh\necho 'satelle 2.0.0 (commit c, built b)'\n",
				"serve-v0.2.0/" + asset("satelled", "0.2.0"): "#!/bin/sh\necho 'satelled 0.2.0 (commit d, built b)'\n",
			}, "")
			status := func() string {
				return strings.TrimSpace(gittest.Git(t, dir, "status", "--porcelain", "--untracked-files=all", "--", ".satelle"))
			}
			if st := status(); st != "" {
				t.Fatalf("fixture is dirty:\n%s", st)
			}
			ids := itemIDs(t)
			before := digest(t, data)
			stdin := ""
			if name == "hook context" {
				stdin = fmt.Sprintf(`{"session_id":"s-start","cwd":%q}`, dir)
			}
			out, err := run(t, stdin, args...)
			if name == "version" || name == "update" || name == "hook context" {
				if err != nil {
					t.Fatalf("%s: %v\n%s", name, err, out)
				}
			}
			if st := status(); st != "" {
				t.Errorf("%s left .satelle changed:\n%s", name, st)
			}
			if d := sameDigest(before, digest(t, data)); len(d) != 0 {
				t.Errorf("%s wrote under .satelle/: %v", name, d)
			}
			if got := itemIDs(t); !slices.Equal(got, ids) {
				t.Errorf("%s opened an item: %v", name, got)
			}
			switch name {
			case "doctor", "doctor --resolve":
				for _, w := range []string{"check:ran-check", "ran-check was here", initProblem("session.md")} {
					if !strings.Contains(out, w) {
						t.Errorf("%s output lacks %q:\n%s", name, w, out)
					}
				}
			case "hook context":
				if !strings.Contains(out, "check:ran-check") {
					t.Errorf("hook context does not name the check:\n%s", out)
				}
			case "update":
				if b, _ := os.ReadFile(cli); !strings.Contains(string(b), "satelle 2.0.0") {
					t.Errorf("update left the satelle binary: %s", b)
				}
				if b, _ := os.ReadFile(daemon); !strings.Contains(string(b), "satelled 0.2.0") {
					t.Errorf("update left satelled: %s", b)
				}
				home := config.GlobalDir()
				if _, err := os.Stat(filepath.Join(home, buildinfo.RecordsFile)); err != nil {
					t.Errorf("update wrote no %s: %v", buildinfo.RecordsFile, err)
				}
				if buildinfo.Lookup(home, cli) == "" || buildinfo.Lookup(home, daemon) == "" {
					t.Error("builds.json does not name both binaries")
				}
			}
		})
	}
}
