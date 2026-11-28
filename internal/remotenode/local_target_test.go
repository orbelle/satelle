package remotenode

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/remote/provider"
)

func TestFireLocalRefusesUnregisteredTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ghost func(f taskFixture) string
	}{
		{"task", func(f taskFixture) string { return f.task }},
		{"input", func(f taskFixture) string { return f.inputs[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := secretFixture(t, "echo checked", nil, "")
			ghost := "ghost"
			if _, err := f.k.Ledger.Append(tc.ghost(f), item.KindUpdated, "me", item.Fields{Repo: &ghost}); err != nil {
				t.Fatal(err)
			}
			script, log := fakeDockerFor(t, "exit 0\n")
			o := f.def
			o.Docker, o.Runs = provider.DockerLocal{Docker: script}, LocalRuns{Dir: t.TempDir()}
			o.Secrets = Secrets{Names: []string{probeSecret}, Values: map[string]string{probeSecret: "v"}}
			_, err := FireLocal(context.Background(), o)
			if err == nil || !strings.Contains(err.Error(), "does not register") || !strings.Contains(err.Error(), "ghost") {
				t.Fatalf("FireLocal = %v", err)
			}
			if _, err := os.Stat(log); err == nil {
				t.Fatal("docker ran for an unregistered target")
			}
			if _, err := os.Stat(o.Runs.path(o.Remote, o.Item)); err == nil {
				t.Fatal("a run was saved for an unregistered target")
			}
			for _, id := range f.chains() {
				events, err := f.k.Ledger.Read(id)
				if err != nil {
					t.Fatal(err)
				}
				if item.HeldSeat(events) {
					t.Fatalf("%s was seated", id)
				}
			}
		})
	}
}
