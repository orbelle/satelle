//go:build !windows

package kernel

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

func injectPart(t *testing.T, constitution bool, settings string) string {
	t.Helper()
	root := skillRoot(t, skillRepos)
	dir := filepath.Join(root, PartsDir, "x")
	if constitution {
		write(t, filepath.Join(dir, ConstitutionFile), "X CONSTITUTION\n")
	}
	write(t, filepath.Join(dir, PrinciplesDir, "p.md"), "X P\n")
	write(t, filepath.Join(dir, SkillsDir, "s.md"), "X S\n")
	write(t, filepath.Join(dir, SettingsFile), settings)
	return root
}

func TestPartInjectEntries(t *testing.T) {
	root := injectPart(t, true, "[hook]\ninject = [\"constitution.md\", \"principles/p.md\"]\n")
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Parts["x"]
	if len(p.Problems) > 0 {
		t.Fatalf("problems: %v", p.Problems)
	}
	want := []string{PartsDir + "/x/" + ConstitutionFile, PartsDir + "/x/" + PrinciplesDir + "/p.md"}
	if !reflect.DeepEqual(p.Inject, want) {
		t.Errorf("Inject %q, want %q", p.Inject, want)
	}
	k, _ := openSkills(t, root)
	got, err := k.InjectFor(item.Item{ID: "i", Repo: "x"})
	abs := []string{filepath.Join(k.SubstrateDir(), want[0]), filepath.Join(k.SubstrateDir(), want[1])}
	if err != nil || !reflect.DeepEqual(got, abs) {
		t.Errorf("InjectFor x: %q %v, want %q", got, err, abs)
	}
	for _, repo := range []string{"", reporeg.Self, "y"} {
		if got, err := k.InjectFor(item.Item{ID: "i", Repo: repo}); err != nil || got != nil {
			t.Errorf("InjectFor %q: %q %v, want nil", repo, got, err)
		}
	}

	for _, c := range []struct {
		name, settings string
		constitution   bool
		want           string
	}{
		{"parent", `inject = ["../satelle.toml"]`, true, "inject entry '../satelle.toml' is not this part's constitution.md"},
		{"dot", `inject = ["./constitution.md"]`, true, "inject entry './constitution.md' is not this part's"},
		{"missing principle", `inject = ["principles/missing.md"]`, true, "inject entry 'principles/missing.md' is not this part's"},
		{"skill", `inject = ["skills/s.md"]`, true, "inject entry 'skills/s.md' is not this part's"},
		{"no constitution", `inject = ["constitution.md"]`, false, "inject entry constitution.md is not this part's"},
		{"repeat", `inject = ["principles/p.md", "principles/p.md"]`, true, "inject entry 'principles/p.md' is listed twice"},
		{"unknown key", "refresh = true", true, "unknown key hook.refresh"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := injectPart(t, c.constitution, "[hook]\n"+c.settings+"\n")
			cfg, err := LoadConfig(root)
			if err != nil {
				t.Fatal(err)
			}
			probs := cfg.Parts["x"].Problems
			if len(probs) != 1 {
				t.Fatalf("problems %v, want one", probs)
			}
			msg := probs[0].Error()
			if !strings.HasPrefix(msg, PartsDir+"/x/"+SettingsFile+": ") || !strings.Contains(msg, c.want) || reporeg.HasUnsafe(msg) {
				t.Errorf("problem %q, want %q under %s/x/%s", msg, c.want, PartsDir, SettingsFile)
			}
			if cfg.PartInject()["x"] != nil {
				t.Errorf("a broken part is in PartInject")
			}
			k, _ := openSkills(t, root)
			if got, err := k.InjectFor(item.Item{ID: "i", Repo: "x"}); !Refused(err) || got != nil {
				t.Errorf("InjectFor: %q %v, want a refusal", got, err)
			}
		})
	}

	for _, settings := range []string{"", "[hook]\ninject = []\n", "[workflows]\n"} {
		root := injectPart(t, true, settings)
		cfg, err := LoadConfig(root)
		if err != nil || len(cfg.Parts["x"].Problems) > 0 || cfg.Parts["x"].Inject != nil {
			t.Errorf("settings %q: %v %+v", settings, err, cfg.Parts["x"])
		}
	}
}
