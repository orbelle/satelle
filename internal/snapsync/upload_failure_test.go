package snapsync

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
)

// refusingBlobs stores every blob but the refused paths, which fail as a server
// that will not store the file does.
type refusingBlobs struct {
	memBlobs
	refuse map[string]bool
}

func (r refusingBlobs) Put(_ context.Context, p string, body []byte) (bool, error) {
	if r.refuse[p] {
		return false, errors.New("HTTP 400: could not store file (too large?)")
	}
	_, had := r.blobs[p]
	r.blobs[p] = body
	return !had, nil
}

// sty_195dd215 AC1: a push the server refuses one file of records as pushed only
// what it stored, and still tries every other file. The refused path is absent
// from the base, so the next push plans it again.
func TestPushBaseOmitsAFileTheServerRefused(t *testing.T) {
	h := &memHosted{blobs: map[string][]byte{}}
	d := testDriver(t, h, nil)
	d.Records = &ptrRecords{h}
	blobs := refusingBlobs{memBlobs{h}, map[string]bool{"tasks/big.md": true}}
	d.Blobs = blobs
	files := []PushFile{
		{Path: "tasks/a.md", Content: []byte("a\n")},
		{Path: "tasks/big.md", Content: []byte("big\n")},
		{Path: "tasks/z.md", Content: []byte("z\n")},
	}
	ctx := context.Background()
	job, err := d.PreparePush(ctx, "tasks", files)
	if err != nil {
		t.Fatalf("PreparePush: %v", err)
	}
	res, err := d.Push(ctx, job)
	var ue *UploadError
	if !errors.As(err, &ue) || len(ue.Failures) != 1 || ue.Failures[0].Path != "tasks/big.md" {
		t.Fatalf("Push error = %v, want one UploadError naming tasks/big.md", err)
	}
	if res.Created != 2 {
		t.Errorf("Created = %d, want 2 — the files after the refused one must still go", res.Created)
	}
	if _, ok := h.blobs["tasks/z.md"]; !ok {
		t.Error("tasks/z.md was not stored after tasks/big.md was refused")
	}
	base, ok, err := hosted.LoadAreaBase(d.Server, d.Project, d.RepoRoot, "tasks")
	if err != nil || !ok {
		t.Fatalf("no tasks base saved: ok=%v err=%v", ok, err)
	}
	if _, has := base.Files["tasks/big.md"]; has {
		t.Errorf("the base records the refused tasks/big.md as pushed: %v", base.Files)
	}
	want := map[string]string{"tasks/a.md": SHA([]byte("a\n")), "tasks/z.md": SHA([]byte("z\n"))}
	if !reflect.DeepEqual(base.Files, want) {
		t.Errorf("base files = %v, want %v", base.Files, want)
	}

	// The next push, with the server now storing it, plans and uploads the file.
	delete(blobs.refuse, "tasks/big.md")
	d2 := &Driver{Server: d.Server, Project: d.Project, RepoRoot: d.RepoRoot, DataDir: d.DataDir, Records: d.Records, Blobs: blobs, Owns: d.Owns}
	job, err = d2.PreparePush(ctx, "tasks", files)
	if err != nil {
		t.Fatalf("second PreparePush: %v", err)
	}
	if !reflect.DeepEqual(job.plan.Uploads, []string{"tasks/big.md"}) {
		t.Errorf("second push uploads %v, want [tasks/big.md]", job.plan.Uploads)
	}
	if _, err := d2.Push(ctx, job); err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if string(h.blobs["tasks/big.md"]) != "big\n" {
		t.Errorf("tasks/big.md not stored by the re-run: %q", h.blobs["tasks/big.md"])
	}
}

// ptrRecords is the Records side of a memHosted that can be claimed: Put appends
// the record and returns its version.
type ptrRecords struct{ h *memHosted }

func (p *ptrRecords) Head(context.Context, string) (int, error) { return len(p.h.recs), nil }
func (p *ptrRecords) Read(ctx context.Context, area string, v int) ([]byte, bool, error) {
	return memRecords{p.h}.Read(ctx, area, v)
}
func (p *ptrRecords) Put(_ context.Context, _ string, body []byte) (int, error) {
	rec, err := Decode(body)
	if err != nil {
		return 0, err
	}
	p.h.recs = append(p.h.recs, rec)
	return len(p.h.recs), nil
}
