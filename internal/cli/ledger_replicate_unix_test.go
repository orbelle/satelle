//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

func storeOn(t *testing.T, m machine, root string) {
	t.Helper()
	configureLedger(t, m.dir, `store = { command = "sh", args = ['`+storeAdapter(t)+`', "--root", '`+root+`'] }`)
}

func TestLedgerPushPullRoundTripsThroughTheReferenceAdapter(t *testing.T) {
	root := t.TempDir()

	a := newMachine(t)
	storeOn(t, a, root)
	id1 := createID(t, "story", "create", "--title", "one from a")
	id2 := createID(t, "story", "create", "--title", "two from a")
	bodies := map[string]string{id1: "# one\n", id2: "# two\n"}
	for _, id := range []string{id1, id2} {
		if err := os.WriteFile("doc.md", []byte(bodies[id]), 0o644); err != nil {
			t.Fatal(err)
		}
		must(t, "story", "attach", id, "--file", "doc.md")
	}
	out := must(t, "ledger", "push")
	if !strings.Contains(out, "blob(s) written on 2 item(s); 0 blob gap(s)") || !strings.Contains(out, " 2 blob(s)") {
		t.Errorf("push = %q", out)
	}

	b := newMachine(t)
	storeOn(t, b, root)
	out = must(t, "ledger", "pull")
	if !strings.Contains(out, "on 2 item(s) and 2 blob(s); 0 blob gap(s)") {
		t.Errorf("pull = %q", out)
	}
	for id, title := range map[string]string{id1: "one from a", id2: "two from a"} {
		var it item.Item
		if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil || it.Title != title {
			t.Fatalf("pulled %s = %+v, %v", id, it, err)
		}
		atts := attachments(t, id)
		if len(atts) != 1 {
			t.Fatalf("pulled attachments of %s = %v", id, atts)
		}
		if body, err := runtimeLedger(t).GetBlob(atts[0].SHA256); err != nil || string(body) != bodies[id] {
			t.Errorf("pulled body of %s = %q, %v", id, body, err)
		}
	}
	must(t, "ledger", "verify")

	a.use(t)
	must(t, "story", "set", id1, "--title", "a")
	must(t, "ledger", "push")
	b.use(t)
	must(t, "story", "set", id1, "--title", "b")
	out, err := run(t, "", "ledger", "push")
	if err == nil || ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), id1) || strings.Contains(err.Error(), id2) {
		t.Fatalf("B's push: %v\n%s", err, out)
	}
	if !strings.Contains(out, "diverged: "+id1+" at event ") {
		t.Errorf("B's push output names no sequence:\n%s", out)
	}
	file := runtimeLedger(t).Path(id1)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	n := bytes.Count(before, []byte("\n"))
	out, err = run(t, "", "ledger", "pull")
	if err == nil || ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), id1) {
		t.Fatalf("B's pull: %v\n%s", err, out)
	}
	if want := "diverged: " + id1 + " at event " + strconv.Itoa(n-1) + ": "; !strings.Contains(out, want) {
		t.Errorf("B's pull output lacks %q:\n%s", want, out)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Error("B's chain changed on a refused pull")
	}
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id2, "--json")), &it); err != nil || it.Title != "two from a" {
		t.Errorf("%s = %+v, %v", id2, it, err)
	}
	must(t, "ledger", "verify")
}

func TestLedgerVerifyExitsThreeWhileABlobGapRemains(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "s")
	if err := os.WriteFile("doc.md", []byte("# doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "attach", id, "--file", "doc.md")
	sha := attachments(t, id)[0].SHA256
	blob := filepath.Join(runtimeLedger(t).Dir(), "blobs", sha)
	body, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	must(t, "ledger", "verify")

	for _, c := range []struct {
		name  string
		spoil func()
		why   string
	}{
		{"missing", func() { os.Remove(blob) }, "missing"},
		{"corrupt", func() { os.WriteFile(blob, []byte("other"), 0o644) }, "corrupt"},
	} {
		c.spoil()
		out, err := run(t, "", "ledger", "verify")
		if err == nil || ExitCodeOf(err) != ExitBlobGaps || ExitBlobGaps != 3 {
			t.Fatalf("%s: verify %v\n%s", c.name, err, out)
		}
		if !strings.Contains(out, "gap: "+id+" event ") || !strings.Contains(out, " attachment.added ") ||
			!strings.Contains(out, "blob "+sha+" (attachment doc.md): "+c.why) || !strings.Contains(out, "1 blob gap(s)") {
			t.Errorf("%s: output:\n%s", c.name, out)
		}
		os.Remove(blob)
		if err := os.WriteFile(blob, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := run(t, "", "ledger", "verify"); err != nil || !strings.Contains(out, "chains intact\n") {
			t.Fatalf("%s: verify after the gap filled: %v\n%s", c.name, err, out)
		}
	}

	f, err := os.OpenFile(runtimeLedger(t).Path(id), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"id":"x","item":"` + id + `","kind":"k","prev":"nope","hash":"bad"}` + "\n")
	f.Close()
	if _, err := run(t, "", "ledger", "verify"); err == nil || ExitCodeOf(err) != 1 {
		t.Errorf("a broken chain: %v", err)
	}
	if out := must(t, "ledger", "verify", "--help"); !strings.Contains(out, "  3  every chain is intact but a referenced blob is missing") {
		t.Errorf("verify --help lacks exit 3:\n%s", out)
	}
}

func TestLedgerPushAndPullKeepTheirExitWithABlobGap(t *testing.T) {
	root := t.TempDir()
	a := newMachine(t)
	storeOn(t, a, root)
	id := createID(t, "story", "create", "--title", "s")
	if err := os.WriteFile("doc.md", []byte("# doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "attach", id, "--file", "doc.md")
	sha := attachments(t, id)[0].SHA256
	os.Remove(filepath.Join(runtimeLedger(t).Dir(), "blobs", sha))
	if out := must(t, "ledger", "push"); !strings.Contains(out, "gap: "+id) || !strings.Contains(out, "1 blob gap(s)") {
		t.Errorf("push = %q", out)
	}
	b := newMachine(t)
	storeOn(t, b, root)
	if out := must(t, "ledger", "pull"); !strings.Contains(out, "gap: "+id) || !strings.Contains(out, "not in the store") {
		t.Errorf("pull = %q", out)
	}
	if _, err := run(t, "", "ledger", "verify"); ExitCodeOf(err) != 3 {
		t.Errorf("verify after a pull with a gap: %v", err)
	}
}

func ledgerIDs(t *testing.T, id string) []string {
	t.Helper()
	var events []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(must(t, "ledger", "list", id, "--json")), &events); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

func TestLedgerAdoptResolvesARefusedFork(t *testing.T) {
	root := t.TempDir()
	a := newMachine(t)
	storeOn(t, a, root)
	id1 := createID(t, "story", "create", "--title", "one from a")
	id2 := createID(t, "story", "create", "--title", "two from a")
	must(t, "ledger", "push")
	b := newMachine(t)
	storeOn(t, b, root)
	must(t, "ledger", "pull")

	a.use(t)
	must(t, "story", "set", id1, "--title", "a")
	must(t, "ledger", "push")
	storeIDs := ledgerIDs(t, id1)
	b.use(t)
	must(t, "story", "set", id1, "--title", "b")
	bIDs := ledgerIDs(t, id1)
	forkID := bIDs[len(bIDs)-1]
	if out, err := run(t, "", "ledger", "pull"); err == nil || ExitCodeOf(err) != 1 {
		t.Fatalf("B's pull: %v\n%s", err, out)
	}
	file := runtimeLedger(t).Path(id1)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "", "ledger", "adopt")
	if err == nil || ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), "diverged from the store: "+id1) || strings.Contains(err.Error(), id2) {
		t.Fatalf("adopt with no item: %v\n%s", err, out)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("adopt with no item changed the chain")
	}
	id2File := runtimeLedger(t).Path(id2)
	id2Before, err := os.ReadFile(id2File)
	if err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "", "ledger", "adopt", id2)
	if err == nil || ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), id2+" is in sync") {
		t.Fatalf("adopt of an in-sync item: %v\n%s", err, out)
	}
	if after, _ := os.ReadFile(id2File); !bytes.Equal(id2Before, after) {
		t.Fatal("a refused adopt changed the chain")
	}

	out = must(t, "ledger", "adopt", id1)
	prefix := "adopted: " + id1 + ": this runtime's events "
	i := strings.Index(out, prefix)
	if i < 0 {
		t.Fatalf("adopt output lacks %q:\n%s", prefix, out)
	}
	line := out[i:]
	line = line[:strings.Index(line, "\n")]
	const in, taken = " set aside in ", "; the store's "
	path := line[strings.Index(line, in)+len(in) : strings.Index(line, taken)]
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("set-aside file %q: %v\n%s", path, err, out)
	}
	if !strings.Contains(string(kept), `"id":"`+forkID+`"`) || !strings.Contains(string(kept), `"kind":"item.updated"`) {
		t.Fatalf("%s lacks B's forked event %s:\n%s", path, forkID, kept)
	}
	if !strings.Contains(out[i+len(line):], "  item.updated  ") {
		t.Fatalf("adopt output lists no item.updated:\n%s", out)
	}
	got := ledgerIDs(t, id1)
	if strings.Join(got, ",") != strings.Join(storeIDs, ",") {
		t.Fatalf("B's cache lists %v, want the store's %v", got, storeIDs)
	}
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id1, "--json")), &it); err != nil || it.Title != "a" {
		t.Fatalf("%s = %+v, %v", id1, it, err)
	}
	if list := must(t, "story", "list", "--json"); strings.Contains(list, `"title":"b"`) {
		t.Fatalf("story list still shows B's title:\n%s", list)
	}
	status := must(t, "ledger", "status")
	synced := false
	for row := range strings.SplitSeq(status, "\n") {
		synced = synced || strings.HasPrefix(row, id1+" ") && strings.HasSuffix(row, "in sync")
	}
	if !synced {
		t.Fatalf("status reads %s not in sync:\n%s", id1, status)
	}
	must(t, "ledger", "verify")
}
