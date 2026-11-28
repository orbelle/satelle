//go:build unix

package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/reporeg"
)

var partNonces = []string{"nonce-x-con", "nonce-x-p", "frame-x"}

func injectFix(t *testing.T, parts bool) string {
	t.Helper()
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	writeRepos(t, dir, "x", "y", "z")
	mapRepos(t, dir, "x", "y", "z")
	sat := filepath.Join(dir, ".satelle")
	writeFile(t, filepath.Join(sat, "principles", "shared-a.md"), "---\nname: shared-a\nframing: true\ndescription: frame-shared\n---\nnonce-shared\n")
	writeHookSettings(t, dir, "inject = [\".satelle/principles/shared-a.md\"]\n")
	if parts {
		writeFile(t, filepath.Join(sat, "repos", "x", "constitution.md"), "nonce-x-con\n")
		writeFile(t, filepath.Join(sat, "repos", "x", "principles", "p.md"), "---\nname: p\nframing: true\ndescription: frame-x\n---\nnonce-x-p\n")
		writeFile(t, filepath.Join(sat, "repos", "x", "satelle.toml"), "[hook]\ninject = [\"constitution.md\", \"principles/p.md\"]\n")
		writeFile(t, filepath.Join(sat, "repos", "y", "constitution.md"), "nonce-y-con\n")
		writeFile(t, filepath.Join(sat, "repos", "z", "constitution.md"), "nonce-z-con\n")
		writeFile(t, filepath.Join(sat, "repos", "z", "satelle.toml"), "[hook]\ninject = []\n")
	}
	must(t, "validate")
	return dir
}

func seatRepo(t *testing.T, repo, session string) string {
	t.Helper()
	args := []string{"story", "create", "--title", "seated work", "--workflow", "gated"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	id := createID(t, args...)
	must(t, "story", "set", id, "--status", "st-two")
	must(t, "story", "seat", id, "--session", session)
	return id
}

func wantShared(t *testing.T, label, text string) {
	t.Helper()
	for _, want := range []string{"nonce-shared", "frame-shared"} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: context lacks %q:\n%s", label, want, text)
		}
	}
	for _, bad := range append(partNonces, "nonce-y-con", "nonce-z-con") {
		if strings.Contains(text, bad) {
			t.Errorf("%s: context carries part file %q:\n%s", label, bad, text)
		}
	}
	if strings.Contains(text, "\n"+injectNotePrefix) {
		t.Errorf("%s: context carries an inject note:\n%s", label, text)
	}
}

func TestSessionContextCarriesThePartsInject(t *testing.T) {
	dir := injectFix(t, true)
	seatRepo(t, "x", "s-x")
	text := hookContext(t, "s-x")
	con, p := strings.Index(text, "nonce-x-con"), strings.Index(text, "nonce-x-p")
	if con < 0 || p < 0 || con > p {
		t.Errorf("context lacks the part files in order:\n%s", text)
	}
	note := "\n" + partInjectNote(filepath.Join(dir, ".satelle"), "x") + "\n"
	for _, want := range []string{"frame-x\n", note, "repository x's part names its own files in .satelle/repos/x/satelle.toml"} {
		if !strings.Contains(text, want) {
			t.Errorf("context lacks %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"nonce-shared", "frame-shared"} {
		if strings.Contains(text, bad) {
			t.Errorf("context carries the shared %q:\n%s", bad, text)
		}
	}

	writeFile(t, filepath.Join(dir, ".satelle", "harness.toml"), "[claude]\ncontext_limit_bytes = 64\n")
	index := hookContext(t, "s-x")
	for _, want := range []string{"\n  .satelle/repos/x/constitution.md", "\n  .satelle/repos/x/principles/p.md — frame-x\n", note} {
		if !strings.Contains(index, want) {
			t.Errorf("index form lacks %q:\n%s", want, index)
		}
	}
	for _, bad := range []string{"shared-a.md", "\nnonce-x-con\n", "\nnonce-x-p\n"} {
		if strings.Contains(index, bad) {
			t.Errorf("index form carries %q:\n%s", bad, index)
		}
	}
}

func TestSessionContextForABrokenPartInjectsNothing(t *testing.T) {
	dir := injectFix(t, true)
	seatRepo(t, "x", "s-x")
	writeFile(t, filepath.Join(dir, ".satelle", "repos", "x", "satelle.toml"), "[hook]\ninject = [\"principles/missing.md\"]\n")
	text := hookContext(t, "s-x")
	if !strings.Contains(text, "\n"+injectNotePrefix+"no files are given: ") || !strings.Contains(text, "principles/missing.md") {
		t.Errorf("context lacks the refusal note:\n%s", text)
	}
	for _, bad := range append(partNonces, "nonce-shared", "frame-shared") {
		if strings.Contains(text, bad) {
			t.Errorf("a broken part's session carries %q:\n%s", bad, text)
		}
	}
}

func TestSeatedInjectFailsClosedOnSeatReadError(t *testing.T) {
	dir := injectFix(t, true)
	seatRepo(t, "x", "s-x")
	r, err := findOwnRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	k, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if files, _ := seatedInject(k, r, "s-x"); len(files) != 2 {
		t.Fatalf("an open cache gives %q, want the part's two files", files)
	}
	k.Cache.Close()
	files, note := seatedInject(k, r, "s-x")
	if files == nil || len(files) != 0 {
		t.Errorf("files %#v, want an empty non-nil list", files)
	}
	src := &sessionSource{Inject: files}
	src.Settings.Hook.Inject = []string{"shared.md"}
	if got := src.injectFiles(); len(got) != 0 {
		t.Errorf("injectFiles %q, want none", got)
	}
	if !strings.HasPrefix(note, injectNotePrefix+"no files are given: this session's seats cannot be read:") || strings.Contains(note, "\n") || reporeg.HasUnsafe(note) {
		t.Errorf("note %q", note)
	}
}

func TestSessionContextMixedSeatsUseShared(t *testing.T) {
	injectFix(t, true)
	seatRepo(t, "x", "s-m")
	seatRepo(t, "", "s-m")
	text := hookContext(t, "s-m")
	if !strings.Contains(text, "nonce-shared") || !strings.Contains(text, "\n"+injectNotePrefix+"this session's seats target several repositories (self, x), so the shared files are given\n") {
		t.Errorf("mixed context:\n%s", text)
	}
	for _, bad := range partNonces {
		if strings.Contains(text, bad) {
			t.Errorf("mixed context carries %q:\n%s", bad, text)
		}
	}

	forged := "y\nsatelle: forged"
	note := mixedSeatsNote([]string{"x", forged, "x"})
	if strings.Contains(note, "\n") || !strings.Contains(note, "(x, "+reporeg.Literal(forged)+")") || reporeg.HasUnsafe(note) {
		t.Errorf("note %q", note)
	}
}

func TestSessionContextWithNoSessionKeepsShared(t *testing.T) {
	dir := injectFix(t, true)
	id := seatRepo(t, "x", "s-x")
	out, err := run(t, `{}`, "hook", "context", "--harness", "claude")
	if err != nil {
		t.Fatalf("hook context: %v\n%s", err, out)
	}
	text := hookText(out)
	wantShared(t, "no session", text)
	for _, bad := range []string{id, "s-x"} {
		if strings.Contains(text, bad) {
			t.Errorf("a sessionless context carries %q:\n%s", bad, text)
		}
	}
	r, err := findOwnRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	k, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if files, note := seatedInject(k, r, ""); files != nil || note != "" {
		t.Errorf("seatedInject with no session: %q %q", files, note)
	}
}

func rawHookContext(t *testing.T, session string) string {
	t.Helper()
	out, err := run(t, `{"session_id":"`+session+`"}`, "hook", "context", "--harness", "claude")
	if err != nil {
		t.Fatalf("hook context: %v\n%s", err, out)
	}
	return out
}

func TestSessionContextKeepsTheSharedInject(t *testing.T) {
	dir := injectFix(t, true)
	var self string
	for _, repo := range []string{"", "y", "z"} {
		id := seatRepo(t, repo, "s-k")
		raw := rawHookContext(t, "s-k")
		wantShared(t, "repo "+repo, hookText(raw))
		must(t, "story", "release", id, "--session", "s-k")
		if repo == "" {
			self = strings.ReplaceAll(strings.ReplaceAll(raw, id, "<id>"), dir, "<dir>")
		}
	}

	plain := injectFix(t, false)
	id := seatRepo(t, "", "s-k")
	raw := strings.ReplaceAll(strings.ReplaceAll(rawHookContext(t, "s-k"), id, "<id>"), plain, "<dir>")
	if raw != self {
		t.Errorf("a tree with no parts gives a different context:\n%s\nwant:\n%s", raw, self)
	}
}
