package cli

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/remotenode"
)

const resolveUserDecision = "user-decision"

const leftUserDecision = "it changes the substrate, and raising the story for it is the user's decision"

const substrateStoryTitle = "Add the .satelle files this release ships"

const substrateAsk = "Ask the user: should I raise and implement a satelle story to update and fix substrate issues?"

func initProblem(rel string) string {
	return "this release ships .satelle/" + rel + ", which the repo lacks"
}

func findingCriterion(check, problem string) string {
	return fmt.Sprintf("satelle doctor no longer reports %s: %s", check, problem)
}

func substrateStoryCommand(category string, rels []string) string {
	var b strings.Builder
	b.WriteString("satelle story create --category " + remotenode.ShQuote(category) + " --title " + remotenode.ShQuote(substrateStoryTitle))
	for _, rel := range rels {
		b.WriteString(" --criteria " + remotenode.ShQuote(findingCriterion("init", initProblem(rel))))
	}
	b.WriteString(" --session <session>")
	return b.String()
}

func printCommand(rel string) string {
	return "satelle init --print .satelle/" + rel + " --session <session>"
}

func categoryUnsetProblem(n int) string {
	return fmt.Sprintf(".satelle/%s names no category for substrate stories (category), so satelle gives no command that raises the story for the %d file(s) the repo lacks", kernel.InitFile, n)
}

func categoryUnsetFix(cats []string) string {
	s := "naming it changes the substrate, which is a substrate story, and raising one is the user's decision: the user gives that story its category when raising it, and its session sets category in .satelle/" + kernel.InitFile + " (satelle help configuration); the same story can add the files."
	if len(cats) == 0 {
		return s + " No workflow rule in .satelle/" + kernel.SettingsFile + " selects a category."
	}
	q := make([]string, len(cats))
	for i, c := range cats {
		q[i] = fmt.Sprintf("%q", c)
	}
	return s + " This repo's workflow rules select the categories: " + strings.Join(q, ", ") + "."
}

const raiseWith = " Raise it with: "

func substrateFix(category string, rels []string, rel string) string {
	s := "adding it changes the substrate, which is a substrate story, and raising one is the user's decision. That story's session writes the file from: " + printCommand(rel) + "."
	if strings.TrimSpace(category) == "" {
		return s + " No command is given until .satelle/" + kernel.InitFile + " names the category: see the init issue for it."
	}
	return s + raiseWith + substrateStoryCommand(category, rels)
}

func userDecisions(issues []doctorIssue) int {
	n := 0
	for _, is := range issues {
		if is.Resolve == resolveUserDecision {
			n++
		}
	}
	return n
}

func ruleCategories(dataDir string) []string {
	st, _ := kernel.LoadSettings(dataDir)
	return st.Workflows.Categories()
}

func initIssues(dataDir string) ([]doctorIssue, error) {
	plan, err := kernel.InitPlan(dataDir)
	if err != nil {
		return nil, err
	}
	if len(plan) == 0 {
		return nil, nil
	}
	pol := kernel.InitPolicy(dataDir)
	var out []doctorIssue
	if !pol.CategorySet() {
		out = append(out, doctorIssue{Check: "init", Problem: categoryUnsetProblem(len(plan)), Fix: categoryUnsetFix(ruleCategories(dataDir)), Resolve: resolveUserDecision})
	}
	for _, rel := range plan {
		out = append(out, doctorIssue{Check: "init", Problem: initProblem(rel), Fix: substrateFix(pol.Category, plan, rel), Resolve: resolveUserDecision})
	}
	return out, nil
}
