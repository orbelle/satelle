package agentstep

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/retrieve"
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

// sty_58a7bdfe AC5: a dispatched seat receives every injected principle whole —
// including one far larger than any former per-seat budget — with no condensing,
// no retrieve marker and no pointer header, and nothing offloaded.
func TestSeatReceivesEveryPrincipleWhole(t *testing.T) {
	principles := []docindex.Doc{
		seatPrinciple(config.OperatingPrinciple, 10), seatPrinciple("p-two", 8),
		seatPrinciple("p-huge", 600), // ~120 KB, past any budget a repo set
	}
	g := New(&fakeRunner{}, fakeDocs{workflow: testWorkflow, extraPrinciples: principles}, "/repo", "")
	g.SetConstitution("A short constitution. Never edit without a story.")
	offloaded := 0
	g.SetDiffOffloader(func(_ context.Context, _ string, content []byte) (string, error) {
		offloaded++
		return retrieve.Hash(content), nil
	})
	req, err := g.buildRequest(context.Background(), invocation{
		charter: reviewerCharter(), rubric: "RUBRIC-BODY", principles: config.PrinciplesSession,
		payload: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := req.SystemPrompt
	for _, p := range principles {
		if !strings.Contains(got, strings.TrimSpace(stripFrontmatter(p.Body))) {
			t.Errorf("%s is not delivered whole", p.Name)
		}
	}
	for _, mark := range []string{"Retrieve more", "OMITTED FOR THE", "CONTEXT BUDGET"} {
		if strings.Contains(got, mark) {
			t.Errorf("the seat prompt carries %q: principles must not be condensed or pointered", mark)
		}
	}
	if offloaded != 0 {
		t.Errorf("%d document(s) offloaded, want none", offloaded)
	}
	for _, tail := range []string{"A short constitution.", "RUBRIC-BODY", reviewerCharter()} {
		if !strings.Contains(got, tail) {
			t.Errorf("the prompt lost %q", tail[:20])
		}
	}
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
