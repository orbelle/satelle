package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/verb"
)

// substrateTrackingFindings reports a repo whose .satelle/ process substrate is
// not tracked in git: ignored by the repo's .gitignore, ignored by an exclude
// file outside it (a machine-wide core.excludesFile or .git/info/exclude), or
// present but never added. The remediation names the tracked-substrate block
// `satelle init` writes (gitignoreNeeded), any line the operator must remove,
// and the git add. No .satelle/ or no git repo ⇒ no finding.
func substrateTrackingFindings(repoRoot string) health.Findings {
	if !dirExists(filepath.Join(repoRoot, config.DefaultDataDir)) || !verb.GitListed(repoRoot, "rev-parse", "--git-dir") {
		return nil
	}
	probe := ""
	for _, rel := range []string{config.DefaultDataDir + "/" + config.ConfigName, config.DefaultDataDir + "/workflows"} {
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(rel))); err == nil {
			probe = rel
			break
		}
	}
	if probe == "" {
		return nil
	}
	steps := []string{"run `satelle init` to write the tracked-substrate .gitignore block (`!.satelle/` re-included, then only " +
		"the machine-local state ignored: .satelle/**/.env, logs, hooks, deployed.version, *.local.toml, " +
		"workflows/agents.local.toml, the pinned binary, tasks/*/exe_*.md and tasks/*/output-exe_*.md)"}
	// The operator's own wholesale ignore outside the managed block: satelle
	// never edits it, so whenever the substrate is untracked the finding names it.
	rootGitignore, _ := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	ownLine, hasOwn := gitignoreOwnSatelleIgnore(string(rootGitignore))
	var detail string
	ignored, src, pattern := verb.GitCheckIgnoreSource(repoRoot, probe)
	switch {
	case ignored && !filepath.IsAbs(src) && filepath.Base(src) == ".gitignore":
		raw, _ := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(src)))
		switch _, removed := retireManagedBlock(string(raw)); {
		case src == ".gitignore" && hasOwn && pattern == ownLine:
			detail = fmt.Sprintf(".gitignore ignores .satelle/ wholesale with the line %q, so the process substrate is not tracked in git", ownLine)
		case len(removed) > 0:
			// satelle's own retired block: init removes the line itself.
			detail = fmt.Sprintf("satelle's managed block in %s still ignores .satelle/ (rule %q), so the process substrate is not tracked in git", src, pattern)
		default:
			detail = fmt.Sprintf("%s ignores the process substrate (rule %q), so it is not tracked in git", src, pattern)
			if !(src == ".gitignore" && hasOwn) {
				steps = append(steps, fmt.Sprintf("remove the rule %q from %s", pattern, src))
			}
		}
	case ignored:
		detail = fmt.Sprintf("%s, an exclude file outside the repo's .gitignore (core.excludesFile or .git/info/exclude), ignores .satelle/ (rule %q), so the process substrate is not tracked in git; the block's `!.satelle/` re-include overrides it", src, pattern)
	case !verb.GitListed(repoRoot, "ls-files", "--", config.DefaultDataDir):
		detail = ".satelle/ is present but no file under it is tracked in git"
	default:
		return nil
	}
	if hasOwn {
		steps = append(steps, fmt.Sprintf("remove the line %q from .gitignore (satelle never edits a line outside its managed block)", ownLine))
	}
	steps = append(steps, "`git add .satelle`", "commit")
	return health.Findings{health.Warn(health.IDSubstrateUntracked, "Process substrate not tracked in git", detail).
		About(config.DefaultDataDir).WithRemediation(strings.Join(steps, "; then "))}
}

// gitignoreConvergeFindings reports a repo whose .gitignore lacks an entry
// satelle needs or holds a retired managed block — the convergeGitignore
// judgement `satelle init` heals and init --all lists STALE — so doctor and
// init agree on what needs init. No .satelle/ or no git repo ⇒ no finding.
func gitignoreConvergeFindings(repoRoot string) health.Findings {
	if !dirExists(filepath.Join(repoRoot, config.DefaultDataDir)) || !verb.GitListed(repoRoot, "rev-parse", "--git-dir") {
		return nil
	}
	raw, _ := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	_, missing, changed := convergeGitignore(string(raw), repoRoot)
	if !changed {
		return nil
	}
	detail := ".gitignore managed block not current — it holds a retired shape"
	if len(missing) > 0 {
		detail = ".gitignore managed block not current — missing " + strings.Join(missing, ", ")
	}
	return health.Findings{health.Warn(health.IDScaffoldStale, ".gitignore managed block not current", detail).
		About(".gitignore").WithRemediation("run `satelle init` to heal")}
}

// repoGitignoreFindings is doctor's .gitignore health: the substrate-tracking
// finding, then the managed-block convergence finding.
func repoGitignoreFindings(repoRoot string) health.Findings {
	return append(substrateTrackingFindings(repoRoot), gitignoreConvergeFindings(repoRoot)...)
}
