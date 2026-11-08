//go:build !windows

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

const artifactAnswer = "Planned.\n```json\n{\"artifact\":{\"name\":\"plan\",\"type\":\"plan\",\"body\":\"# The plan\"}}\n```"

// performedChain is a chain with a performer run whose output is a blob and an
// attachment the run's artifact added.
func performedChain(t *testing.T) (*Kernel, string, []ledger.Event, PerformerRun) {
	t.Helper()
	k, id, err := performIn(t, artifactAnswer)
	if err != nil {
		t.Fatal(err)
	}
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return k, id, events, runOf(t, k, id)
}

func TestBlobRefs(t *testing.T) {
	k, id, events, pr := performedChain(t)
	atts := attachmentsOf(t, k, id)
	if len(atts) != 1 || pr.Output == "" {
		t.Fatalf("fixture: attachments %+v run %+v", atts, pr)
	}
	refs := BlobRefs(events)
	if len(refs) != 2 {
		t.Fatalf("refs %+v, want the run output and the attachment", refs)
	}
	for i, want := range []struct{ kind, sha, what string }{
		{KindPerformerRun, pr.Output, "performer output (work)"},
		{"attachment.added", atts[0].SHA256, "attachment plan"},
	} {
		r := refs[i]
		if r.Kind != want.kind || r.SHA256 != want.sha || r.What != want.what {
			t.Errorf("ref %d = %+v, want %s %s %q", i, r, want.kind, want.sha, want.what)
		}
		if e := events[r.Seq]; e.Kind != want.kind || e.ID != r.Event {
			t.Errorf("ref %d at seq %d names event %s, the chain holds %s %s there", i, r.Seq, r.Event, e.Kind, e.ID)
		}
	}
	// A malformed or empty sha names no blob.
	bad, _ := json.Marshal(PerformerRun{State: "x", Output: "not-a-sha"})
	if refs := BlobRefs([]ledger.Event{{Kind: KindPerformerRun, Payload: bad}}); len(refs) != 0 {
		t.Errorf("malformed sha: %+v", refs)
	}
}

func fsAdapter(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "adapters", "satelle-store-fs"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPushKeys(t *testing.T) {
	k, id, events, pr := performedChain(t)
	atts := attachmentsOf(t, k, id)
	root, work := t.TempDir(), t.TempDir()
	// The recording wrapper logs each operation and key, then runs the
	// reference adapter.
	log := filepath.Join(work, "log")
	stub := filepath.Join(work, "store")
	body := `printf '%s %s\n' "$1" "$2" >> '` + log + `'; exec sh '` + fsAdapter(t) + `' --root '` + root + `' "$@"` + "\n"
	if err := os.WriteFile(stub, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := ledgerstore.Command{Path: "sh", Args: []string{stub}, Dir: work}
	var out bytes.Buffer
	r := ledgerrepl.Replicator{Ledger: k.Ledger, Objects: c, Refs: BlobRefs, Out: &out}
	ctx := context.Background()
	res, err := r.Push(ctx)
	if err != nil || res.Failed() || len(res.Gaps) != 0 {
		t.Fatalf("push: %+v %v\n%s", res, err, out.String())
	}
	chains, err := c.List(ctx, "chains/", "")
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := c.List(ctx, "blobs/", "")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := range events {
		want = append(want, ledgerrepl.ChainKey(id, i))
	}
	if !slices.Equal(chains, want) {
		t.Errorf("chain keys %q, want %q", chains, want)
	}
	wantBlobs := []string{"blobs/" + pr.Output, "blobs/" + atts[0].SHA256}
	slices.Sort(wantBlobs)
	if !slices.Equal(blobs, wantBlobs) {
		t.Errorf("blob keys %q, want %q (the run output and the attachment)", blobs, wantBlobs)
	}
	if !strings.HasPrefix(want[0], "chains/"+id+"/000000000000.json") {
		t.Errorf("first key %s", want[0])
	}
	for i, e := range events {
		b, found, err := c.Get(ctx, ledgerrepl.ChainKey(id, i))
		if err != nil || !found {
			t.Fatalf("get %d: %v %v", i, found, err)
		}
		enc, _ := ledger.Encode(e)
		if !bytes.Equal(b, enc) {
			t.Errorf("event %d body %s, want %s", i, b, enc)
		}
		var got ledger.Event
		if json.Unmarshal(b, &got) != nil || got.Hash != e.Hash {
			t.Errorf("event %d hash %s, want %s", i, got.Hash, e.Hash)
		}
	}
	// Each blob is put before the event that references it.
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	pos := func(line string) int { return slices.Index(lines, line) }
	for _, ref := range BlobRefs(events) {
		b, e := pos("put-if-absent blobs/"+ref.SHA256), pos("put-if-absent "+ledgerrepl.ChainKey(id, ref.Seq))
		if b < 0 || e < 0 || b > e {
			t.Errorf("blob %s put at line %d, its event %d at line %d:\n%s", ref.SHA256, b, ref.Seq, e, logged)
		}
	}
}
