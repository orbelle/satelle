//go:build integration && unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRiskTierRecord(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	block := shBlock0(t, ".satelle/skills/satelle-risk-tier-record.md")
	for _, c := range []struct {
		name, tags, stdin, wantSet, wantOut string
	}{
		{"no risk tag", `[]`, `{"notes":"TIER: low\nsmall change"}`, "story set sto_test --add-tag risk:low", "recorded risk:low on sto_test"},
		{"another risk tag", `["risk:high","area:cli"]`, `{"notes":"TIER: low\nsmall change"}`, "story set sto_test --add-tag risk:low --remove-tag risk:high", "recorded risk:low on sto_test"},
		{"no TIER line", `[]`, `{"notes":"looks fine"}`, "story set sto_test --add-tag risk:high", "no TIER line"},
		{"panel", `[]`, `{"members":[{"notes":"TIER: high"}]}`, "story set sto_test --add-tag risk:high", "recorded risk:high on sto_test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.MkdirAll(filepath.Join(dir, ".satelle"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".satelle", "satelle.toml"), []byte("[review]\ntier_tag = \"risk\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "tags.json"), []byte(`{"tags":`+c.tags+`}`), 0o644); err != nil {
				t.Fatal(err)
			}
			stub := "#!/bin/sh\necho \"$*\" >> \"" + filepath.Join(dir, "calls") + "\"\nif [ \"$2\" = get ]; then cat \"" + filepath.Join(dir, "tags.json") + "\"; fi\nexit 0\n"
			if err := os.WriteFile(filepath.Join(bin, "satelle"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/bash", "-c", block)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SATELLE_ITEM=sto_test")
			cmd.Stdin = strings.NewReader(c.stdin)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("exit: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), c.wantOut) {
				t.Fatalf("output %q lacks %q", out, c.wantOut)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			var set string
			for _, l := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if strings.HasPrefix(l, "story set") {
					set = l
				}
			}
			if set != c.wantSet {
				t.Fatalf("set call %q, want %q", set, c.wantSet)
			}
		})
	}
}
