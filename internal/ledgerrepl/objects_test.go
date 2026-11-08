package ledgerrepl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
)

var ctx = context.Background()

// memObjects is an in-memory store. hidden keys are held but not listed: a
// listing taken before another writer's puts.
type memObjects struct {
	objs   map[string][]byte
	hidden map[string]bool
}

func newMem() *memObjects { return &memObjects{objs: map[string][]byte{}, hidden: map[string]bool{}} }

func (m *memObjects) PutIfAbsent(_ context.Context, key string, body []byte) ([]byte, bool, error) {
	if b, ok := m.objs[key]; ok {
		return b, false, nil
	}
	m.objs[key] = bytes.Clone(body)
	return nil, true, nil
}

func (m *memObjects) Get(_ context.Context, key string) ([]byte, bool, error) {
	b, ok := m.objs[key]
	return b, ok, nil
}

func (m *memObjects) List(_ context.Context, prefix, after string) ([]string, error) {
	out := []string{}
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) && k > after && !m.hidden[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// newStore is an empty runtime ledger.
func newStore(t *testing.T) *ledger.Store {
	t.Helper()
	s, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// add appends n events to item's chain, each tagged.
func add(t *testing.T, s *ledger.Store, it, tag string, n int) {
	t.Helper()
	for i := range n {
		if _, err := s.Append(it, "note", "me", map[string]string{"t": fmt.Sprintf("%s%d", tag, i)}); err != nil {
			t.Fatal(err)
		}
	}
}

// attach stores body and appends an attachment.added naming it.
func attach(t *testing.T, s *ledger.Store, it, name, body string) (ledger.Event, string) {
	t.Helper()
	sha, err := s.PutBlob([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Append(it, item.KindAttachmentAdd, "me", item.Attachment{Name: name, SHA256: sha, Size: int64(len(body)), MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	return e, sha
}

// copyChain gives to the same chain of item that from holds.
func copyChain(t *testing.T, from, to *ledger.Store, it string) {
	t.Helper()
	events, err := from.Read(it)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := to.Import(events); err != nil {
		t.Fatal(err)
	}
}

func chain(t *testing.T, s *ledger.Store, it string) []ledger.Event {
	t.Helper()
	events, err := s.Read(it)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func repl(s *ledger.Store, o Objects, out *bytes.Buffer) *Replicator {
	return &Replicator{Ledger: s, Store: &ObjectStore{Objects: o, Name: "test"}, Refs: kernel.BlobRefs, Out: out}
}

// keysOf is the store's chain keys for item, as sequences.
func seqsOf(t *testing.T, o Objects, it string) []int {
	t.Helper()
	keys, err := o.List(ctx, ChainsPrefix+it+"/", "")
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, k := range keys {
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(k, ChainsPrefix+it+"/"), "%012d.json", &n); err != nil {
			t.Fatalf("key %s: %v", k, err)
		}
		out = append(out, n)
	}
	return out
}

func upTo(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// storeHash is the hash of the store's event at seq.
func storeHash(t *testing.T, o Objects, it string, seq int) string {
	t.Helper()
	b, found, err := o.Get(ctx, ChainKey(it, seq))
	if err != nil || !found {
		t.Fatalf("get %s %d: %v %v", it, seq, found, err)
	}
	var e ledger.Event
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	return e.Hash
}

func TestChainKey(t *testing.T) {
	if got := ChainKey("sto_1", 7); got != "chains/sto_1/000000000007.json" {
		t.Fatalf("ChainKey %s", got)
	}
	if !slices.Equal(upTo(3), []int{0, 1, 2}) {
		t.Fatal("upTo")
	}
}

func chainFile(t *testing.T, s *ledger.Store, it string) []byte {
	t.Helper()
	b, err := os.ReadFile(s.Path(it))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
