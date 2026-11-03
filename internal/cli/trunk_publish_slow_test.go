//go:build clislow

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

func TestSlowTrunkPublishRecordsTheCombinedHeadOnTheStory(t *testing.T) {
	slices := map[string]func(testutil.TrunkRepos, *testing.T){
		"linear": func(r testutil.TrunkRepos, t *testing.T) { r.LinearSlice(t) },
		"epic":   func(r testutil.TrunkRepos, t *testing.T) { r.EpicSlice(t) },
	}
	for name, build := range slices {
		t.Run(name, func(t *testing.T) {
			seen := filepath.Join(t.TempDir(), "proved-sha")
			prove := filepath.Join(t.TempDir(), "prove.sh")
			body := fmt.Sprintf("test -f other.txt || exit 1\ngit rev-parse HEAD > %s\n", seen)
			if err := os.WriteFile(prove, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			repo, r, id := trunkEngageRepo(t, fmt.Sprintf("[trunk]\nprove = 'sh %s'\npublish_rounds = 2\n", prove))
			build(r, t)
			r.PublishFromPusher(t, "other.txt")

			out, err := runRoot(t, "trunk", "publish", "--story", id, "--json")
			if err != nil {
				t.Fatalf("publish: %v\n%s", err, out)
			}
			head := r.Head(t, repo)
			pushed, combined := jsonField(t, out, "pushed"), jsonField(t, out, "combined")
			if got := r.RemoteHead(t); pushed != got || combined != got || head != got {
				t.Fatalf("pushed=%s combined=%s subject=%s remote=%s, want all equal", pushed, combined, head, got)
			}
			if b, _ := os.ReadFile(seen); strings.TrimSpace(string(b)) != combined {
				t.Errorf("the configured proof ran on %q, want the combined head %s", b, combined)
			}

			led, err := runRoot(t, "ledger", "list", "--story", id)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"trunk_publish", "satelle: trunk published " + pushed, "combined " + combined} {
				if !strings.Contains(led, want) {
					t.Errorf("ledger lacks %q:\n%s", want, led)
				}
			}
		})
	}
}
