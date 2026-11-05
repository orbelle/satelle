package agentstep

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
)

// seatPrinciple is a session-resident principle of about n paragraphs.
func seatPrinciple(name string, n int) docindex.Doc {
	var ps []string
	for i := 0; i < n; i++ {
		ps = append(ps, fmt.Sprintf(
			"%s states paragraph %d plainly. Never skip step %d of %s. Do not reorder the %s steps. "+
				"This closing sentence is plain commentary that adds nothing a reader needs.", name, i, i, name, name))
	}
	body := "---\nname: " + name + "\ntags: [principles:session]\n---\n# " + name + "\n\n" + strings.Join(ps, "\n\n") + "\n"
	return docindex.Doc{Kind: "principles", Name: name, Body: body}
}

// BuildSeatPayload is the constitution, the principles under one heading, then
// the tail, in that order and nothing else.
func TestBuildSeatPayloadConcatenatesInOrder(t *testing.T) {
	p1, p2 := seatPrinciple("p-one", 1), seatPrinciple("p-two", 1)
	in := SeatPayloadInput{Constitution: "CONST", Principles: []docindex.Doc{p1, p2}, Charter: "CHARTER", Rubric: "RUBRIC"}
	want := "# Project constitution\n\nCONST\n\n" + principlesGroup + "\n\n" +
		strings.TrimSpace(stripFrontmatter(p1.Body)) + "\n\n" + strings.TrimSpace(stripFrontmatter(p2.Body)) + "\n\n" +
		composeSystemPrompt(promptParts{charter: in.Charter, rubric: in.Rubric})
	if got := BuildSeatPayload(in); got != want {
		t.Errorf("BuildSeatPayload =\n%q\nwant\n%q", got, want)
	}
	if got := BuildSeatPayload(SeatPayloadInput{Charter: "CHARTER"}); strings.Contains(got, "# Project constitution") || strings.Contains(got, principlesGroup) {
		t.Errorf("a seat with no documents gets no document headings: %q", got)
	}
}

// sty_58a7bdfe AC4: no dispatch is refused or measured against a budget — a
// payload of more than 1 MB reaches the adapter whole.
func TestLargeDispatchPayloadIsSentWhole(t *testing.T) {
	fr := &fakeRunner{out: "done"}
	g := New(fr, fakeDocs{}, t.TempDir(), "")
	big := strings.Repeat("x", 1<<20+4096)
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Tools: "Read", Principles: config.PrinciplesNone},
		Section: "coder", Rubric: "do it", StoryID: "sty_big", Skill: "code",
		Payload: map[string]string{"blob": big}, Expect: ExpectPerform, Runner: fr,
	})
	if res.Err != nil {
		t.Fatalf("a large dispatch must run, got %v", res.Err)
	}
	if !strings.Contains(fr.got.Payload, big) {
		t.Errorf("the adapter received %d payload bytes, want the whole %d-byte blob", len(fr.got.Payload), len(big))
	}
}
