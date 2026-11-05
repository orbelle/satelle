package remotechan

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
)

// interopServer is the hosted server the interop test signs in to.
const interopServer = "https://satelle.dev"

// TestRelayInteropProduction runs this package's real client against the live
// relay, not an imitation of it: enrollment through Open and Join, one sealed
// message each way, and a job whose heartbeat outlives one interval. It needs a
// stored sign-in (satelle login) and the network, so it runs only when
// SATELLE_RELAY_INTEROP=1; SATELLE_RELAY_INTEROP_SERVER names another server.
//
// Run it against production with the real credentials path named, because the
// hosted store's test guard refuses the real path otherwise:
//
//	SATELLE_HOME=$HOME/.config/satelle SATELLE_RELAY_INTEROP=1 \
//	  go test ./internal/remotechan -run TestRelayInteropProduction -v -count=1
func TestRelayInteropProduction(t *testing.T) {
	if os.Getenv("SATELLE_RELAY_INTEROP") != "1" {
		t.Skip("set SATELLE_RELAY_INTEROP=1 to run against the production relay")
	}
	server := interopServer
	if s := os.Getenv("SATELLE_RELAY_INTEROP_SERVER"); s != "" {
		server = s
	}
	client, err := hosted.Dial(server, hosted.DefaultStore(), hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	relay := strings.TrimRight(server, "/") + "/relay"

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	res := make(chan joined, 1)
	launch := LauncherFunc(func(_ context.Context, spec LaunchSpec) ([]byte, error) {
		t.Logf("relay %s session %s…", spec.RelayURL, spec.Session[:8])
		go func() {
			s, err := Join(ctx, spec.RelayURL, spec.Session, id, spec.Token)
			res <- joined{s, err}
		}()
		return id.Public(), nil
	})

	local, err := Open(ctx, client, relay, launch)
	if err != nil {
		t.Fatalf("Open against %s: %v", relay, err)
	}
	defer local.Close()
	j := <-res
	if j.err != nil {
		t.Fatalf("Join against %s: %v", relay, j.err)
	}
	remote := j.s
	defer remote.Close()
	t.Log("enrolled: both ends admitted through the production relay")

	if err := local.Send(ctx, "", []byte("hello remote")); err != nil {
		t.Fatalf("local Send: %v", err)
	}
	m, err := remote.Recv(ctx)
	if err != nil || string(m.Body) != "hello remote" {
		t.Fatalf("remote Recv = %q, %v", m.Body, err)
	}
	if err := remote.Send(ctx, "", []byte("hello local")); err != nil {
		t.Fatalf("remote Send: %v", err)
	}
	m, err = local.Recv(ctx)
	if err != nil || string(m.Body) != "hello local" {
		t.Fatalf("local Recv = %q, %v", m.Body, err)
	}
	t.Log("sealed exchange: one message each way")

	// Wait past the longest a silent job survives (LostAfter, checked every
	// HeartbeatInterval): the job is still not lost only if its heartbeats arrived.
	wait := LostAfter + HeartbeatInterval + 2*time.Second
	local.Expect("interop")
	job := remote.StartJob(context.Background(), "interop")
	select {
	case l := <-local.Lost():
		t.Fatalf("job %s reported lost while beating", l.Job)
	case <-time.After(wait):
	}
	if err := job.Done(); err != nil {
		t.Fatalf("Done: %v", err)
	}
	t.Logf("heartbeat: job stayed alive for %s, past LostAfter+HeartbeatInterval (%s); only heartbeats crossing the relay could keep it from being reported lost", wait, LostAfter+HeartbeatInterval)
}
