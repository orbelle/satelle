//go:build integration && unix

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

const (
	packVersionSkill = "satelle-pack-version-check"
	packVersionCheck = "check:" + packVersionSkill
)

func packCheckSandbox(t *testing.T, server, ref, version string) *sandbox {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("jq is required by %s and is not on PATH", packVersionSkill)
	}
	s := newSandbox(t)
	s.env["SATELLE_TOKEN"] = devToken
	writeFile(t, filepath.Join(s.env["SATELLE_HOME"], config.GlobalConfigName), fmt.Sprintf("[hosted]\nserver = %q\npush = false\n", server), 0o644)
	s.must("init")
	s.write(".satelle/doctor.toml", repoText(t, ".satelle/doctor.toml"))
	s.write(".satelle/skills/satelle-drift-ownership-check.md", repoText(t, ".satelle/skills/satelle-drift-ownership-check.md"))
	body := "---\nname: review\ntype: skill\ndescription: review skill\n---\n\nreview.\n"
	s.write(".satelle/skills/review/SKILL.md", body)
	sum := sha256.Sum256([]byte(body))
	s.write(".satelle/packs.toml", fmt.Sprintf(`[[pack]]
ref = %q
name = "review"
version = %q
digest = "%s"
skills = ["review"]

  [[pack.file]]
  path = "review/SKILL.md"
  sha256 = "%s"
`, ref, version, strings.Repeat("0", 64), hex.EncodeToString(sum[:])))
	return s
}

func seedReview(srv *fakeserver.Server, ref string, versions ...string) {
	for _, v := range versions {
		b, _ := fakeserver.PackBundle("review", v, []fakeserver.PackFile{{Path: "review/SKILL.md", Body: []byte(v)}})
		srv.SeedPack(ref, v, b)
	}
}

func TestPackVersionCheck(t *testing.T) {
	t.Parallel()

	t.Run("AC2 newer version is an issue", func(t *testing.T) {
		t.Parallel()
		srv := fakeserver.Start(t, devToken)
		seedReview(srv, "acme/review", "1.0.0", "1.1.0")
		s := packCheckSandbox(t, srv.URL, "acme/review", "1.0.0")
		rep := doctorJSON(t, s, satelleBin)
		got := checkIssues(rep, packVersionCheck)
		if len(got) != 1 {
			t.Fatalf("%s issues = %+v, want one", packVersionCheck, got)
		}
		p := got[0].Problem
		for _, want := range []string{"acme/review", "1.0.0", "1.1.0", "satelle pack update acme/review --version 1.1.0"} {
			if !strings.Contains(p, want) {
				t.Errorf("issue %q lacks %q", p, want)
			}
		}
		if !strings.Contains(got[0].Fix, "update command the issue names") {
			t.Errorf("fix %q", got[0].Fix)
		}
	})

	t.Run("AC3 newest raises no issue", func(t *testing.T) {
		t.Parallel()
		srv := fakeserver.Start(t, devToken)
		seedReview(srv, "acme/current", "1.0.0")
		s := packCheckSandbox(t, srv.URL, "acme/current", "1.0.0")
		rep := doctorJSON(t, s, satelleBin)
		if got := checkIssues(rep, packVersionCheck); len(got) != 0 {
			t.Errorf("%s issues = %+v, want none", packVersionCheck, got)
		}
		if got := checkNotes(rep, packVersionCheck); len(got) != 0 {
			t.Errorf("%s notes = %v, want none", packVersionCheck, got)
		}
	})

	t.Run("AC4 unreachable marketplace is a note", func(t *testing.T) {
		t.Parallel()
		s := packCheckSandbox(t, "http://127.0.0.1:1", "acme/review", "1.0.0")
		out := s.run("doctor").out
		if want := "note: " + packVersionCheck + ": acme/review 1.0.0: the marketplace could not be read"; !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
		rep := doctorJSON(t, s, satelleBin)
		if got := checkIssues(rep, packVersionCheck); len(got) != 0 {
			t.Errorf("%s issues = %+v, want none", packVersionCheck, got)
		}
		for _, is := range rep.Issues {
			if strings.Contains(is.Problem, "could not check") {
				t.Errorf("issue %+v", is)
			}
		}
	})

	t.Run("unplain record raises no runnable fix", func(t *testing.T) {
		t.Parallel()
		srv := fakeserver.Start(t, devToken)
		s := packCheckSandbox(t, srv.URL, "-x/y", "1.0.0")
		rep := doctorJSON(t, s, satelleBin)
		got := checkIssues(rep, packVersionCheck)
		if len(got) != 1 || !strings.Contains(got[0].Problem, "could not check") {
			t.Errorf("%s issues = %+v, want one could not check", packVersionCheck, got)
		}
		for _, is := range rep.Issues {
			if strings.Contains(is.Problem, "satelle pack update") {
				t.Errorf("issue names an update command: %+v", is)
			}
		}
	})
}

func TestPackVersionCheckShipsWithInit(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	got, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "skills", packVersionSkill+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != repoText(t, ".satelle/skills/"+packVersionSkill+".md") {
		t.Errorf("init's %s differs from this repo's copy", packVersionSkill)
	}
	if string(got) != repoText(t, "internal/kernel/template/skills/"+packVersionSkill+".md") {
		t.Errorf("init's %s differs from the template", packVersionSkill)
	}
}
