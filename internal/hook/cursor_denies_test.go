package hook

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
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

// The live cursor test runs every form of the edit gate's corpus: one per case,
// plus the third PerformerDenies verb, each naming satelle and its own id, so a
// stream's shell call is tied to exactly one form (sto_0f41dda9 AC3).
func TestEditGateDenyFormsCoverTheCorpus(t *testing.T) {
	forms := editGateDenyForms("")
	if want := len(operatorRecordCases) + len(operatorRecordCodeCases) + 1; len(forms) != want {
		t.Fatalf("%d forms, want %d", len(forms), want)
	}
	ids := regexp.MustCompile(`sto_l(\d+)\b`)
	verb := false
	for i, f := range forms {
		if !strings.Contains(f, "satelle") {
			t.Errorf("form %d names no satelle: %q", i+1, f)
		}
		m := ids.FindAllStringSubmatch(f, -1)
		if len(m) == 0 {
			t.Errorf("form %d carries no id: %q", i+1, f)
		}
		for _, id := range m {
			if id[1] != strconv.Itoa(i+1) {
				t.Errorf("form %d carries %s: %q", i+1, id[0], f)
			}
		}
		verb = verb || strings.Contains(f, "execution set")
	}
	if !verb {
		t.Error("no form runs execution set")
	}
}
