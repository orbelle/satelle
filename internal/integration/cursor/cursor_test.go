package cursor

import (
	"os"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

func TestCursorPerformerRules(t *testing.T) {
	writer := []string{"Read", "Grep", "Glob", "Edit", "Write"}
	denies := []string{"Bash(satelle story set:*)", "Bash(satelle task set:*)", "Bash(satelle execution set:*)"}
	if g, err := cursorFor(common.Binding{Tools: writer}, denies); err != nil || !slices.Equal(g.rules, []string{cursorDenyAllShell}) {
		t.Fatalf("cursor performer rules %v %v", g.rules, err)
	}
}

func TestParseCursorStream(t *testing.T) {
	stream, err := os.ReadFile("testdata/cursor-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	res, err := parseCursor(stream)
	if err != nil || res.Text != "OK" || res.TokensIn != 4+9854+10126 || res.TokensOut != 4 || res.Model != "Claude Opus 5.5 300K Medium" {
		t.Fatalf("cursor %+v %v", res, err)
	}
}
