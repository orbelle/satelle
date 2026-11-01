package substratereview

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Dispositions the judge may return for an item.
const (
	// DispRemove: an embedded default covers it, or nothing reads it.
	DispRemove = "remove"
	// DispReduce: keep only the lines no default carries; Replacement is the
	// whole text it is reduced to.
	DispReduce = "reduce"
	// DispKeep: it carries a named quality rule the default lacks.
	DispKeep = "keep"
	// DispConflict: it contradicts the code, a default, the constitution or the
	// BDD baseline; Evidence cites where.
	DispConflict = "conflict"
	// NotJudged is rendered for an item or rule the judge returned nothing for,
	// or returned without the reason and evidence a disposition needs.
	NotJudged = "not judged"
)

// Notes recorded on an item a judgement came back for but could not count.
const (
	NoteNoReason      = "returned without reason/evidence"
	NoteNoReplacement = "reduce returned without replacement text"
)

func validDisposition(d string) bool {
	return d == DispRemove || d == DispReduce || d == DispKeep || d == DispConflict
}

// ItemJudgement is the judge's disposition of one item.
type ItemJudgement struct {
	Item        string   `json:"item"`
	Disposition string   `json:"disposition"`
	KeptLines   []string `json:"kept_lines,omitempty"`
	Rule        string   `json:"rule,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	// Evidence is what the disposition rests on: a file:line, a story id, or
	// the default that covers the item.
	Evidence string `json:"evidence,omitempty"`
	// Replacement is the full text a reduce leaves.
	Replacement string `json:"replacement,omitempty"`
	// MissedDefaults are the improvements the current embedded default carries
	// that an edited or seeded copy lacks.
	MissedDefaults []string `json:"missed_defaults,omitempty"`
}

// RuleFinding is the judge's finding against one constitution rule: a file and
// line in the codebase that contradicts it, or None when it found none.
type RuleFinding struct {
	Rule string `json:"rule"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	None bool   `json:"none,omitempty"`
	Note string `json:"note,omitempty"`
}

// Judgement is what the reviewer seat returns.
type Judgement struct {
	Items        []ItemJudgement `json:"items"`
	Constitution []RuleFinding   `json:"constitution_findings"`
}

// Merge joins the judgements of several batches into one.
func Merge(js ...Judgement) Judgement {
	var out Judgement
	for _, j := range js {
		out.Items = append(out.Items, j.Items...)
		out.Constitution = append(out.Constitution, j.Constitution...)
	}
	return out
}

// ParseJudgement reads the judgement out of a reviewer's notes: the first JSON
// object in them that decodes as a Judgement. Prose around it is ignored.
func ParseJudgement(notes string) (Judgement, error) {
	for i := strings.IndexByte(notes, '{'); i >= 0; {
		dec := json.NewDecoder(strings.NewReader(notes[i:]))
		var j Judgement
		if err := dec.Decode(&j); err == nil && (j.Items != nil || j.Constitution != nil) {
			return j, nil
		}
		next := strings.IndexByte(notes[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return Judgement{}, fmt.Errorf("the reviewer's notes carry no judgement object {\"items\":[…],\"constitution_findings\":[…]}")
}

// Dispositions is the item → disposition map of a judgement in notes, the form
// `satelle review score` checks a substrate-review case's expected
// dispositions against. A judgement the report would read as not judged maps to
// NotJudged.
func Dispositions(notes string) (map[string]string, error) {
	j, err := ParseJudgement(notes)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, ij := range j.Items {
		d, _ := judged(ij)
		out[ij.Item] = d
	}
	return out, nil
}

// judged is the disposition an item judgement counts as, and the note when it
// does not count: an unknown disposition, or one returned without its reason
// and evidence (or a reduce without its replacement), is NotJudged — never
// keep.
func judged(ij ItemJudgement) (string, string) {
	switch {
	case !validDisposition(ij.Disposition):
		return NotJudged, ""
	case strings.TrimSpace(ij.Reason) == "" || strings.TrimSpace(ij.Evidence) == "":
		return NotJudged, NoteNoReason
	case ij.Disposition == DispReduce && strings.TrimSpace(ij.Replacement) == "":
		return NotJudged, NoteNoReplacement
	}
	return ij.Disposition, ""
}

// JudgeInput is what the review knows about the items of one judge dispatch
// (or of the whole review, before Batches splits it by area). It is never sent
// as it stands: the judge receives ManifestOf it — pointers, not content.
type JudgeInput struct {
	// Area is the area this batch judges ("" for the whole review), and Chunk
	// its 1-based part when the area was split (0 when it was not).
	Area  string
	Chunk int
	// Scratch is the directory outside the tree the item, history and default
	// files were written under.
	Scratch           string
	Items             []JudgeItem
	Rules             []Rule
	ConstitutionPath  string
	OrchestratorModel string
}

// JudgeItem is one item with its evidence and the files the judge reads it
// through.
type JudgeItem struct {
	Item
	Evidence Evidence
	// Path is a readable file holding the item's text; DefaultPath the current
	// embedded default of an edited or seeded copy; HistoryPath the item's
	// matched ledger rows (rejection reasons, dissent notes).
	Path        string
	DefaultPath string
	HistoryPath string
}

// ReportItem is one item's row in the report.
type ReportItem struct {
	Item
	Disposition    string   `json:"disposition"`
	KeptLines      []string `json:"kept_lines,omitempty"`
	Rule           string   `json:"rule,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Citation       string   `json:"citation,omitempty"`
	Replacement    string   `json:"replacement,omitempty"`
	MissedDefaults []string `json:"missed_defaults,omitempty"`
	// Note says why a returned judgement was not counted.
	Note     string   `json:"note,omitempty"`
	Evidence Evidence `json:"evidence"`
}

// ReportRule is one constitution rule's row in the report.
type ReportRule struct {
	Rule
	// Finding is "finding", "no finding" or NotJudged.
	Finding string `json:"finding"`
	File    string `json:"file,omitempty"`
	FLine   int    `json:"finding_line,omitempty"`
	Note    string `json:"note,omitempty"`
}

// BatchFailure is one judge dispatch that returned no usable judgement: its
// items read NotJudged and the seat's raw answer is kept at RawPath.
type BatchFailure struct {
	Area    string `json:"area"`
	Chunk   int    `json:"chunk,omitempty"`
	RawPath string `json:"raw_path,omitempty"`
	Err     string `json:"error"`
}

// Report is the whole review.
type Report struct {
	GeneratedAt       time.Time         `json:"generated_at"`
	Binding           string            `json:"binding,omitempty"`
	OrchestratorModel string            `json:"orchestrator_model,omitempty"`
	SeatModels        map[string]string `json:"seat_models,omitempty"`
	Items             []ReportItem      `json:"items"`
	Rules             []ReportRule      `json:"constitution"`
	// Batches is how many judge dispatches the review made, and Failures the
	// ones that returned no usable judgement.
	Batches  int            `json:"batches,omitempty"`
	Failures []BatchFailure `json:"failures,omitempty"`
	// ExecutionID is the task execution the review ran as, so a cut applied from
	// the report names the run it came from.
	ExecutionID string `json:"execution,omitempty"`
}

// Build joins the inventory, its evidence and the judgement. Every item and
// every rule appears; one the judge omitted, gave an unknown disposition, or
// returned without its reason and evidence renders NotJudged.
func Build(in JudgeInput, j Judgement) Report {
	byItem := map[string]ItemJudgement{}
	for _, ij := range j.Items {
		byItem[ij.Item] = ij
	}
	byRule := map[string]RuleFinding{}
	for _, f := range j.Constitution {
		byRule[f.Rule] = f
	}
	rep := Report{OrchestratorModel: in.OrchestratorModel}
	for _, it := range in.Items {
		ri := ReportItem{Item: it.Item, Evidence: it.Evidence, Disposition: NotJudged}
		if ij, ok := byItem[it.Key]; ok {
			ri.Disposition, ri.Note = judged(ij)
			ri.Reason, ri.Citation = ij.Reason, ij.Evidence
			if ri.Disposition != NotJudged {
				ri.KeptLines, ri.Rule, ri.Replacement, ri.MissedDefaults = ij.KeptLines, ij.Rule, ij.Replacement, ij.MissedDefaults
			}
		}
		rep.Items = append(rep.Items, ri)
	}
	for _, r := range in.Rules {
		rr := ReportRule{Rule: r, Finding: NotJudged}
		if f, ok := byRule[r.Name]; ok {
			switch {
			case f.File != "":
				rr.Finding, rr.File, rr.FLine, rr.Note = "finding", f.File, f.Line, f.Note
			case f.None:
				rr.Finding, rr.Note = "no finding", f.Note
			}
		}
		rep.Rules = append(rep.Rules, rr)
	}
	return rep
}

// WriteJSON writes the report to path.
func (r Report) WriteJSON(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ReadReport reads a report written by WriteJSON.
func ReadReport(path string) (Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return Report{}, fmt.Errorf("%s is not a substrate review report: %w", path, err)
	}
	return r, nil
}

// Find returns the report's row for an item key.
func (r Report) Find(key string) (ReportItem, bool) {
	for _, it := range r.Items {
		if it.Key == key {
			return it, true
		}
	}
	return ReportItem{}, false
}

// Render writes the report as markdown.
func (r Report) Render(out io.Writer) {
	fmt.Fprintln(out, "# Substrate review")
	fmt.Fprintln(out)
	orch := r.OrchestratorModel
	if orch == "" {
		orch = "not recorded"
	}
	fmt.Fprintf(out, "Orchestrator model (latest in-loop session): %s\n", orch)
	if r.Binding != "" {
		fmt.Fprintf(out, "Judged by binding: %s\n", r.Binding)
	}
	counts := map[string]int{}
	for _, it := range r.Items {
		counts[it.Disposition]++
	}
	fmt.Fprintf(out, "Items: %d — remove %d, reduce %d, keep %d, conflict %d, not judged %d\n\n",
		len(r.Items), counts[DispRemove], counts[DispReduce], counts[DispKeep], counts[DispConflict], counts[NotJudged])

	fmt.Fprintln(out, "## Items")
	for _, it := range r.Items {
		fmt.Fprintf(out, "\n### %s — %s\n\n", it.Key, it.Disposition)
		fmt.Fprintf(out, "- origin: %s (%s)\n", it.Origin, it.Source)
		if it.Note != "" {
			fmt.Fprintf(out, "- not counted: %s\n", it.Note)
		}
		if it.Reason != "" {
			fmt.Fprintf(out, "- reason: %s\n", oneLine(it.Reason))
		}
		if it.Citation != "" {
			fmt.Fprintf(out, "- evidence: %s\n", oneLine(it.Citation))
		}
		if it.Rule != "" {
			fmt.Fprintf(out, "- rule: %s\n", it.Rule)
		}
		for _, l := range it.KeptLines {
			fmt.Fprintf(out, "- kept: %s\n", oneLine(l))
		}
		for _, m := range it.MissedDefaults {
			fmt.Fprintf(out, "- default improvement not carried: %s\n", oneLine(m))
		}
		if it.Replacement != "" {
			fmt.Fprintf(out, "- replacement:\n\n%s\n\n", indent(it.Replacement))
		}
		for _, l := range it.Evidence.Lines() {
			fmt.Fprintf(out, "- %s\n", l)
		}
	}

	if len(r.Failures) > 0 {
		fmt.Fprintln(out, "\n## Batches not judged")
		fmt.Fprintln(out)
		for _, f := range r.Failures {
			fmt.Fprintf(out, "- %s: %s", f.Label(), oneLine(f.Err))
			if f.RawPath != "" {
				fmt.Fprintf(out, " (raw answer: %s)", f.RawPath)
			}
			fmt.Fprintln(out)
		}
	}

	fmt.Fprintln(out, "\n## Constitution")
	fmt.Fprintln(out)
	for _, rr := range r.Rules {
		where := rr.Origin
		if rr.Line > 0 {
			where = fmt.Sprintf("constitution:%d", rr.Line)
		}
		line := fmt.Sprintf("- %s (%s): %s", rr.Name, where, rr.Finding)
		if rr.File != "" {
			line += fmt.Sprintf(" %s:%d", rr.File, rr.FLine)
		}
		if rr.Note != "" {
			line += " — " + oneLine(rr.Note)
		}
		fmt.Fprintln(out, line)
	}
}

// Label names a batch: its area, and its part when the area was split.
func (f BatchFailure) Label() string {
	if f.Chunk > 0 {
		return fmt.Sprintf("%s-%d", f.Area, f.Chunk)
	}
	return f.Area
}

// indent renders text as an indented markdown block.
func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// Lines renders the evidence as report lines.
func (e Evidence) Lines() []string {
	var out []string
	out = append(out, fmt.Sprintf("history: %d presentation(s), %d rejection(s); amended %d (%.0f%%), reversed %d (%.0f%%), re-presented to accept %d (%.0f%%); parks %d; rework %d; flaky %d",
		e.Presentations, e.Rejections, e.Amended, e.AmendedShare, e.Reversed, e.ReversedShare, e.Represented, e.RepresentedShare, e.Parks, e.Rework, e.Flaky))
	for _, r := range e.Reasons {
		out = append(out, "rejected: "+r)
	}
	switch {
	case !e.DissentRecorded:
		out = append(out, "dissent: not recorded")
	case len(e.Dissent) == 0:
		out = append(out, "dissent: none")
	}
	for _, d := range e.Dissent {
		verdict := "rejected"
		if d.Accepted {
			verdict = "accepted"
		}
		out = append(out, fmt.Sprintf("dissent: %s %s %s under %s — %s", d.Story, d.Seat, verdict, d.Model, d.Notes))
	}
	for _, m := range e.Models {
		out = append(out, fmt.Sprintf("model %s: %d invocation(s), %d accept(s), %d rejection(s) — %s",
			m.Model, m.Invocations, m.Accepts, m.Rejections, strings.Join(m.Stories, ", ")))
	}
	for _, seat := range sortedKeys(e.CurrentModels) {
		m := e.CurrentModels[seat]
		if m == "" {
			m = "(binding names none)"
		}
		out = append(out, fmt.Sprintf("seat %s is bound to %s", seat, m))
	}
	if len(e.Stories) > 0 {
		out = append(out, "stories: "+strings.Join(e.Stories, ", "))
	}
	return out
}
