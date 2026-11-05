//go:build unix

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// performIn creates an item, moves it into the performing state and runs its
// performer with answer.
func performIn(t *testing.T, answer string) (*Kernel, string, error) {
	t.Helper()
	k, d, repo := multiState(t)
	ctx := context.Background()
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t"), Body: ptr("b")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(repo, "marker"), nil, 0o644)
	if res, err := k.Transition(ctx, it.ID, "work", "me"); err != nil || !res.Moved {
		t.Fatalf("open->work: %v %s", err, gates(res))
	}
	d.set("doer", answer)
	_, err = k.Perform(ctx, it.ID, "me")
	return k, it.ID, err
}

func runOf(t *testing.T, k *Kernel, id string) PerformerRun {
	t.Helper()
	runs, _ := k.Cache.Events(id, KindPerformerRun)
	var pr PerformerRun
	if len(runs) != 1 || runs[0].Decode(&pr) != nil {
		t.Fatalf("performer.run: %d runs", len(runs))
	}
	return pr
}

func attachmentsOf(t *testing.T, k *Kernel, id string) []item.Attachment {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return item.FoldAttachments(events)
}

func TestPerformAttachesArtifact(t *testing.T) {
	answer := "Planned.\n```json\n{\"artifact\":{\"name\":\"plan\",\"type\":\"plan\",\"body\":\"# The plan\"}}\n```"
	k, id, err := performIn(t, answer)
	if err != nil {
		t.Fatal(err)
	}
	atts := attachmentsOf(t, k, id)
	if len(atts) != 1 || atts[0].Name != "plan" || atts[0].MediaType != "text/markdown; type=plan" {
		t.Fatalf("attachments %+v", atts)
	}
	if b, _ := k.Ledger.GetBlob(atts[0].SHA256); string(b) != "# The plan" {
		t.Fatalf("attachment body %q", b)
	}
	if pr := runOf(t, k, id); pr.Error != "" || pr.Output == "" {
		t.Fatalf("performer.run %+v", pr)
	}
}

func TestPerformWithoutArtifact(t *testing.T) {
	k, id, err := performIn(t, "did it")
	if err != nil {
		t.Fatal(err)
	}
	if atts := attachmentsOf(t, k, id); len(atts) != 0 {
		t.Fatalf("attachments %+v", atts)
	}
	pr := runOf(t, k, id)
	if b, _ := k.Ledger.GetBlob(pr.Output); string(b) != "did it" || pr.Error != "" {
		t.Fatalf("performer.run %+v output %q", pr, b)
	}
}

func TestPerformMalformedArtifact(t *testing.T) {
	answer := `{"artifact":{"type":"plan","body":"# P"}}`
	k, id, err := performIn(t, answer)
	if err == nil || !strings.Contains(err.Error(), "malformed artifact") || !strings.Contains(err.Error(), "name") {
		t.Fatalf("err = %v", err)
	}
	if atts := attachmentsOf(t, k, id); len(atts) != 0 {
		t.Fatalf("attachments %+v", atts)
	}
	pr := runOf(t, k, id)
	if !strings.Contains(pr.Error, "malformed artifact") {
		t.Fatalf("performer.run error %q", pr.Error)
	}
	if b, _ := k.Ledger.GetBlob(pr.Output); string(b) != answer {
		t.Fatalf("raw output kept: %q", b)
	}
}
