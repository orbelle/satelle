//go:build !windows

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

const (
	webOrigin = "https://github.com/o/web"
	sha1Tree  = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

type targetFix struct {
	ctl, tgt string
	group    string
	extra    string
}

func hostGitFloor(t *testing.T) {
	t.Helper()
	v, err := gitfacts.ParseVersion(gittest.Git(t, "", "version"))
	if err != nil {
		t.Fatal(err)
	}
	if !v.AtLeast(2, 44) {
		t.Skipf("git %s is older than 2.44, below which a target checkout is refused by design", v.Raw)
	}
}

func targetRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	gittest.Git(t, dir, append([]string{"init", "-q", "-b", "main"}, args...)...)
	gittest.Git(t, dir, "remote", "add", "origin", webOrigin)
	head := commitFile(t, dir, "web/a.go", "package web\n")
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", head)
	return head
}

func targetFixture(t *testing.T, floor bool) *targetFix {
	t.Helper()
	if floor {
		hostGitFloor(t)
	}
	f := &targetFix{ctl: t.TempDir(), tgt: t.TempDir(), group: `"code-ac"`}
	gittest.Git(t, f.ctl, "init", "-q", "-b", "main")
	write(t, filepath.Join(f.ctl, "ctl", "a.go"), "package ctl\n")
	write(t, filepath.Join(f.ctl, ".gitignore"), ".satelle/runtime/\n.satelle/repos.local.toml\n")
	gittest.Git(t, f.ctl, "add", "-A")
	gittest.Git(t, f.ctl, "commit", "-q", "-m", "init")
	gittest.Git(t, f.ctl, "update-ref", "refs/remotes/origin/main", gittest.Git(t, f.ctl, "rev-parse", "HEAD"))
	targetRepo(t, f.tgt)
	write(t, filepath.Join(f.ctl, ".satelle", reporeg.TrackedFile), "[repos.web]\norigin = \""+webOrigin+"\"\n")
	f.mapTo(t, f.tgt)
	return f
}

func (f *targetFix) mapTo(t *testing.T, path string) {
	t.Helper()
	local := filepath.Join(f.ctl, ".satelle", reporeg.LocalFile)
	if path == "" {
		os.Remove(local)
		return
	}
	write(t, local, "[repos.web]\npath = \""+strings.ReplaceAll(strings.ReplaceAll(path, `\`, `\\`), `"`, `\"`)+"\"\n")
}

func (f *targetFix) open(t *testing.T) (*Kernel, *fakeDispatcher) {
	t.Helper()
	root := filepath.Join(f.ctl, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"line\"\n")
	write(t, filepath.Join(root, "agents.toml"), "[agents.r1]\ncommand = \"claude\"\ntools = [\"Read\"]\n")
	write(t, filepath.Join(root, "skills", "review.md"), "Review.\n")
	write(t, filepath.Join(root, "workflows", "line.toml"), `
[states.a]
start = true
[states.b]
[states.c]
terminal = true
[gates.code-ac]
skill = "review"
agent = "r1"
`+f.extra+`
[[transition]]
from = ["a"]
to = "b"
[[transition]]
from = ["b"]
to = "c"
groups = [{ gates = [`+f.group+`] }]
`)
	k, err := Open(Options{Root: root, Dir: f.ctl})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"r1": accept}}
	k.Dispatcher = d
	return k, d
}

func atB(t *testing.T, k *Kernel, repo string) item.Item {
	t.Helper()
	f := item.Fields{Kind: ptr("story"), Title: ptr("t")}
	if repo != "" {
		f.Repo = ptr(repo)
	}
	it, err := k.Create(f, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	return it
}

func (f *targetFix) seat(t *testing.T, k *Kernel, id string) item.Seat {
	t.Helper()
	s, err := k.Seat(id, "s1", "local:"+f.ctl, "", "ada")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func kindCount(t *testing.T, k *Kernel, id, kind string) int {
	t.Helper()
	n := 0
	for _, e := range chainOf(t, k, id) {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func checkoutState(t *testing.T, err error, state string) *TargetCheckoutError {
	t.Helper()
	var tce *TargetCheckoutError
	if !errors.As(err, &tce) || tce.State != state {
		t.Fatalf("err = %v, want a TargetCheckoutError with state %q", err, state)
	}
	return tce
}

func dryInput(t *testing.T, k *Kernel, id, gateName string) (GateDryResult, error) {
	t.Helper()
	return k.GateDry(context.Background(), GateDryRequest{Workflow: "line", Gate: gateName, Item: id, To: "c", ShowInput: true})
}

type gitCall struct {
	argv string
	env  []string
}

func gitShim(t *testing.T, pre string) func() []gitCall {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, logs := t.TempDir(), t.TempDir()
	script := `#!/bin/sh
f=$(mktemp '` + logs + `/call.XXXXXX')
printf '%s\n' "$*" > "$f"
env >> "$f"
sub=""
skip=0
for a in "$@"; do
	if [ "$skip" = 1 ]; then skip=0; continue; fi
	case "$a" in
	-C|-c) skip=1 ;;
	--attr-source=*) ;;
	*) sub="$a"; break ;;
	esac
done
` + pre + `
exec '` + real + `' "$@"
`
	write(t, filepath.Join(bin, "git"), script)
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []gitCall {
		ents, err := os.ReadDir(logs)
		if err != nil {
			t.Fatal(err)
		}
		var out []gitCall
		for _, e := range ents {
			b, err := os.ReadFile(filepath.Join(logs, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
			c := gitCall{argv: lines[0]}
			for _, l := range lines[1:] {
				if name, _, _ := strings.Cut(l, "="); name != "PWD" && name != "SHLVL" && name != "_" && name != "OLDPWD" {
					c.env = append(c.env, l)
				}
			}
			out = append(out, c)
		}
		return out
	}
}

func at(calls []gitCall, dir string) []gitCall {
	var out []gitCall
	for _, c := range calls {
		if strings.HasPrefix(c.argv, "-C "+dir+" ") {
			out = append(out, c)
		}
	}
	return out
}

func TestTargetItemGitFactsComeFromTarget(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"target", "self", "n1"} {
		t.Run(mode, func(t *testing.T) {
			f := targetFixture(t, true)
			if mode == "n1" {
				os.Remove(filepath.Join(f.ctl, ".satelle", reporeg.TrackedFile))
				f.mapTo(t, "")
			}
			k, d := f.open(t)
			g, o, repo := f.tgt, f.ctl, "web"
			if mode != "target" {
				g, o, repo = f.ctl, f.tgt, ""
			}
			headG := gittest.Git(t, g, "rev-parse", "HEAD")
			it := atB(t, k, repo)
			if got, _ := k.Get(it.ID); got.Base != headG {
				t.Fatalf("create base %s, want %s's HEAD %s", got.Base, g, headG)
			}
			s := f.seat(t, k, it.ID)
			if s.Base != headG {
				t.Fatalf("seat base %s, want %s", s.Base, headG)
			}
			raw := string(chainOf(t, k, it.ID)[len(chainOf(t, k, it.ID))-1].Payload)
			if mode == "target" && (s.Checkout != f.tgt || !strings.Contains(raw, `"checkout":"`+f.tgt+`"`)) {
				t.Fatalf("target seat %+v, event %s", s, raw)
			}
			if mode != "target" && (s.Checkout != "" || strings.Contains(raw, "checkout")) {
				t.Fatalf("self seat %+v, event %s", s, raw)
			}
			if b, err := k.Seat(it.ID, "s1", "local:"+f.ctl, "main", "ada"); err != nil || b.Base != headG {
				t.Fatalf("seat --base main: %+v %v", b, err)
			}
			commitFile(t, g, "feat/x.go", "package feat\n\nvar X = 1\n")
			commitFile(t, o, "noise/y.go", "package noise\n")
			write(t, filepath.Join(g, "feat", "u.txt"), "u\n")
			write(t, filepath.Join(o, "noise", "v.txt"), "v\n")
			diff, stale, err := k.ItemChange(it.ID)
			if err != nil || stale != nil || !slices.Equal(diff.Paths, []string{"feat/x.go"}) || !slices.Contains(diff.Untracked, "feat/u.txt") || slices.Contains(diff.Untracked, "noise/v.txt") {
				t.Fatalf("ItemChange %+v %v %v", diff, stale, err)
			}
			if st, err := k.ItemStat(diff); err != nil || len(st) != 1 || st[0].Path != "feat/x.go" {
				t.Fatalf("ItemStat %+v %v", st, err)
			}
			dry, err := dryInput(t, k, it.ID, "code-ac")
			if err != nil || !strings.Contains(dry.Input.Prompt, "feat/x.go") || !strings.Contains(dry.Input.Prompt, "+var X = 1") || strings.Contains(dry.Input.Prompt, "noise/y.go") {
				t.Fatalf("dry prompt %v:\n%v", err, dry.Input)
			}
			if _, err := k.Transition(ctx, it.ID, "c", "ada"); err != nil {
				t.Fatal(err)
			}
			m := lastMove(t, k, it.ID)
			if m.To != "c" || m.Change == nil || m.Change.Base != headG {
				t.Fatalf("frozen move %+v", m)
			}
			if mode == "target" {
				if err := gittest.Command(t, o, "cat-file", "-e", m.Change.Head).Run(); err == nil {
					t.Fatalf("the frozen head %s is a commit of the control", m.Change.Head)
				}
			}
			frozen, _, err := k.ItemChange(it.ID)
			if err != nil || !frozen.Frozen || !slices.Equal(frozen.Paths, []string{"feat/x.go"}) {
				t.Fatalf("frozen diff %+v %v", frozen, err)
			}
			st, err := k.ItemStat(frozen)
			if err != nil || len(st) != 1 || st[0].Path != "feat/x.go" || st[0].Added != 3 {
				t.Fatalf("frozen stat %+v %v", st, err)
			}
			commitFile(t, o, "noise/z.go", "package noise\n")
			if again, _, err := k.ItemChange(it.ID); err != nil || !slices.Equal(again.Paths, frozen.Paths) {
				t.Fatalf("frozen diff moved: %+v %v", again, err)
			}

			j := atB(t, k, repo)
			f.seat(t, k, j.ID)
			commitFile(t, g, "feat/j.go", "package feat\n")
			d.set("r1", reject)
			k.Transition(ctx, j.ID, "c", "ada")
			jc, err := k.JudgedChanges(j.ID)
			if err != nil || len(jc) == 0 || jc[0].Now != jc[0].Judged {
				t.Fatalf("judged %+v %v", jc, err)
			}
			commitFile(t, o, "noise/j.go", "package noise\n")
			if jc2, _ := k.JudgedChanges(j.ID); jc2[0].Now != jc[0].Now {
				t.Fatal("a change in the other repository moved the judged change")
			}
			write(t, filepath.Join(g, "feat", "j.go"), "package feat\n\nvar J = 1\n")
			if jc3, _ := k.JudgedChanges(j.ID); jc3[0].Now == jc[0].Now {
				t.Fatal("a change in the item's repository left the judged change as it was")
			}

			gittest.Git(t, g, "checkout", "-q", "-b", "feature")
			commitFile(t, g, "feat/c.go", "package feat\n")
			gittest.Git(t, g, "checkout", "-q", "main")
			c := atB(t, k, repo)
			col, err := k.Collect(c.ID, "feature", "", "ada")
			if err != nil || col.Commit != gittest.Git(t, g, "rev-parse", "feature") {
				t.Fatalf("collect %+v %v", col, err)
			}
			if mode == "target" {
				gittest.Git(t, f.ctl, "branch", "only-ctl")
				if _, err := k.Collect(c.ID, "only-ctl", "", "ada"); err == nil || !strings.Contains(err.Error(), "branch only-ctl is not here") {
					t.Fatalf("collect of a control-only branch: %v", err)
				}
			}

			gittest.Git(t, g, "checkout", "-q", "--orphan", "fresh")
			commitFile(t, g, "root.go", "package root\n")
			gittest.Git(t, g, "branch", "-f", "main", "fresh")
			gittest.Git(t, g, "checkout", "-q", "main")
			s2 := atB(t, k, repo)
			if _, err := k.Ledger.Append(s2.ID, item.KindSeatBound, "ada", item.Seat{Session: "s9", Location: "local:" + f.ctl, Base: headG, Checkout: map[bool]string{true: f.tgt}[mode == "target"]}); err != nil {
				t.Fatal(err)
			}
			calls := len(d.calls)
			_, stale, err = k.ItemChange(s2.ID)
			if err != nil || stale == nil || stale.Head != gittest.Git(t, g, "rev-parse", "HEAD") {
				t.Fatalf("stale %+v %v", stale, err)
			}
			if _, err := k.Transition(ctx, s2.ID, "c", "ada"); !errors.As(err, new(*StaleBaseError)) || len(d.calls) != calls {
				t.Fatalf("stale transition %v, calls %v", err, d.calls[calls:])
			}
		})
	}
}

func TestTargetUnregisteredReportsUnregistered(t *testing.T) {
	f := targetFixture(t, true)
	k, d := f.open(t)
	it := atB(t, k, "web")
	if _, err := k.Ledger.Append(it.ID, item.KindUpdated, "me", item.Fields{Repo: ptr("api")}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "main"} {
		_, err := k.Seat(it.ID, "s1", "local:"+f.ctl, ref, "ada")
		refusedTarget(t, err, "api")
		if errors.As(err, new(*TargetCheckoutError)) {
			t.Fatalf("an unregistered target reported as a checkout problem: %v", err)
		}
	}
	if kindCount(t, k, it.ID, item.KindSeatBound) != 0 {
		t.Fatal("a refused seat was bound")
	}
	_, _, err := k.ItemChange(it.ID)
	refusedTarget(t, err, "api")
	_, err = k.JudgedChanges(it.ID)
	refusedTarget(t, err, "api")
	_, err = k.Collect(it.ID, "main", "", "ada")
	refusedTarget(t, err, "api")
	_, err = k.Transition(context.Background(), it.ID, "c", "ada")
	refusedTarget(t, err, "api")
	if len(d.calls) != 0 {
		t.Fatalf("dispatched %v", d.calls)
	}
	j, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("g")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	targetGhost(t, k, j.ID)
	if _, err := k.Transition(context.Background(), j.ID, "b", "ada"); err != nil {
		t.Fatalf("a groupless move of an unregistered target: %v", err)
	}
	if m := lastMove(t, k, j.ID); m.Change != nil {
		t.Fatalf("a groupless move recorded %+v", m.Change)
	}
}

func TestTargetGitEnvWithholdsCredentials(t *testing.T) {
	for _, mode := range []string{"target", "self"} {
		t.Run(mode, func(t *testing.T) {
			f := targetFixture(t, true)
			for _, kv := range [][2]string{{"API_TOKEN", "t"}, {"MY_SECRET", "s"}, {"DB_PASSWORD", "p"}, {"NODE_KEY", "k"}, {"SATELLE_NODE_WITHHOLD", "NODE_KEY"}, {"ANTHROPIC_API_KEY", "a"}, {"AWS_ACCESS_KEY_ID", "w"}} {
				t.Setenv(kv[0], kv[1])
			}
			calls := gitShim(t, "")
			k, _ := f.open(t)
			repo, g := "web", f.tgt
			if mode == "self" {
				repo, g = "", f.ctl
			}
			it := atB(t, k, repo)
			f.seat(t, k, it.ID)
			commitFile(t, g, "feat/x.go", "package feat\n")
			d, _, err := k.ItemChange(it.ID)
			if err != nil {
				t.Fatal(err)
			}
			k.ItemStat(d)
			dryInput(t, k, it.ID, "code-ac")
			if _, err := k.Transition(context.Background(), it.ID, "c", "ada"); err != nil {
				t.Fatal(err)
			}
			all := calls()
			if mode == "self" {
				ctl := at(all, f.ctl)
				if len(ctl) == 0 {
					t.Fatal("no control reads")
				}
				for _, c := range ctl {
					if !slices.Contains(c.env, "API_TOKEN=t") || strings.Contains(c.argv, " -c ") || strings.Contains(c.argv, "--attr-source") || strings.Contains(c.argv, "--no-textconv") || strings.Contains(c.argv, "--ignore-submodules") || strings.Contains(c.argv, "--get-regexp") || strings.HasPrefix(c.argv, "-C "+f.ctl+" version") {
						t.Errorf("self read %q (env %d entries)", c.argv, len(c.env))
					}
				}
				return
			}
			tgt := at(all, f.tgt)
			if len(tgt) == 0 {
				t.Fatal("no target reads")
			}
			pre := "-C " + f.tgt + " -c core.fsmonitor=false -c credential.helper= -c core.attributesFile= -c diff.submodule=short -c submodule.recurse=false -c status.submoduleSummary=false "
			post := "-C " + f.tgt + " --attr-source=" + sha1Tree + " -c core.fsmonitor=false -c credential.helper= -c core.attributesFile= -c diff.submodule=short -c submodule.recurse=false -c status.submoduleSummary=false "
			versions, names, texts, numstats := 0, 0, 0, 0
			for _, c := range tgt {
				env := slices.Clone(c.env)
				sort.Strings(env)
				for _, kv := range env {
					name, _, _ := strings.Cut(kv, "=")
					switch name {
					case "PATH", "HOME", "XDG_CONFIG_HOME":
					default:
						if !slices.Contains([]string{"GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"}, kv) {
							t.Errorf("target read %q carries %s", c.argv, kv)
						}
					}
				}
				rest, early := strings.CutPrefix(c.argv, pre)
				if !early {
					var ok bool
					if rest, ok = strings.CutPrefix(c.argv, post); !ok {
						t.Errorf("target argv %q", c.argv)
						continue
					}
				}
				isEarly := strings.HasPrefix(rest, "config --local --get remote.origin.url") || rest == "version" || rest == "rev-parse --show-toplevel" || strings.HasPrefix(rest, "config --includes --null --get-regexp") || rest == "rev-parse --show-object-format"
				if early != isEarly {
					t.Errorf("target argv %q: attribute source %v for this read", c.argv, !early)
				}
				if rest == "version" {
					versions++
				}
				if strings.HasPrefix(rest, "diff ") {
					if !strings.HasPrefix(rest, "diff --no-ext-diff --no-textconv --ignore-submodules=dirty --submodule=short ") {
						t.Errorf("target diff %q", c.argv)
					}
					switch {
					case strings.Contains(rest, "--name-only"):
						names++
					case strings.Contains(rest, "--numstat"):
						numstats++
					default:
						texts++
					}
				}
			}
			if versions > 1 || names == 0 || texts == 0 || numstats == 0 {
				t.Errorf("versions %d, name-only %d, text %d, numstat %d", versions, names, texts, numstats)
			}
		})
	}
}

func TestTargetGitIgnoresCallerGitOverrides(t *testing.T) {
	f := targetFixture(t, true)
	for k, v := range map[string]string{
		"GIT_DIR": filepath.Join(f.ctl, ".git"), "GIT_WORK_TREE": f.ctl, "GIT_INDEX_FILE": filepath.Join(f.ctl, ".git", "index"),
		"GIT_CEILING_DIRECTORIES": f.tgt, "GIT_NO_LAZY_FETCH": "0", "GIT_TERMINAL_PROMPT": "1",
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "remote.origin.url", "GIT_CONFIG_VALUE_0": webOrigin,
		"GIT_CONFIG_PARAMETERS": "'core.worktree'='" + f.ctl + "'",
	} {
		t.Setenv(k, v)
	}
	k, _ := f.open(t)
	for _, n := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CEILING_DIRECTORIES", "GIT_CONFIG_PARAMETERS"} {
		os.Unsetenv(n)
	}
	headT := gittest.Git(t, f.tgt, "rev-parse", "HEAD")
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t"), Repo: ptr("web")}, "ada")
	if err != nil || it.Base != headT {
		t.Fatalf("create %+v %v, want base %s", it, err, headT)
	}
	k.Transition(context.Background(), it.ID, "b", "ada")
	f.seat(t, k, it.ID)
	commitFile(t, f.tgt, "feat/x.go", "package feat\n")
	commitFile(t, f.ctl, "noise/y.go", "package noise\n")
	d, _, err := k.ItemChange(it.ID)
	if err != nil || d.Base != headT || !slices.Equal(d.Paths, []string{"feat/x.go"}) {
		t.Fatalf("ItemChange %+v %v", d, err)
	}
	gittest.Git(t, f.tgt, "remote", "set-url", "origin", "https://github.com/o/other")
	j := atB(t, k, "web")
	_, err = k.Seat(j.ID, "s1", "local:"+f.ctl, "", "ada")
	checkoutState(t, err, reporeg.CheckoutMismatch)
}

func TestTargetGitRunsNoConfiguredProgram(t *testing.T) {
	f := targetFixture(t, true)
	scripts := t.TempDir()
	mark := func(name string) (script, m string) {
		m = filepath.Join(scripts, name+".ran")
		script = filepath.Join(scripts, name+".sh")
		write(t, script, "#!/bin/sh\ntouch '"+m+"'\ncat\n")
		os.Chmod(script, 0o755)
		return script, m
	}
	write(t, filepath.Join(f.tgt, ".gitattributes"), "*.go diff=mark filter=mark\n")
	gittest.Git(t, f.tgt, "add", ".gitattributes")
	gittest.Git(t, f.tgt, "commit", "-q", "-m", "attrs")
	k, _ := f.open(t)
	it := atB(t, k, "web")
	f.seat(t, k, it.ID)
	commitFile(t, f.tgt, "feat/x.go", "package feat\n")
	write(t, filepath.Join(f.tgt, "web", "a.go"), "package web\n\nvar A = 1\n")
	var marks []string
	for key, name := range map[string]string{"diff.mark.textconv": "textconv", "core.fsmonitor": "fsmonitor", "filter.mark.clean": "clean", "filter.mark.process": "process", "credential.helper": "credential"} {
		s, m := mark(name)
		gittest.Git(t, f.tgt, "config", key, s)
		marks = append(marks, m)
	}
	d, _, err := k.ItemChange(it.ID)
	if err != nil || !slices.Contains(d.Paths, "web/a.go") {
		t.Fatalf("ItemChange %+v %v", d, err)
	}
	if _, err := k.ItemStat(d); err != nil {
		t.Fatal(err)
	}
	dry, err := dryInput(t, k, it.ID, "code-ac")
	if err != nil || !strings.Contains(dry.Input.Prompt, "web/a.go") {
		t.Fatalf("dry %v %+v", err, dry.Input)
	}
	k.JudgedChanges(it.ID)
	if _, err := k.Transition(context.Background(), it.ID, "c", "ada"); err != nil {
		t.Fatal(err)
	}
	for _, m := range marks {
		if _, err := os.Stat(m); err == nil {
			t.Errorf("a target read ran %s", filepath.Base(m))
		}
	}
	if _, err := (gitfacts.Repo{Dir: f.tgt}).Diff(gittest.Git(t, f.tgt, "rev-parse", "HEAD~2"), nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"textconv", "process"} {
		if _, err := os.Stat(filepath.Join(scripts, name+".ran")); err != nil {
			t.Errorf("the trusted diff did not run %s, so the fixture proves nothing", name)
		}
	}
}

func TestTargetGitVersionFloor(t *testing.T) {
	banner := func(b string) string {
		return `[ "$sub" = version ] && { echo '` + b + `'; exit 0; }`
	}
	t.Run("old", func(t *testing.T) {
		f := targetFixture(t, false)
		calls := gitShim(t, banner("git version 2.43.0"))
		k, d := f.open(t)
		it := atB(t, k, "web")
		_, err := k.Seat(it.ID, "s1", "local:"+f.ctl, "", "ada")
		tce := checkoutState(t, err, CheckoutGitTooOld)
		if tce.Found != "2.43.0" || !strings.Contains(err.Error(), "git 2.43.0 is older than 2.44, which satelle needs to read a target checkout without fetching") {
			t.Fatalf("err %v", err)
		}
		if kindCount(t, k, it.ID, item.KindSeatBound) != 0 {
			t.Fatal("a refused seat was bound")
		}
		for _, c := range at(calls(), f.tgt) {
			if !strings.Contains(c.argv, "config --local --get remote.origin.url") && !strings.HasSuffix(c.argv, " version") {
				t.Errorf("a read ran below the floor: %q", c.argv)
			}
		}
		j := atB(t, k, "web")
		if _, err := k.Ledger.Append(j.ID, item.KindSeatBound, "ada", item.Seat{Session: "s1", Location: "local:" + f.ctl, Checkout: f.tgt}); err != nil {
			t.Fatal(err)
		}
		_, err = k.Transition(context.Background(), j.ID, "c", "ada")
		checkoutState(t, err, CheckoutGitTooOld)
		_, err = dryInput(t, k, j.ID, "code-ac")
		checkoutState(t, err, CheckoutGitTooOld)
		_, _, err = k.ItemChange(j.ID)
		checkoutState(t, err, CheckoutGitTooOld)
		_, err = k.JudgedChanges(j.ID)
		checkoutState(t, err, CheckoutGitTooOld)
		if len(d.calls) != 0 || kindCount(t, k, j.ID, "gate.run") != 0 || lastMove(t, k, j.ID).To != "b" {
			t.Fatalf("a refused move dispatched %v", d.calls)
		}
		s := atB(t, k, "")
		f.seat(t, k, s.ID)
		commitFile(t, f.ctl, "feat/x.go", "package feat\n")
		if diff, _, err := k.ItemChange(s.ID); err != nil || !slices.Equal(diff.Paths, []string{"feat/x.go"}) {
			t.Fatalf("self ItemChange %+v %v", diff, err)
		}
		if _, err := k.Transition(context.Background(), s.ID, "c", "ada"); err != nil || len(d.calls) == 0 {
			t.Fatalf("self transition %v %v", err, d.calls)
		}
		for _, c := range at(calls(), f.ctl) {
			if strings.HasSuffix(c.argv, " version") {
				t.Errorf("a self item read the git version: %q", c.argv)
			}
		}
	})
	t.Run("unparseable", func(t *testing.T) {
		f := targetFixture(t, false)
		gitShim(t, banner("git version banana"))
		k, _ := f.open(t)
		it := atB(t, k, "web")
		_, err := k.Seat(it.ID, "s1", "local:"+f.ctl, "", "ada")
		checkoutState(t, err, CheckoutGitUnreadable)
		if kindCount(t, k, it.ID, item.KindSeatBound) != 0 {
			t.Fatal("a refused seat was bound")
		}
	})
	for _, b := range []string{"git version 2.44.0", "git version 2.45.1.windows.1"} {
		t.Run(b, func(t *testing.T) {
			f := targetFixture(t, true)
			gitShim(t, banner(b))
			k, _ := f.open(t)
			if _, err := k.worktreeNamed("web"); err != nil {
				var tce *TargetCheckoutError
				if errors.As(err, &tce) && (tce.State == CheckoutGitTooOld || tce.State == CheckoutGitUnreadable) {
					t.Fatalf("%s refused: %v", b, err)
				}
			}
		})
	}
}

func objectsListing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(filepath.Join(dir, ".git", "objects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			info, _ := d.Info()
			out = append(out, p+" "+strconv.FormatInt(info.Size(), 10))
		}
		return nil
	})
	return out
}

func TestTargetPartialCloneRefused(t *testing.T) {
	f := targetFixture(t, false)
	first := gittest.Git(t, f.tgt, "rev-parse", "HEAD")
	commitFile(t, f.tgt, "web/a.go", "package web\n\nvar Second = 2\n")
	src := filepath.Join(t.TempDir(), "src.git")
	gittest.Git(t, f.tgt, "clone", "-q", "--bare", f.tgt, src)
	gittest.Git(t, src, "config", "uploadpack.allowFilter", "true")
	scripts := t.TempDir()
	marker := filepath.Join(scripts, "upload.ran")
	upload := filepath.Join(scripts, "upload.sh")
	write(t, upload, "#!/bin/sh\ntouch '"+marker+"'\nexec git-upload-pack \"$@\"\n")
	os.Chmod(upload, 0o755)
	clone := func(t *testing.T) string {
		part := t.TempDir()
		os.Remove(part)
		gittest.Git(t, filepath.Dir(part), "clone", "-q", "--no-local", "--filter=blob:none", "file://"+src, part)
		gittest.Git(t, part, "remote", "rename", "origin", "lazy")
		gittest.Git(t, part, "config", "extensions.partialclone", "lazy")
		gittest.Git(t, part, "config", "remote.lazy.uploadpack", upload)
		gittest.Git(t, part, "remote", "add", "origin", webOrigin)
		return part
	}
	unset := func(t *testing.T, part string, keep string) {
		for _, key := range []string{"extensions.partialclone", "remote.lazy.promisor", "remote.lazy.partialclonefilter"} {
			if key != keep {
				gittest.Git(t, part, "config", "--unset", key)
			}
		}
	}
	gitShim(t, `[ "$sub" = version ] && { echo 'git version 2.56.0'; exit 0; }`)
	cases := []struct {
		name  string
		setup func(t *testing.T, part string)
	}{
		{"clone", func(t *testing.T, part string) {}},
		{"extension", func(t *testing.T, part string) { unset(t, part, "extensions.partialclone") }},
		{"promisor", func(t *testing.T, part string) { unset(t, part, "remote.lazy.promisor") }},
		{"filter", func(t *testing.T, part string) { unset(t, part, "remote.lazy.partialclonefilter") }},
		{"include", func(t *testing.T, part string) {
			unset(t, part, "")
			inc := filepath.Join(t.TempDir(), "inc")
			write(t, inc, "[remote \"lazy\"]\n\tpromisor = true\n")
			gittest.Git(t, part, "config", "include.path", inc)
		}},
		{"global", func(t *testing.T, part string) {
			unset(t, part, "")
			home := t.TempDir()
			write(t, filepath.Join(home, ".gitconfig"), "[remote \"lazy\"]\n\tpromisor = true\n")
			t.Setenv("HOME", home)
		}},
		{"odd", func(t *testing.T, part string) {
			unset(t, part, "")
			gittest.Git(t, part, "config", "remote.lazy.promisor", "2")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			part := clone(t)
			c.setup(t, part)
			f.mapTo(t, part)
			countSrc := gittest.Git(t, src, "count-objects", "-v")
			countPart := gittest.Git(t, part, "count-objects", "-v")
			listing := objectsListing(t, part)
			calls := gitShim(t, `[ "$sub" = version ] && { echo 'git version 2.56.0'; exit 0; }`)
			k, d := f.open(t)
			it := atB(t, k, "web")
			_, err := k.Seat(it.ID, "s1", "local:"+f.ctl, "", "ada")
			tce := checkoutState(t, err, CheckoutPartialClone)
			if tce.Path != part || !strings.Contains(err.Error(), "is a partial clone; satelle reads only a complete checkout") {
				t.Fatalf("err %v", err)
			}
			if kindCount(t, k, it.ID, item.KindSeatBound) != 0 {
				t.Fatal("a refused seat was bound")
			}
			j := atB(t, k, "web")
			k.Ledger.Append(j.ID, item.KindSeatBound, "ada", item.Seat{Session: "s1", Location: "local:" + f.ctl, Checkout: part})
			_, err = k.Transition(context.Background(), j.ID, "c", "ada")
			checkoutState(t, err, CheckoutPartialClone)
			_, err = dryInput(t, k, j.ID, "code-ac")
			checkoutState(t, err, CheckoutPartialClone)
			_, _, err = k.ItemChange(j.ID)
			checkoutState(t, err, CheckoutPartialClone)
			_, err = k.JudgedChanges(j.ID)
			checkoutState(t, err, CheckoutPartialClone)
			_, err = k.Collect(j.ID, "main", "", "ada")
			checkoutState(t, err, CheckoutPartialClone)
			if len(d.calls) != 0 || kindCount(t, k, j.ID, "gate.run") != 0 {
				t.Fatalf("a refused move dispatched %v", d.calls)
			}
			for _, call := range at(calls(), part) {
				rest := call.argv
				for _, s := range []string{" diff ", " ls-files ", " merge-base ", " status ", "--show-object-format"} {
					if strings.Contains(rest, s) {
						t.Errorf("a partial clone was read: %q", call.argv)
					}
				}
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the promisor remote was asked for objects")
			}
			if gittest.Git(t, src, "count-objects", "-v") != countSrc || gittest.Git(t, part, "count-objects", "-v") != countPart || !slices.Equal(objectsListing(t, part), listing) {
				t.Fatal("objects changed")
			}
		})
	}
	t.Run("promisor-false", func(t *testing.T) {
		part := clone(t)
		unset(t, part, "")
		gittest.Git(t, part, "config", "remote.lazy.promisor", "false")
		f.mapTo(t, part)
		k, _ := f.open(t)
		if _, err := k.worktreeNamed("web"); err != nil {
			var tce *TargetCheckoutError
			if errors.As(err, &tce) && tce.State == CheckoutPartialClone {
				t.Fatalf("a false promisor refused: %v", err)
			}
		}
	})
	t.Run("control", func(t *testing.T) {
		part := clone(t)
		os.Remove(marker)
		gittest.Git(t, part, "show", first+":web/a.go")
		if _, err := os.Stat(marker); err != nil {
			t.Fatal("the fixture's partial clone did not fetch, so the refusal proves nothing")
		}
	})
}

func TestTargetGitInspectsNoSubmodule(t *testing.T) {
	f := targetFixture(t, true)
	sub := t.TempDir()
	gittest.Git(t, sub, "init", "-q", "-b", "main")
	write(t, filepath.Join(sub, ".gitattributes"), "*.txt filter=evil\n")
	write(t, filepath.Join(sub, "a.txt"), "a\n")
	gittest.Git(t, sub, "add", ".")
	gittest.Git(t, sub, "commit", "-q", "-m", "sub")
	gittest.Git(t, f.tgt, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "s")
	gittest.Git(t, f.tgt, "commit", "-q", "-m", "add s")
	home := t.TempDir()
	t.Setenv("HOME", home)
	trace := filepath.Join(t.TempDir(), "trace")
	calls := gitShim(t, `case "$*" in "-C `+f.tgt+` "*) GIT_TRACE='`+trace+`'; export GIT_TRACE ;; esac`)
	k, _ := f.open(t)
	it := atB(t, k, "web")
	f.seat(t, k, it.ID)
	commitFile(t, f.tgt, "feat/x.go", "package feat\n")
	s := filepath.Join(f.tgt, "s")
	commitFile(t, s, "b.txt", "b\n")
	scripts := t.TempDir()
	var marks []string
	mk := func(name string) string {
		m := filepath.Join(scripts, name+".ran")
		p := filepath.Join(scripts, name+".sh")
		write(t, p, "#!/bin/sh\ntouch '"+m+"'\ncat\n")
		os.Chmod(p, 0o755)
		marks = append(marks, m)
		return p
	}
	gittest.Git(t, s, "config", "filter.evil.clean", mk("subclean"))
	gittest.Git(t, s, "config", "filter.evil.process", mk("subprocess"))
	write(t, filepath.Join(home, ".gitconfig"), "[filter \"evil\"]\n\tclean = "+mk("homeclean")+"\n\tprocess = "+mk("homeprocess")+"\n[diff]\n\tsubmodule = diff\n")
	write(t, filepath.Join(s, "a.txt"), "changed\n")
	d, _, err := k.ItemChange(it.ID)
	if err != nil || !slices.Equal(d.Paths, []string{"feat/x.go", "s"}) {
		t.Fatalf("ItemChange %+v %v", d, err)
	}
	st, err := k.ItemStat(d)
	if err != nil || !slices.ContainsFunc(st, func(fs gitfacts.FileStat) bool { return fs.Path == "s" }) {
		t.Fatalf("ItemStat %+v %v", st, err)
	}
	dry, err := dryInput(t, k, it.ID, "code-ac")
	if err != nil || !strings.Contains(dry.Input.Prompt, "Subproject commit") || strings.Contains(dry.Input.Prompt, "+changed") {
		t.Fatalf("dry %v:\n%+v", err, dry.Input)
	}
	k.JudgedChanges(it.ID)
	if _, err := k.Transition(context.Background(), it.ID, "c", "ada"); err != nil {
		t.Fatal(err)
	}
	for _, m := range marks {
		if _, err := os.Stat(m); err == nil {
			t.Errorf("%s ran", filepath.Base(m))
		}
	}
	if b, _ := os.ReadFile(trace); strings.Contains(string(b), "run_command:") {
		t.Errorf("a child process was started:\n%s", b)
	}
	for _, c := range at(calls(), f.tgt) {
		if strings.Contains(c.argv, " diff ") && !strings.Contains(c.argv, "--ignore-submodules=dirty --submodule=short") {
			t.Errorf("target diff %q", c.argv)
		}
	}
}

func TestTargetObjectFormat(t *testing.T) {
	t.Run("sha256", func(t *testing.T) {
		f := targetFixture(t, true)
		f.tgt = t.TempDir()
		targetRepo(t, f.tgt, "--object-format=sha256")
		f.mapTo(t, f.tgt)
		calls := gitShim(t, "")
		k, _ := f.open(t)
		it := atB(t, k, "web")
		f.seat(t, k, it.ID)
		commitFile(t, f.tgt, "feat/x.go", "package feat\n\nvar X = 1\n")
		d, _, err := k.ItemChange(it.ID)
		if err != nil || len(d.Base) != 64 || !slices.Equal(d.Paths, []string{"feat/x.go"}) {
			t.Fatalf("ItemChange %+v %v", d, err)
		}
		dry, err := dryInput(t, k, it.ID, "code-ac")
		if err != nil || !strings.Contains(dry.Input.Prompt, "+var X = 1") {
			t.Fatalf("dry %v %+v", err, dry.Input)
		}
		post := 0
		for _, c := range at(calls(), f.tgt) {
			if strings.Contains(c.argv, "--attr-source=") {
				post++
				if !strings.Contains(c.argv, "--attr-source=6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321 ") {
					t.Errorf("sha256 target argv %q", c.argv)
				}
			}
		}
		if post == 0 {
			t.Fatal("no post-format target read")
		}
	})
	for _, c := range []struct{ name, pre string }{
		{"sha3", `case "$*" in *--show-object-format*) echo sha3; exit 0 ;; esac`},
		{"fails", `case "$*" in *--show-object-format*) exit 1 ;; esac`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := targetFixture(t, true)
			k, d := f.open(t)
			j := atB(t, k, "web")
			f.seat(t, k, j.ID)
			k.Close()
			calls := gitShim(t, c.pre)
			k, d = f.open(t)
			it := atB(t, k, "web")
			_, err := k.Seat(it.ID, "s1", "local:"+f.ctl, "", "ada")
			checkoutState(t, err, CheckoutUnreadableFormat)
			if kindCount(t, k, it.ID, item.KindSeatBound) != 0 {
				t.Fatal("a refused seat was bound")
			}
			for _, call := range at(calls(), f.tgt) {
				for _, s := range []string{" diff ", " ls-files ", " merge-base "} {
					if strings.Contains(call.argv, s) {
						t.Errorf("read with no format: %q", call.argv)
					}
				}
			}
			_, err = k.Transition(context.Background(), j.ID, "c", "ada")
			checkoutState(t, err, CheckoutUnreadableFormat)
			if len(d.calls) != 0 {
				t.Fatalf("dispatched %v", d.calls)
			}
		})
	}
}

func TestTargetCheckoutPathRefusals(t *testing.T) {
	t.Run("subdir", func(t *testing.T) {
		f := targetFixture(t, true)
		k, d := f.open(t)
		j := atB(t, k, "web")
		f.seat(t, k, j.ID)
		k.Close()
		f.mapTo(t, filepath.Join(f.tgt, "web"))
		k, d = f.open(t)
		it := atB(t, k, "web")
		_, err := k.Seat(it.ID, "s1", "local:"+f.ctl, "", "ada")
		tce := checkoutState(t, err, CheckoutNotTopLevel)
		if tce.Top != f.tgt || !strings.Contains(err.Error(), "map "+f.tgt+" in .satelle/repos.local.toml") {
			t.Fatalf("err %v", err)
		}
		if kindCount(t, k, it.ID, item.KindSeatBound) != 0 {
			t.Fatal("a refused seat was bound")
		}
		_, _, err = k.ItemChange(j.ID)
		checkoutState(t, err, CheckoutNotTopLevel)
		_, err = k.Transition(context.Background(), j.ID, "c", "ada")
		checkoutState(t, err, CheckoutNotTopLevel)
		if len(d.calls) != 0 {
			t.Fatalf("dispatched %v", d.calls)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		f := targetFixture(t, true)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(f.tgt, link); err != nil {
			t.Fatal(err)
		}
		f.mapTo(t, link)
		k, _ := f.open(t)
		wt, err := k.worktreeNamed("web")
		if err != nil || wt.dir != f.tgt {
			t.Fatalf("worktree %+v %v", wt, err)
		}
	})
	t.Run("unmapped", func(t *testing.T) {
		f := targetFixture(t, true)
		f.mapTo(t, "")
		k, d := f.open(t)
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t"), Repo: ptr("web")}, "ada")
		if err != nil || it.Base != "" {
			t.Fatalf("create %+v %v", it, err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
			t.Fatalf("a groupless move: %v", err)
		}
		_, err = k.Seat(it.ID, "s1", "local:"+f.ctl, "", "ada")
		checkoutState(t, err, reporeg.CheckoutUnmapped)
		_, _, err = k.ItemChange(it.ID)
		checkoutState(t, err, reporeg.CheckoutUnmapped)
		_, err = k.Transition(context.Background(), it.ID, "c", "ada")
		checkoutState(t, err, reporeg.CheckoutUnmapped)
		if len(d.calls) != 0 || kindCount(t, k, it.ID, "gate.run") != 0 {
			t.Fatalf("dispatched %v", d.calls)
		}
	})
}

func TestTargetCheckoutPathShellSafe(t *testing.T) {
	cp := func(t *testing.T, src, dst string) {
		t.Helper()
		if out, err := exec.Command("cp", "-a", src+"/.", dst).CombinedOutput(); err != nil {
			t.Fatalf("cp: %v %s", err, out)
		}
	}
	for _, name := range []string{"dollar", "space", "backtick", "symlink"} {
		t.Run(name, func(t *testing.T) {
			f := targetFixture(t, true)
			marker := filepath.Join(t.TempDir(), "check.ran")
			write(t, filepath.Join(f.ctl, ".satelle", "skills", "facts.md"), "---\ntype: check\n---\n```sh\ntouch '"+marker+"'\n```\n")
			f.extra = "[gates.facts]\nskill = \"facts\"\n"
			f.group = `"facts", "code-ac"`
			parent := t.TempDir()
			unsafe := map[string]string{"dollar": `d"$(id)`, "space": "a b", "backtick": "b`id`", "symlink": `d"$(id)`}[name]
			dir := filepath.Join(parent, unsafe)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			cp(t, f.tgt, dir)
			mapped := dir
			if name == "symlink" {
				mapped = filepath.Join(t.TempDir(), "safe")
				if err := os.Symlink(dir, mapped); err != nil {
					t.Fatal(err)
				}
			}
			calls := gitShim(t, "")
			f.mapTo(t, mapped)
			k, d := f.open(t)
			it := atB(t, k, "web")
			_, err := k.Seat(it.ID, "s1", "local:"+f.ctl, "", "ada")
			checkoutState(t, err, reporeg.CheckoutUnsafe)
			if !strings.Contains(err.Error(), "A-Z a-z 0-9 . _ / + @ -") || !strings.Contains(err.Error(), "'") {
				t.Fatalf("err %v", err)
			}
			if kindCount(t, k, it.ID, item.KindSeatBound) != 0 {
				t.Fatal("a refused seat was bound")
			}
			if c := at(calls(), dir); len(c) != 0 {
				t.Fatalf("git ran at the unsafe path: %v", c)
			}
			k.Close()
			f.mapTo(t, f.tgt)
			k, d = f.open(t)
			j := atB(t, k, "web")
			f.seat(t, k, j.ID)
			k.Close()
			f.mapTo(t, mapped)
			k, d = f.open(t)
			_, err = k.Transition(context.Background(), j.ID, "c", "ada")
			checkoutState(t, err, reporeg.CheckoutUnsafe)
			_, err = dryInput(t, k, j.ID, "code-ac")
			checkoutState(t, err, reporeg.CheckoutUnsafe)
			if _, err := os.Stat(marker); err == nil || len(d.calls) != 0 || kindCount(t, k, j.ID, "gate.run") != 0 || lastMove(t, k, j.ID).To != "b" {
				t.Fatalf("a refused move ran its gates: %v", d.calls)
			}
		})
	}
}

func TestTargetCheckoutMovedSinceSeat(t *testing.T) {
	f := targetFixture(t, true)
	k, d := f.open(t)
	it := atB(t, k, "web")
	if s := f.seat(t, k, it.ID); s.Checkout != f.tgt {
		t.Fatalf("seat %+v", s)
	}
	commitFile(t, f.tgt, "feat/x.go", "package feat\n")
	k.Close()
	tgt2 := t.TempDir()
	if out, err := exec.Command("cp", "-a", f.tgt+"/.", tgt2).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	commitFile(t, tgt2, "feat/y.go", "package feat\n")
	f.mapTo(t, tgt2)
	k, d = f.open(t)
	_, err := k.Transition(context.Background(), it.ID, "c", "ada")
	tce := checkoutState(t, err, CheckoutMoved)
	if tce.Seated != f.tgt || tce.Path != tgt2 || !strings.Contains(err.Error(), f.tgt) || !strings.Contains(err.Error(), tgt2) {
		t.Fatalf("err %v", err)
	}
	if len(d.calls) != 0 || kindCount(t, k, it.ID, "gate.run") != 0 || lastMove(t, k, it.ID).To != "b" {
		t.Fatalf("dispatched %v", d.calls)
	}
	_, err = dryInput(t, k, it.ID, "code-ac")
	checkoutState(t, err, CheckoutMoved)
	_, _, err = k.ItemChange(it.ID)
	checkoutState(t, err, CheckoutMoved)
	_, err = k.JudgedChanges(it.ID)
	checkoutState(t, err, CheckoutMoved)
	_, err = k.Collect(it.ID, "main", "", "ada")
	checkoutState(t, err, CheckoutMoved)
	if s := f.seat(t, k, it.ID); s.Checkout != tgt2 {
		t.Fatalf("re-seat %+v", s)
	}
	if _, err := k.Transition(context.Background(), it.ID, "c", "ada"); err != nil {
		t.Fatal(err)
	}
	if m := lastMove(t, k, it.ID); m.Change == nil || m.Change.Head != gittest.Git(t, tgt2, "rev-parse", "HEAD") {
		t.Fatalf("frozen %+v", m.Change)
	}

	j := atB(t, k, "web")
	k.Ledger.Append(j.ID, item.KindSeatBound, "ada", item.Seat{Session: "s1", Location: "local:" + f.ctl})
	_, err = k.Transition(context.Background(), j.ID, "c", "ada")
	if tce := checkoutState(t, err, CheckoutNotRecorded); !strings.Contains(tce.Error(), "re-seat") {
		t.Fatalf("err %v", err)
	}
	f.seat(t, k, j.ID)
	if _, err := k.Transition(context.Background(), j.ID, "c", "ada"); err != nil {
		t.Fatal(err)
	}

	p := atB(t, k, "web")
	f.seat(t, k, p.ID)
	k.Ledger.Append(p.ID, item.KindSeatBound, "ada", item.Seat{Session: "s2", Location: "local:" + f.ctl})
	if _, _, err := k.ItemChange(p.ID); err != nil {
		t.Fatalf("a perform seat with no checkout: %v", err)
	}
}

func TestRetargetBeforeSeatRebases(t *testing.T) {
	f := targetFixture(t, true)
	k, _ := f.open(t)
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t")}, "ada")
	if err != nil || it.Base != gittest.Git(t, f.ctl, "rev-parse", "HEAD") {
		t.Fatalf("create %+v %v", it, err)
	}
	if it, err = k.Update(it.ID, item.Fields{Repo: ptr("web")}, "ada"); err != nil || it.Base != gittest.Git(t, f.tgt, "rev-parse", "HEAD") {
		t.Fatalf("retarget %+v %v", it, err)
	}
	k.Close()
	f.mapTo(t, "")
	k, _ = f.open(t)
	j, _ := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("u")}, "ada")
	if j, err = k.Update(j.ID, item.Fields{Repo: ptr("web")}, "ada"); err != nil || j.Base != "" {
		t.Fatalf("retarget to an unmapped checkout %+v %v", j, err)
	}
}

func TestTargetGatedTransitionInputs(t *testing.T) {
	for _, mode := range []string{"target", "self"} {
		t.Run(mode, func(t *testing.T) {
			f := targetFixture(t, true)
			log := filepath.Join(t.TempDir(), "facts.log")
			write(t, filepath.Join(f.ctl, ".satelle", "skills", "facts.md"), "---\ntype: check\n---\n```sh\n{\n  echo \"pwd=$PWD\"\n  echo \"base=$SATELLE_BASE\"\n  echo \"worktree=${SATELLE_WORKTREE-unset}\"\n  echo \"stdin=$(cat)\"\n  echo \"head=$(git -C \"${SATELLE_WORKTREE:-$PWD}\" rev-parse HEAD)\"\n  echo \"names=$(git -C \"${SATELLE_WORKTREE:-$PWD}\" diff --name-only \"$SATELLE_BASE\" | tr '\\n' ' ')\"\n} >> '"+log+"'\n```\n")
			f.extra = "[gates.git-facts]\nskill = \"facts\"\n"
			f.group = `"git-facts", "code-ac"`
			t.Setenv("SATELLE_WORKTREE", "/tmp/x")
			k, d := f.open(t)
			repo, g, o := "web", f.tgt, f.ctl
			if mode == "self" {
				repo, g, o = "", f.ctl, f.tgt
			}
			it := atB(t, k, repo)
			s := f.seat(t, k, it.ID)
			commitFile(t, g, "feat/x.go", "package feat\n\nvar X = 1\n")
			commitFile(t, o, "noise/y.go", "package noise\n")
			dry, err := dryInput(t, k, it.ID, "git-facts")
			if err != nil || dry.Input.Dir != f.ctl {
				t.Fatalf("dry %v %+v", err, dry.Input)
			}
			hasVar := slices.Contains(dry.Input.Check.Env, "SATELLE_WORKTREE="+f.tgt)
			if mode == "target" && (dry.Input.Worktree != f.tgt || !hasVar) || mode == "self" && (dry.Input.Worktree != "" || hasVar) {
				t.Fatalf("dry input %+v", dry.Input)
			}
			if _, err := k.Transition(context.Background(), it.ID, "c", "ada"); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				k, v, _ := strings.Cut(l, "=")
				got[k] = v
			}
			wantWT := map[bool]string{true: f.tgt, false: "unset"}[mode == "target"]
			if got["pwd"] != f.ctl || got["base"] != s.Base || got["worktree"] != wantWT || got["head"] != gittest.Git(t, g, "rev-parse", "HEAD") || strings.TrimSpace(got["names"]) != "feat/x.go" {
				t.Fatalf("check saw %v", got)
			}
			var stdin map[string]any
			if err := json.Unmarshal([]byte(got["stdin"]), &stdin); err != nil {
				t.Fatal(err)
			}
			if w, ok := stdin["worktree"]; mode == "target" && w != f.tgt || mode == "self" && ok {
				t.Fatalf("stdin %v", stdin)
			}
			if len(d.reqs) != 1 {
				t.Fatalf("reqs %v", d.calls)
			}
			req := d.reqs[0]
			wantDir := map[bool]string{true: f.tgt, false: f.ctl}[mode == "target"]
			if req.Dir != wantDir || (req.Target != nil) != (mode == "target") || !slices.Equal(req.Env, []string{"SATELLE_SUBSTRATE_DIR=" + filepath.Join(f.ctl, ".satelle")}) {
				t.Fatalf("request dir %s target %+v env %v", req.Dir, req.Target, req.Env)
			}
			if mode == "target" && (req.Target.Worktree != f.tgt || req.Target.Scratch == "") {
				t.Fatalf("request target %+v", req.Target)
			}
			if !strings.Contains(req.Prompt, "feat/x.go") || !strings.Contains(req.Prompt, "+var X = 1") || strings.Contains(req.Prompt, "noise/y.go") {
				t.Fatalf("prompt:\n%s", req.Prompt)
			}
			named := strings.Contains(req.Prompt, "Target repository: web\n") && strings.Contains(req.Prompt, "Target work tree: "+f.tgt+"\n")
			if named != (mode == "target") || mode == "self" && strings.Contains(req.Prompt, "Target ") {
				t.Fatalf("target lines in a %s prompt:\n%s", mode, req.Prompt)
			}
			for _, r := range GateRuns(chainOf(t, k, it.ID)) {
				if r.Gate == "code-ac" && (r.Change == nil || r.Change.Base != s.Base || r.Change.Head != gittest.Git(t, g, "rev-parse", "HEAD")) {
					t.Fatalf("code-ac change %+v", r.Change)
				}
			}
		})
	}
}

type recordingRemote struct{ calls []string }

func (r *recordingRemote) RunRemote(_ context.Context, c gate.RemoteCheck) (gate.RemoteOutcome, error) {
	r.calls = append(r.calls, c.Head.Commit)
	return gate.RemoteOutcome{Node: "n1", Exit: 0}, nil
}

func TestTargetRemoteGateRefusedFailClosed(t *testing.T) {
	for _, mode := range []string{"target", "self"} {
		t.Run(mode, func(t *testing.T) {
			f := targetFixture(t, true)
			root := filepath.Join(f.ctl, ".satelle")
			write(t, filepath.Join(root, "skills", "chk.md"), "---\ntype: check\n---\n```sh\nexit 0\n```\n")
			write(t, filepath.Join(root, "remotes", "ci", "remote.toml"), "version = 1\nname = \"ci\"\nprovider = \"docker-local\"\nsatelle_version = \"1.0.0\"\n[drive]\nkind = \"item\"\n[lifecycle]\nscope = \"run\"\n")
			f.extra = "[gates.far]\nskill = \"chk\"\nplacement = \"remote:ci\"\n"
			f.group = `"far"`
			k, _ := f.open(t)
			rc := &recordingRemote{}
			k.RemoteChecks = rc
			repo := "web"
			if mode == "self" {
				repo = ""
				gittest.Git(t, f.ctl, "add", "-A")
				gittest.Git(t, f.ctl, "commit", "-q", "-m", "cfg")
				gittest.Git(t, f.ctl, "update-ref", "refs/remotes/origin/main", gittest.Git(t, f.ctl, "rev-parse", "HEAD"))
			}
			it := atB(t, k, repo)
			f.seat(t, k, it.ID)
			res, err := k.Transition(context.Background(), it.ID, "c", "ada")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "target" {
				if res.Moved || len(res.Gates) == 0 || !strings.Contains(res.Gates[0].Notes, "targets repo web") || !strings.Contains(res.Gates[0].Notes, "place this gate locally") || len(rc.calls) != 0 {
					t.Fatalf("target remote gate %+v, calls %v", res.Gates, rc.calls)
				}
				return
			}
			if len(rc.calls) != 1 || rc.calls[0] != gittest.Git(t, f.ctl, "rev-parse", "HEAD") {
				t.Fatalf("self remote gate %+v, calls %v", res.Gates, rc.calls)
			}
		})
	}
}
