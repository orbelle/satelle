//go:build !windows

package ledgerrepl

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// gapped is A with an attachment whose blob is gone from A.
func gapped(t *testing.T) (*ledger.Store, ledger.Event, string) {
	t.Helper()
	a := newStore(t)
	add(t, a, "it", "a", 1)
	e, sha := attach(t, a, "it", "doc", "the body")
	add(t, a, "it", "a", 1)
	if err := os.Remove(filepath.Join(a.Dir(), "blobs", sha)); err != nil {
		t.Fatal(err)
	}
	return a, e, sha
}

func gapLine(e ledger.Event, sha, why string) string {
	return "gap: it event 1 attachment.added " + e.ID + " blob " + sha + " (attachment doc): " + why
}

func TestPushAndPullNameAMissingBlob(t *testing.T) {
	o := fsObjects(t)
	a, e, sha := gapped(t)
	res, out := push(t, a, o)
	if res.Failed() || res.Events != 3 || len(res.Gaps) != 1 {
		t.Fatalf("push %+v\n%s", res, out)
	}
	if !strings.Contains(out, gapLine(e, sha, "not in this runtime and not in the store")) {
		t.Errorf("push output:\n%s", out)
	}
	if got := seqsOf(t, o, "it"); !slices.Equal(got, upTo(3)) {
		t.Errorf("store holds %v", got)
	}
	b := newStore(t)
	res, out = pull(t, b, o)
	if res.Failed() || res.Events != 3 || len(res.Gaps) != 1 {
		t.Fatalf("pull %+v\n%s", res, out)
	}
	if !strings.Contains(out, gapLine(e, sha, "not in the store")) {
		t.Errorf("pull output:\n%s", out)
	}
}

func TestPullChecksALateBlobBeforeItFills(t *testing.T) {
	t.Run("wrong sha256", func(t *testing.T) {
		o := fsObjects(t)
		a, e, sha := gapped(t)
		push(t, a, o)
		if _, created, err := o.PutIfAbsent(ctx, BlobKey(sha), []byte("other bytes")); err != nil || !created {
			t.Fatal(created, err)
		}
		b := newStore(t)
		res, out := pull(t, b, o)
		if res.Failed() || res.Blobs != 0 || len(res.Gaps) != 1 {
			t.Fatalf("pull %+v\n%s", res, out)
		}
		if !strings.Contains(out, gapLine(e, sha, "the store's body hashes to ")) || !strings.Contains(out, "; not stored") {
			t.Errorf("pull output:\n%s", out)
		}
		if _, err := b.GetBlob(sha); err == nil {
			t.Error("the wrong body was stored")
		}
	})
	t.Run("right sha256", func(t *testing.T) {
		o := fsObjects(t)
		a, _, sha := gapped(t)
		push(t, a, o)
		b := newStore(t)
		if res, _ := pull(t, b, o); len(res.Gaps) != 1 {
			t.Fatalf("no gap before the blob arrives: %+v", res)
		}
		if _, err := a.PutBlob([]byte("the body")); err != nil {
			t.Fatal(err)
		}
		if res, out := push(t, a, o); res.Blobs != 1 || len(res.Gaps) != 0 {
			t.Fatalf("A's push of the late blob %+v\n%s", res, out)
		}
		res, out := pull(t, b, o)
		if res.Failed() || res.Blobs != 1 || len(res.Gaps) != 0 || !strings.Contains(out, "filled: it event 1 blob "+sha) {
			t.Fatalf("pull %+v\n%s", res, out)
		}
		if got, err := b.GetBlob(sha); err != nil || string(got) != "the body" {
			t.Fatalf("the filled blob %q %v", got, err)
		}
	})
}
