package agentstep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A value satelle placed in its own process environment (a remote node's
// run-minted github-app token) is scrubbed from a performer's output with the
// binding's own env, and clearing the set stops scrubbing it.
func TestProcessSecretsAreScrubbedFromPerformerOutput(t *testing.T) {
	const token = "ghs_PROCESS-SECRET-VALUE"
	g := New(nil, nil, t.TempDir(), "")
	g.SetProcessSecrets(map[string]string{"GH_TOKEN": token})

	out := "pushed with " + token + " and key-from-binding\n"
	got := verb.OutputTail(out, g.scrubValues(map[string]string{"API_KEY": "key-from-binding"}))
	if strings.Contains(got, token) || strings.Contains(got, "key-from-binding") {
		t.Errorf("OutputTail = %q, want both the process secret and the binding value scrubbed", got)
	}

	g.SetProcessSecrets(nil)
	if _, ok := g.scrubValues(nil)["GH_TOKEN"]; ok {
		t.Error("a cleared process secret is still scrubbed")
	}
}

// The set a dispatch's output is redacted of at its source (agentcli.Request.Redact,
// built as scrubValues(nil, resolved)) carries the process secrets beside what the
// dispatch resolved, so the bytes every log, row and result is made from never
// hold the value, and without process secrets it is just what was resolved.
func TestProcessSecretsJoinTheSourceRedactionSet(t *testing.T) {
	const token = "ghs_PROCESS-SECRET-VALUE"
	resolved := map[string]string{"infisical://p/dev/API_KEY": "resolved-api-key-value"}
	g := New(nil, nil, t.TempDir(), "")
	g.SetProcessSecrets(map[string]string{"GH_TOKEN": token})

	out := string(redactBytes([]byte("used "+token+" and resolved-api-key-value\n"), g.scrubValues(nil, resolved)))
	if strings.Contains(out, token) || strings.Contains(out, "resolved-api-key-value") ||
		!strings.Contains(out, "[redacted:GH_TOKEN]") {
		t.Errorf("source-redacted output = %q, want the process secret and the resolved value redacted", out)
	}

	g.SetProcessSecrets(nil)
	if got := g.scrubValues(nil, resolved); len(got) != 1 || got["infisical://p/dev/API_KEY"] != "resolved-api-key-value" {
		t.Errorf("redaction set without process secrets = %v, want only the resolved set", got)
	}
}

const checkToken = "ghs_CHECK-PROCESS-SECRET"

// tokenCheck is a fake g.check that prints the process secret and fails with an
// error naming it and carrying exit code 1.
func tokenCheck(context.Context, string, string, string) (string, error) {
	return "printed " + checkToken + "\n", tokenExitErr{}
}

type tokenExitErr struct{}

func (tokenExitErr) Error() string { return "exit status 1 (" + checkToken + ")" }
func (tokenExitErr) ExitCode() int { return 1 }

// assertRedacted fails when s holds the process secret or lacks its marker.
func assertRedacted(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, checkToken) || !strings.Contains(s, "[redacted:GH_TOKEN]") {
		t.Errorf("%s = %q, want the redaction marker and never the process secret", what, s)
	}
}

// A gate check's output and error are scrubbed of the process secrets, and the
// scrubbed error still carries the exit code the when and functional gates read.
func TestRedactedCheckRedactsProcessSecrets(t *testing.T) {
	g := New(nil, nil, t.TempDir(), "")
	g.SetProcessSecrets(map[string]string{"GH_TOKEN": checkToken})
	g.check = tokenCheck
	out, err := g.redactedCheck(context.Background(), "", "cmd", "")
	assertRedacted(t, "output", out)
	if err == nil {
		t.Fatal("the check's error was dropped")
	}
	assertRedacted(t, "error", err.Error())
	var ee interface{ ExitCode() int }
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Errorf("the redacted error lost its exit code: %#v", err)
	}

	g.SetProcessSecrets(nil)
	if out, _ := g.redactedCheck(context.Background(), "", "cmd", ""); !strings.Contains(out, checkToken) {
		t.Errorf("with no process secrets the output must pass unchanged, got %q", out)
	}
}

// The combine check folding a panel is scrubbed: its output reaches the gate's
// notes as the redaction marker.
func TestCombineCheckRedactsProcessSecrets(t *testing.T) {
	r := &modeRunner{reply: func(string) string { return `{"decision":"accept","notes":"fine"}` }}
	gate := "\n[[gate]]\nskill = \"rev-g\"\non = [\"in_progress\"]\npanel = [\"reviewer\"]\ncombine = \"fold-check\"\n"
	g := modeEngine(t, "", gate, r)
	g.SetProcessSecrets(map[string]string{"GH_TOKEN": checkToken})
	g.check = tokenCheck
	dec := modeGate(t, g)
	rv := reviewerOf(t, dec, "rev-g")
	if rv.Accept {
		t.Fatalf("a failing combine check must reject: %+v", rv)
	}
	assertRedacted(t, "combine notes", rv.Notes)
	if b, _ := json.Marshal(dec); strings.Contains(string(b), checkToken) {
		t.Errorf("the gate decision holds the process secret: %s", b)
	}
}

// A when check's exit 1 still skips its gate through the redacted error, and the
// skip's telemetry detail holds the marker, not the value.
func TestWhenSkipDetailRedactsProcessSecrets(t *testing.T) {
	docs := fakeDocs{workflow: whenWF, skillFound: true, skillBody: "rubric", extraSkills: []docindex.Doc{whenCheckSkill()}}
	g, r := newEngine(t, `{"decision":"accept"}`, docs)
	recs := captureTelemetry(g)
	g.SetProcessSecrets(map[string]string{"GH_TOKEN": checkToken})
	g.check = tokenCheck
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_w", Status: "backlog"}, "in_progress"); err != nil {
		t.Fatal(err)
	}
	if r.got.SystemPrompt != "" {
		t.Fatal("exit 1 must still skip the gate through the redacted error")
	}
	ev := whenEvents(*recs)
	if len(ev) != 1 || ev[0].data["reason"] != "when" {
		t.Fatalf("want one when skip event, got %#v", ev)
	}
	assertRedacted(t, "when skip detail", fmt.Sprint(ev[0].data["detail"]))
}

// Every production call of the engine's check seam goes through redactedCheck,
// so no gate check reaches a consumer unredacted.
func TestNoDirectGateCheckCall(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name.Name == "redactedCheck" {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "check" {
						t.Errorf("%s: %s calls .check directly; route it through redactedCheck",
							fset.Position(call.Pos()), fn.Name.Name)
					}
					return true
				})
			}
		}
	}
}

// A functional check gate whose command prints GH_TOKEN, set as a process
// secret, keeps the value out of the decision the ledger row is written from,
// the telemetry the timeline reads and the log the compressor is handed.
func TestFunctionalCheckRedactsProcessSecrets(t *testing.T) {
	const ghToken = "ghs_FUNCTIONAL-CHECK-TOKEN"
	t.Setenv("GH_TOKEN", ghToken)
	wf := spineWF("", "", "", "in_progress|executor", "integrated|executor||token-check", "done")
	body := "---\nname: token-check\ntype: skill\ndescription: prints the token\n---\n# Gate\n\n```check\necho \"token=$GH_TOKEN\"; exit 1\n```\n"
	g, _ := newEngine(t, ``, fakeDocs{workflow: wf, skillBody: body, skillFound: true})
	g.repoRoot = t.TempDir()
	recs := captureTelemetry(g)
	g.SetProcessSecrets(map[string]string{"GH_TOKEN": ghToken})
	var log string
	g.SetCheckLogCompressor(func(_ context.Context, _, l string) string { log = l; return l })

	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_f", Status: "in_progress"}, "integrated")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Accept {
		t.Fatalf("a check exiting 1 must reject: %+v", dec)
	}
	check := func(what, s string) {
		t.Helper()
		if strings.Contains(s, ghToken) || !strings.Contains(s, "[redacted:GH_TOKEN]") {
			t.Errorf("%s = %q, want the redaction marker and never the token", what, s)
		}
	}
	check("check log", log)
	check("gate notes", dec.Notes)
	check("gate row notes", reviewerOf(t, dec, "token-check").Notes)
	if b, _ := json.Marshal(dec); strings.Contains(string(b), ghToken) {
		t.Errorf("the gate decision holds the token: %s", b)
	}
	if s := fmt.Sprintf("%#v", *recs); strings.Contains(s, ghToken) {
		t.Errorf("telemetry holds the token: %s", s)
	}
}
