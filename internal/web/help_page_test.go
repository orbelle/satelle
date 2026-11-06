package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/help"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestHelpPageRendersFormattedTopics(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.TouchPartition(t.Context(), "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	ms := NewMirror(s)
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/r/demo/help", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, `<pre class="prose">`) {
		t.Fatal("help must not dump raw markdown in pre.prose")
	}
	if !strings.Contains(body, `class="doc-article help-doc"`) {
		t.Fatal("help topic bodies must render as article.help-doc")
	}
	if !strings.Contains(body, `class="doc-article help-doc"`) || !strings.Contains(body, "<p>") {
		t.Fatalf("expected formatted HTML:\n%s", body[:min(800, len(body))])
	}
	// <code> must come from a topic body, not the page header's `satelle help`.
	if !strings.Contains(body, `<article class="doc-article help-doc">`) ||
		!regexp.MustCompile(`(?s)<article class="doc-article help-doc">.*<code>`).MatchString(body) {
		t.Fatal("topic bodies must contain formatted <code>")
	}
	topics := help.List()
	if n := strings.Count(body, `class="help-topic"`); n != len(topics) {
		t.Fatalf("help-topic count %d, want %d", n, len(topics))
	}
	for _, tp := range topics {
		if !strings.Contains(body, `id="`+tp.Name+`"`) {
			t.Errorf("missing topic anchor id=%q", tp.Name)
		}
	}
	// AC4: stripLeadingHeading is wired — help-doc articles must not start with <h1>
	// of the same title already shown in the section <h2>.
	arts := regexp.MustCompile(`(?s)<article class="doc-article help-doc">(.*?)</article>`).FindAllStringSubmatch(body, -1)
	if len(arts) != len(topics) {
		t.Fatalf("help-doc articles %d, want %d", len(arts), len(topics))
	}
	for _, m := range arts {
		if strings.Contains(m[1], "<h1>") {
			t.Fatalf("help-doc still contains <h1> (leading heading not stripped):\n%s", m[1][:min(200, len(m[1]))])
		}
	}
}

func TestHelpDocCSSFillsWrap(t *testing.T) {
	raw, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	if !strings.Contains(css, ".doc-article { font-size: 14.5px; line-height: 1.65; max-width: 75ch; }") {
		t.Error(".doc-article must cap at 75ch")
	}
	rule := regexp.MustCompile(`(?s)\.help-doc\s*\{[^}]*\}`)
	m := rule.FindString(css)
	if m == "" {
		t.Fatal("app.css missing .help-doc rule")
	}
	if !strings.Contains(m, "max-width: none") {
		t.Errorf(".help-doc must set max-width: none; got %q", m)
	}
}

func TestStripLeadingHeading(t *testing.T) {
	got := stripLeadingHeading("# Title\n\nbody line\n")
	if got != "body line\n" {
		t.Fatalf("stripped = %q", got)
	}
	same := "no heading\n## later\n"
	if stripLeadingHeading(same) != same {
		t.Fatal("paragraph-first body must be unchanged")
	}
	if !strings.Contains(stripLeadingHeading("# T\n\n## later\n"), "## later") {
		t.Fatal("later heading must survive")
	}
}
