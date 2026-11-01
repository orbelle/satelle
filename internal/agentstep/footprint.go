package agentstep

import (
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
)

// SeatKind names which role charter a dispatched seat's system prompt opens with.
type SeatKind string

const (
	// SeatReviewer is an isolated gate reviewer (and the step summariser).
	SeatReviewer SeatKind = "reviewer"
	// SeatPerformer is a named isolated executor performing a step (planner, coder).
	SeatPerformer SeatKind = "performer"
	// SeatConsult is a binding opened as a consultant by the rework relay.
	SeatConsult SeatKind = "consult"
)

// SeatPayloadInput is everything one dispatched seat's system prompt is built
// from. The caller resolves the documents (the constitution body, the resident
// principle documents — empty when the seat's binding injects none); the text of
// the prompt is BuildSeatPayload's.
type SeatPayloadInput struct {
	Constitution string
	Principles   []docindex.Doc // resident principle documents, in delivery order
	Charter      string         // role charter
	Scratch      string         // this dispatch's scratch dir; "" omits the briefing
	Rubric       string         // the skill body
	Judging      bool           // the judging briefing replaces the pull-context call to action
	Inline       bool           // judging with the work item inline: the pull-context call to action stays
	Shell        bool           // the judging briefing also names the CLI lines
	Verdict      string         // the shared verdict rules (ReviewerVerdictDoc); delivered only when Judging
}

// principlesGroup heads the resident principles in a seat's system prompt.
const principlesGroup = "# Always-resident principles (satelle)"

// BuildSeatPayload builds the system prompt a dispatched seat receives, in the
// one canonical order: constitution and principles, role charter, scratch
// briefing, pull-context call to action, skill rubric. Every document is
// delivered whole: satelle fits no seat to a size, because managing a seat's
// context is its CLI's job (sty_58a7bdfe).
func BuildSeatPayload(in SeatPayloadInput) string {
	tail := composeSystemPrompt(promptParts{
		charter: in.Charter, scratch: in.Scratch, rubric: in.Rubric, judging: in.Judging, inline: in.Inline, shell: in.Shell, verdict: in.Verdict,
	})
	var b strings.Builder
	if c := strings.TrimSpace(in.Constitution); c != "" {
		b.WriteString("# Project constitution\n\n" + c + "\n\n")
	}
	grouped := false
	for _, d := range in.Principles {
		body := strings.TrimSpace(stripFrontmatter(d.Body))
		if body == "" {
			continue
		}
		// A judging seat is handed the verdict rules in the tail; a selector that
		// also picks them up (system, all) must not deliver them a second time.
		if in.Judging && strings.TrimSpace(in.Verdict) != "" && d.Name == config.ReviewerVerdictPrinciple {
			continue
		}
		if !grouped {
			b.WriteString(principlesGroup + "\n\n")
			grouped = true
		}
		b.WriteString(body + "\n\n")
	}
	return b.String() + tail
}

// SeatPromptInput is what SeatPayloadFor assembles a seat's prompt from for the
// injected-size report: the role the seat plays, and the documents it is handed.
type SeatPromptInput struct {
	Kind     SeatKind
	Section  string // agents.toml section the seat runs under
	Step     string // step or status the seat serves; only names the charter
	Workflow string // workflow name; only names the performer charter
	// Constitution and Principles are the project constitution body and the
	// resolved principle documents. Empty means the binding injects none.
	Constitution string
	Principles   []docindex.Doc
	// Rubric is the seat's skill body (its largest one, for a size estimate).
	Rubric string
	// Judging is whether the seat judges a gate — the dispatch that is handed the
	// judging briefing and Verdict. A summary seat of a reviewer kind does not
	// judge, so the estimate keys on this and not on Kind.
	Judging bool
	// Verdict is ReviewerVerdictDoc's text; delivered only when Judging.
	Verdict string
	// Shell is whether the seat's grant admits a shell, which a judging
	// reviewer's briefing then spells three more CLI lines for.
	Shell bool
}

// estimateScratch stands in for a dispatch's scratch directory, which only exists
// for a live dispatch: a path of the length a real one has (<tmp>/satelle/<repo
// key>/<story>/<dispatch id>), so the estimate carries the briefing's weight and
// its length is stable across runs.
const estimateScratch = "/tmp/satelle/000000000000/sty_00000000/00000000"

// SeatPayloadFor returns the system prompt a dispatched seat would receive, built
// by the same BuildSeatPayload buildRequest uses, with a stand-in scratch
// directory. The injected-size report and the context audit measure this; it is
// estimation, never dispatch.
func SeatPayloadFor(in SeatPromptInput) string {
	var charter string
	switch in.Kind {
	case SeatReviewer:
		charter = reviewerCharter()
	case SeatConsult:
		charter = consultCharter(in.Section, "consultant", in.Step)
	default:
		charter = executorCharter(in.Section, in.Step, in.Workflow)
	}
	return BuildSeatPayload(SeatPayloadInput{
		Constitution: in.Constitution, Principles: in.Principles, Charter: charter, Rubric: in.Rubric,
		Scratch: estimateScratch, Judging: in.Judging, Verdict: in.Verdict, Shell: in.Shell,
	})
}
