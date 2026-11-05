package config

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/bobmcallan/satelle/internal/structure"
)

// Word-budget ceilings for embedded description fields (sty_6830e78e).
// Description ceilings after sty_24365c69 token diet (workflows/skills ≤ 60).
var descWordBudget = map[string]int{
	"principles": 80,
	"skills":     60,
	"workflows":  60,
	"tasks":      60,
}

// workflowScopeManifest: embedded defaults are scope:system (constitution). The
// shipped lifecycle is one derived route in two halves (sty_3795e7f6).
var workflowScopeManifest = map[string]string{
	"done": "system",
	"step": "system",
}

// verdictBlockWaiver: skills whose body still carries more than one decision+notes
// JSON example (usually an n/a-accept plus the contract template). Ratchet to 1.
var verdictBlockWaiver = map[string]int{
	"satelle-story-scope-review":     2,
	"satelle-workflow-change-review": 2,
}

// dogfoodIDWaiver: empty after sty_24365c69 removed all concrete sty_/tsk_ ids.
// Any new id fails until waived; terminal state is empty.
var dogfoodIDWaiver = map[string]int{}

// dogfoodIDRe matches concrete hex ids only — not placeholders like <sty_id>, sty_…, tsk_<id>.
var dogfoodIDRe = regexp.MustCompile(`\b(sty|tsk)_[0-9a-f]{8}\b`)

// bannedStoriesPath phrases folded from TestEmbeddedSubstrateOmitsInRepoStoriesPath.
var bannedStoriesPaths = []string{
	"under `.satelle/stories/",
	"under .satelle/stories/",
	"live under `.satelle/stories",
}

// dogfoodRepoDeferralRe catches deferral to "the satelle dogfood repo" (or
// similar) as an authority for a product rule (sty_a319db89). Deferral-shaped
// only — NOT a bare "dogfood" token ban: tsk_context-audit legitimately says
// "no dogfood-repo story ids in the rubric" (rule stating itself).
var dogfoodRepoDeferralRe = regexp.MustCompile(`(?i)(see|per|consult|record|reason|decision|full reason).{0,80}dogfood[ -]?repo`)

// gitignoredPathDeferralRe catches authority-deferral to a gitignored tree
// path (sty_a319db89). Deferral-shaped only so pedagogical "do not write under
// .satelle/documents/" lines (e.g. satelle-reviewer-objective-audit) stay green.
var gitignoredPathDeferralRe = regexp.MustCompile(`(?i)(see|consult|read|open|record in|decision).{0,60}\.satelle/(documents|stories)/`)

// legalSkillRoleTags — at most one role tag beyond type:skill.
var legalSkillRoleTags = map[string]bool{
	"type:reviewer":         true,
	"type:executor":         true,
	"type:audit":            true,
	"type:summariser":       true,
	"type:functional-check": true,
}

// conformProblems returns structural / corpus-contract problems for one embedded
// artifact. Pure — synthetic fixtures can be fed without touching the embed FS.
func conformProblems(kind, name, body string) []string {
	var p []string
	key := kind + "/" + name

	// Structure / OKF fold-in
	switch kind {
	case "skills", "workflows", "principles":
		p = append(p, structure.Doc(kind, name, body, nil)...)
	case "tasks":
		p = append(p, structure.CheckTask(body)...)
	}

	fm, rest, ok := splitFrontmatter(body)
	if !ok {
		// structure already reported missing FM for checked kinds
		return p
	}

	// type: scalar must match singular of dir
	wantType := singularKind(kind)
	if got := fmScalarLine(fm, "type"); got != "" && got != wantType {
		p = append(p, fmt.Sprintf("type: %q does not match kind %q (want %q)", got, kind, wantType))
	}

	// scope rules
	scope := fmScalarLine(fm, "scope")
	switch kind {
	case "workflows":
		want, known := workflowScopeManifest[name]
		if !known {
			p = append(p, "workflow missing from workflowScopeManifest — add it with its authored scope")
		} else if scope != want {
			p = append(p, fmt.Sprintf("scope: %q, want %q per workflowScopeManifest", scope, want))
		}
	case "principles":
		if scope != "" {
			p = append(p, "principle must not carry inert scope: (residency is the principles:session tag alone)")
		}
	default:
		if scope != "" && scope != "system" && scope != "project" {
			p = append(p, fmt.Sprintf("scope: %q is not in {system, project}", scope))
		}
	}

	// tags
	tags := parseTagsLine(fm)
	switch kind {
	case "skills":
		if !hasTag(tags, "type:skill") {
			p = append(p, "skill tags must include type:skill")
		}
		roleCount := 0
		for _, t := range tags {
			if t == "type:skill" {
				continue
			}
			if strings.HasPrefix(t, "type:") {
				if !legalSkillRoleTags[t] {
					p = append(p, fmt.Sprintf("illegal skill role tag %q", t))
				}
				roleCount++
			}
		}
		if roleCount > 1 {
			// type:reviewer + type:functional-check is the allowed dual-role pair
			if !(hasTag(tags, "type:reviewer") && hasTag(tags, "type:functional-check") && roleCount == 2) {
				p = append(p, fmt.Sprintf("skill has %d role tags; at most one (or reviewer+functional-check)", roleCount))
			}
		}
	case "principles":
		if !hasTag(tags, "type:principle") {
			p = append(p, "principle tags must include type:principle")
		}
		for _, t := range tags {
			if strings.HasPrefix(t, "principles:") && t != SessionResidencyTag && t != SeatResidencyTag {
				p = append(p, fmt.Sprintf("illegal principles: namespace tag %q (only %s and %s are legal)", t, SessionResidencyTag, SeatResidencyTag))
			}
		}
	case "workflows":
		if !hasTag(tags, "type:workflow") {
			p = append(p, "workflow tags must include type:workflow")
		}
	case "tasks":
		// tasks classify via OKF type: task scalar; topic tags only — no type: tag rule
	}

	// description word budget
	if budget, ok := descWordBudget[kind]; ok {
		if desc := fmScalarLine(fm, "description"); desc != "" {
			n := wordCount(desc)
			if n > budget {
				p = append(p, fmt.Sprintf("description has %d words; ceiling for %s is %d", n, kind, budget))
			}
		}
	}

	// dogfood ids (ratcheting waiver)
	ids := dogfoodIDRe.FindAllString(body, -1)
	n := len(ids)
	if waived, ok := dogfoodIDWaiver[key]; ok {
		if n != waived {
			p = append(p, fmt.Sprintf("dogfood id count %d != waiver %d (ratchet: shrink only as ids are removed)", n, waived))
		}
	} else if n > 0 {
		p = append(p, fmt.Sprintf("contains %d dogfood sty_/tsk_ id(s) with no waiver: %v", n, ids))
	}

	// banned obsolete stories path
	for _, ban := range bannedStoriesPaths {
		if strings.Contains(body, ban) {
			p = append(p, fmt.Sprintf("directs agents under .satelle/stories/ (%q)", ban))
		}
	}

	// Unreachable authority: deferral to a dogfood repo or a gitignored path
	// (sty_a319db89). Deferral-shaped — see dogfoodRepoDeferralRe /
	// gitignoredPathDeferralRe comments for why not a bare token ban.
	if m := dogfoodRepoDeferralRe.FindString(body); m != "" {
		p = append(p, fmt.Sprintf("defers to a dogfood repo as authority (%q)", m))
	}
	if m := gitignoredPathDeferralRe.FindString(body); m != "" {
		p = append(p, fmt.Sprintf("defers to a gitignored path as authority (%q)", m))
	}

	// reviewer skills: accept/reject criteria + verdict JSON block(s) naming decision+notes.
	// Target is exactly one contract block; a few skills still ship an extra n/a
	// example (grandfathered counts). sty_24365c69 should collapse those to 1.
	if kind == "skills" && isReviewerArtifact(name, tags) {
		// functional-check skills exempt from LLM verdict contract (structure already skips)
		if structure.CheckFence(body) == "" {
			lower := strings.ToLower(rest)
			if !strings.Contains(lower, "accept") {
				p = append(p, "reviewer skill missing accept criteria")
			}
			if !strings.Contains(lower, "reject") {
				p = append(p, "reviewer skill missing reject criteria")
			}
			n := countVerdictBlocks(body)
			want := 1
			if w, ok := verdictBlockWaiver[name]; ok {
				want = w
			}
			if n != want {
				p = append(p, fmt.Sprintf("reviewer skill verdict JSON block count %d, want %d (decision+notes)", n, want))
			}
			// also fold structure's decision/notes check
			p = append(p, structure.ReviewerSkillContract(body)...)
		}
	}

	return p
}

func singularKind(kind string) string {
	switch kind {
	case "skills":
		return "skill"
	case "workflows":
		return "workflow"
	case "principles":
		return "principle"
	case "tasks":
		return "task"
	default:
		return kind
	}
}

func isReviewerArtifact(name string, tags []string) bool {
	if strings.HasSuffix(strings.ToLower(name), "-review") {
		return true
	}
	return hasTag(tags, "type:reviewer")
}

// countVerdictBlocks counts fenced ```json blocks (or bare decision snippets)
// that name both "decision" and "notes".
func countVerdictBlocks(body string) int {
	n := 0
	// fenced json blocks
	parts := strings.Split(body, "```")
	for i := 1; i+1 < len(parts); i += 2 {
		infoAnd := parts[i]
		// first line is info string
		nl := strings.IndexByte(infoAnd, '\n')
		var content string
		if nl >= 0 {
			info := strings.TrimSpace(infoAnd[:nl])
			content = infoAnd[nl+1:]
			if info != "" && !strings.HasPrefix(info, "json") && info != "json" {
				// still count if content has decision
			}
		} else {
			content = infoAnd
		}
		if strings.Contains(content, `"decision"`) && strings.Contains(content, `"notes"`) {
			n++
		}
	}
	// bare inline example without fence (only if no fenced count)
	if n == 0 && strings.Contains(body, `"decision"`) && strings.Contains(body, `"notes"`) {
		n = 1
	}
	return n
}

func splitFrontmatter(body string) (fm []string, rest string, ok bool) {
	if !strings.HasPrefix(body, "---") {
		return nil, body, false
	}
	// skip opening ---
	restBody := body[3:]
	if strings.HasPrefix(restBody, "\n") {
		restBody = restBody[1:]
	}
	idx := strings.Index(restBody, "\n---")
	if idx < 0 {
		return nil, body, false
	}
	block := restBody[:idx]
	rest = strings.TrimPrefix(restBody[idx+4:], "\n")
	return strings.Split(block, "\n"), rest, true
}

func fmScalarLine(fm []string, key string) string {
	prefix := key + ":"
	for _, ln := range fm {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, prefix) {
			v := strings.TrimSpace(strings.TrimPrefix(t, prefix))
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func parseTagsLine(fm []string) []string {
	raw := fmScalarLine(fm, "tags")
	if raw == "" {
		// try bracket form on tags: line
		for _, ln := range fm {
			t := strings.TrimSpace(ln)
			if !strings.HasPrefix(t, "tags:") {
				continue
			}
			raw = strings.TrimSpace(strings.TrimPrefix(t, "tags:"))
			break
		}
	}
	raw = strings.Trim(raw, "[]")
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		t := strings.TrimSpace(part)
		t = strings.Trim(t, `"'`)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func wordCount(s string) int {
	n := 0
	inWord := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			n++
			inWord = true
		}
	}
	return n
}

// TestConformanceRejectsNonConformantFixture proves synthetic bad artifacts fail
// the rule engine without polluting the embed FS (sty_6830e78e AC1).
func TestConformanceRejectsNonConformantFixture(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		artName string
		body    string
		wantSub string
	}{
		{
			name:    "illegal principles tag",
			kind:    "principles",
			artName: "bad-principle",
			body: `---
name: bad-principle
type: principle
tags: [type:principle, principles:foo]
description: A short principle used only as a conformance fixture.
---

# Bad

Body.
`,
			wantSub: "principles:foo",
		},
		{
			name:    "description over budget",
			kind:    "principles",
			artName: "wordy",
			body: `---
name: wordy
type: principle
tags: [type:principle]
description: ` + strings.Repeat("word ", 90) + `
---

# Wordy

Body.
`,
			wantSub: "ceiling",
		},
		{
			name:    "dogfood id without waiver",
			kind:    "principles",
			artName: "leaky",
			body: `---
name: leaky
type: principle
tags: [type:principle]
description: Mentions a dogfood story id.
---

# Leaky

See sty_1a2b3c4d for context.
`,
			wantSub: "dogfood",
		},
		{
			name:    "principle with scope",
			kind:    "principles",
			artName: "scoped",
			body: `---
name: scoped
type: principle
scope: system
tags: [type:principle]
description: Should not carry scope.
---

# Scoped

Body.
`,
			wantSub: "scope",
		},
		{
			name:    "reviewer with two verdict blocks",
			kind:    "skills",
			artName: "double-verdict-review",
			body: `---
name: double-verdict-review
type: skill
tags: [type:skill, type:reviewer]
description: Reviewer fixture with two verdict JSON blocks.
---

# Double

Accept when good. Reject when bad.

` + "```json\n" + `{"decision":"accept","notes":"ok"}
` + "```\n\n" + "```json\n" + `{"decision":"reject","notes":"no"}
` + "```\n",
			wantSub: "verdict JSON block count",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			probs := conformProblems(tc.kind, tc.artName, tc.body)
			if len(probs) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
			joined := strings.Join(probs, " | ")
			if !strings.Contains(strings.ToLower(joined), strings.ToLower(tc.wantSub)) {
				t.Errorf("problems %v do not mention %q", probs, tc.wantSub)
			}
		})
	}
}
