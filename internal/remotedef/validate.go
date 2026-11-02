package remotedef

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Validate checks the rules of a definition the node acts on before it reads a
// secret, the ones satelle-server applies to a github-app secret. Its token is
// minted with exactly its declared permissions (sty_5b905ad8), so a github-app
// secret must declare a non-empty permissions table of GitHub App permission
// names, each "read" or "write", and no other secret may declare one. It is
// minted scoped to the definition's [[repo]] entries on its host (sty_c7f12b60),
// so a definition with none there cannot be given one. The rest of the
// definition's validation is the server's, and Parse stays lenient.
func Validate(d Definition) error {
	for i, s := range d.Secret {
		if err := validatePermissions(i, s); err != nil {
			return err
		}
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

// permissionName is the shape of a GitHub App permission name.
var permissionName = regexp.MustCompile(`^[a-z_]+$`)

// validatePermissions checks the permissions table of the secret at index i:
// required and non-empty on a github-app secret, refused on any other, each name
// matching [a-z_]+ and each value exactly "read" or "write". Names are checked in
// sorted order, so the refusal is the same on every run.
func validatePermissions(i int, s SecretDecl) error {
	field := fmt.Sprintf("secret[%d].permissions", i)
	if !s.IsGitHubApp() {
		if s.Permissions != nil {
			return fmt.Errorf("%s: only a from = %q secret declares permissions", field, FromGitHubApp)
		}
		return nil
	}
	if s.Permissions == nil {
		return fmt.Errorf("%s: required for from = %q", field, FromGitHubApp)
	}
	if len(s.Permissions) == 0 {
		return fmt.Errorf("%s: must not be empty", field)
	}
	names := make([]string, 0, len(s.Permissions))
	for name := range s.Permissions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !permissionName.MatchString(name) {
			return fmt.Errorf("%s: permission name %q must match [a-z_]+", field, name)
		}
		if v := s.Permissions[name]; v != "read" && v != "write" {
			return fmt.Errorf("%s: permission %s = %q must be \"read\" or \"write\"", field, name, v)
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
