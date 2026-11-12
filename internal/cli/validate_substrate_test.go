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

// In any repo satelle init made, satelle validate reports each substrate miss
// (a command the binary lacks, a dead link) with its file and line, and fails;
// the kernel still opens; fixing both makes validate pass (sto_a1681009 AC2).
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

	// The kernel still opens: a drifted substrate is not a configuration problem.
	must(t, "story", "list")

	write("---\nname: stale\ntype: skill\ndescription: Names what the binary has.\n---\n\nRun `satelle story get <id>`.\n\nSee [[stale]].\n")
	out = must(t, "validate", "--json")
	if !strings.Contains(out, `"substrate": []`) || !strings.Contains(out, `"valid": true`) {
		t.Fatalf("fixed substrate: want valid and substrate []:\n%s", out)
	}
}

// repoLinks are the names a [[link]] in this repo's own documents may name:
// its principles and skills.
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

// Every embedded help topic names only commands, flags and topics this binary
// has, and links only principles or skills of this repo: the help topics ship
// in the binary, so they are checked here rather than by satelle validate.
func TestHelpTopicsNameTheBinary(t *testing.T) {
	links := repoLinks(t)
	tree := substrateTree(NewRootCmd())
	for _, tp := range help.List() {
		for _, f := range substrate.CheckText("internal/help/topics/"+tp.Name+".md", "md", tp.Body, tree, links) {
			t.Error(f.String())
		}
	}
}

// reReadmeTarget is a markdown link's target: [text](target).
var reReadmeTarget = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// reReadmeSpan is a backtick span outside a fenced block.
var reReadmeSpan = regexp.MustCompile("`([^`]+)`")

// reReadmeFile is a span that names a file by its extension.
var reReadmeFile = regexp.MustCompile(`\.(md|toml|json|ts|sh|go|yml|yaml|version)$`)

// readmePath reports whether a backtick span names a file path to resolve: one
// word with a slash or a file extension, and no placeholder (<name>), home
// (~), variable ($), glob (*), package pattern (./...), URL or flag (--).
func readmePath(span string) bool {
	if strings.ContainsAny(span, " <>~$*|") || strings.HasPrefix(span, "-") || strings.Contains(span, "://") || strings.Contains(span, "...") {
		return false
	}
	return strings.Contains(span, "/") || reReadmeFile.MatchString(span)
}

// readmePathMisses lists what text, a README, names that is not there: each
// relative link target and each file path (readmePath) in a backtick span or
// a fenced block must be a path
// under root, a file under root/.satelle (the configuration it describes) or a
// harness hook file (hook.File), which satelle writes in the repo it is run in.
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
			// A fenced line is shell: each word that is a path is resolved.
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

// README.md is the first thing a new user's agent reads, and it is outside
// the substrate satelle validate checks. It names only commands, flags and
// help topics this binary has, and every relative link and inline file path in
// it names a path that exists (sto_33132ea7).
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

	// The path rule itself: a link and an inline path that are absent are each
	// reported; a placeholder, a home path and a harness hook file are not.
	stale := "See [the spec](./docs/spec.md) and `docs/agent-model.md`.\n" +
		"Kept: `skills/<name>.md`, `~/.satelle/config.toml`, `.cursor/hooks.json`, `satelle.toml`, [LICENSE](./LICENSE).\n" +
		"```sh\ncat docs/gone.md   # and go test ./... with scripts/install.sh\n```\n"
	got := readmePathMisses(root, stale)
	if len(got) != 3 || !strings.Contains(got[0], "./docs/spec.md") || !strings.Contains(got[1], "docs/agent-model.md") || !strings.Contains(got[2], "docs/gone.md") {
		t.Errorf("stale text: want the three absent docs paths reported, got %q", got)
	}

	// A command the binary lacks is reported wherever the README writes it in
	// full, as the Commands table does: a span, a table cell, a fenced line.
	for _, text := range []string{"Run `satelle reindex`.\n", "| `satelle story`, `satelle sync` | items |\n", "```sh\nsatelle serve\n```\n"} {
		if len(substrate.CheckText("README.md", "md", text, substrateTree(NewRootCmd()), nil)) != 1 {
			t.Errorf("stale command not reported in %q", text)
		}
	}

	// Every backtick span outside a fence is a satelle command written in full,
	// a path, or one of the bare words below (names that are not commands), so
	// a command cannot be named in a form the checks above do not read.
	bare := map[string]bool{"satelle": true, "satelled": true, "claude": true, "pi": true, "grok": true, "cursor": true,
		"SATELLE_INSTALL_DIR": true, "SATELLE_NO_SERVICE=1": true, "--no-service": true, "no issues": true,
		"<check>: <problem>": true, "fix:": true, "make install": true, "... | sh -s -- --no-service": true}
	fence := false
	for n, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		// The checks read one line at a time, so a span never crosses a line.
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
