package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/help"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/substrate"
)

func TestValidateReportsSubstrateDrift(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	skill := filepath.Join(dir, ".satelle", "skills", "stale.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(skill, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("---\nname: stale\ntype: skill\ndescription: Names what the binary lacks.\n---\n\nRun `satelle nosuchcmd <id>`.\n\nSee [[missing]].\n")

	out, err := run(t, "", "validate")
	if err == nil {
		t.Fatalf("validate passed on a drifted substrate:\n%s", out)
	}
	for _, w := range []string{".satelle/skills/stale.md:7: command: satelle nosuchcmd — ", ".satelle/skills/stale.md:9: link: missing — "} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("validate error lacks %q:\n%v", w, err)
		}
	}

	out, err = run(t, "", "validate", "--json")
	if err == nil {
		t.Fatalf("validate --json passed on a drifted substrate:\n%s", out)
	}
	var rep struct {
		Valid     bool                `json:"valid"`
		Problems  []string            `json:"problems"`
		Substrate []substrate.Finding `json:"substrate"`
	}
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("validate --json: %v\n%s", jerr, out)
	}
	if rep.Valid || len(rep.Problems) != 0 || len(rep.Substrate) != 2 {
		t.Fatalf("want valid:false, no problems and two substrate entries:\n%s", out)
	}
	if f := rep.Substrate[0]; f.Kind != substrate.KindCommand || f.File != ".satelle/skills/stale.md" || f.Line != 7 {
		t.Errorf("first entry %+v", f)
	}
	if f := rep.Substrate[1]; f.Kind != substrate.KindLink || f.File != ".satelle/skills/stale.md" || f.Line != 9 || f.Subject != "missing" {
		t.Errorf("second entry %+v", f)
	}

	must(t, "story", "list")

	write("---\nname: stale\ntype: skill\ndescription: Names what the binary has.\n---\n\nRun `satelle story get <id>`.\n\nSee [[stale]].\n")
	out = must(t, "validate", "--json")
	if !strings.Contains(out, `"substrate": []`) || !strings.Contains(out, `"valid": true`) {
		t.Fatalf("fixed substrate: want valid and substrate []:\n%s", out)
	}
}

func repoLinks(t *testing.T) map[string]bool {
	t.Helper()
	links := map[string]bool{}
	for _, d := range []string{"principles", "skills"} {
		ents, err := os.ReadDir(filepath.Join("..", "..", ".satelle", d))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			links[strings.TrimSuffix(e.Name(), ".md")] = true
		}
	}
	return links
}

func TestHelpTopicsNameTheBinary(t *testing.T) {
	links := repoLinks(t)
	tree := substrateTree(NewRootCmd())
	for _, tp := range help.List() {
		for _, f := range substrate.CheckText("internal/help/topics/"+tp.Name+".md", "md", tp.Body, tree, links) {
			t.Error(f.String())
		}
	}
}

var reReadmeTarget = regexp.MustCompile(`\]\(([^)\s]+)\)`)

var reReadmeSpan = regexp.MustCompile("`([^`]+)`")

var reReadmeFile = regexp.MustCompile(`\.(md|toml|json|ts|sh|go|yml|yaml|version)$`)

func readmePath(span string) bool {
	if strings.ContainsAny(span, " <>~$*|") || strings.HasPrefix(span, "-") || strings.Contains(span, "://") || strings.Contains(span, "...") {
		return false
	}
	return strings.Contains(span, "/") || reReadmeFile.MatchString(span)
}

func readmePathMisses(root, text string) []string {
	written := map[string]bool{}
	for _, h := range hook.Harnesses {
		if f, err := hook.File(h); err == nil {
			written[f] = true
		}
	}
	exists := func(p string) bool {
		p = filepath.FromSlash(strings.TrimSuffix(p, "/"))
		for _, base := range []string{root, filepath.Join(root, config.DefaultDataDir)} {
			if _, err := os.Stat(filepath.Join(base, p)); err == nil {
				return true
			}
		}
		return false
	}
	var out []string
	fence := false
	for n, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			continue
		}
		var paths []string
		if fence {
			for _, w := range strings.Fields(line) {
				if w = strings.Trim(w, "\"'(),:;"); readmePath(w) {
					paths = append(paths, w)
				}
			}
		}
		for _, m := range reReadmeTarget.FindAllStringSubmatch(line, -1) {
			if t := m[1]; !strings.Contains(t, "://") && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "mailto:") {
				t, _, _ = strings.Cut(t, "#")
				paths = append(paths, t)
			}
		}
		for _, m := range reReadmeSpan.FindAllStringSubmatch(line, -1) {
			if readmePath(m[1]) {
				paths = append(paths, m[1])
			}
		}
		for _, p := range paths {
			if !written[p] && !exists(p) {
				out = append(out, fmt.Sprintf("README.md:%d: path: %s — not in this repo, its .satelle, or a harness hook file", n+1, p))
			}
		}
	}
	return out
}

func TestReadmeNamesTheBinaryAndRepo(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range substrate.CheckText("README.md", "md", string(b), substrateTree(NewRootCmd()), repoLinks(t)) {
		t.Error(f.String())
	}
	for _, miss := range readmePathMisses(root, string(b)) {
		t.Error(miss)
	}

	stale := "See [the spec](./docs/spec.md) and `docs/agent-model.md`.\n" +
		"Kept: `skills/<name>.md`, `~/.satelle/config.toml`, `.cursor/hooks.json`, `satelle.toml`, [LICENSE](./LICENSE).\n" +
		"```sh\ncat docs/gone.md   # and go test ./... with scripts/install.sh\n```\n"
	got := readmePathMisses(root, stale)
	if len(got) != 3 || !strings.Contains(got[0], "./docs/spec.md") || !strings.Contains(got[1], "docs/agent-model.md") || !strings.Contains(got[2], "docs/gone.md") {
		t.Errorf("stale text: want the three absent docs paths reported, got %q", got)
	}

	for _, text := range []string{"Run `satelle reindex`.\n", "| `satelle story`, `satelle sync` | items |\n", "```sh\nsatelle serve\n```\n"} {
		if len(substrate.CheckText("README.md", "md", text, substrateTree(NewRootCmd()), nil)) != 1 {
			t.Errorf("stale command not reported in %q", text)
		}
	}

	bare := map[string]bool{"satelle": true, "satelled": true, "claude": true, "pi": true, "grok": true, "cursor": true,
		"SATELLE_INSTALL_DIR": true, "SATELLE_NO_SERVICE=1": true, "--no-service": true, "no issues": true,
		"<check>: <problem>": true, "fix:": true, "make install": true, "... | sh -s -- --no-service": true,
		"<session>": true, "--session": true}
	fence := false
	for n, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if strings.Count(line, "`")%2 != 0 {
			t.Errorf("README.md:%d: a backtick span crosses the line end, where no check reads it; keep each span on one line", n+1)
		}
		for _, m := range reReadmeSpan.FindAllStringSubmatch(line, -1) {
			span := m[1]
			if strings.HasPrefix(span, "satelle ") || bare[span] || strings.ContainsAny(span, "/~") || reReadmeFile.MatchString(span) {
				continue
			}
			t.Errorf("README.md:%d: span `%s` is neither a satelle command written in full, a path, nor a known bare word", n+1, span)
		}
	}
}
