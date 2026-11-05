package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
)

const (
	holderID = "usr_holder"
	memberID = "usr_member"
)

func holderOf(id string) hosted.SubstrateHolder {
	return hosted.SubstrateHolder{UserID: id, Email: id + "@example.com"}
}

// serverHostedPath is the server's hosted list: what only the holder may PUT.
func serverHostedPath(p string) bool {
	for _, prefix := range []string{"workflows/", "skills/", "principles/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return p == "constitution.md" || p == config.ConfigName
}

// seedCredAs stores a credential for server whose principal (and bearer, which
// the fake reads as the caller) is id.
func seedCredAs(t *testing.T, server, id string) {
	t.Helper()
	if err := (hosted.FileStore{}).Save(hosted.Credential{ServerURL: server, AccessToken: id, RefreshToken: "r", PrincipalID: id, Email: id + "@example.com"}); err != nil {
		t.Fatal(err)
	}
}

const memberTask = "tasks/tsk_member.md"

func memberTaskBody(v string) string {
	return "---\nid: tsk_member\ntype: task\n---\n# member task " + v + "\n"
}

func assertOneHolderWarning(t *testing.T, stderr string, paths ...string) {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.Contains(l, "team substrate not published") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "warning: ") {
		t.Fatalf("want exactly one holder warning, got %d:\n%s", len(lines), stderr)
	}
	for _, want := range append([]string{holderOf(holderID).Email}, paths...) {
		if !strings.Contains(lines[0], want) {
			t.Errorf("warning must name %s: %s", want, lines[0])
		}
	}
}
