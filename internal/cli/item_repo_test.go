package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

func writeRepos(t *testing.T, dir string, names ...string) {
	t.Helper()
	var b strings.Builder
	for _, n := range names {
		b.WriteString("[repos." + n + "]\norigin = \"https://github.com/o/" + n + "\"\n")
	}
	writeFile(t, filepath.Join(dir, ".satelle", reporeg.TrackedFile), b.String())
}

func mapRepos(t *testing.T, dir string, names ...string) {
	t.Helper()
	var b strings.Builder
	for _, n := range names {
		b.WriteString("[repos." + n + "]\npath = \"" + checkoutWith(t, "https://github.com/o/"+n) + "\"\n")
	}
	writeFile(t, filepath.Join(dir, ".satelle", reporeg.LocalFile), b.String())
}

func chainBytes(t *testing.T, id string) []byte {
	t.Helper()
	b, err := os.ReadFile(runtimeLedger(t).Path(id))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func itemJSON(t *testing.T, kind, id string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(must(t, kind, "get", id, "--json")), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func listRepo(t *testing.T, id string) string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(must(t, "story", "list", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r["id"] == id {
			s, _ := r["repo"].(string)
			return s
		}
	}
	t.Fatalf("story list --json lacks %s", id)
	return ""
}

func plantRepo(t *testing.T, id, name string) {
	t.Helper()
	if _, err := runtimeLedger(t).Append(id, item.KindUpdated, "me", item.Fields{Repo: &name}); err != nil {
		t.Fatal(err)
	}
}

func TestItemRepoRecordedAndShown(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeRepos(t, dir, "web")
	must(t, "validate")
	id := createID(t, "story", "create", "--title", "a", "--repo", "web")
	first, _, _ := bytes.Cut(chainBytes(t, id), []byte("\n"))
	if !bytes.Contains(first, []byte(`"repo":"web"`)) {
		t.Fatalf("created event lacks the repo: %s", first)
	}
	if out := must(t, "story", "get", id); !strings.Contains(out, "repo       web\n") {
		t.Fatalf("story get:\n%s", out)
	}
	if m := itemJSON(t, "story", id); m["repo"] != "web" {
		t.Fatalf("story get --json repo %v", m["repo"])
	}
	found := false
	for _, line := range strings.Split(must(t, "story", "list"), "\n") {
		f := strings.Fields(line)
		found = found || (len(f) == 4 && f[0] == id && f[2] == "web" && f[3] == "a")
	}
	if !found {
		t.Fatalf("story list has no line with %s, web, a:\n%s", id, must(t, "story", "list"))
	}
	if r := listRepo(t, id); r != "web" {
		t.Fatalf("story list --json repo %q", r)
	}
	self := createID(t, "story", "create", "--title", "b", "--repo", "self")
	if !bytes.Contains(chainBytes(t, self), []byte(`"repo":"self"`)) {
		t.Fatal("repo self not recorded")
	}
	if out := must(t, "story", "get", self); !strings.Contains(out, "repo       self\n") {
		t.Fatalf("story get:\n%s", out)
	}
}

func TestItemRepoRefusesUnregistered(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeRepos(t, dir, "web")
	out, err := run(t, "", "story", "create", "--title", "x", "--repo", "nope")
	if err == nil || !strings.Contains(out+err.Error(), "repo nope is not registered") || !strings.Contains(out+err.Error(), "registered: self, web") {
		t.Fatalf("create --repo nope: %v\n%s", err, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(must(t, "story", "list", "--json")), &rows); err != nil || len(rows) != 0 {
		t.Fatalf("an item was created: %v %v", rows, err)
	}
	id := createID(t, "story", "create", "--title", "y")
	before := chainBytes(t, id)
	out, err = run(t, "", "story", "set", id, "--repo", "nope")
	if err == nil || !strings.Contains(out+err.Error(), "repo nope is not registered") || !strings.Contains(out+err.Error(), "registered: self, web") {
		t.Fatalf("set --repo nope: %v\n%s", err, out)
	}
	if !bytes.Equal(before, chainBytes(t, id)) {
		t.Fatal("a refused set changed the chain")
	}
	out, err = run(t, "", "story", "create", "--title", "z", "--repo", "$(id)")
	if err == nil || !strings.Contains(out+err.Error(), "repo '$(id)' is not registered") || strings.Contains(out+err.Error(), `"$(id)"`) {
		t.Fatalf("create --repo $(id): %v\n%s", err, out)
	}
	if err := os.Remove(filepath.Join(dir, ".satelle", reporeg.TrackedFile)); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "", "story", "create", "--title", "w", "--repo", "web")
	if err == nil || !strings.Contains(out+err.Error(), "(registered: self)") {
		t.Fatalf("create --repo web with no registry: %v\n%s", err, out)
	}
}

func TestItemRepoSetOnceAfterSeat(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeRepos(t, dir, "web", "api")
	mapRepos(t, dir, "web", "api")
	id := createID(t, "story", "create", "--title", "a", "--repo", "web")
	must(t, "story", "set", id, "--repo", "api")
	must(t, "story", "seat", id, "--session", "s-1")
	must(t, "story", "release", id, "--session", "s-1")
	before := chainBytes(t, id)
	out, err := run(t, "", "story", "set", id, "--repo", "web")
	if err == nil || !strings.Contains(out+err.Error(), "has held a seat") || !strings.Contains(out+err.Error(), "set once") {
		t.Fatalf("set after seat: %v\n%s", err, out)
	}
	if !bytes.Equal(before, chainBytes(t, id)) {
		t.Fatal("a refused set changed the chain")
	}
	if m := itemJSON(t, "story", id); m["repo"] != "api" {
		t.Fatalf("repo %v after a refused set", m["repo"])
	}
	must(t, "story", "set", id, "--repo", "api")
	after := chainBytes(t, id)
	if bytes.Contains(after[len(before):], []byte(`"repo"`)) {
		t.Fatalf("setting the same repo after a seat wrote it:\n%s", after[len(before):])
	}
}

func TestItemRepoDefaultsToSelf(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "a")
	if bytes.Contains(chainBytes(t, id), []byte(`"repo"`)) {
		t.Fatal("create without --repo recorded a repo")
	}
	if m := itemJSON(t, "story", id); m["repo"] != "self" {
		t.Fatalf("repo %v, want self", m["repo"])
	}
	if out := must(t, "story", "get", id); strings.Contains(out, "repo ") {
		t.Fatalf("story get shows a repo line:\n%s", out)
	}
	if out := must(t, "story", "list"); len(strings.Fields(strings.TrimSpace(out))) != 3 {
		t.Fatalf("story list has a repo column:\n%s", out)
	}
}

func TestLegacyItemReadsAsSelfWithoutAppending(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "a")
	before := chainBytes(t, id)
	if m := itemJSON(t, "story", id); m["repo"] != "self" {
		t.Fatalf("repo %v", m["repo"])
	}
	if r := listRepo(t, id); r != "self" {
		t.Fatalf("list repo %q", r)
	}
	if !bytes.Equal(before, chainBytes(t, id)) {
		t.Fatal("reading a legacy item appended an event")
	}
	must(t, "story", "seat", id, "--session", "s-1")
	must(t, "story", "release", id, "--session", "s-1")
	after := chainBytes(t, id)
	if !bytes.HasPrefix(after, before) {
		t.Fatal("the old chain is not a prefix of the new one")
	}
	if bytes.Contains(after, []byte(`"repo"`)) {
		t.Fatal("seating a legacy item wrote a repo")
	}
	_, evs := readChain(t, id)
	for _, e := range evs[1:] {
		if e.Kind != item.KindSeatBound && e.Kind != item.KindSeatReleased {
			t.Fatalf("unexpected %s on a legacy item", e.Kind)
		}
	}
}

func withoutLiteral(s, lit string) string { return strings.ReplaceAll(s, lit, "") }

func TestUnregisteredTargetNamesAreShellSafe(t *testing.T) {
	for _, name := range []string{"$(curl x|sh)", "${HOME}", "`id`", "it's", "-rf", "my repo", "web; curl a|sh", "x\x1b[31m"} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			must(t, "init")
			id := createID(t, "story", "create", "--title", "hostile")
			plantRepo(t, id, name)
			lit := reporeg.Literal(name)
			repos, _, _, _ := doctorRepos(t)
			var mine *doctorIssue
			for i := range repos {
				if strings.Contains(repos[i].Problem, id) {
					mine = &repos[i]
				}
			}
			if mine == nil {
				t.Fatalf("doctor names no repos issue for %s: %+v", id, repos)
			}
			for _, s := range []string{mine.Problem, mine.Fix} {
				if !strings.Contains(s, lit) || strings.Contains(withoutLiteral(s, lit), name) {
					t.Errorf("doctor line %q does not hold %s alone", s, lit)
				}
			}
			if strings.Contains(mine.Fix, "satelle repo add") {
				t.Errorf("doctor offers a command for an invalid name: %q", mine.Fix)
			}
			text, _ := run(t, "", "doctor")
			get := must(t, "story", "get", id)
			list := must(t, "story", "list")
			for label, s := range map[string]string{"doctor": text, "story get": get, "story list": list} {
				if !strings.Contains(s, lit) || strings.Contains(s, "\x1b") || strings.Contains(withoutLiteral(s, lit), name) {
					t.Errorf("%s does not print %s safely:\n%s", label, lit, s)
				}
			}
			out, err := run(t, "", "story", "seat", id, "--session", "s-1")
			if err == nil {
				t.Fatal("seat on an unregistered target succeeded")
			}
			msg := out + err.Error()
			if !strings.Contains(msg, lit) || strings.Contains(withoutLiteral(msg, lit), name) {
				t.Errorf("seat error does not print %s safely: %s", lit, msg)
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		isolate(t)
		must(t, "init")
		id := createID(t, "story", "create", "--title", "plain")
		plantRepo(t, id, "web")
		repos, _, _, _ := doctorRepos(t)
		if len(repos) != 1 || !strings.Contains(repos[0].Fix, "satelle repo add web --origin") {
			t.Fatalf("doctor %+v", repos)
		}
	})
}

func TestDoctorRegisteredNamesQuoted(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "ghost")
	plantRepo(t, id, "ghost")
	writeFile(t, filepath.Join(dir, ".satelle", reporeg.TrackedFile), "[repos.\"bad\u202ename\"]\norigin = \"https://github.com/o/w\"\n")
	if _, err := run(t, "", "validate"); err == nil {
		t.Fatal("validate accepted a name holding U+202E")
	}
	repos, _, _, _ := doctorRepos(t)
	var problem string
	for _, is := range repos {
		if strings.Contains(is.Problem, id) {
			problem = is.Problem
		}
	}
	if !strings.Contains(problem, reporeg.Literal("bad\u202ename")) || strings.Contains(problem, "\u202e") {
		t.Fatalf("doctor problem %q", problem)
	}
}

func TestWakeRefusesUnsettledFireOverUnregisteredMember(t *testing.T) {
	for _, ghost := range []bool{true, false} {
		t.Run(map[bool]string{true: "unregistered member", false: "legacy claim settles"}[ghost], func(t *testing.T) {
			dir := isolate(t)
			must(t, "init")
			wakeWorkflowFile(t, dir)
			task := createID(t, "task", "create", "--title", "release")
			body := "title = \"Release\"\n[filter]\nkind = \"story\"\nstatus = \"approved\"\n[trigger]\ntask = \"" + task + "\"\nbase = \"HEAD\"\nmin_size = 0\noverlap = \"allow\"\n"
			writeFile(t, filepath.Join(dir, ".satelle", "queues", "release.toml"), body)
			s1 := approvedStory(t, "one")
			runID := item.NewID("exe")
			if _, err := runtimeLedger(t).Append("que_release", item.KindQueueFired, "me", item.QueueFire{Run: runID, Task: task, Cause: "by hand"}); err != nil {
				t.Fatal(err)
			}
			if ghost {
				plantRepo(t, s1, "ghost")
			}
			must(t, "project", "add")
			enableWake(t, dir)
			out := must(t, "ledger", "autosync")
			if !ghost {
				if !strings.Contains(out, "wake release: held:") {
					t.Fatalf("autosync:\n%s", out)
				}
				var exe item.Item
				if err := json.Unmarshal([]byte(must(t, "execution", "get", runID, "--json")), &exe); err != nil || exe.Parent != task {
					t.Fatalf("settled run %+v %v", exe, err)
				}
				if rec := must(t, "ledger", "list", runID, "--kind", "task.run", "--json"); !strings.Contains(rec, s1) {
					t.Fatalf("task.run lacks %s:\n%s", s1, rec)
				}
				return
			}
			for _, want := range []string{"wake release: not fired:", "does not register", "ghost"} {
				if !strings.Contains(out, want) {
					t.Fatalf("autosync lacks %q:\n%s", want, out)
				}
			}
			if n := len(executions(t)); n != 0 {
				t.Fatalf("%d executions", n)
			}
			if _, err := run(t, "", "execution", "get", runID); err == nil {
				t.Fatalf("%s exists", runID)
			}
			if n := len(wakeFires(t)); n != 1 {
				t.Fatalf("%d queue.fired, want the planted claim only", n)
			}
		})
	}
}
