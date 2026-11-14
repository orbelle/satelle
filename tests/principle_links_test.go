//go:build integration

package tests

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

// principleLink matches a [[name]] link in substrate markdown.
var principleLink = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// codeSpan is a backtick code span; a [[link]] inside one is text, not a link,
// as the substrate check reads it.
var codeSpan = regexp.MustCompile("`[^`]*`")

// TestPrincipleLinksResolve proves every [[link]] in the constitution and the
// principles names an existing principle: a file whose frontmatter carries that
// name, type principle and a description. Each dead link is reported with its
// file and line, so one run lists them all.
func TestPrincipleLinksResolve(t *testing.T) {
	root := ".."
	dir := filepath.Join(root, ".satelle", "principles")
	principles, err := workflow.LoadSkills(dir)
	if err != nil {
		t.Fatalf("load principles: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append([]string{filepath.Join(root, ".satelle", "constitution.md")}, files...)

	seen := map[string]bool{}
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", rel, err)
		}
		sc := bufio.NewScanner(f)
		for n := 1; sc.Scan(); n++ {
			for _, m := range principleLink.FindAllStringSubmatch(codeSpan.ReplaceAllString(sc.Text(), ""), -1) {
				name := m[1]
				seen[name] = true
				p, ok := principles[name]
				switch {
				case !ok:
					t.Errorf("%s:%d: [[%s]] names no principle file", rel, n, name)
				case p.Meta["name"] != name:
					t.Errorf("%s:%d: [[%s]] resolves to a file whose name is %q", rel, n, name, p.Meta["name"])
				case p.Meta["type"] != "principle":
					t.Errorf("%s:%d: [[%s]] has type %q, want principle", rel, n, name, p.Meta["type"])
				case p.Meta["description"] == "":
					t.Errorf("%s:%d: [[%s]] has no description", rel, n, name)
				}
			}
		}
		if err := sc.Err(); err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		_ = f.Close()
	}

	if len(seen) == 0 {
		t.Fatal("no [[link]] found in the constitution or the principles")
	}
	for _, want := range []string{"satelle-story-shape", "satelle-start-small"} {
		if !seen[want] {
			t.Errorf("[[%s]] was not among the links checked", want)
		}
	}
}
