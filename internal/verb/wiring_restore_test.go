package verb

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
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

func sorted(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
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
