package hook

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
)

// editGateDenyForms are the edit gate's own operator-record corpus turned into
// the performer-deny verbs: record becomes set, each form gets a unique id
// (sto_l<N>), and the third PerformerDenies verb gets a form of its own. fake
// replaces the absolute satelle path, for a live run with a fake on PATH.
func editGateDenyForms(fake string) []string {
	var out []string
	for i, c := range append(append([]string{}, operatorRecordCases...), operatorRecordCodeCases...) {
		id := fmt.Sprintf("sto_l%d", i+1)
		f := strings.ReplaceAll(c, "record", "set")
		f = strings.ReplaceAll(strings.ReplaceAll(f, "sto_1", id), "t_1", id)
		if strings.Contains(f, "xargs satelle story set < ids") {
			f = "echo " + id + " | xargs satelle story set"
		}
		if fake != "" {
			f = strings.ReplaceAll(f, "/usr/local/bin/satelle", fake+"/satelle")
		}
		out = append(out, f)
	}
	return append(out, fmt.Sprintf("satelle execution set sto_l%d --status done", len(out)+1))
}

// cursorRuleRegexp is a Cursor Shell(...) glob as Cursor documents it: ** any
// run of characters (newlines and / included), * any run without /, the rest
// literal.
func cursorRuleRegexp(rule string) *regexp.Regexp {
	g := strings.TrimSuffix(strings.TrimPrefix(rule, "Shell("), ")")
	var b strings.Builder
	b.WriteString(`(?s)^`)
	for i := 0; i < len(g); i++ {
		switch {
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(`.*`)
			i++
		case g[i] == '*':
			b.WriteString(`[^/]*`)
		default:
			b.WriteString(regexp.QuoteMeta(string(g[i])))
		}
	}
	b.WriteString(`$`)
	return regexp.MustCompile(b.String())
}

// The cursor performer's deny rule matches every form in which the edit gate
// reads satelle: any word position, any path, behind every wrapper, control
// word, compound operator, substitution and shell or interpreter program
// (sto_0f41dda9 AC3). A corpus form added later that the rule misses fails
// here.
func TestCursorDenyRuleCoversEveryEditGateForm(t *testing.T) {
	rules, err := agent.CursorDenies([]string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}, agent.PerformerDenies)
	if err != nil {
		t.Fatal(err)
	}
	var res []*regexp.Regexp
	for _, r := range rules {
		res = append(res, cursorRuleRegexp(r))
	}
	forms := editGateDenyForms("")
	if len(forms) < len(operatorRecordCases)+len(operatorRecordCodeCases) {
		t.Fatalf("only %d forms", len(forms))
	}
	for _, f := range forms {
		matched := false
		for _, re := range res {
			if re.MatchString(f) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("no rule of %v matches %q", rules, f)
		}
	}
	// The controls a live run uses do not name satelle, so the rule leaves them.
	for _, ok := range []string{"ctlprobe sto_l1", "printf ok > allowed_1.txt"} {
		for _, re := range res {
			if re.MatchString(ok) {
				t.Errorf("the rule %s matches the control %q", re, ok)
			}
		}
	}
}
