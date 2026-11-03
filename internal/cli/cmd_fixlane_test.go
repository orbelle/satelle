package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/fixlane"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// laneRepo is an engaged story at "plan" — a state the route allocates to the
// planner, so the ORDINARY gate refuses the driving session an edit — in a repo
// whose [fix_lane] declares internal/** and tests/** as product surface.
func laneRepo(t *testing.T) string {
	t.Helper()
	repo := editStateRepo(t, "plan", "plan", false)
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw) + "\n[fix_lane]\nproduct_surface = [\"internal/**\", \"tests/**\"]\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	origTree := sessionWorktree
	t.Cleanup(func() { sessionWorktree = origTree })
	sessionWorktree = func() string { return repo }

	// Stamp the seat to THIS session so the hook and the claim verb both resolve
	// it as the driver's (same shape as TestHookGatePrefersEnvStampOverPayload).
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	leases, err := db.Leases.List(ctx)
	if err != nil || len(leases) != 1 {
		t.Fatalf("leases: %v n=%d", err, len(leases))
	}
	storyID := leases[0].ItemID
	_ = db.Leases.ForceRelease(ctx, storyID)
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: storyID, Kind: "story", Owner: lease.ResolveOwner(),
		State: "plan", StorySeat: true, Worktree: repo, SessionID: "sess-lane",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Leases.Confirm(ctx, storyID, "plan"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.SessionEnv, "sess-lane")
	return repo
}

func laneEditEvent(path, oldS, newS string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "s1",
		"tool_name":  "Edit",
		"tool_input": map[string]any{"file_path": path, "old_string": oldS, "new_string": newS},
	})
	return string(b)
}

func ledgerRows(t *testing.T, kind string) []ledger.Entry {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Ledger.List(context.Background(), ledger.ListFilter{Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func claimArgs(path string, lines string) []string {
	return []string{"fix", "claim", path, "--reason", "stale sentence in the readme", "--lines", lines, "--test", "TestReadmeMentionsLane"}
}

// AC1 + AC5 + the expiry rules: claim, then the one edit is allowed; the same
// edit again is denied; the row precedes the change and survives the process.
func TestFixLaneClaimThenEditThenNoSecondEdit(t *testing.T) {
	laneRepo(t)
	edit := laneEditEvent("docs/notes.md", "old\n", "new\nline\n")

	if out, err := runRootIn(t, edit, "hook", "gate"); err == nil {
		t.Fatalf("with no claim the ordinary gate must refuse a plan-state edit:\n%s", out)
	}
	if out, err := runRoot(t, claimArgs("docs/notes.md", "3")...); err != nil {
		t.Fatalf("claim on a non-product path within the bound must be granted: %v\n%s", err, out)
	}
	if out, err := runRootIn(t, edit, "hook", "gate"); err != nil {
		t.Fatalf("the recorded claim must license the edit: %v\n%s", err, out)
	}
	// (a) a SECOND edit on the same path is denied after one allow.
	if out, err := runRootIn(t, edit, "hook", "gate"); err == nil {
		t.Fatalf("a second edit must be denied — the claim was consumed by the first:\n%s", out)
	}

	claims, uses := ledgerRows(t, ledger.KindFixClaim), ledgerRows(t, ledger.KindFixClaimUse)
	if len(claims) != 1 || len(uses) != 1 {
		t.Fatalf("want one claim row and one use row, got %d / %d", len(claims), len(uses))
	}
	if !claims[0].CreatedAt.Before(uses[0].CreatedAt) {
		t.Errorf("claim row (%v) must precede the use row (%v) that precedes the change", claims[0].CreatedAt, uses[0].CreatedAt)
	}
	var use fixlane.UsePayload
	if err := json.Unmarshal(uses[0].Payload, &use); err != nil || use.Path != "docs/notes.md" || use.Lines != 2 {
		t.Errorf("use row = %+v (%v), want docs/notes.md at 2 lines", use, err)
	}
	var refs struct{ Claim string }
	if err := json.Unmarshal(uses[0].Refs, &refs); err != nil || refs.Claim != claims[0].ID {
		t.Errorf("use row refs = %s, want it to point at claim %s", uses[0].Refs, claims[0].ID)
	}
}

// AC5, "survives the process" taken literally: the claim and use rows written in
// this process are read back by a SEPARATE process — this test binary re-executed
// as the CLI — through `satelle fix report`, not by reopening the file here.
func TestFixLaneRowsSurviveIntoAnotherProcess(t *testing.T) {
	laneRepo(t)
	if out, err := runRoot(t, claimArgs("docs/notes.md", "3")...); err != nil {
		t.Fatalf("claim: %v\n%s", err, out)
	}
	if out, err := runRootIn(t, laneEditEvent("docs/notes.md", "old\n", "new\n"), "hook", "gate"); err != nil {
		t.Fatalf("the recorded claim must license the edit: %v\n%s", err, out)
	}

	cmd := exec.Command(os.Args[0], "fix", "report", "--json")
	cmd.Env = append(os.Environ(), "SATELLE_CLI_REEXEC=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("separate-process fix report: %v\n%s", err, out)
	}
	var f fixlane.Figures
	if err := json.Unmarshal(out, &f); err != nil {
		t.Fatalf("separate-process report is not JSON: %v\n%s", err, out)
	}
	if f.Granted != 1 || f.Consumed != 1 || f.Bound.Total != 3 || f.Used.Total != 1 {
		t.Errorf("a fresh process read %+v, want the 1 granted claim (bound 3) and its 1 use (1 line)", f)
	}
}

// (b) the claim no longer licenses an edit after a transition.
func TestFixLaneClaimDiesAtNextTransition(t *testing.T) {
	laneRepo(t)
	if out, err := runRoot(t, claimArgs("docs/notes.md", "3")...); err != nil {
		t.Fatalf("claim: %v\n%s", err, out)
	}
	claims := ledgerRows(t, ledger.KindFixClaim)
	if len(claims) != 1 {
		t.Fatalf("want one claim, got %d", len(claims))
	}
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: claims[0].StoryID, Kind: ledger.KindStatusTransition, Body: "plan→in_progress",
	}, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out, err := runRootIn(t, laneEditEvent("docs/notes.md", "a\n", "b\n"), "hook", "gate")
	if err == nil {
		t.Fatalf("a claim recorded before a transition must not license an edit after it:\n%s", out)
	}
}

// The bound is enforced at the edit, not only at the claim: an edit larger than
// the claim declared is refused, and the refusal does not burn the claim.
func TestFixLaneEditOverTheBoundIsRefusedAndKeepsTheClaim(t *testing.T) {
	laneRepo(t)
	if out, err := runRoot(t, claimArgs("docs/notes.md", "2")...); err != nil {
		t.Fatalf("claim: %v\n%s", err, out)
	}
	big := laneEditEvent("docs/notes.md", "a\n", "1\n2\n3\n4\n5\n")
	out, err := runRootIn(t, big, "hook", "gate")
	if err == nil || !strings.Contains(out+err.Error(), "over claim") {
		t.Fatalf("a 5-line edit against a 2-line claim must be refused naming the bound: %v\n%s", err, out)
	}
	if out, err := runRootIn(t, laneEditEvent("docs/notes.md", "a\n", "b\n"), "hook", "gate"); err != nil {
		t.Fatalf("the claim must survive a refused oversize edit: %v\n%s", err, out)
	}
}

// The lane is for the driving session. A dispatched performer/reviewer never
// gets it, claim or no claim.
func TestFixLaneNeverAppliesToADispatchedPerformer(t *testing.T) {
	laneRepo(t)
	if out, err := runRoot(t, claimArgs("docs/notes.md", "3")...); err != nil {
		t.Fatalf("claim: %v\n%s", err, out)
	}
	t.Setenv(config.DispatchAgentEnv, "reviewer")
	t.Setenv(config.DispatchStepEnv, "plan")
	t.Setenv(config.DispatchItemEnv, "sty_other")
	if out, err := runRootIn(t, laneEditEvent("docs/notes.md", "a\n", "b\n"), "hook", "gate"); err == nil {
		t.Fatalf("a dispatched performer must not ride the driver's claim:\n%s", out)
	}
	if out, err := runRoot(t, claimArgs("docs/other.md", "3")...); err == nil {
		t.Fatalf("a dispatched performer must not record a claim:\n%s", out)
	}
	if n := len(ledgerRows(t, ledger.KindFixClaimUse)); n != 0 {
		t.Errorf("no use row may be written for a refused performer edit, got %d", n)
	}
}

// A relative claim path means relative to the driver's working directory. Typed
// from inside internal/, "foo.go" is internal/foo.go — product surface — and
// must not be judged as a root-level file.
func TestFixLaneResolvesRelativeClaimPathsAgainstTheWorkingDirectory(t *testing.T) {
	repo := laneRepo(t)
	for _, d := range []string{"internal", "docs"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(filepath.Join(repo, "internal"))
	out, err := runRoot(t, claimArgs("foo.go", "3")...)
	if err == nil || !strings.Contains(out+err.Error(), "class "+fixlane.ClassProductSurface) {
		t.Fatalf("foo.go from inside internal/ is internal/foo.go, product surface: got %v / %q", err, out)
	}
	out, err = runRoot(t, claimArgs("../docs/notes.md", "3")...)
	if err != nil {
		t.Fatalf("../docs/notes.md from internal/ is docs/notes.md and must be granted: %v\n%s", err, out)
	}
	t.Chdir(filepath.Join(repo, "docs"))
	out, err = runRoot(t, claimArgs("../internal/foo.go", "3")...)
	if err == nil || !strings.Contains(out+err.Error(), "class "+fixlane.ClassProductSurface) {
		t.Fatalf("../internal/foo.go from docs/ is product surface: got %v / %q", err, out)
	}
	if _, err := runRoot(t, claimArgs("../../elsewhere.md", "3")...); err == nil {
		t.Fatal("a relative path that climbs out of the repo must be refused")
	}
	var paths []string
	for _, r := range ledgerRows(t, ledger.KindFixClaim) {
		var p fixlane.ClaimPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p.Path+":"+p.Decision)
	}
	want := "internal/foo.go:refused docs/notes.md:recorded internal/foo.go:refused"
	if got := strings.Join(paths[:3], " "); got != want {
		t.Errorf("recorded claim paths = %q, want %q (repo-relative, resolved from the working directory)", got, want)
	}
	// The claim on docs/notes.md then licenses the edit the gate sees for it.
	if out, err := runRootIn(t, laneEditEvent(filepath.Join(repo, "docs", "notes.md"), "a\n", "b\n"), "hook", "gate"); err != nil {
		t.Fatalf("the claim recorded from a subdirectory must license that file: %v\n%s", err, out)
	}
}

// A claim needs an engaged story: with none, the claim is refused (and logged)
// and no edit is licensed.
func TestFixLaneNeedsAnEngagedStory(t *testing.T) {
	repo := tempRepo(t)
	t.Chdir(repo)
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfgPath, []byte("[review]\ngate_create = false\n[fix_lane]\nproduct_surface = [\"internal/**\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runRoot(t, claimArgs("docs/notes.md", "3")...)
	if err == nil || !strings.Contains(out+err.Error(), "class "+fixlane.ClassNoStory) {
		t.Fatalf("no engaged story: got %v / %q, want class %s", err, out, fixlane.ClassNoStory)
	}
	if out, err := runRootIn(t, laneEditEvent("docs/notes.md", "a\n", "b\n"), "hook", "gate"); err == nil {
		t.Fatalf("no story and no claim: the edit must be denied:\n%s", out)
	}
}

// judgedSnapshot is everything a gate judges by: the body of every gate skill
// (embedded and authored) and, for every state of the governing route, the
// reviewer set gating entry into it. Byte-compared across a lane run.
func judgedSnapshot(t *testing.T) string {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	snap := map[string]any{}
	skills := map[string]string{}
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "skills" {
			skills["embedded/"+d.Name] = d.Body
		}
	}
	authored, err := db.DocIndex.List(ctx, "skills")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range authored {
		skills["authored/"+d.Name] = d.Body
	}
	snap["skills"] = skills
	wfs, err := db.DocIndex.List(ctx, "workflows")
	if err != nil {
		t.Fatal(err)
	}
	items, err := db.Stories.List(ctx, workitem.ListFilter{})
	if err != nil || len(items) == 0 {
		t.Fatalf("stories: %v n=%d", err, len(items))
	}
	spec, _, _, err := wfgovern.SpecFor(wfs, items[0])
	if err != nil {
		t.Fatal(err)
	}
	reviewers := map[string][]string{}
	for _, status := range []string{"backlog", "plan", "in_progress", "integration", "release", "done"} {
		var names []string
		for _, r := range spec.ScopedReviewers(status, items[0].Tags) {
			names = append(names, r.Skill)
		}
		reviewers[status] = names
	}
	reviewers["states"] = nil
	for _, st := range spec.States {
		reviewers["states"] = append(reviewers["states"], st.Name+":"+st.Agent+":"+st.Skill)
	}
	snap["reviewers"] = reviewers

	// The shipped default route carries real reviewer gates; pin its sets too, so
	// the byte comparison is not vacuous on a fixture route with none.
	var shipped []docindex.Doc
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "workflows" {
			shipped = append(shipped, docindex.Doc{Kind: d.Kind, Name: d.Name, Ext: d.Ext, Body: d.Body, Embedded: true,
				Path: "embedded:workflows/" + d.Name + d.Ext})
		}
	}
	shippedSets := map[string][]string{}
	if sspec, _, _, err := wfgovern.SpecFor(shipped, items[0]); err == nil {
		for _, status := range []string{"backlog", "plan", "in_progress", "integration", "release", "done"} {
			for _, r := range sspec.ScopedReviewers(status, items[0].Tags) {
				shippedSets[status] = append(shippedSets[status], r.Skill)
			}
		}
	}
	snap["shipped_reviewers"] = shippedSets
	snap["workflows"] = len(wfs)
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// AC4: the lane changes WHO MAY EDIT, never WHAT IS JUDGED. The gate skill
// bodies and an edge's reviewer set are byte-identical with the lane
// unconfigured, configured, and after a claim has been recorded and consumed.
func TestFixLaneNeverChangesWhatAGateJudges(t *testing.T) {
	repo := laneRepo(t)
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	withLane, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(strings.SplitN(string(withLane), "\n[fix_lane]", 2)[0]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	without := judgedSnapshot(t)
	if !strings.Contains(without, `"reviewers"`) || !strings.Contains(without, `"skills"`) || strings.Contains(without, `"shipped_reviewers":{}`) {
		t.Fatalf("snapshot is empty of what it should pin: %.400s", without)
	}

	if err := os.WriteFile(cfgPath, withLane, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := judgedSnapshot(t); got != without {
		t.Fatal("configuring the lane changed a gate skill body or a reviewer set")
	}
	if out, err := runRoot(t, claimArgs("docs/notes.md", "3")...); err != nil {
		t.Fatalf("claim: %v\n%s", err, out)
	}
	if out, err := runRootIn(t, laneEditEvent("docs/notes.md", "a\n", "b\n"), "hook", "gate"); err != nil {
		t.Fatalf("edit under claim: %v\n%s", err, out)
	}
	if got := judgedSnapshot(t); got != without {
		t.Fatal("a claim and its edit changed a gate skill body or a reviewer set")
	}
}

func TestEditLinesMeasuresEveryHarnessShape(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
		ok   bool
	}{
		{"claude edit uses the larger side", `{"tool_input":{"file_path":"a","old_string":"1\n2\n3","new_string":"x"}}`, 3, true},
		{"claude write is the whole content", `{"tool_input":{"file_path":"a","content":"1\n2\n3\n4\n"}}`, 4, true},
		{"multi-edit sums its edits", `{"tool_input":{"file_path":"a","edits":[{"old_string":"1","new_string":"a\nb"},{"old_string":"2","new_string":"c"}]}}`, 3, true},
		{"pi camelCase edits", `{"toolInput":{"path":"a","edits":[{"oldText":"1","newText":"a\nb\nc"}]}}`, 3, true},
		{"grok search_replace", `{"tool_input":{"file_path":"a","old_str":"x","new_str":"y"}}`, 1, true},
		{"no text is unmeasurable", `{"tool_input":{"file_path":"a"}}`, 0, false},
		{"not json", `nope`, 0, false},
	}
	for _, tc := range cases {
		got, ok := editLines([]byte(tc.raw), "")
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: editLines = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// A replace-all edit rewrites every occurrence in the file, so it is measured by
// its REACH, not by its own text: a one-line pattern that matches a whole file
// must not fit inside a small bound.
func TestEditLinesMeasuresReplaceAllByReach(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(file, []byte("foo\nbar\nfoo\nbaz\nfoo\nfoo\nfoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := func(inner string) []byte { return []byte(`{"tool_input":{"file_path":"x",` + inner + `}}`) }
	cases := []struct {
		name string
		raw  []byte
		file string
		want int
		ok   bool
	}{
		{"replace_all counts every occurrence", ev(`"old_string":"foo","new_string":"qux","replace_all":true`), file, 5, true},
		{"camelCase flag", ev(`"oldString":"foo","newString":"qux","replaceAll":true`), file, 5, true},
		{"string-typed flag", ev(`"old_string":"foo","new_string":"qux","replace_all":"true"`), file, 5, true},
		{"recognised replace_all with old text under an unrecognised key fails closed",
			ev(`"pattern":"foo","new_string":"qux","replace_all":true`), file, 0, false},
		{"recognised replaceAll with no old text at all fails closed",
			ev(`"new_string":"qux","replaceAll":true`), file, 0, false},
		{"unrecognised old-text key inside a multi-edit fails closed",
			[]byte(`{"tool_input":{"file_path":"x","edits":[{"find":"foo","new_string":"a","replace_all":true}]}}`), file, 0, false},
		{"unrecognised hyphenated replace-all flag fails closed", ev(`"old_string":"foo","new_string":"qux","replace-all":true`), file, 0, false},
		{"unrecognised global flag fails closed", ev(`"old_string":"foo","new_string":"qux","global":true`), file, 0, false},
		{"unrecognised bare all flag fails closed", ev(`"old_string":"foo","new_string":"qux","all":"true"`), file, 0, false},
		{"unrecognised flag inside a multi-edit fails closed",
			[]byte(`{"tool_input":{"file_path":"x","edits":[{"old_string":"foo","new_string":"a","replaceEvery":true,"all_matches":true}]}}`), file, 0, false},
		{"an unrecognised flag that is false is just an edit", ev(`"old_string":"foo","new_string":"qux","global":false`), file, 1, true},
		{"an unrelated true flag is just an edit", ev(`"old_string":"foo","new_string":"qux","dry_run":true`), file, 1, true},
		{"flag false is one edit", ev(`"old_string":"foo","new_string":"qux","replace_all":false`), file, 1, true},
		{"multi-line pattern scales by its size", ev(`"old_string":"foo\nbar","new_string":"x","replace_all":true`), file, 2, true},
		{"unreadable file: reach unknowable", ev(`"old_string":"foo","new_string":"qux","replace_all":true`), filepath.Join(t.TempDir(), "gone"), 0, false},
		{"empty pattern: reach unknowable", ev(`"old_string":"","new_string":"qux","replace_all":true`), file, 0, false},
		{"multi-edit: only the flagged edit scales",
			[]byte(`{"tool_input":{"file_path":"x","edits":[{"old_string":"foo","new_string":"a","replace_all":true},{"old_string":"baz","new_string":"b"}]}}`), file, 6, true},
		{"matches manufactured by an earlier edit count",
			[]byte(`{"tool_input":{"file_path":"x","edits":[{"old_string":"baz","new_string":"foo\nfoo\nfoo"},{"old_string":"foo","new_string":"z","replace_all":true}]}}`), file, 3 + 8, true},
	}
	for _, tc := range cases {
		got, ok := editLines(tc.raw, tc.file)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: editLines = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// The bypass at the gate: a 3-line claim must not license a one-line
// replace_all that rewrites 5 lines; a replace_all whose reach is within the
// bound is fine, and a refused oversize edit does not burn the claim.
func TestFixLaneReplaceAllCannotSlipUnderTheBound(t *testing.T) {
	repo := laneRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(repo, "docs", "notes.md")
	if err := os.WriteFile(notes, []byte("foo\nbar\nfoo\nfoo\nfoo\nfoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runRoot(t, claimArgs("docs/notes.md", "3")...); err != nil {
		t.Fatalf("claim: %v\n%s", err, out)
	}
	ev := func(all bool) string {
		b, _ := json.Marshal(map[string]any{"session_id": "s1", "tool_name": "Edit", "tool_input": map[string]any{
			"file_path": "docs/notes.md", "old_string": "foo", "new_string": "qux", "replace_all": all}})
		return string(b)
	}
	out, err := runRootIn(t, ev(true), "hook", "gate")
	if err == nil || !strings.Contains(out+err.Error(), "5 lines, over claim") {
		t.Fatalf("a 5-occurrence replace_all must be measured at 5 lines and refused: %v\n%s", err, out)
	}
	if n := len(ledgerRows(t, ledger.KindFixClaimUse)); n != 0 {
		t.Fatalf("a refused edit must not consume the claim, got %d use rows", n)
	}
	if out, err := runRootIn(t, ev(false), "hook", "gate"); err != nil {
		t.Fatalf("the same edit without replace_all is one line and within the bound: %v\n%s", err, out)
	}
}

// Init seeds the section; heal appends it ONLY when absent; an authored section
// is left byte-untouched. Fail-closed is only real if the declaration is easy to
// make and never silently overwritten.
func TestFixLaneSeedAndHeal(t *testing.T) {
	// A fresh scaffold carries the section, parseable, with the lane closed.
	if !config.HasSection(scaffoldToml, "fix_lane") {
		t.Fatal("a fresh init must write the [fix_lane] section")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, config.ConfigName)
	if err := os.WriteFile(path, []byte(scaffoldToml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("the seeded scaffold must load: %v", err)
	}
	if got := cfg.ResolveFixLane(dir); len(got.ProductSurface) != 0 || got.MaxLines <= 0 {
		t.Errorf("seeded lane = %+v, want closed (no product surface) with the default ceiling", got)
	}

	// The ceiling shown in the seeded comment is rendered from the embedded
	// default — one source, no bound spelled in Go.
	want := fmt.Sprintf("# max_lines = %d\n", config.EmbeddedFixLane().MaxLines)
	if !strings.Contains(scaffoldToml, want) || !strings.Contains(fixLaneScaffoldBlock(), want) {
		t.Errorf("seeded block must carry %q rendered from EmbeddedFixLane", want)
	}

	// Heal appends when absent, once.
	old := "[review]\ngate_create = true\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := healFixLane(dir); err != nil || !changed {
		t.Fatalf("heal on a repo lacking [fix_lane] = (%v, %v), want it to append", changed, err)
	}
	healed, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(healed), old) || !config.HasSection(string(healed), "fix_lane") {
		t.Fatalf("heal must append after the existing content:\n%s", healed)
	}
	if changed, err := healFixLane(dir); err != nil || changed {
		t.Fatalf("a second heal = (%v, %v), want a no-op", changed, err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(healed) {
		t.Error("a second heal must leave the file byte-identical")
	}

	// An AUTHORED section — even one an operator emptied — is never touched.
	authored := "[review]\ngate_create = true\n\n[fix_lane]\nmax_lines = 5\nproduct_surface = [\"src/**\"]\n# hand-written note\n"
	if err := os.WriteFile(path, []byte(authored), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := healFixLane(dir); err != nil || changed {
		t.Fatalf("heal on an authored section = (%v, %v), want untouched", changed, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != authored {
		t.Errorf("an authored [fix_lane] must stay byte-identical:\n%s", after)
	}
}
