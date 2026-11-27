package hook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func refuseBranch(target string) func(string) Verdict {
	return func(branch string) Verdict {
		if branch == target {
			return Verdict{Reason: "push to " + branch + " refused"}
		}
		return allow
	}
}

func TestGateJudgesPushes(t *testing.T) {
	root := t.TempDir()
	g := Gate{Root: root, TempRoots: []string{}, Grant: func(string) Grant { return Grant{Tree: true} }, Push: refuseBranch("sto_b")}
	for _, c := range []string{
		"git push origin HEAD:sto_b",
		"git push origin x:refs/heads/sto_b",
		"git push origin --delete sto_b",
		"git push origin sto_b",
		"git push origin HEAD:sto_a HEAD:sto_b",
	} {
		if v := g.Bash(c, root); v.Allow || !strings.Contains(v.Reason, "sto_b") {
			t.Errorf("%q: %+v, want a refusal naming sto_b", c, v)
		}
	}
	for _, c := range []string{
		"A+=1 git push origin HEAD:sto_b",
		`A="1" "git" push origin HEAD:sto_b`,
		`A='1' 'git' push origin HEAD:sto_b`,
		`A+="1" "git" push origin HEAD:sto_b`,
		"A=1; git push origin HEAD:sto_b",
	} {
		if v := g.Bash(c, root); v.Allow || !(strings.Contains(v.Reason, "sto_b") || strings.Contains(v.Reason, "fail-closed rule")) {
			t.Errorf("%q: %+v, want a refusal naming sto_b or the fail-closed rule", c, v)
		}
	}
	for _, c := range []string{"git push origin HEAD:sto_a", "git status", "go test ./..."} {
		if v := g.Bash(c, root); !v.Allow {
			t.Errorf("%q: refused: %s", c, v.Reason)
		}
	}
	called := false
	probe := g
	probe.Push = func(string) Verdict { called = true; return allow }
	for _, c := range []string{"git status", "go test ./...", "ls -la", "git log --oneline | grep push"} {
		probe.Bash(c, root)
	}
	if called {
		t.Error("Push was called for a command that holds no push")
	}
	nilPush := g
	nilPush.Push = nil
	if v := nilPush.Bash("git push origin HEAD:sto_b", root); !v.Allow {
		t.Errorf("with Push nil a push was judged: %s", v.Reason)
	}
}

var pushBranchRows = map[string][]string{
	"git push origin HEAD:B":                  {"B"},
	"git push origin HEAD:refs/heads/B":       {"B"},
	"git push origin x:heads/B":               {"B"},
	"git push origin +x:B":                    {"B"},
	"git push origin HEAD~1:B":                {"B"},
	`git push origin "HEAD:refs/heads/B"`:     {"B"},
	`git push origin 'HEAD:B'`:                {"B"},
	`git push origin HEAD:'B'`:                {"B"},
	"git --no-pager push origin HEAD:B":       {"B"},
	"git -C ../b push origin HEAD:B":          {"B"},
	"/usr/bin/git push origin HEAD:B":         {"B"},
	`"git" push origin HEAD:B`:                {"B"},
	`'git' push origin HEAD:B`:                {"B"},
	`"/usr/bin/git" push origin HEAD:B`:       {"B"},
	"git push -u -q origin HEAD:refs/heads/B": {"B"},
	"git push origin HEAD:B --force":          {"B"},
	"git push origin -- HEAD:B":               {"B"},
	"git push origin --delete B":              {"B"},
	"git push -d origin B":                    {"B"},
	"git push origin :B":                      {"B"},
	"git push origin B":                       {"B"},
	"git push -u origin B":                    {"B"},
	"git status && git push origin HEAD:B":    {"B"},
	"cd ../x && git push origin HEAD:B":       {"B"},
	"go test ./... ; git push origin HEAD:B":  {"B"},
	"ls || git push origin HEAD:B":            {"B"},
	"ls | git push origin HEAD:B":             {"B"},
	"git push origin HEAD:A HEAD:B":           {"A", "B"},
}

var pushNothingRows = []string{
	"git status", "git log --oneline | grep push | wc -l", `grep -r "git push" docs`, "git commit -m 'git push origin HEAD:B'",
	"git --attr-source HEAD log", "time go test ./...", "A=1", `A="1"`,
	"git status 2>&1", `cd "$d" && git log`, "go test ./... 2>&1 | tail -5",
	"1A=x git push origin HEAD:B", `"A"=1 git push origin HEAD:B`, `A"="1 git push origin HEAD:B`, `"A=1" git push origin HEAD:B`,
}

var pushUnreadRows = map[string]string{
	"git push":                                            pushNotWritten,
	"git push origin":                                     pushNotWritten,
	"git push origin HEAD":                                pushNotWritten,
	"git push origin @":                                   pushNotWritten,
	"git push origin HEAD:":                               pushNotWritten,
	"git push origin HEAD:HEAD":                           pushNotWritten,
	"git push origin a:b:c":                               "refspec a:b:c is not read",
	"git push --all origin":                               "git push option --all is not read",
	"git push --mirror origin":                            "git push option --mirror is not read",
	"git push origin --tags":                              "git push option --tags is not read",
	"git push -uf origin HEAD:B":                          "git push option -uf is not read",
	"git push --repo=origin HEAD:B":                       "git push option --repo=origin is not read",
	"git push -o x origin HEAD:B":                         "git push option -o is not read",
	"git push origin 'refs/heads/*:refs/heads/*'":         "refs/heads/*:refs/heads/* is a pattern refspec",
	"git push origin 'refs/heads/sto_*:refs/heads/sto_*'": "refs/heads/sto_*:refs/heads/sto_* is a pattern refspec",
	"git push origin 'refs/heads/*'":                      "refs/heads/* is a pattern refspec",
	"git push origin ':refs/heads/*'":                     ":refs/heads/* is a pattern refspec",
	"git push origin 'refs/*:refs/*'":                     "refs/*:refs/* is a pattern refspec",
	`git push origin "HEAD:refs/heads/*"`:                 "HEAD:refs/heads/* is a pattern refspec",
	"git push origin --delete 'refs/heads/*'":             "refs/heads/* is a pattern refspec",
	"git push origin refs/tags/v1:refs/tags/v1":           "destination refs/tags/v1 is not a branch",
	"git push origin HEAD:refs/remotes/origin/B":          "destination refs/remotes/origin/B is not a branch",
	"git push origin HEAD:refs/heads/":                    "destination refs/heads/ is not a branch",
	"git --attr-source HEAD push origin HEAD:B":           "git option --attr-source before the subcommand is not read",
	"git --attr-source=HEAD push origin HEAD:B":           "git option --attr-source=HEAD before the subcommand is not read",
	"git -c x.y=z push origin HEAD:B":                     "git option -c before the subcommand is not read",
	"git --git-dir .git push origin HEAD:B":               "git option --git-dir before the subcommand is not read",
	"git --work-tree . push origin HEAD:B":                "git option --work-tree before the subcommand is not read",
	"git --no-pag push origin HEAD:B":                     "git option --no-pag before the subcommand is not read",
	`"git" -c x.y=z push origin HEAD:B`:                   "git option -c before the subcommand is not read",
}

func scanLine(c string) BashTargets {
	return ScanBash(c, "/w", "/w", func(string) bool { return false })
}

func scanPushes(c string) []PushDest {
	return scanLine(c).Pushes
}

func TestReadPushes(t *testing.T) {
	for c, want := range pushBranchRows {
		var got []string
		for _, d := range scanPushes(c) {
			if d.Branch == "" {
				t.Errorf("%q: %+v, want a branch", c, d)
			}
			got = append(got, d.Branch)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: branches %v, want %v", c, got, want)
		}
	}
	for _, c := range pushNothingRows {
		if got := scanPushes(c); len(got) != 0 {
			t.Errorf("%q: %+v, want no push", c, got)
		}
	}
	for c, want := range pushUnreadRows {
		got := scanPushes(c)
		if len(got) != 1 || got[0].Unread != want {
			t.Errorf("%q: %+v, want Unread %q", c, got, want)
		}
	}
}

func TestReadPushesRecoversToRefuse(t *testing.T) {
	readPushesFault = func() { panic("fault") }
	t.Cleanup(func() { readPushesFault = nil })
	segs, why := readPlainLine("git push origin HEAD:sto_a")
	if why != "" {
		t.Fatalf("not a plain line: %s", why)
	}
	got := readPushes(segs)
	if len(got) != 1 || got[0].Unread != pushReaderFault {
		t.Fatalf("a fault in the reader gave %+v", got)
	}
	root := t.TempDir()
	g := Gate{Root: root, TempRoots: []string{}, Grant: func(string) Grant { return Grant{Tree: true} }, Push: func(string) Verdict { return allow }}
	if v := g.Bash("git push origin HEAD:sto_a", root); v.Allow || !strings.Contains(v.Reason, pushReaderFault) {
		t.Errorf("a fault in the reader: %+v, want a refusal", v)
	}
}

var pushFailClosedRows = []string{
	"git push origin $DEST", `cd "$D" && git push origin HEAD:B`, "git push origin $(echo HEAD:B)", "git push origin `echo HEAD:B`", "git push origin $'HEAD:B'",
	`git push origin HEAD:\B`, `g\it push origin HEAD:B`,
	"(git push origin HEAD:B)", "{ git push origin HEAD:B; }", "git push origin HEAD:{B,C}", "git push origin HEAD:B*",
	"git push origin HEAD:B 2>&1", "git push origin HEAD:B > /dev/null", "git push origin HEAD:B <<<x",
	"true & git push origin HEAD:B", "git push origin HEAD:B # x", "! git push origin HEAD:B", "[[ -n x ]] && git push origin HEAD:B", "[[ -n x ]]; git push origin HEAD:B", "git push origin HEAD:B\nls",
	"if true; then git push origin HEAD:B; fi", "time git push origin HEAD:B", "time -p git push origin HEAD:B", "for x in 1; do git push origin HEAD:B; done",
	"while true; do git push origin HEAD:B; done", "until false; do git push origin HEAD:B; done", "if git push origin HEAD:B; then true; fi",
	"A=1 time git push origin HEAD:B", `A="1" time git push origin HEAD:B`, "coproc git push origin HEAD:B", "select x in a; do git push origin HEAD:B; done",
	`"time" git push origin HEAD:B`,
	"A=1 git push origin HEAD:B", "A+=1 git push origin HEAD:B", "FOO+=1 git push origin HEAD:B", `A="1" git push origin HEAD:B`, `A='1' git push origin HEAD:B`,
	`A+="1" git push origin HEAD:B`, `A="1" "git" push origin HEAD:B`, `A='1' 'git' push origin HEAD:B`, `A+="1" "git" push origin HEAD:B`,
	"A=1; git push origin HEAD:B", "A=1 && git push origin HEAD:B",
	"false || git push origin HEAD:B", "echo y | git push origin HEAD:B",
}

func TestPushReaderFailsClosed(t *testing.T) {
	root := t.TempDir()
	g := Gate{Root: root, TempRoots: []string{}, Grant: func(string) Grant { return Grant{Tree: true} }, Push: func(string) Verdict { return allow }}
	for _, c := range pushFailClosedRows {
		if got := scanLine(c); got.Unreadable == "" || len(got.Pushes) != 0 {
			t.Errorf("%q: %+v, want an unread line with no push read", c, got)
		}
		if v := g.Bash(c, root); v.Allow || !strings.Contains(v.Reason, "fail-closed rule") || !strings.Contains(v.Reason, "git -C <dir>") {
			t.Errorf("%q: %+v, want the fail-closed refusal", c, v)
		}
	}
	for _, c := range []string{
		"git status 2>&1", `cd "$d" && git log`, "go test ./... 2>&1 | tail -5", "for f in *.go; do echo $f; done",
		"time go test ./...", "if true; then ls; fi", "cat <<EOF\nx\nEOF",
	} {
		if got := scanPushes(c); len(got) != 0 {
			t.Errorf("%q names no git push but gave %+v", c, got)
		}
	}
}

func runUnderBash(t *testing.T, gitFunc, line string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash oracle runs on unix")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	base := t.TempDir()
	for _, d := range []string{"w", "x", "b", "empty"} {
		if err := os.Mkdir(filepath.Join(base, d), 0o755); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "-c", `PATH="$EMPTY"; `+gitFunc+`; eval "$LINE"`)
	cmd.WaitDelay = time.Second
	cmd.Dir = filepath.Join(base, "w")
	cmd.Env = append(os.Environ(), "EMPTY="+filepath.Join(base, "empty"), "LINE="+line, "D=.", "DEST=HEAD:B")
	cmd.Stdin = strings.NewReader("")
	out, _ := cmd.Output()
	return string(out)
}

func TestPushReaderReadsWhatGitReceives(t *testing.T) {
	const record = `git() { for a; do if [ "$a" = push ]; then printf '%s\037' "$@"; printf '\036'; exit 0; fi; done; }`
	for c := range pushBranchRows {
		if strings.Contains(c, "/usr/bin/git") {
			continue
		}
		var fromBash [][]string
		for _, rec := range strings.Split(strings.TrimSuffix(runUnderBash(t, record, c), "\036"), "\036") {
			if rec == "" {
				continue
			}
			words := append([]string{"git"}, strings.Split(strings.TrimSuffix(rec, "\037"), "\037")...)
			_, _, args, ok := gitInvocationArgs(words)
			if !ok {
				t.Fatalf("%q: bash passed %q", c, words)
			}
			fromBash = append(fromBash, args)
		}
		segs, why := readPlainLine(c)
		read, unread := pushInvocations(segs)
		if why != "" || unread != "" || len(read) != len(fromBash) || len(read) == 0 {
			t.Errorf("%q: reader %q (%s%s), bash %q", c, read, why, unread, fromBash)
			continue
		}
		for i := range read {
			rp, rd, _ := pushPositionals(read[i])
			bp, bd, _ := pushPositionals(fromBash[i])
			if strings.Join(rp, "\x00") != strings.Join(bp, "\x00") || rd != bd {
				t.Errorf("%q: reader reads %q (delete %v), git receives %q (delete %v)", c, rp, rd, bp, bd)
			}
		}
	}
}

func TestPushReaderRefusesWhatBashWouldRun(t *testing.T) {
	const hit = `git() { for a; do [ "$a" = push ] && { printf HIT; exit 0; }; done; }`
	var rows []string
	for c := range pushBranchRows {
		if !strings.Contains(c, "/usr/bin/git") {
			rows = append(rows, c)
		}
	}
	for c := range pushUnreadRows {
		rows = append(rows, c)
	}
	rows = append(rows, pushFailClosedRows...)
	rows = append(rows, pushNothingRows...)
	hits := 0
	for _, c := range rows {
		if !strings.Contains(runUnderBash(t, hit, c), "HIT") {
			continue
		}
		hits++
		if got := scanLine(c); len(got.Pushes) == 0 && got.Unreadable == "" {
			t.Errorf("%q: bash runs git push, the gate reads no push and does not refuse the line as unread", c)
		}
	}
	if hits < len(pushBranchRows) {
		t.Fatalf("bash ran git push for only %d of %d rows; the oracle is not working", hits, len(rows))
	}
	for _, c := range []string{`grep -r "git push" .`, "git log | head"} {
		if strings.Contains(runUnderBash(t, hit, c), "HIT") || len(scanPushes(c)) != 0 {
			t.Errorf("%q runs no git push but is read as one", c)
		}
	}
}
