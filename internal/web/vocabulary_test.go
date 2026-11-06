package web

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// satelled names the local list Projects everywhere: the breadcrumb root reads
// projects, and no page uses "workspace" for it. The help page's projects
// topic, which defines the satelle.dev word, is the one exception.
func TestLocalListIsProjects(t *testing.T) {
	srv := mirrorWith(t, fixtureSnapshot())
	for _, path := range []string{"/", "/fragment/projects", "/logs", "/settings/global", "/r/proj/", "/r/proj/settings", "/r/proj/story/sto_1", "/r/proj/help"} {
		body := httpGetBody(t, srv.URL+path)
		switch path {
		case "/", "/logs", "/settings/global", "/r/proj/":
			if !strings.Contains(body, `<nav class="crumbs"><a href="/">projects</a>`) {
				t.Errorf("%s: the breadcrumb root is not projects", path)
			}
			if !strings.Contains(body, ">Projects</a>") {
				t.Errorf("%s: the top bar lost its Projects link", path)
			}
		case "/r/proj/help":
			before, rest, ok := strings.Cut(body, `id="projects"`)
			if !ok {
				t.Fatal("the help page has no projects topic")
			}
			_, after, _ := strings.Cut(rest, "</section>")
			body = before + after
		}
		if strings.Contains(strings.ToLower(body), "workspace") {
			t.Errorf("%s says workspace", path)
		}
	}
	isolateHome(t)
	st, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	empty := httptest.NewServer(NewMirrorWithInstance(st, "test").Handler)
	t.Cleanup(empty.Close)
	if body := httpGetBody(t, empty.URL+"/"); !strings.Contains(body, "satelle project add") {
		t.Error("the empty Projects page does not name satelle project add")
	}
}

// satelled's help page defines both words, from the one help topic.
func TestHelpPageDefinesProjectAndWorkspace(t *testing.T) {
	srv := mirrorWith(t, fixtureSnapshot())
	body := httpGetBody(t, srv.URL+"/r/proj/help")
	inOrder(t, body, `id="projects"`, "<strong>project</strong> is a repo or directory", "<strong>workspace</strong> is a collection of projects on satelle.dev")
}
