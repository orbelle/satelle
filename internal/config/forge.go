package config

import "strings"

// ForgeConfig is the [forge] table: which forge provider a PR, review or commit
// status goes through (internal/forge), and that provider's own settings. The
// forge is a provider seam ([[satelle-local-first]]): the binary names its
// providers, the repo selects one.
type ForgeConfig struct {
	// Provider names the forge provider. Empty is DefaultForgeProvider, the one
	// provider this build ships; ForgeProviderNone posts nothing, with a warning.
	Provider string `toml:"provider"`
	// PRHeads are the branch prefixes a story's PR is found on, each followed by
	// the story id ("epic/", "feat/"). Empty is DefaultForgePRHeads.
	PRHeads []string `toml:"pr_heads"`
	// GitHub is the [forge.github] table.
	GitHub ForgeGitHubConfig `toml:"github"`
}

// ForgeGitHubConfig is the [forge.github] table.
type ForgeGitHubConfig struct {
	// StatusContext is the context the commit status an approval posts is named
	// by. Empty is DefaultForgeStatusContext.
	StatusContext string `toml:"status_context"`
}

const (
	// DefaultForgeProvider is the forge an unset [forge] provider selects.
	DefaultForgeProvider = "github"
	// ForgeProviderNone selects no forge: nothing is read from or posted to one.
	ForgeProviderNone = "none"
	// DefaultForgeStatusContext names an approval's commit status when
	// [forge.github] status_context is unset.
	DefaultForgeStatusContext = "satelle/pr-approval"
)

// DefaultForgePRHeads are the branch prefixes an unset [forge] pr_heads finds a
// story's PR on.
var DefaultForgePRHeads = []string{"epic/", "feat/"}

// ResolvedPRHeads are the branch prefixes a story's PR is found on: the
// non-blank [forge] pr_heads entries, else DefaultForgePRHeads.
func (f ForgeConfig) ResolvedPRHeads() []string { return nonBlank(f.PRHeads, DefaultForgePRHeads) }

// nonBlank is the trimmed non-blank entries of list, else a copy of def.
func nonBlank(list, def []string) []string {
	var out []string
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), def...)
	}
	return out
}

// ResolvedProvider is the provider [forge] selects.
func (f ForgeConfig) ResolvedProvider() string {
	if p := strings.TrimSpace(f.Provider); p != "" {
		return p
	}
	return DefaultForgeProvider
}

// ResolvedStatusContext is the context an approval's commit status is posted
// under.
func (f ForgeConfig) ResolvedStatusContext() string {
	if c := strings.TrimSpace(f.GitHub.StatusContext); c != "" {
		return c
	}
	return DefaultForgeStatusContext
}
