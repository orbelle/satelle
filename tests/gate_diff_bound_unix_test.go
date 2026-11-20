//go:build integration && unix

package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
)

func recordingJudgeBin(t *testing.T) (bin, promptFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "claude")
	promptFile = filepath.Join(dir, "prompt.txt")
	script := "#!/bin/sh\n" +
		"cat > '" + promptFile + "'\n" +
		"printf '%s\\n' '{\"result\":\"{\\\"verdict\\\":\\\"accept\\\",\\\"notes\\\":\\\"judged\\\"}\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n"
	writeFile(t, bin, script, 0o755)
	return bin, promptFile
}

func readCapturedPrompt(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured prompt %s: %v", path, err)
	}
	return string(b)
}

func TestGateDiffBoundAndCutFilesEndToEnd(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")

	tightBin, tightPrompt := recordingJudgeBin(t)
	defaultBin, defaultPrompt := recordingJudgeBin(t)

	s.write(".satelle/agents.toml", fmt.Sprintf(`
[agents.judge-tight]
command = %q
model = "fake-tight"
tools = ["Read"]

[agents.judge-default]
command = %q
model = "fake-default"
tools = ["Read"]
`, tightBin, defaultBin))

	s.write(".satelle/skills/review.md", "---\nname: review\n---\nReview the change.\n")
	s.write(".satelle/skills/noop-check.md", "---\ntype: check\n---\n```sh\nexit 0\n```\n")
	s.write(".satelle/workflows/bounded.toml", `name = "bounded"
[states.todo]
start = true
[states.done]
terminal = true

[gates.tight]
skill = "review"
agent = "judge-tight"
diff_max_bytes = 500

[gates.default-bound]
skill = "review"
agent = "judge-default"

[[transition]]
from = ["todo"]
to = "done"
groups = [{ mode = "sequential", gates = ["tight", "default-bound"] }]
`)
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"bounded\"\n")
	s.mustConfigValid()

	s.write("a_small.txt", "before\n")
	s.write("b_medium.txt", "before\n")
	s.write("c_huge.txt", "before\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "baseline")

	it := s.create("story", "--title", "bounded diff")
	s.must("story", "seat", it.ID, "--session", "s1")

	s.write("a_small.txt", "after\n")
	var med strings.Builder
	for i := range 25 {
		fmt.Fprintf(&med, "medium-line-%04d-0123456789012345678901234567890123456789\n", i)
	}
	s.write("b_medium.txt", med.String())
	var huge strings.Builder
	for i := range 1600 {
		fmt.Fprintf(&huge, "huge-line-%04d-01234567890123456789012345678901234567890123\n", i)
	}
	s.write("c_huge.txt", huge.String())
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "changes")

	s.must("story", "set", it.ID, "--status", "done")

	tp := readCapturedPrompt(t, tightPrompt)
	_, tDiff, ok := strings.Cut(tp, "\nDiff:\n")
	if !ok {
		t.Fatalf("tight prompt has no Diff block:\n%s", tp)
	}
	tHead, tTrailer, ok := strings.Cut(tDiff, "… diff truncated at 500 bytes; cut files:\n")
	if !ok {
		t.Fatalf("tight prompt was not cut at 500 bytes:\n%s", tDiff)
	}
	if len(strings.TrimSuffix(tHead, "\n")) > 500 {
		t.Fatalf("tight diff body %d bytes exceeds 500", len(tHead))
	}
	for _, want := range []string{"- b_medium.txt\n", "- c_huge.txt\n"} {
		if !strings.Contains(tTrailer, want) {
			t.Fatalf("tight cut list lacks %q:\n%s", want, tTrailer)
		}
	}
	if strings.Contains(tTrailer, "- a_small.txt\n") {
		t.Fatalf("tight cut list includes intact a_small.txt:\n%s", tTrailer)
	}

	dp := readCapturedPrompt(t, defaultPrompt)
	_, dDiff, ok := strings.Cut(dp, "\nDiff:\n")
	if !ok {
		t.Fatalf("default prompt has no Diff block:\n%s", dp)
	}
	wantHeader := fmt.Sprintf("… diff truncated at %d bytes; cut files:\n", gate.DefaultDiffMaxBytes)
	dHead, dTrailer, ok := strings.Cut(dDiff, wantHeader)
	if !ok {
		t.Fatalf("default prompt was not cut at %d bytes:\n%s", gate.DefaultDiffMaxBytes, dDiff[max(0, len(dDiff)-400):])
	}
	if len(strings.TrimSuffix(dHead, "\n")) > gate.DefaultDiffMaxBytes {
		t.Fatalf("default diff body %d bytes exceeds %d", len(dHead), gate.DefaultDiffMaxBytes)
	}
	if !strings.Contains(dTrailer, "- c_huge.txt\n") {
		t.Fatalf("default cut list lacks c_huge.txt:\n%s", dTrailer)
	}
	for _, intact := range []string{"- a_small.txt\n", "- b_medium.txt\n"} {
		if strings.Contains(dTrailer, intact) {
			t.Fatalf("default cut list includes intact %q:\n%s", intact, dTrailer)
		}
	}

	s.write(".satelle/workflows/bad-zero.toml", `name = "bad-zero"
[states.todo]
start = true
[states.done]
terminal = true
[gates.g]
skill = "review"
agent = "judge-tight"
diff_max_bytes = 0
[[transition]]
from = ["todo"]
to = "done"
groups = [{ gates = ["g"] }]
`)
	if r := s.run("validate"); r.code == 0 || !strings.Contains(r.out+r.err, "diff_max_bytes 0 must be positive") {
		t.Fatalf("validate with diff_max_bytes = 0: exit %d\n%s%s", r.code, r.out, r.err)
	}
	os.Remove(filepath.Join(s.dir, ".satelle", "workflows", "bad-zero.toml"))

	s.write(".satelle/workflows/bad-check.toml", `name = "bad-check"
[states.todo]
start = true
[states.done]
terminal = true
[gates.g]
skill = "noop-check"
diff_max_bytes = 500
[[transition]]
from = ["todo"]
to = "done"
groups = [{ gates = ["g"] }]
`)
	if r := s.run("validate"); r.code == 0 || !strings.Contains(r.out+r.err, `skill "noop-check" is a check`) {
		t.Fatalf("validate with diff_max_bytes on check gate: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestGateDiffSummarisesDeletedFilesEndToEnd(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")

	judgeBin, judgePrompt := recordingJudgeBin(t)
	s.write(".satelle/agents.toml", fmt.Sprintf(`
[agents.judge-one]
command = %q
model = "fake-one"
tools = ["Read"]
`, judgeBin))
	s.write(".satelle/skills/review.md", "---\nname: review\n---\nReview the change.\n")
	s.write(".satelle/workflows/del.toml", `name = "del"
[states.todo]
start = true
[states.done]
terminal = true

[gates.review]
skill = "review"
agent = "judge-one"

[[transition]]
from = ["todo"]
to = "done"
groups = [{ gates = ["review"] }]
`)
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"del\"\n")
	s.mustConfigValid()

	var gone strings.Builder
	for i := range 2500 {
		fmt.Fprintf(&gone, "deleted-sentinel-line-%04d-xxxxxxxxxxxxxxxxxxxxxxxx\n", i)
	}
	s.write("removed_big.txt", gone.String())
	s.write("kept.txt", "before\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "baseline")

	it := s.create("story", "--title", "delete big file")
	s.must("story", "seat", it.ID, "--session", "s1")

	s.git("rm", "-q", "removed_big.txt")
	s.write("kept.txt", "after-kept-change\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "delete and edit")

	s.must("story", "set", it.ID, "--status", "done")

	prompt := readCapturedPrompt(t, judgePrompt)
	if !strings.Contains(prompt, "deleted file: removed_big.txt (2500 lines)\n") {
		t.Fatalf("prompt lacks deleted file summary:\n%s", prompt)
	}
	if strings.Contains(prompt, "deleted-sentinel-line-") {
		t.Fatalf("prompt leaked removed lines of deleted file:\n%s", prompt)
	}
	if !strings.Contains(prompt, "+after-kept-change\n") {
		t.Fatalf("prompt lost modified file hunk:\n%s", prompt)
	}
	if strings.Contains(prompt, "… diff truncated") {
		t.Fatalf("prompt was unexpectedly truncated after summarising deletion:\n%s", prompt)
	}
}
