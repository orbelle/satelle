package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// waveRepo scaffolds a repo whose epic-parent route declares schedule, with an
// epic of three children: two free and one waiting on an unfinished dependency.
func waveRepo(t *testing.T, schedule string) (epic string, free []string, waiting, dep string) {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]

[epic-parent]
obligations = ["raised", "ready", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[ready]
status = "ready"
waits_on_children = true
schedule = "`+schedule+`"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
		t.Fatalf("doc sync: %v", err)
	}
	mk := func(category string, status string, tags ...string) string {
		it, err := db.Stories.Create(ctx, workitem.CreateInput{
			Kind: workitem.KindStory, Title: "x", Body: "b", AcceptanceCriteria: "1. ok",
			Status: status, Category: category, Tags: append([]string{"epic:w"}, tags...),
		}, time.Now().UTC())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return it.ID
	}
	epic = mk("epic-parent", "backlog")
	free = []string{mk("fix", "backlog"), mk("fix", "backlog")}
	dep = mk("fix", "backlog")
	waiting = mk("fix", "backlog", "depends-on:"+dep)
	return
}

func TestStoryWaveCommandPrintsRunnableIDs(t *testing.T) {
	epic, free, waiting, dep := waveRepo(t, "parallel")
	stdout, stderr, err := runRootSplit(t, "", "story", "wave", epic)
	if err != nil {
		t.Fatalf("story wave: %v\nstderr: %s", err, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	ids := map[string]bool{}
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") {
			ids[l] = true
		}
	}
	// the free children and the dependency itself (it has no dependency) are
	// runnable; the child waiting on the unfinished dependency is not.
	for _, id := range append(append([]string{}, free...), dep) {
		if !ids[id] {
			t.Errorf("%s must be printed as runnable:\n%s", id, stdout)
		}
	}
	if ids[waiting] {
		t.Errorf("%s waits on %s and must not be printed as runnable:\n%s", waiting, dep, stdout)
	}
	if !strings.Contains(stdout, "# omitted "+waiting) || !strings.Contains(stdout, dep) {
		t.Errorf("the omitted child must be listed naming its dependency:\n%s", stdout)
	}
}

// deliveryRepo scaffolds a repo whose epic readiness step records the delivery
// choice (sty_c931cd90): the working lane's coded step is allocated to a headless
// [coder] and honours a placement pin, and the docs lane's authored step is the
// in-loop [executor]'s. The epic has four open children: three on the working lane
// pinned in-loop, local and to a team remote, and one unpinned docs child.
func deliveryRepo(t *testing.T) (epic string, inLoop, local, team, docs string) {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]

[docs]
obligations = ["raised", "authored", "docs-verified"]

[epic-parent]
obligations = ["raised", "ready", "parent-closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
placement_tag = "placement:"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[authored]
status = "in_progress"
agent = "executor"
placement_tag = "placement:"
requires = ["raised"]

[docs-verified]
status = "done"
terminal = true
requires = ["authored"]

[ready]
status = "ready"
agent = "ready-reviewer"
skills = ["ready"]
waits_on_children = true
delivery_heading = "Delivery"
lane_tag = "lane:"
requires = ["raised"]

[parent-closed]
status = "done"
agent = "reviewer"
terminal = true
requires = ["ready"]
`)
	agents := "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[orchestrator]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[coder]\nrole = \"agent\"\ncommand = \"claude -p {system}\"\ntools = \"Read\"\n\n" +
		"[ready-reviewer]\nrole = \"agent\"\ncommand = \"claude -p {system}\"\ntools = \"Read\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo, ".satelle", config.AgentsConfigName))
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
		t.Fatalf("doc sync: %v", err)
	}
	mk := func(category string, tags ...string) string {
		it, err := db.Stories.Create(ctx, workitem.CreateInput{
			Kind: workitem.KindStory, Title: "x", Body: "b", AcceptanceCriteria: "1. ok",
			Status: "backlog", Category: category, Tags: append([]string{"epic:w"}, tags...),
		}, time.Now().UTC())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return it.ID
	}
	epic = mk("epic-parent")
	inLoop = mk("fix", "placement:in-loop")
	local = mk("fix", "placement:local", "lane:trunk")
	team = mk("fix", "placement:team:/teams/x")
	docs = mk("docs")
	return
}

// `story wave` prints where each open child's delivering step runs — the engine's
// own resolution, honouring the orchestrator's pins — with the reason the
// readiness round recorded, after the bare runnable ids.
func TestStoryWaveCommandPrintsDelivery(t *testing.T) {
	epic, inLoop, local, team, docs := deliveryRepo(t)
	if _, stderr, err := runRootSplit(t, "", "story", "attach", epic, "--name", "ready", "--type", "review", "--body",
		"parallel: all\n\n## Delivery\n"+inLoop+": needs the driving session's context\n"+team+": long-running\n"); err != nil {
		t.Fatalf("attach: %v\n%s", err, stderr)
	}

	stdout, stderr, err := runRootSplit(t, "", "story", "wave", epic)
	if err != nil {
		t.Fatalf("story wave: %v\nstderr: %s", err, stderr)
	}
	delivery := map[string]string{}
	runnable := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if rest, ok := strings.CutPrefix(l, "# delivery "); ok {
			id, line, _ := strings.Cut(rest, ": ")
			delivery[id] = line
		} else if !strings.HasPrefix(l, "#") {
			runnable[l] = true
		}
	}
	for _, id := range []string{inLoop, local, team, docs} {
		if !runnable[id] {
			t.Errorf("%s must still print as a bare runnable id:\n%s", id, stdout)
		}
	}
	for id, want := range map[string][]string{
		inLoop: {"parallel", "step=in_progress", "in-loop", "agent=coder", "— needs the driving session's context"},
		local:  {"parallel", "lane=trunk", "step=in_progress", "dispatched", "agent=coder"},
		team:   {"parallel", "step=in_progress", "team:/teams/x", "agent=coder", "— long-running"},
		docs:   {"parallel", "step=in_progress", "executor (in-loop)", "agent=executor"},
	} {
		got, ok := delivery[id]
		if !ok {
			t.Errorf("no '# delivery' line for %s:\n%s", id, stdout)
			continue
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("delivery line for %s = %q, want it to contain %q", id, got, w)
			}
		}
	}
	if strings.Contains(delivery[local], "—") || strings.Contains(delivery[docs], "—") {
		t.Errorf("a child with no recorded reason must print none:\n%s", stdout)
	}
}

func TestStoryWaveCommandSequentialRefusalPrintsNoRunnableID(t *testing.T) {
	epic, free, _, _ := waveRepo(t, "sequential")
	stdout, stderr, err := runRootSplit(t, "", "story", "wave", epic)
	if err == nil {
		t.Fatalf("sequential with several runnable children must exit non-zero; stdout: %s", stdout)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("a refusal must print no id on stdout, got %q", stdout)
	}
	msg := err.Error() + stderr
	for _, want := range append(append([]string{}, free...), "depends-on") {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must contain %q: %s", want, msg)
		}
	}
}

// The JSON and text forms render one response: the runnable ids agree, and the
// held child carries a machine-readable cause and its blocker.
func TestStoryWaveJSONMatchesText(t *testing.T) {
	epic, _, waiting, dep := waveRepo(t, "parallel")
	text, stderr, err := runRootSplit(t, "", "story", "wave", epic)
	if err != nil {
		t.Fatalf("story wave: %v\nstderr: %s", err, stderr)
	}
	textIDs := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			textIDs[l] = true
		}
	}
	out, stderr, err := runRootSplit(t, "", "story", "wave", epic, "--json")
	if err != nil {
		t.Fatalf("story wave --json: %v\nstderr: %s", err, stderr)
	}
	var res verb.WaveResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("--json must print one JSON object: %v\n%s", err, out)
	}
	jsonIDs := map[string]bool{}
	for _, id := range res.Runnable {
		jsonIDs[id] = true
	}
	if len(jsonIDs) != len(textIDs) {
		t.Errorf("runnable = %v, text printed %v", res.Runnable, textIDs)
	}
	for id := range textIDs {
		if !jsonIDs[id] {
			t.Errorf("%s printed as runnable by text but not in JSON runnable %v", id, res.Runnable)
		}
	}
	if len(res.Omitted) != 1 || res.Omitted[0].ID != waiting {
		t.Fatalf("omitted = %+v, want only %s", res.Omitted, waiting)
	}
	o := res.Omitted[0]
	if o.Cause != verb.WaveCauseDependency {
		t.Errorf("cause = %q, want %q", o.Cause, verb.WaveCauseDependency)
	}
	if len(o.WaitingOn) != 1 || o.WaitingOn[0].ID != dep || o.WaitingOn[0].Status != "backlog" || o.WaitingOn[0].Cancelled {
		t.Errorf("waiting_on = %+v, want %s in backlog, not cancelled", o.WaitingOn, dep)
	}
}

func TestStoryWaveJSONSequentialRefusal(t *testing.T) {
	epic, _, _, _ := waveRepo(t, "sequential")
	stdout, _, err := runRootSplit(t, "", "story", "wave", epic, "--json")
	if err == nil {
		t.Fatalf("sequential with several runnable children must exit non-zero with --json; stdout: %s", stdout)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("a refusal must print nothing on stdout with --json, got %q", stdout)
	}
}

func TestStoryWaveIsDocumented(t *testing.T) {
	out, err := runHelp(t, "help", "epic-wave")
	if err != nil || !strings.Contains(out, "story wave") || !strings.Contains(out, "depends-on") {
		t.Errorf("help epic-wave must document the command: err=%v\n%s", err, out)
	}
	for _, field := range []string{"runnable", "omitted", "cause", "waiting_on"} {
		if !strings.Contains(out, field) {
			t.Errorf("help epic-wave must name the JSON field %q:\n%s", field, out)
		}
	}
	root := NewRootCmd()
	cmd, _, ferr := root.Find([]string{"story", "wave"})
	if ferr != nil || cmd == nil || cmd.Name() != "wave" || !strings.Contains(cmd.Long, "satelle help epic-wave") {
		t.Errorf("story wave must be a registered command pointing at its help topic: %v", ferr)
		return
	}
	for _, field := range []string{"runnable", "omitted", "cause", "waiting_on"} {
		if !strings.Contains(cmd.Long, field) {
			t.Errorf("story wave help must name the JSON field %q:\n%s", field, cmd.Long)
		}
	}
}
