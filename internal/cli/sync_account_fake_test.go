package cli

import (
	"context"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/syncscope"
)

// Project kinds a testAccount answers for a binding.
const (
	kindPersonal = "personal" // a project of the account's personal team
	kindTeam     = "team"     // a project of a team the account is a member of
	kindForeign  = "foreign"  // a project of a team the account is not in
	kindMissing  = "missing"  // a project the account cannot see
)

// testAccount stands in for the hosted account service a scope resolve asks:
// every binding is a project of kind fallback unless kinds names it. It counts
// the calls made, so a test can prove an unbound or signed-out resolve asked
// nothing.
type testAccount struct {
	mu       sync.Mutex
	fallback string
	kinds    map[string]string
	calls    int
}

const testAccountEmail = "ada@example.com"

func (a *testAccount) kindOf(slug string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if k, ok := a.kinds[slug]; ok {
		return k
	}
	return a.fallback
}

func (a *testAccount) Me(context.Context) (hosted.Principal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return hosted.Principal{ID: "u-ada", Email: testAccountEmail}, nil
}

func (a *testAccount) GetProject(_ context.Context, slug string) (hosted.Project, error) {
	kind := a.kindOf(slug)
	if kind == kindMissing {
		return hosted.Project{}, hosted.ErrProjectNotFound
	}
	return hosted.Project{Slug: slug, TeamID: "w-" + kind}, nil
}

func (a *testAccount) Workspaces(context.Context) ([]hosted.Workspace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return []hosted.Workspace{
		{ID: "w-" + kindPersonal, Kind: "personal", Name: "Ada"},
		{ID: "w-" + kindTeam, Kind: "team", Name: "Acme"},
	}, nil
}

func (a *testAccount) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// suiteAccount is the account the CLI suite resolves against: every bound,
// signed-in repo is personal unless a test says otherwise.
var suiteAccount = &testAccount{fallback: kindPersonal}

// installSuiteAccount points the scope resolve at suiteAccount for the run.
func installSuiteAccount() {
	syncAccountFor = func(context.Context, string, string) syncscope.Account { return suiteAccount }
}

// useAccount resolves against a for the rest of t.
func useAccount(t *testing.T, a *testAccount) {
	t.Helper()
	prev := syncAccountFor
	syncAccountFor = func(context.Context, string, string) syncscope.Account { return a }
	t.Cleanup(func() { syncAccountFor = prev })
}

// useScope makes every binding a project of kind for the rest of t.
func useScope(t *testing.T, kind string) *testAccount {
	t.Helper()
	a := &testAccount{fallback: kind}
	useAccount(t, a)
	return a
}

// signInTo gives this test its own credential store holding a credential for
// server, so a scope resolve finds the machine signed in.
func signInTo(t *testing.T, server string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedCred(t, server)
}
