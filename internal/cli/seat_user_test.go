package cli

import (
	"os"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// boundUsers maps each session to the user its seat.bound payload on id
// names.
func boundUsers(t *testing.T, id string) map[string]string {
	t.Helper()
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	st, err := ledger.OpenExisting(r.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := st.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	users := map[string]string{}
	for _, e := range evs {
		if e.Kind != item.KindSeatBound {
			continue
		}
		var s item.Seat
		if err := e.Decode(&s); err != nil {
			t.Fatal(err)
		}
		users[s.Session] = s.User
	}
	return users
}

// A seat records the satelle user who bound it: the signed-in identity when
// there is one, else <OS user>@<host>, also for a harness session seating
// under the session its context hook printed (sto_600b9a85 AC1).
func TestSeatRecordsTheUser(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "seated")

	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	server := hosted.ConfiguredServer(gc)
	if err := (hosted.FileStore{}).Save(hosted.Credential{ServerURL: server, AccessToken: "a", RefreshToken: "r", DisplayName: "Ada", Email: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "seat", id, "--session", "s1")
	if got := boundUsers(t, id)["s1"]; got != "ada@example.com" {
		t.Errorf("signed-in seat user %q, want ada@example.com", got)
	}

	if err := (hosted.FileStore{}).Delete(server); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USER", "bob")
	t.Setenv("USERNAME", "bob")
	host, _ := os.Hostname()
	must(t, "story", "seat", id, "--session", "s2")
	if got := boundUsers(t, id)["s2"]; got != "bob@"+host {
		t.Errorf("signed-out seat user %q, want bob@%s", got, host)
	}

	if _, err := run(t, `{"session_id":"s3"}`, "hook", "context"); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "seat", id, "--session", "s3")
	if got := boundUsers(t, id)["s3"]; got != "bob@"+host {
		t.Errorf("hook session's seat user %q, want bob@%s", got, host)
	}
}
