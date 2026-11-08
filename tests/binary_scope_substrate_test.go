//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The binary-scope judge applies mechanism-vs-substrate's git rule as the
// principle states it (operator, 2026-11-08): the binary is read-only on the
// user's repo, its refs and its remotes, and git in a scratch repo it creates
// for one run is mechanism. The skill restates both sentences, rejects a Go
// path that writes the repo by every write the ruling names, accepts reads and
// the scratch repo, and no longer says the binary takes no git action at all.
func TestBinaryScopeReviewFollowsTheGitRuling(t *testing.T) {
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	principle := flat(read(".satelle/principles/satelle-mechanism-vs-substrate.md"))
	skill := flat(read(".satelle/skills/satelle-binary-scope-review.md"))

	// The principle's two sentences of the rule, as it words them.
	rules := []*regexp.Regexp{
		regexp.MustCompile(`The binary is read-only on the user's repo, its refs and its remotes`),
		regexp.MustCompile(`Git it runs in a scratch repo it creates for one run`),
	}
	for _, re := range rules {
		m := re.FindString(principle)
		if m == "" {
			t.Fatalf("satelle-mechanism-vs-substrate.md no longer states %q", re)
		}
		if !strings.Contains(skill, m) {
			t.Errorf("satelle-binary-scope-review.md does not state the principle's %q", m)
		}
	}

	_, judge, ok := strings.Cut(skill, "## Judge")
	if !ok {
		t.Fatal("the skill has no ## Judge section")
	}
	one, _, _ := strings.Cut(judge, " 2. ")
	if !strings.Contains(one, "**Reject** each planned or changed Go path") || !strings.Contains(one, "writes the user's repo, its refs or its remotes") {
		t.Errorf("rubric item 1 does not reject a Go path that writes the user's repo, its refs or remotes:\n%s", one)
	}
	for _, w := range []string{"commit", "push", "merge", "branch", "tag", "reset", "worktree removal"} {
		if !strings.Contains(one, w) {
			t.Errorf("rubric item 1 does not name the write %q", w)
		}
	}
	if !strings.Contains(one, "**Accept** Go that only reads the user's repo") || !strings.Contains(one, "scratch repo it creates for one run") {
		t.Errorf("rubric item 1 does not accept reads of the user's repo and git in a one-run scratch repo:\n%s", one)
	}

	for _, bare := range regexp.MustCompile(`takes no git or forge action[^.]*`).FindAllString(skill, -1) {
		if !strings.Contains(bare, "user's repo") {
			t.Errorf("the skill still says, unqualified: %q", bare)
		}
	}
	if !strings.Contains(skill, `{"verdict": "accept", "notes": "<what you judged: quote the work, or name the file, criterion or record path you checked>"}`) {
		t.Error("the skill's verdict format changed")
	}
}
