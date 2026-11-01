package substratereview

import (
	"fmt"
	"strings"
)

// ApplyCategory is the category of the story --apply raises for a cut: the
// repo's substrate lane.
const ApplyCategory = "substrate"

// ApplyTag marks every story --apply raises.
const ApplyTag = "substrate-review"

// TeamHolding is whether this repo may cut a team-owned item: Held when the
// caller is the project's substrate holder and this tree's copy is the tier's,
// so the cut raised here is the one the holder publishes. Holder names who
// holds the substrate (or why that is unknown) for the refusal.
type TeamHolding struct {
	Held   bool
	Holder string
}

// Refusal is why an item cannot be applied in this repo, or "" when it can.
// An embedded item has no file in this repo to cut. A team item is owned by the
// project tier: team says whether this repo holds it, and one it does not hold
// is raised where its substrate holder is, not here. A nil team holds nothing.
func Refusal(it ReportItem, team func(ReportItem) TeamHolding) string {
	switch it.Origin {
	case OriginEmbedded:
		return fmt.Sprintf("%s: origin embedded — it is the binary's default with no file in this repo, so there is nothing here to cut; change it in satelle itself", it.Key)
	case OriginTeam:
		var h TeamHolding
		if team != nil {
			h = team(it)
		}
		if h.Held {
			return ""
		}
		return fmt.Sprintf("%s: origin team — the project tier owns it (%s); only its substrate holder can cut it — raise it there (holder: %s)", it.Key, it.Source, nonEmpty(h.Holder, "unknown"))
	}
	return ""
}

// StoryFor is the story --apply raises for one item: its title, body and
// acceptance criteria carry the item, its origin, its disposition and its
// evidence, so the story stands on its own when it is planned.
func StoryFor(it ReportItem) (title, body, criteria string) {
	title = fmt.Sprintf("substrate review: %s %s", it.Disposition, it.Key)
	var b strings.Builder
	fmt.Fprintf(&b, "Raised by `satelle substrate review --apply %s`.\n\n", it.Key)
	fmt.Fprintf(&b, "- item: %s (%s)\n", it.Key, it.Kind)
	fmt.Fprintf(&b, "- origin: %s\n", it.Origin)
	fmt.Fprintf(&b, "- source: %s\n", it.Source)
	fmt.Fprintf(&b, "- disposition: %s\n", it.Disposition)
	if it.Reason != "" {
		fmt.Fprintf(&b, "- reason: %s\n", oneLine(it.Reason))
	}
	if it.Rule != "" {
		fmt.Fprintf(&b, "- rule: %s\n", it.Rule)
	}
	if it.Citation != "" {
		fmt.Fprintf(&b, "- evidence: %s\n", oneLine(it.Citation))
	}
	for _, l := range it.KeptLines {
		fmt.Fprintf(&b, "- kept: %s\n", oneLine(l))
	}
	for _, m := range it.MissedDefaults {
		fmt.Fprintf(&b, "- default improvement not carried: %s\n", oneLine(m))
	}
	if it.Replacement != "" {
		fmt.Fprintf(&b, "\n## Replacement\n\n%s\n", indent(it.Replacement))
	}
	b.WriteString("\n## Evidence\n\n")
	for _, l := range it.Evidence.Lines() {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	body = b.String()

	evidence := it.Evidence.Lines()[0]
	var first string
	switch it.Disposition {
	case DispRemove:
		first = fmt.Sprintf("%s is removed from %s, and the default that covers it governs in its place (evidence: %s).", it.Key, it.Source, evidence)
	case DispReduce:
		if it.Replacement != "" && len(it.KeptLines) == 0 {
			first = fmt.Sprintf("%s is reduced to the text under Replacement in this story's body (evidence: %s).", it.Key, evidence)
			break
		}
		first = fmt.Sprintf("%s keeps only these lines: %s (evidence: %s).", it.Key, quoteLines(it.KeptLines), evidence)
	case DispKeep:
		first = fmt.Sprintf("%s is kept, and the quality rule it carries that the default lacks (%s) is named beside it (evidence: %s).", it.Key, nonEmpty(it.Rule, oneLine(it.Reason)), evidence)
	case DispConflict:
		first = fmt.Sprintf("%s no longer contradicts %s (evidence: %s).", it.Key, nonEmpty(it.Rule, "the constitution or the BDD baseline"), evidence)
	default:
		first = fmt.Sprintf("%s is judged and given a disposition by its owner (the review returned %s; evidence: %s).", it.Key, it.Disposition, evidence)
	}
	criteria = "1. " + first + "\n2. `satelle validate` passes after the change."
	return title, body, criteria
}

func quoteLines(ls []string) string {
	if len(ls) == 0 {
		return "(the review named none)"
	}
	q := make([]string, len(ls))
	for i, l := range ls {
		q[i] = "\"" + oneLine(l) + "\""
	}
	return strings.Join(q, "; ")
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}
