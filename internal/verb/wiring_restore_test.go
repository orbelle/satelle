package verb

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/oplog"
	"github.com/bobmcallan/satelle/internal/retrieve"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// withWiring restores verb's package-level wiring to what it was when the test
// started. Every verb test that calls a Set*/Clear*/Add* function, or assigns a
// wiring variable directly, calls it first — never a hand-written reset to nil
// or zero, which would clobber a production default.
func withWiring(t *testing.T) {
	t.Helper()
	restore, _ := SnapshotWiring()
	t.Cleanup(restore)
}

type wiringStubGater struct {
	TransitionGater
	n int
}
type wiringStubCreateReviewer struct {
	CreateReviewer
	n int
}
type wiringStubAmendReviewer struct {
	AmendReviewer
	n int
}
type stubWorkflowResolver struct {
	WorkflowResolver
	n int
}
type stubExecutorDispatcher struct {
	ExecutorDispatcher
	n int
}
type stubRetrospector struct {
	Retrospector
	n int
}
type stubStepSummariser struct {
	StepSummariser
	n int
}

// wiringCase drives one exported Set*/Clear*/Add* function. A Set* or Add* case
// calls the function from a clean slate and expects the named variables to
// differ afterwards. A Clear* case names its pair: pre installs a non-default
// value through that Set* first, so the snapshot taken next holds it and Clear*
// has something to undo.
type wiringCase struct {
	fn   string
	pair string // for Clear*: the Set* whose value pre installs
	pre  func()
	call func()
	want []string
}

func wiringCases() []wiringCase {
	f := func() string { return "sentinel" }
	guard := func(context.Context) error { return nil }
	probe := func(context.Context, string) ([]string, error) { return nil, nil }
	loc := func() (string, string) { return "loc_sentinel", "" }
	note := func(string) {}
	vocab := config.Config{Tags: config.TagsConfig{Vocabulary: map[string][]string{"wiring": {"sentinel"}}}}
	epic := config.Config{Engagement: config.EngagementConfig{Parallel: config.ParallelEpic}}
	wt := config.Config{Worktree: config.WorktreeConfig{Branch: "wiring-sentinel"}}
	agents := config.AgentsConfig{Agents: map[string]config.AgentBinding{"wiring-sentinel": {}}}
	vars := map[string]string{"wiring": "sentinel"}
	clear0 := func() {
		SetChangeNotifier(nil)
		ClearAgentsConfig()
		ClearTagVocabulary()
		ClearWorktreeConfig()
		ClearEngagementMode()
	}
	return []wiringCase{
		{fn: "SetAssigneeResolver", pre: ClearAssigneeResolver, call: func() { SetAssigneeResolver(f) }, want: []string{"assigneeResolver"}},
		{fn: "ClearAssigneeResolver", pair: "SetAssigneeResolver", pre: func() { SetAssigneeResolver(f) }, call: ClearAssigneeResolver, want: []string{"assigneeResolver"}},
		{fn: "SetEngageGuard", call: func() { SetEngageGuard(guard) }, want: []string{"engageGuard"}},
		{fn: "SetDirtyPathsProbe", call: func() { SetDirtyPathsProbe(probe) }, want: []string{"dirtyPathsProbe"}},
		{fn: "SetActorResolver", call: func() { SetActorResolver(f) }, want: []string{"actorResolver"}},
		{fn: "SetAuthoredDirs", call: func() { SetAuthoredDirs(map[string]string{"wiring": "sentinel"}) }, want: []string{"authoredDirs"}},
		{fn: "SetSubstrateConfigDir", call: func() { SetSubstrateConfigDir("wiring-sentinel") }, want: []string{"substrateConfigDir"}},
		{fn: "SetEngagementMode", pre: ClearEngagementMode, call: func() { SetEngagementMode(epic) }, want: []string{"engagementParallel"}},
		{fn: "ClearEngagementMode", pair: "SetEngagementMode", pre: func() { SetEngagementMode(epic) }, call: ClearEngagementMode, want: []string{"engagementParallel"}},
		{fn: "SetAgentsConfig", pre: ClearAgentsConfig, call: func() { SetAgentsConfig(agents, vars) }, want: []string{"agentsLayer", "agentsVars", "agentsWired"}},
		{fn: "ClearAgentsConfig", pair: "SetAgentsConfig", pre: func() { SetAgentsConfig(agents, vars) }, call: ClearAgentsConfig, want: []string{"agentsLayer", "agentsVars", "agentsWired"}},
		{fn: "SetLocationResolver", call: func() { SetLocationResolver(loc) }, want: []string{"locationResolver"}},
		{fn: "ClearLocationResolver", pair: "SetLocationResolver", pre: func() { SetLocationResolver(loc) }, call: ClearLocationResolver, want: []string{"locationResolver"}},
		{fn: "SetProcessProbe", call: func() { SetProcessProbe(&ProcessProbe{InvokingRoot: "wiring-sentinel"}) }, want: []string{"processProbe"}},
		{fn: "SetRetrieveRetention", call: func() { SetRetrieveRetention(7311) }, want: []string{"retrieveKeepDays"}},
		{fn: "SetVerdictRecorder", call: func() { SetVerdictRecorder(note) }, want: []string{"verdictRecord"}},
		{fn: "SetTransitionGater", call: func() { SetTransitionGater(&wiringStubGater{n: 1}) }, want: []string{"transitionGater"}},
		{fn: "SetCreateReviewer", call: func() { SetCreateReviewer(&wiringStubCreateReviewer{n: 1}) }, want: []string{"createReviewer"}},
		{fn: "SetAmendReviewer", call: func() { SetAmendReviewer(&wiringStubAmendReviewer{n: 1}) }, want: []string{"amendReviewer"}},
		{fn: "SetWorkflowResolver", call: func() { SetWorkflowResolver(&stubWorkflowResolver{n: 1}) }, want: []string{"workflowResolver"}},
		{fn: "SetExecutorDispatcher", call: func() { SetExecutorDispatcher(&stubExecutorDispatcher{n: 1}) }, want: []string{"executorDispatcher"}},
		{fn: "SetRetrospector", call: func() { SetRetrospector(&stubRetrospector{n: 1}) }, want: []string{"retrospector"}},
		{fn: "SetStepSummariser", call: func() { SetStepSummariser(&stubStepSummariser{n: 1}) }, want: []string{"stepSummariser"}},
		{fn: "SetAttachmentPolicy", call: func() { SetAttachmentPolicy(12345, []string{"wiring/sentinel"}) }, want: []string{"attachMaxBytes", "attachAllowTypes"}},
		{fn: "SetStoryDir", call: func() { SetStoryDir("wiring-sentinel") }, want: []string{"storyDir"}},
		{fn: "SetDataDir", call: func() { SetDataDir("wiring-sentinel") }, want: []string{"dataDir"}},
		{fn: "SetBackupsDir", call: func() { SetBackupsDir("wiring-sentinel") }, want: []string{"backupsDir"}},
		{fn: "SetTaskDir", call: func() { SetTaskDir("wiring-sentinel") }, want: []string{"taskDir"}},
		{fn: "SetStoryRetention", call: func() { SetStoryRetention(7311, 7312) }, want: []string{"storyKeepClosed", "storyKeepDays"}},
		{fn: "SetTagVocabulary", pre: ClearTagVocabulary, call: func() { SetTagVocabulary(vocab) }, want: []string{"tagVocabCfg", "tagVocabWired"}},
		{fn: "ClearTagVocabulary", pair: "SetTagVocabulary", pre: func() { SetTagVocabulary(vocab) }, call: ClearTagVocabulary, want: []string{"tagVocabCfg", "tagVocabWired"}},
		{fn: "SetOpLog", call: func() { SetOpLog(new(oplog.Logger)) }, want: []string{"opLog"}},
		{fn: "SetWorkItemStore", call: func() { SetWorkItemStore(new(workitem.Store)) }, want: []string{"workItemStore"}},
		{fn: "SetLedgerStore", call: func() { SetLedgerStore(new(ledger.Store)) }, want: []string{"ledgerStore"}},
		{fn: "SetTxRunner", call: func() { SetTxRunner(func(context.Context, func(*sql.Tx) error) error { return nil }) }, want: []string{"txRunner"}},
		{fn: "SetDocIndexStore", call: func() { SetDocIndexStore(new(docindex.Store)) }, want: []string{"docIndexStore"}},
		{fn: "SetLeaseStore", call: func() { SetLeaseStore(new(lease.Store)) }, want: []string{"leaseStore"}},
		{fn: "SetRetrieveStore", call: func() { SetRetrieveStore(new(retrieve.Store)) }, want: []string{"retrieveStore"}},
		{fn: "SetChangeNotifier", pre: clear0, call: func() { SetChangeNotifier(note) }, want: []string{"changeNotifiers"}},
		{fn: "AddChangeNotifier", pre: clear0, call: func() { AddChangeNotifier(note) }, want: []string{"changeNotifiers"}},
		{fn: "SetAfterTagCASGetHook", call: func() { SetAfterTagCASGetHook(func(context.Context, string, string) {}) }, want: []string{"afterTagCASGetHook"}},
		{fn: "SetWorktreeConfig", pre: ClearWorktreeConfig, call: func() { SetWorktreeConfig(wt, "wiring-sentinel") }, want: []string{"worktreeCfg", "worktreeRoot", "worktreeWired"}},
		{fn: "ClearWorktreeConfig", pair: "SetWorktreeConfig", pre: func() { SetWorktreeConfig(wt, "wiring-sentinel") }, call: ClearWorktreeConfig, want: []string{"worktreeCfg", "worktreeRoot", "worktreeWired"}},
		{fn: "SetTrunkConfig", pre: ClearTrunkConfig, call: func() { SetTrunkConfig(config.TrunkConfig{Check: new(bool)}, "wiring-sentinel") }, want: []string{"trunkCfg", "trunkRepo"}},
		{fn: "ClearTrunkConfig", pair: "SetTrunkConfig", pre: func() { SetTrunkConfig(config.TrunkConfig{Check: new(bool)}, "wiring-sentinel") }, call: ClearTrunkConfig, want: []string{"trunkCfg", "trunkRepo"}},
		{fn: "SetTrunkOutput", call: func() { SetTrunkOutput(new(strings.Builder)) }, want: []string{"trunkOut"}},
		{fn: "SetApplyOutput", call: func() { SetApplyOutput(new(strings.Builder)) }, want: []string{"applyOut"}},
		{fn: "SetReleaseFields", call: func() { SetReleaseFields([]string{"wiring-sentinel"}) }, want: []string{"releaseFields"}},
	}
}

func sorted(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

// TestSnapshotWiringReportsAndRestores drives every exported Set*, Clear* and
// Add* function and asserts the snapshot names exactly the variables it
// touched, and that restore puts all of them back.
func TestSnapshotWiringReportsAndRestores(t *testing.T) {
	for _, c := range wiringCases() {
		t.Run(c.fn, func(t *testing.T) {
			withWiring(t) // outer: undoes pre and call
			if c.pre != nil {
				c.pre()
			}
			restore, changed := SnapshotWiring()
			if got := changed(); len(got) != 0 {
				t.Fatalf("changed() right after the snapshot = %v, want none", got)
			}
			c.call()
			got := changed()
			if !reflect.DeepEqual(sorted(got), sorted(c.want)) {
				t.Fatalf("changed() after %s = %v, want %v", c.fn, got, c.want)
			}
			restore()
			if got := changed(); len(got) != 0 {
				t.Fatalf("changed() after restore() = %v, want none", got)
			}
			restore() // idempotent
			if got := changed(); len(got) != 0 {
				t.Fatalf("changed() after a second restore() = %v, want none", got)
			}
		})
	}
}

// TestWiringCasesCoverEverySetter fails when an exported Set*/Clear*/Add*
// function has no case above, or a Clear* case names no Set* pair.
func TestWiringCasesCoverEverySetter(t *testing.T) {
	srcs := readNonTestSources(t)
	declared, err := exportedWiringFuncs(srcs)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]wiringCase{}
	for _, c := range wiringCases() {
		have[c.fn] = c
	}
	for _, fn := range declared {
		if _, ok := have[fn]; !ok {
			t.Errorf("%s has no case in wiringCases", fn)
		}
	}
	isDeclared := map[string]bool{}
	for _, fn := range declared {
		isDeclared[fn] = true
	}
	for fn, c := range have {
		if !isDeclared[fn] {
			t.Errorf("case %s names no declared function", fn)
		}
		if strings.HasPrefix(fn, "Clear") && (c.pair == "" || !isDeclared[c.pair] || !strings.HasPrefix(c.pair, "Set")) {
			t.Errorf("%s must name the Set* whose value it clears, got %q", fn, c.pair)
		}
	}
}

// TestSnapshotWiringDistinguishesClosures: openAppForCmd installs a fresh
// closure from one literal on every run, so a snapshot that compared code
// pointers would call every run a no-op.
func TestSnapshotWiringDistinguishesClosures(t *testing.T) {
	withWiring(t)
	mk := func(s string) func() string { return func() string { return s } }
	SetAssigneeResolver(mk("one"))
	restore, changed := SnapshotWiring()
	if got := changed(); len(got) != 0 {
		t.Fatalf("changed() after the snapshot = %v", got)
	}
	SetAssigneeResolver(mk("two"))
	if got := changed(); !reflect.DeepEqual(got, []string{"assigneeResolver"}) {
		t.Fatalf("swapping to a second closure from one literal: changed() = %v, want [assigneeResolver]", got)
	}
	restore()
	if got := changed(); len(got) != 0 {
		t.Fatalf("changed() after restore() = %v", got)
	}
	if got := assigneeResolver(); got != "one" {
		t.Fatalf("restore did not bring the first closure back: got %q", got)
	}

	a, b := mk("one"), mk("two")
	other := a
	if sameWiring(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem()) {
		t.Error("two closures from one literal over different captures compare as the same")
	}
	if !sameWiring(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&other).Elem()) {
		t.Error("a closure does not compare as itself")
	}
	var nilFn func() string
	if sameWiring(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&nilFn).Elem()) {
		t.Error("a closure compares as the same as nil")
	}
}

// TestSnapshotWiringSeesInPlaceAppend: Add appends in place, so the
// snapshot must still see the longer list as different.
func TestSnapshotWiringSeesInPlaceAppend(t *testing.T) {
	withWiring(t)
	SetChangeNotifier(func(string) {})
	restore, changed := SnapshotWiring()
	AddChangeNotifier(func(string) {})
	if got := changed(); !reflect.DeepEqual(got, []string{"changeNotifiers"}) {
		t.Fatalf("changed() after an append = %v, want [changeNotifiers]", got)
	}
	restore()
	if got := changed(); len(got) != 0 {
		t.Fatalf("changed() after restore() = %v", got)
	}
	if len(changeNotifiers) != 1 {
		t.Fatalf("restore left %d notifiers, want 1", len(changeNotifiers))
	}
}

func readNonTestSources(t *testing.T) map[string]string {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	srcs := map[string]string{}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		srcs[n] = string(b)
	}
	return srcs
}

var wiringFuncRE = regexp.MustCompile(`^(Set|Clear|Add)[A-Z]`)

// exportedWiringFuncs lists the exported Set*/Clear*/Add* functions declared in
// the given sources.
func exportedWiringFuncs(srcs map[string]string) ([]string, error) {
	var out []string
	fset := token.NewFileSet()
	for name, src := range srcs {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && wiringFuncRE.MatchString(fd.Name.Name) {
				out = append(out, fd.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// declaredPackageVars lists every package-level var in the given sources.
func declaredPackageVars(srcs map[string]string) (map[string]bool, error) {
	out := map[string]bool{}
	fset := token.NewFileSet()
	for name, src := range srcs {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, s := range gd.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					if n.Name != "_" {
						out[n.Name] = true
					}
				}
			}
		}
	}
	return out, nil
}

// wiringTableProblems reports every way the restore table and the exemption
// list fail to account for the package-level vars in srcs, each naming the
// variable.
func wiringTableProblems(srcs map[string]string, table []string, exempt map[string]string) ([]string, error) {
	declared, err := declaredPackageVars(srcs)
	if err != nil {
		return nil, err
	}
	inTable := map[string]bool{}
	var problems []string
	for _, n := range table {
		inTable[n] = true
		if !declared[n] {
			problems = append(problems, n+": in the restore table but declared nowhere")
		}
		if _, ok := exempt[n]; ok {
			problems = append(problems, n+": both in the restore table and exempt")
		}
	}
	for n, reason := range exempt {
		if strings.TrimSpace(reason) == "" {
			problems = append(problems, n+": exempt with no reason")
		}
		if !declared[n] {
			problems = append(problems, n+": exempt but declared nowhere")
		}
	}
	for n := range declared {
		if _, ok := exempt[n]; !inTable[n] && !ok {
			problems = append(problems, n+": neither restored by SnapshotWiring nor exempt with a reason")
		}
	}
	sort.Strings(problems)
	return problems, nil
}

func wiringTableNames() []string {
	names := make([]string, len(wiringVars))
	for i, w := range wiringVars {
		names[i] = w.name
	}
	return names
}

// TestWiringVarsComplete: every package-level var in verb is either restored by
// SnapshotWiring or exempt with a stated reason, so new wiring cannot be added
// and left leaking between tests.
func TestWiringVarsComplete(t *testing.T) {
	problems, err := wiringTableProblems(readNonTestSources(t), wiringTableNames(), wiringExempt)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	for i, w := range wiringVars {
		if reflect.TypeOf(w.ptr).Kind() != reflect.Pointer {
			t.Errorf("%s: table entry %d is not a pointer", w.name, i)
		}
	}
}

func TestWiringScanCatchesNewVar(t *testing.T) {
	const base = "package verb\nvar a string\nvar (\n\tb int\n\tc = 1\n)\nvar _ = 0\n"
	table := []string{"a", "b"}
	exempt := map[string]string{"c": "constant-like"}
	cases := []struct {
		name   string
		extra  string
		table  []string
		exempt map[string]string
		want   string // substring of the single expected problem, "" for none
	}{
		{"all accounted for", "", table, exempt, ""},
		{"added unlisted var", "var foo string\n", table, exempt, "foo: neither"},
		{"added unlisted var in a block", "var (\n\tbar int\n)\n", table, exempt, "bar: neither"},
		{"table names an undeclared var", "", append([]string{"ghost"}, table...), exempt, "ghost: in the restore table but declared nowhere"},
		{"exemption with no reason", "", table, map[string]string{"c": " "}, "c: exempt with no reason"},
		{"exempt and restored", "", append([]string{"c"}, table...), exempt, "c: both"},
	}
	for _, c := range cases {
		got, err := wiringTableProblems(map[string]string{"x.go": base + c.extra}, c.table, c.exempt)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		switch {
		case c.want == "" && len(got) != 0:
			t.Errorf("%s: unexpected problems %v", c.name, got)
		case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
			t.Errorf("%s: problems = %v, want one containing %q", c.name, got, c.want)
		}
	}
}

// TestProductionWiringDefaults: the seams with a non-zero default survive both
// process start and a set-then-restore cycle, and each is callable.
func TestProductionWiringDefaults(t *testing.T) {
	exercise := func(t *testing.T, when string) {
		t.Helper()
		for _, w := range wiringVars {
			word, ok := startWiring.funcs[w.name]
			if !ok {
				continue
			}
			v := reflect.ValueOf(w.ptr).Elem()
			if v.IsNil() {
				t.Errorf("%s: %s is nil", when, w.name)
			} else if funcWord(v) != word {
				t.Errorf("%s: %s is not the production default", when, w.name)
			}
		}
		verdictRecord("production-default probe")
		if changelogPath() == "" {
			t.Errorf("%s: changelogPath() is empty", when)
		}
		_ = dispatchLogsDir() // "" is a legitimate answer outside a satelle repo
		snap := driverSnapshotter("", "", t.TempDir())
		if snap.Available || !strings.Contains(snap.UnavailableReason, agentcli.HarnessUnknown) {
			t.Errorf("%s: driverSnapshotter with no harness/session = %+v, want unavailable naming %q", when, snap, agentcli.HarnessUnknown)
		}
		win := driverWindowReader("", "", t.TempDir(), time.Time{}, time.Time{})
		if win.Available || !strings.Contains(win.UnavailableReason, agentcli.HarnessUnknown) {
			t.Errorf("%s: driverWindowReader with no harness/session = %+v, want unavailable naming %q", when, win, agentcli.HarnessUnknown)
		}
		if engagementParallel != config.ParallelNone {
			t.Errorf("%s: engagementParallel = %q, want %q", when, engagementParallel, config.ParallelNone)
		}
	}

	if startWiring.parallel != config.ParallelNone {
		t.Errorf("at process start engagementParallel = %q, want %q", startWiring.parallel, config.ParallelNone)
	}
	for _, name := range []string{"verdictRecord", "dispatchLogsDir", "changelogPath", "driverWindowReader", "driverSnapshotter"} {
		if startWiring.funcs[name] == nil {
			t.Errorf("at process start %s is nil", name)
		}
	}
	exercise(t, "before any set")

	t.Run("after set then restore", func(t *testing.T) {
		restore, changed := SnapshotWiring()
		t.Cleanup(restore)
		SetVerdictRecorder(nil)
		SetEngagementMode(config.Config{Engagement: config.EngagementConfig{Parallel: config.ParallelEpic}})
		dispatchLogsDir = func() string { return "x" }
		changelogPath = func() string { return "x" }
		driverWindowReader = func(string, string, string, time.Time, time.Time) agentcli.DriverWindowUsage {
			return agentcli.DriverWindowUsage{}
		}
		driverSnapshotter = func(string, string, string) agentcli.DriverSnapshot { return agentcli.DriverSnapshot{} }
		if got := changed(); len(got) != 6 {
			t.Fatalf("changed() = %v, want the six variables just set", got)
		}
		restore()
		exercise(t, "after set then restore")
	})
}
