package substrate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/substrate"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	data := filepath.Join(t.TempDir(), ".satelle")
	for rel, body := range files {
		p := filepath.Join(data, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

func TestRelativeReadsReportsEachCommandLineThatReadsTheSubstrateByARelativePath(t *testing.T) {
	data := writeTree(t, map[string]string{
		"skills/s/scripts/run.sh": "#!/bin/sh\n# .satelle/comment\nsed -n p .satelle/remotes/x/remote.toml\nx=.satelle/y\ncat \"$SATELLE_SUBSTRATE_DIR/x\"\necho \".satelle/a/b is gone\" >&2\n",
		"skills/s/SKILL.md":       "---\nname: s\n---\n\nProse reads `cat .satelle/inline`.\n\n```sh\ncat ./.satelle/a\nv=$(cat .satelle/b)\n# .satelle/c\n```\n\n```toml\nfile = \".satelle/toml\"\n```\n\n    cat .satelle/indented\n",
		"tasks/t.md":              "# t\n\n```check\ntest -f <.satelle/t.toml\n```\n",
		"skills/testdata/x.sh":    "cat .satelle/testdata\n",
		"remotes/r/remote.toml":   "version = 1\n\n[connect]\nargv = [\"cat\", \".satelle/z\"]\n\n[setup]\non_create = [\n  \"satelle version\",\n  \"test -f .satelle/satelle.toml\",\n]\n",
	})
	got, err := substrate.RelativeReads(data, []string{"remotes/r/remote.toml", "remotes/gone/remote.toml"})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		file string
		line int
		why  string
	}{
		{".satelle/remotes/r/remote.toml", 9, `reads .satelle/satelle.toml relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/satelle.toml"`},
		{".satelle/skills/s/SKILL.md", 8, `reads ./.satelle/a relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/a"`},
		{".satelle/skills/s/SKILL.md", 9, `reads .satelle/b relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/b"`},
		{".satelle/skills/s/scripts/run.sh", 3, `reads .satelle/remotes/x/remote.toml relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/remotes/x/remote.toml"`},
		{".satelle/skills/s/scripts/run.sh", 6, `reads .satelle/a/b relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/a/b"`},
		{".satelle/tasks/t.md", 4, `reads .satelle/t.toml relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/t.toml"`},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d findings, want %d:\n%v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.File != w.file || g.Line != w.line || g.Why != w.why || g.Kind != substrate.RelativeField {
			t.Errorf("finding %d = %+v, want %s:%d %s", i, g, w.file, w.line, w.why)
		}
	}
}

func TestRelativeReadsJoinsSeveralPathsOnOneLine(t *testing.T) {
	data := writeTree(t, map[string]string{"skills/a.sh": "diff .satelle/one ./.satelle/two\n"})
	got, err := substrate.RelativeReads(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `reads .satelle/one, ./.satelle/two relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/one", "$SATELLE_SUBSTRATE_DIR/two"`
	if len(got) != 1 || got[0].Why != want {
		t.Fatalf("got %+v, want one finding %q", got, want)
	}
}

func TestRelativeReadsFindsNothingInTheInitTemplate(t *testing.T) {
	data := filepath.Join(t.TempDir(), ".satelle")
	if err := kernel.Init(data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "skills")); err != nil {
		t.Fatalf("init wrote no skills: %v", err)
	}
	got, err := substrate.RelativeReads(data, kernel.RemoteFiles(data))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, f := range got {
		lines = append(lines, f.String())
	}
	if len(got) != 0 {
		t.Fatalf("the init template reads the substrate relative to the working directory:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRelativeReadsSkipsAQuotedHeredocBodyButReportsTheReadBesideIt(t *testing.T) {
	data := writeTree(t, map[string]string{
		"tasks/b.md": "```sh\nsatelle task create --body - <<'EOF'\nACTION: follow .satelle/tasks/b.md\nEOF\ncat .satelle/tasks/b.md\n```\n",
	})
	got, err := substrate.RelativeReads(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].File != ".satelle/tasks/b.md" || got[0].Line != 5 {
		t.Fatalf("got %+v, want one finding at .satelle/tasks/b.md:5", got)
	}
}
