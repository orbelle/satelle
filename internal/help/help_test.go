package help

import (
	"strings"
	"testing"
)

func TestTopicsShipWithTitles(t *testing.T) {
	want := map[string]bool{"satelle": false, "workflows": false, "edit-gate": false, "configuration": false}
	for _, top := range List() {
		if top.Title == "" || top.Body == "" {
			t.Errorf("topic %q has no title or body", top.Name)
		}
		if _, ok := want[top.Name]; ok {
			want[top.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("topic %q is missing", name)
		}
	}
}

// The two words are defined once, in the projects topic.
func TestProjectAndWorkspaceDefinedOnce(t *testing.T) {
	top, ok := Get("projects")
	if !ok {
		t.Fatal("no projects topic")
	}
	for _, want := range []string{"A **project** is a repo or directory", "bound to at most one satelle.dev project", "A **workspace** is a collection of projects on satelle.dev"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the projects topic lacks %q", want)
		}
	}
	for _, other := range List() {
		if other.Name == "projects" {
			continue
		}
		if strings.Contains(other.Body, "is a repo or directory") || strings.Contains(other.Body, "is a collection of projects") {
			t.Errorf("topic %s defines a word again", other.Name)
		}
	}
	if s, _ := Get("satelle"); !strings.Contains(s.Body, "satelle help projects") {
		t.Error("the satelle topic does not point to satelle help projects")
	}
}
