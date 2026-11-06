package workflow

import (
	"os"
	"path/filepath"
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
	if q == nil || q.ChainID("que") != "que_release" || len(q.Validate(func(string) bool { return false })) != 0 {
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
