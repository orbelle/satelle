//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const framingPrinciple = "principles/satelle-reference-not-restate.md"

var framingRoots = map[string]string{
	"the template": filepath.Join("..", "internal", "kernel", "template"),
	"this repo":    filepath.Join("..", ".satelle"),
}

func framingClaims(description string) (asOfWritten, byCommand bool) {
	for _, sentence := range strings.Split(strings.ToLower(strings.ReplaceAll(description, "; ", ". ")), ". ") {
		if strings.Contains(sentence, ".satelle") && strings.Contains(sentence, "as of when it was written") {
			asOfWritten = true
		}
		if strings.Contains(sentence, "verify") && strings.Contains(sentence, "volatile fact") && strings.Contains(sentence, "by running the command") {
			byCommand = true
		}
	}
	return asOfWritten, byCommand
}

func readFraming(t *testing.T, root string) workflow.Skill {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(framingPrinciple))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sk, err := workflow.ParseSkill(path, string(b))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return sk
}

func TestReferencePrincipleIsFramingAndStatesBothClaims(t *testing.T) {
	for name, root := range framingRoots {
		sk := readFraming(t, root)
		d := sk.Meta["description"]
		if !sk.IsFraming() {
			t.Errorf("%s: the reference principle is not a framing principle: %+v", name, sk.Meta)
		}
		if strings.TrimSpace(d) == "" || strings.Contains(d, "\n") {
			t.Errorf("%s: its description is not one non-empty line: %q", name, d)
		}
		if written, command := framingClaims(d); !written || !command {
			t.Errorf("%s: its description states as-of-written %v and verify-by-command %v, want both: %q", name, written, command, d)
		}
	}
	for name, c := range map[string]struct {
		description      string
		written, command bool
	}{
		"the description before this story": {"Point to where a changing fact is answered rather than writing the fact down. A changing fact is written only with the date it held, and rules are written plainly.", false, false},
		"loose words":                       {"Notes on when files were written and how to verify them", false, false},
		"the first claim alone":             {"A .satelle file states what was true as of when it was written.", true, false},
		"verify with no command":            {"A .satelle file states what was true as of when it was written. Verify a volatile fact before relying on it.", true, false},
		"the claims split across sentences": {"A .satelle file is a record. It is true as of when it was written. Verify a volatile fact. Do so by running the command.", false, false},
	} {
		if written, command := framingClaims(c.description); written != c.written || command != c.command {
			t.Errorf("control %s: as-of-written %v and verify-by-command %v, want %v and %v", name, written, command, c.written, c.command)
		}
	}
}

func TestReferencePrincipleIsTheFirstFramingEntry(t *testing.T) {
	for name, root := range framingRoots {
		st, err := kernel.LoadSettings(root)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		first := ""
		for _, entry := range st.Hook.Inject {
			rel, ok := strings.CutPrefix(filepath.ToSlash(entry), ".satelle/")
			if !ok {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				continue
			}
			if sk, err := workflow.ParseSkill(entry, string(b)); err == nil && sk.IsFraming() && strings.TrimSpace(sk.Meta["description"]) != "" {
				first = rel
				break
			}
		}
		if first != framingPrinciple {
			t.Errorf("%s: the first framing entry of [hook] inject is %q, want %s (inject: %v)", name, first, framingPrinciple, st.Hook.Inject)
		}
	}
}
