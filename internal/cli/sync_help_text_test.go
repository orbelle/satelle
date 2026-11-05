package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
)

// syncScopeKeyUses names each place text uses a removed [sync] scope key as a
// setting: the phrase "[sync] <key>" anywhere, or a "<key> =" line (commented or
// not) inside a [sync] block — from a [sync] header to the next table header —
// so another table's same-named key ([substrate_roots] skills) is not one.
func syncScopeKeyUses(text string) []string {
	type pat struct {
		key          string
		phrase, line *regexp.Regexp
	}
	var pats []pat
	for _, k := range config.RemovedSyncKeys {
		q := regexp.QuoteMeta(k)
		pats = append(pats, pat{k, regexp.MustCompile(`\[sync\]\s+` + q + `\b`), regexp.MustCompile(`^\s*#?\s*` + q + `\s*=`)})
	}
	var uses []string
	inSync := false
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#")); strings.HasPrefix(s, "[") {
			inSync = strings.HasPrefix(s, "[sync]")
		}
		for _, p := range pats {
			if p.phrase.MatchString(line) || (inSync && p.line.MatchString(line)) {
				uses = append(uses, p.key+": "+strings.TrimSpace(line))
			}
		}
	}
	return uses
}

// AC8 (sty_b336c2b0): the help of satelle sync and every command under it, of
// satelle project bind, and the init scaffold of satelle.toml use no removed
// [sync] scope key as a setting; neither does what the sync verbs print for an
// unbound repo.
func TestHelpNamesNoSyncScopeKeys(t *testing.T) {
	var paths [][]string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		paths = append(paths, path)
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" {
				continue
			}
			walk(sub, append(append([]string{}, path...), sub.Name()))
		}
	}
	root := NewRootCmd()
	for _, c := range root.Commands() {
		switch c.Name() {
		case "sync":
			walk(c, []string{"sync"})
		case "project":
			paths = append(paths, []string{"project", "bind"})
		}
	}
	if len(paths) < 10 {
		t.Fatalf("test premise: found only %d help pages: %v", len(paths), paths)
	}
	for _, p := range paths {
		out, err := runRoot(t, append(append([]string{}, p...), "--help")...)
		if err != nil {
			t.Fatalf("%v --help: %v\n%s", p, err, out)
		}
		for _, u := range syncScopeKeyUses(out) {
			t.Errorf("satelle %s --help uses a removed [sync] scope key — %s", strings.Join(p, " "), u)
		}
	}
	for _, u := range syncScopeKeyUses(scaffoldToml) {
		t.Errorf("the init scaffold uses a removed [sync] scope key — %s", u)
	}

	tempRepo(t)
	for _, args := range [][]string{
		{"sync"}, {"sync", "status"}, {"sync", "push"}, {"sync", "pull"},
		{"sync", "documents", "push"}, {"sync", "documents", "pull"},
		{"sync", "workstate", "push"}, {"sync", "workstate", "pull"},
	} {
		out, _ := runRoot(t, args...)
		if strings.Contains(out, "Set [sync]") {
			t.Errorf("satelle %s tells an unbound repo to set a [sync] key:\n%s", strings.Join(args, " "), out)
		}
		for _, u := range syncScopeKeyUses(out) {
			t.Errorf("satelle %s output uses a removed [sync] scope key — %s", strings.Join(args, " "), u)
		}
	}
}
