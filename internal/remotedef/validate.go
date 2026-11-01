package remotedef

import (
	"fmt"
	"net/url"
	"strings"
)

// Validate checks the one rule of a definition the node acts on before it reads a
// secret: a github-app secret is minted scoped to the definition's [[repo]]
// entries on its host, so a definition with none there cannot be given one. It is
// the rule satelle-server applies (sty_c7f12b60); the rest of the definition's
// validation is the server's, and Parse stays lenient.
func Validate(d Definition) error {
	for _, s := range d.Secret {
		if !s.IsGitHubApp() {
			continue
		}
		host := s.GitHubHost()
		if !hasRepoOn(d.Repos, host) {
			return fmt.Errorf("secret %s: from = %q needs a [[repo]] on %s", s.Key, FromGitHubApp, host)
		}
	}
	return nil
}

// hasRepoOn reports whether any repo's URL is on host.
func hasRepoOn(repos []Repo, host string) bool {
	for _, r := range repos {
		u, err := url.Parse(strings.TrimSpace(r.URL))
		if err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	return false
}
