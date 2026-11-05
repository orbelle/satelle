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
	// ReleaseHeads are the branch prefixes a batch release PR is opened from
	// ("release/"). Empty is DefaultForgeReleaseHeads.
	ReleaseHeads []string `toml:"release_heads"`
	// ReleaseFiles are the path globs (path.Match) a release PR may change beyond
	// the story PRs it merges. Empty is DefaultForgeReleaseFiles.
	ReleaseFiles []string `toml:"release_files"`
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

// DefaultForgeReleaseHeads are the branch prefixes an unset [forge]
// release_heads finds a release PR on.
var DefaultForgeReleaseHeads = []string{"release/"}

// DefaultForgeReleaseFiles are the paths an unset [forge] release_files lets a
// release PR change beyond its stories: the version stamp and the changelogs.
var DefaultForgeReleaseFiles = []string{".version", "CHANGELOG.md", "internal/verb/embedded/CHANGELOG.md"}

// ResolvedPRHeads are the branch prefixes a story's PR is found on: the
// non-blank [forge] pr_heads entries, else DefaultForgePRHeads.
func (f ForgeConfig) ResolvedPRHeads() []string { return nonBlank(f.PRHeads, DefaultForgePRHeads) }

// ResolvedReleaseHeads are the branch prefixes a release PR is opened from: the
// non-blank [forge] release_heads entries, else DefaultForgeReleaseHeads.
func (f ForgeConfig) ResolvedReleaseHeads() []string {
	return nonBlank(f.ReleaseHeads, DefaultForgeReleaseHeads)
}

// ResolvedReleaseFiles are the path globs a release PR may change beyond its
// stories: the non-blank [forge] release_files entries, else
// DefaultForgeReleaseFiles.
func (f ForgeConfig) ResolvedReleaseFiles() []string {
	return nonBlank(f.ReleaseFiles, DefaultForgeReleaseFiles)
}

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
