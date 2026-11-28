package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

func TestLoadQueuesAndApply(t *testing.T) {
	dir := t.TempDir()
	if qs, err := LoadQueues(filepath.Join(dir, "missing")); err != nil || len(qs) != 0 {
		t.Fatalf("a missing directory: %v %v", qs, err)
	}
	body := "title = \"Release\"\n[filter]\nkind = \"story\"\n[trigger]\ntask = \"tas_1\"\nmin_size = 2\n"
	if err := os.WriteFile(filepath.Join(dir, "release.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	qs, err := LoadQueues(dir)
	if err != nil {
		t.Fatal(err)
	}
	q := qs["release"]
	if q == nil || q.ChainID("que") != "que_release" || len(q.Validate(func(string) bool { return false }, nil)) != 0 {
		t.Fatalf("queue %+v", q)
	}
	q.ID = "que_65bc4edc"
	if q.ChainID("que") != "que_65bc4edc" {
		t.Fatal("an id is the chain")
	}
	it := item.Item{Title: "old", Inputs: &item.Selection{Status: "x"}, Trigger: &item.Trigger{Workflow: "w"}}
	q.Apply(&it)
	if it.Title != "Release" || it.Inputs.Kind != "story" || it.Inputs.Status != "" || it.Trigger.Task != "tas_1" || it.Trigger.MinSize != 2 {
		t.Fatalf("applied %+v", it)
	}
	it.Inputs.Kind = "changed"
	if q.Filter.Kind != "story" {
		t.Fatal("Apply shares the file's filter with the item")
	}
	q.Filter = nil
	q.Apply(&it)
	if it.Inputs != nil {
		t.Fatalf("no filter left inputs %+v", it.Inputs)
	}
}

func TestQueueOverlapSetting(t *testing.T) {
	dir := t.TempDir()
	write := func(name, overlap string) *Queue {
		t.Helper()
		p := filepath.Join(dir, name+".toml")
		body := "title = \"Q\"\n[trigger]\ntask = \"tas_1\"\noverlap = \"" + overlap + "\"\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		q, err := ParseQueue(p)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	q := write("allow", "allow")
	if q.Trigger.Overlap != item.OverlapAllow || !q.Trigger.Overlaps() || len(q.Validate(func(string) bool { return false }, nil)) != 0 {
		t.Fatalf("allow: %+v %v", q.Trigger, q.Validate(func(string) bool { return false }, nil))
	}
	q = write("odd", "sometimes")
	errs := q.Validate(func(string) bool { return false }, nil)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), q.File) || !strings.Contains(errs[0].Error(), "sometimes") {
		t.Fatalf("unknown overlap: %v", errs)
	}
	b, err := json.Marshal(item.Trigger{Task: "t"})
	if err != nil || strings.Contains(string(b), "overlap") {
		t.Fatalf("an unset overlap changes the trigger's json: %s %v", b, err)
	}
}

func TestQueueFilterRepo(t *testing.T) {
	no := func(string) bool { return false }
	repos := []string{"self", "satelle-server"}
	for _, r := range []string{"self", "satelle-server"} {
		q := &Queue{Name: "release", File: "release.toml", Title: "R", Filter: &item.Selection{Repo: r}}
		if errs := q.Validate(no, repos); len(errs) != 0 {
			t.Fatalf("repo %s: %v", r, errs)
		}
	}
	q := &Queue{Name: "release", File: "release.toml", Title: "R", Filter: &item.Selection{Repo: "other"}}
	errs := q.Validate(no, repos)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "other") || !strings.Contains(errs[0].Error(), "not registered") || !strings.Contains(errs[0].Error(), "satelle-server") {
		t.Fatalf("unregistered: %v", errs)
	}
	q.Filter.Repo = "Bad\x1bName"
	errs = q.Validate(no, repos)
	if len(errs) != 1 || strings.Contains(errs[0].Error(), "\x1b") || !strings.Contains(errs[0].Error(), "not a repository name") {
		t.Fatalf("bad name: %q", errs)
	}
}
